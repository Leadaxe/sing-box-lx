//go:build with_lx_command

package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/certificate"
	"github.com/sagernet/sing-box/common/lxtrace"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/miekg/dns"
	"google.golang.org/grpc"
)

// lx: SPEC 139 — StartConnectionTest: an active connection test with a
// stage trace. The handler is a §3.6 bridge: it validates the request, puts
// a lxtrace.Trace into the context and drives a real request through the
// core — the app's DNS queries, the router (as an ordinary inbound
// connection over a net.Pipe) or a node directly, and the test's own HTTP
// client — while the pipeline's `// lx:` insertions write the stages. The
// stages are streamed as they happen; the last message is the result with
// the root cause (SPEC 139 §5.2). Findings (§6) are release 2.
//
// Without with_lx_command the stub twin answers codes.Unimplemented.

var errConnectionTestTimeout = errors.New("timeout")

func (s *StartedService) StartConnectionTest(request *ConnectionTestRequest, server grpc.ServerStreamingServer[ConnectionTestProgress]) error {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED || s.instance == nil {
		s.serviceAccess.RUnlock()
		return os.ErrInvalid
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()

	test, err := newConnectionTest(boxService, request)
	if err != nil {
		// An invalid request is a stream of one result with error and no stages (§3.2).
		return sendConnectionTestResult(server, &ConnectionTestResult{Error: err.Error()})
	}
	select {
	case connectionTestSlots <- struct{}{}:
	default:
		return sendConnectionTestResult(server, &ConnectionTestResult{Error: errConnectionTestBusy.Error()})
	}
	defer func() { <-connectionTestSlots }()
	return test.run(server)
}

func sendConnectionTestResult(server grpc.ServerStreamingServer[ConnectionTestProgress], result *ConnectionTestResult) error {
	return server.Send(&ConnectionTestProgress{Event: &ConnectionTestProgress_Result{Result: result}})
}

// run executes the validated test and streams its stages and result.
func (t *connectionTest) run(stream grpc.ServerStreamingServer[ConnectionTestProgress]) error {
	t.stream = stream
	t.started = time.Now()
	t.dialDone = make(chan struct{})
	t.routeDone = make(chan struct{})
	t.httpBegun = make(map[string]bool)
	t.synthesized = make(map[uint32]bool)

	timeoutCtx, cancelTimeout := context.WithTimeout(stream.Context(), t.timeout)
	defer cancelTimeout()
	ctx, cancel := context.WithCancelCause(timeoutCtx)
	defer cancel(nil)
	t.tr = lxtrace.New(lxtrace.Options{
		Fresh: t.fresh,
		Probe: true,
		Depth: t.depth,
		Emit:  t.emit,
	})
	ctx = lxtrace.With(ctx, t.tr)
	t.ctx = ctx
	t.cancel = cancel

	result := &ConnectionTestResult{}
	t.execute(ctx, result)
	// Every connection of the test is closed at the end (§5.14).
	t.conns.closeAll()

	if stream.Context().Err() != nil {
		// The client closed the stream: nothing is sent (§3.2).
		t.tr.Finish(stream.Context().Err())
		return nil
	}
	var cause error
	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		cause = errConnectionTestTimeout // unfinished stages end FAILED/timeout (§5.11)
	}
	summary := t.tr.Finish(cause)
	result.RootCause = summary.RootCause
	result.StagesTruncated = summary.Truncated
	result.ElapsedMs = clampRttMs(time.Since(t.started))
	if t.sendFailed.Load() {
		return nil
	}
	return sendConnectionTestResult(stream, result)
}

// execute walks the pipeline as far as the depth allows (PLAN §2).
func (t *connectionTest) execute(ctx context.Context, result *ConnectionTestResult) {
	var answer netip.Addr
	if t.appDNS {
		answer = t.runAppDNS(ctx)
		if ctx.Err() != nil || t.depth == lxtrace.DepthDNS {
			return
		}
	}
	destination := t.destination(answer)
	t.target = destination.String()
	switch {
	case t.detour != nil:
		t.runViaNode(ctx, destination, result)
	case t.network == N.NetworkUDP:
		t.runPacketRoute(ctx, destination)
	default:
		t.runViaRouter(ctx, destination, result)
	}
}

// runAppDNS issues the A, AAAA and HTTPS queries the app would issue, through
// the DNS router with the source metadata, the way the hijack path does
// (route/dns.go). It returns the first address answered, which the test
// then connects to unless connectIP overrides it.
func (t *connectionTest) runAppDNS(ctx context.Context) netip.Addr {
	var answer netip.Addr
	if _, err := netip.ParseAddr(t.host); err == nil {
		return answer // an app does not resolve an IP literal
	}
	for _, query := range []struct {
		name  string
		qType uint16
	}{
		{"A", dns.TypeA},
		{"AAAA", dns.TypeAAAA},
		{"HTTPS", dns.TypeHTTPS},
	} {
		if ctx.Err() != nil {
			return answer
		}
		stageCtx, stage := lxtrace.Begin(ctx, stageAppDNS, t.host)
		stage.Attr("type", query.name)
		message := new(dns.Msg)
		message.SetQuestion(dns.Fqdn(t.host), query.qType)
		metadata := t.metadata // a copy per query: the router annotates it
		response, err := t.dnsRouter.Exchange(adapter.WithContext(stageCtx, &metadata), message, adapter.DNSQueryOptions{DisableCache: t.fresh})
		if err != nil {
			stage.End(err)
			continue
		}
		stage.Attr("rcode", dns.RcodeToString[response.Rcode])
		var answers []string
		for _, record := range response.Answer {
			switch typed := record.(type) {
			case *dns.A:
				answers = append(answers, typed.A.String())
				if !answer.IsValid() {
					answer, _ = netip.AddrFromSlice(typed.A)
				}
			case *dns.AAAA:
				answers = append(answers, typed.AAAA.String())
				if !answer.IsValid() {
					answer, _ = netip.AddrFromSlice(typed.AAAA)
				}
			case *dns.CNAME:
				answers = append(answers, "CNAME "+typed.Target)
			case *dns.HTTPS:
				answers = append(answers, "HTTPS "+typed.Target)
			default:
				answers = append(answers, dns.TypeToString[record.Header().Rrtype])
			}
		}
		if len(answers) > 0 {
			stage.Attr("answers", strings.Join(answers, ","))
		}
		if response.Rcode != dns.RcodeSuccess {
			stage.End(E.New("rcode ", dns.RcodeToString[response.Rcode]))
			continue
		}
		stage.End(nil)
	}
	return answer
}

// detourType is the protocol type of the node the test goes through directly.
func (t *connectionTest) detourType() string {
	if outbound, isOutbound := t.detour.(adapter.Outbound); isOutbound {
		return outbound.Type()
	}
	return ""
}

// runViaNode is the GetURLViaOutbound path plus the trace: the node's
// DialContext inside a `dial` stage, then the test's HTTP client.
func (t *connectionTest) runViaNode(ctx context.Context, destination M.Socksaddr, result *ConnectionTestResult) {
	dial := func(dialCtx context.Context) (net.Conn, error) {
		stageCtx, stage := lxtrace.Begin(dialCtx, stageDial, t.detourTag)
		stage.Attr("type", t.detourType())
		stage.Attr("destination", destination.String())
		conn, err := t.detour.DialContext(stageCtx, N.NetworkTCP, destination)
		stage.End(err)
		if err != nil {
			t.pathFailed.Store(true) // before the HTTP client sees the error, not via the async emit
			return nil, err
		}
		t.conns.add(conn)
		return conn, nil
	}
	switch t.depth {
	case lxtrace.DepthRoute:
		// No router is involved: there is no rule or group selection to show.
		return
	case lxtrace.DepthConnect:
		dial(ctx)
		return
	}
	t.runHTTP(ctx, result, dial)
}

// runViaRouter enters the router as an ordinary inbound connection over a
// net.Pipe: the server side is routed, the client side is what the HTTP
// client dials. At DEPTH_ROUTE the HTTP client is only the payload the
// sniffer needs; the router closes the pipe once the route is decided.
func (t *connectionTest) runViaRouter(ctx context.Context, destination M.Socksaddr, result *ConnectionTestResult) {
	metadata := t.metadata
	metadata.Destination = destination
	route := func(context.Context) (net.Conn, error) {
		clientConn, serverConn := net.Pipe()
		t.conns.add(clientConn)
		t.conns.add(serverConn)
		// The route lives on the test ctx (with the trace), not on the HTTP
		// transport's dial ctx, which ends with the dial.
		go t.router.RouteConnectionEx(ctx, serverConn, metadata, N.OnceClose(t.onRouteClosed))
		return clientConn, nil
	}
	switch t.depth {
	case lxtrace.DepthConnect:
		// No payload at this depth (§7: early-handshake protocols send their
		// header with the first payload, so there is none to send). The test
		// ends when the first-hop dial ends, or when the route fails.
		route(ctx)
		select {
		case <-t.dialDone:
		case <-t.routeDone:
		case <-ctx.Done():
		}
	default:
		t.runHTTP(ctx, result, route)
		if result.HttpStatus != 0 {
			return // the exchange completed: nothing to wait for from the router
		}
	}
	t.recordRouteError(ctx, destination)
}

// runPacketRoute is the UDP DEPTH_ROUTE test: a payload-less packet pipe
// through RoutePacketConnectionEx; the route stage is all it produces.
func (t *connectionTest) runPacketRoute(ctx context.Context, destination M.Socksaddr) {
	conn := newIdlePacketConn(t.metadata.Source)
	t.conns.add(conn)
	metadata := t.metadata
	metadata.Destination = destination
	go t.router.RoutePacketConnectionEx(ctx, conn, metadata, N.OnceClose(t.onRouteClosed))
	select {
	case <-t.routeDone:
	case <-ctx.Done():
	}
	t.recordRouteError(ctx, destination)
}

// onRouteClosed is the onClose of the routed connection: a non-nil error is a
// route refusal (reject, outbound not found, missing fakeip record) or the
// first-hop dial failure, both of which end the path.
func (t *connectionTest) onRouteClosed(err error) {
	t.routeAccess.Lock()
	t.routeClosed = true
	t.routeErr = err
	t.routeAccess.Unlock()
	if err != nil {
		t.pathFailed.Store(true)
	}
	close(t.routeDone)
}

// recordRouteError writes the route refusal as a FAILED `route` stage when
// the router refused the connection before the route decision was recorded
// (a reject rule returns before lxtrace.Routed runs).
func (t *connectionTest) recordRouteError(ctx context.Context, destination M.Socksaddr) {
	t.waitRouteClosed(200 * time.Millisecond)
	t.routeAccess.Lock()
	err := t.routeErr
	t.routeAccess.Unlock()
	if err == nil || t.routeSeen.Load() {
		return
	}
	_, stage := lxtrace.Begin(ctx, stageRoute, destination.String())
	stage.Attr("network", t.network)
	if t.metadata.Inbound != "" {
		stage.Attr("inbound", t.metadata.Inbound)
	}
	stage.End(err)
	t.routeSeen.Store(true)
}

// waitRouteClosed gives the router's onClose a moment to land after the
// pipe failure surfaced on the client side.
func (t *connectionTest) waitRouteClosed(wait time.Duration) {
	if t.router == nil {
		return
	}
	select {
	case <-t.routeDone:
	case <-time.After(wait):
	}
}

// runHTTP is the test's HTTP client (as GetURLViaOutbound, 058): a per-call
// transport with DisableKeepAlives, the certificate store on Android and a
// body clamp, plus httptrace for the target_tls / first_byte / http stages
// (DEPTH_HTTP only; at DEPTH_ROUTE the client is just the payload).
func (t *connectionTest) runHTTP(ctx context.Context, result *ConnectionTestResult, dial func(context.Context) (net.Conn, error)) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, t.link.String(), nil)
	if err != nil {
		t.finishHTTPFailure(ctx, err)
		return
	}
	for _, header := range t.request.Headers {
		if http.CanonicalHeaderKey(header.Key) == "Host" {
			httpRequest.Host = header.Value
			continue
		}
		httpRequest.Header.Set(header.Key, header.Value)
	}
	if t.depth == lxtrace.DepthHTTP {
		httpRequest = httpRequest.WithContext(httptrace.WithClientTrace(httpRequest.Context(), t.clientTrace(ctx)))
	}

	transport := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: C.TCPTimeout,
		DisableKeepAlives:   true,
		DialContext: func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			return dial(dialCtx)
		},
	}
	if C.IsAndroid {
		// The system x509 pool is empty inside a mobile process (058).
		store, err := certificate.NewStore(t.instance.ctx, logger.NOP(), option.CertificateOptions{})
		if err != nil {
			t.finishHTTPFailure(ctx, E.Cause(err, "initialize certificate store"))
			return
		}
		storeScope := adapter.NewScope(t.instance.ctx, log.NewNOPFactory().Logger())
		defer storeScope.Close()
		err = store.Start(adapter.StartStateInitialize, storeScope)
		if err != nil {
			t.finishHTTPFailure(ctx, E.Cause(err, "initialize certificate store"))
			return
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: store.Pool()}
	}
	defer transport.CloseIdleConnections()

	httpResponse, err := (&http.Client{Transport: transport}).Do(httpRequest)
	if t.depth != lxtrace.DepthHTTP {
		if httpResponse != nil {
			httpResponse.Body.Close()
		}
		return
	}
	if err != nil {
		t.finishHTTPFailure(ctx, err)
		return
	}
	defer httpResponse.Body.Close()

	t.httpAccess.Lock()
	stage := t.httpStage
	if stage == nil {
		// GotFirstResponseByte did not fire (HTTP/2 push-less edge); open it now.
		_, stage = lxtrace.Begin(ctx, stageHTTP, t.link.String())
		t.httpStage = stage
		t.httpBegun[stageHTTP] = true
	}
	t.httpAccess.Unlock()
	stage.Attr("status", strconv.Itoa(httpResponse.StatusCode))
	if contentType := httpResponse.Header.Get("Content-Type"); contentType != "" {
		stage.Attr("content_type", contentType)
	}
	stage.Attr("proto", httpResponse.Proto)

	// One byte past the limit tells "ended at the limit" from "was cut" (058).
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, t.maxBytes+1))
	if err != nil {
		stage.Attr("bytes", strconv.Itoa(len(body)))
		stage.End(err)
		return
	}
	var truncated bool
	if int64(len(body)) > t.maxBytes {
		body = body[:t.maxBytes]
		truncated = true
	}
	stage.Attr("bytes", strconv.Itoa(len(body)))
	if truncated {
		stage.Attr("truncated", "true")
	}
	stage.End(nil)

	result.HttpStatus = uint32(httpResponse.StatusCode)
	result.Body = body
	result.Truncated = truncated
	result.ContentType = httpResponse.Header.Get("Content-Type")
}

// clientTrace maps the HTTP client's progress to the target_tls, first_byte
// and http stages. The callbacks run on the transport's goroutines.
func (t *connectionTest) clientTrace(ctx context.Context) *httptrace.ClientTrace {
	beginFirstByte := func() {
		t.httpAccess.Lock()
		defer t.httpAccess.Unlock()
		if t.httpBegun[stageFirstByte] {
			return
		}
		_, t.firstByteStage = lxtrace.Begin(ctx, stageFirstByte, t.host)
		t.httpBegun[stageFirstByte] = true
	}
	return &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) {
			if !t.https {
				beginFirstByte()
			}
		},
		TLSHandshakeStart: func() {
			t.httpAccess.Lock()
			defer t.httpAccess.Unlock()
			if t.httpBegun[stageTargetTLS] {
				return
			}
			_, t.tlsStage = lxtrace.Begin(ctx, stageTargetTLS, t.host)
			t.tlsStage.Attr("sni", t.host)
			t.httpBegun[stageTargetTLS] = true
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			t.httpAccess.Lock()
			stage := t.tlsStage
			if stage != nil {
				if state.NegotiatedProtocol != "" {
					stage.Attr("alpn", state.NegotiatedProtocol)
				}
				if state.Version != 0 {
					stage.Attr("version", tls.VersionName(state.Version))
				}
				if err != nil {
					// Failed or Skipped is decided once the path outcome is known.
					t.tlsErr = err
				} else {
					stage.End(nil)
					t.tlsStage = nil
				}
			}
			t.httpAccess.Unlock()
			if err == nil {
				beginFirstByte()
			}
		},
		GotFirstResponseByte: func() {
			t.httpAccess.Lock()
			defer t.httpAccess.Unlock()
			if t.firstByteStage != nil {
				t.firstByteStage.End(nil)
				t.firstByteStage = nil
			}
			if !t.httpBegun[stageHTTP] {
				_, t.httpStage = lxtrace.Begin(ctx, stageHTTP, t.link.String())
				t.httpBegun[stageHTTP] = true
			}
		},
	}
}

// finishHTTPFailure closes the HTTP stages after a failed exchange. When the
// path broke earlier (route refusal, first-hop dial), the HTTP stages did
// not run on their own account and are SKIPPED, not FAILED, so the root
// cause stays at the stage that broke (§5.2); otherwise the running stage
// takes the error and the stages after it are SKIPPED.
func (t *connectionTest) finishHTTPFailure(ctx context.Context, err error) {
	t.waitRouteClosed(200 * time.Millisecond)
	pathFailed := t.pathFailed.Load()

	t.httpAccess.Lock()
	defer t.httpAccess.Unlock()
	failed := false
	closeRunning := func(stage *lxtrace.Stage, stageErr error) {
		if stage == nil {
			return
		}
		if pathFailed {
			stage.Attr("reason", "path failed")
			stage.Mark(lxtrace.Skipped)
			return
		}
		if stageErr == nil {
			stageErr = err
		}
		stage.End(stageErr)
		failed = true
	}
	closeRunning(t.tlsStage, t.tlsErr)
	t.tlsStage = nil
	closeRunning(t.firstByteStage, nil)
	t.firstByteStage = nil
	if t.httpStage != nil {
		closeRunning(t.httpStage, nil)
		t.httpStage = nil
	}

	skip := func(kind, subject string) {
		if t.httpBegun[kind] {
			return
		}
		t.httpBegun[kind] = true
		_, stage := lxtrace.Begin(ctx, kind, subject)
		stage.Mark(lxtrace.Skipped)
	}
	if !pathFailed && !failed {
		// Nothing of the path and nothing of the exchange carries the error
		// yet (e.g. a client-side failure before any dial): the http stage does.
		if !t.httpBegun[stageHTTP] {
			t.httpBegun[stageHTTP] = true
			_, stage := lxtrace.Begin(ctx, stageHTTP, t.link.String())
			stage.End(err)
		}
	}
	if t.https {
		skip(stageTargetTLS, t.host)
	}
	skip(stageFirstByte, t.host)
	skip(stageHTTP, t.link.String())
}

// emit receives every stage snapshot from the trace's emitter goroutine:
// it observes the pipeline (route seen, first-hop dial, path failures) and
// forwards the snapshot to the stream. Only this goroutine sends stages, and
// the result is sent after Finish has stopped it, so Send is never concurrent.
func (t *connectionTest) emit(snapshot lxtrace.StageSnapshot) {
	t.observe(snapshot)
	if t.sendFailed.Load() {
		return
	}
	err := t.stream.Send(&ConnectionTestProgress{Event: &ConnectionTestProgress_Stage{Stage: connectionTestStageToProto(snapshot)}})
	if err != nil {
		t.sendFailed.Store(true)
		t.cancel(err)
	}
}

func (t *connectionTest) observe(snapshot lxtrace.StageSnapshot) {
	if snapshot.Result == lxtrace.Running {
		return
	}
	switch snapshot.Kind {
	case stageRoute:
		t.routeSeen.Store(true)
		if snapshot.Result == lxtrace.Failed && snapshot.Parent == 0 {
			t.pathFailed.Store(true)
		}
	case stageDial, stageDetour:
		if snapshot.Parent == 0 {
			if snapshot.Result == lxtrace.Failed {
				t.pathFailed.Store(true)
			}
			if snapshot.Kind == stageDial && snapshot.Result == lxtrace.OK {
				t.dialDoneOnce.Do(func() { close(t.dialDone) })
			}
		}
		t.httpAccess.Lock()
		already := t.synthesized[snapshot.ID]
		t.synthesized[snapshot.ID] = true
		t.httpAccess.Unlock()
		if !already {
			t.synthesizeAfterDial(t.ctx, snapshot, t.target)
		}
	}
}

func connectionTestStageToProto(snapshot lxtrace.StageSnapshot) *ConnectionTestStage {
	stage := &ConnectionTestStage{
		Id:         snapshot.ID,
		Parent:     snapshot.Parent,
		Kind:       snapshot.Kind,
		Subject:    snapshot.Subject,
		StartMs:    snapshot.StartMs,
		DurationMs: snapshot.DurationMs,
		Result:     ConnectionTestStageResult(snapshot.Result),
		Error:      snapshot.Error,
	}
	if len(snapshot.Attrs) > 0 {
		stage.Attrs = make([]*ConnectionTestAttr, 0, len(snapshot.Attrs))
		for _, attr := range snapshot.Attrs {
			stage.Attrs = append(stage.Attrs, &ConnectionTestAttr{Key: attr.Key, Value: attr.Value})
		}
	}
	return stage
}
