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
	"github.com/sagernet/sing-box/dns/transport/fakeip"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	R "github.com/sagernet/sing-box/route/rule"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

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
// the root cause (SPEC 139 §5.2) and the findings (§6).
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
		Fresh:     t.fresh,
		Probe:     true,
		Depth:     t.depth,
		Emit:      t.emit,
		Outbounds: t.instance.outboundManager, // the stream ctx carries no service registry
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
	if result.HttpStatus != 0 || t.depth == lxtrace.DepthConnect && t.dialFinished.Load() {
		// The path was completed (§3.2): a stage still open when the response
		// arrived (a lost race ending late, a detour unwinding) is not a cause,
		// nor is a failed attempt the transport retried on its own (MASQUE h3
		// falling back to h2, TrustTunnel replacing a stale session).
		result.RootCause = 0
	}
	result.StagesTruncated = summary.Truncated
	summary.RootCause = result.RootCause
	result.Findings = connectionTestFindingsToProto(lxtrace.Findings(summary, t.findingState(summary, result)))
	result.ElapsedMs = clampRttMs(time.Since(t.started))
	if t.sendFailed.Load() {
		return nil
	}
	// The emitter has stopped; stage ends it could not queue go out now, on
	// this goroutine, before the result, so the client's tree is complete.
	for _, snapshot := range summary.Dropped {
		err := stream.Send(&ConnectionTestProgress{Event: &ConnectionTestProgress_Stage{Stage: connectionTestStageToProto(snapshot)}})
		if err != nil {
			return nil
		}
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
		for _, record := range response.Answer {
			stage.Attr("answer", recordText(record, message.Question[0].Name))
			if answer.IsValid() {
				continue
			}
			switch typed := record.(type) {
			case *dns.A:
				answer, _ = netip.AddrFromSlice(typed.A)
			case *dns.AAAA:
				answer, _ = netip.AddrFromSlice(typed.AAAA)
			}
		}
		if len(response.Answer) == 0 && (response.Rcode == dns.RcodeSuccess || response.Rcode == dns.RcodeNameError) {
			// NODATA / NXDOMAIN: the SOA says which zone answered and how
			// long the negative answer is cached.
			for _, record := range response.Ns {
				stage.Attr("authority", recordText(record, message.Question[0].Name))
			}
		}
		attrAnswerSource(stage)
		if response.Rcode != dns.RcodeSuccess {
			stage.End(E.New("rcode ", dns.RcodeToString[response.Rcode]))
			continue
		}
		stage.End(nil)
	}
	return answer
}

// recordText is a resource record in presentation format without the class:
// `A 300 192.0.2.1`, `HTTPS 300 1 . alpn="h2,h3"`. The owner name leads only
// when it is not the queried name: the records after a CNAME, the zone of a
// SOA in the authority section.
func recordText(record dns.RR, question string) string {
	header := record.Header()
	rdata := strings.TrimPrefix(record.String(), header.String())
	text := dns.Type(header.Rrtype).String() + " " + strconv.FormatUint(uint64(header.Ttl), 10) + " " + rdata
	if !strings.EqualFold(header.Name, question) {
		text = header.Name + " " + text
	}
	return text
}

// attrAnswerSource copies where the answer came from onto the app_dns stage:
// the last `dns` child is the exchange the router took the answer from
// (rules may reject earlier ones). fakeip and hosts answer from the core
// itself: `local`, not `network`. No child means the rule answered itself
// (predefined, reject) and there is no source to name.
func attrAnswerSource(stage *lxtrace.Stage) {
	var source *lxtrace.StageSnapshot
	for _, child := range stage.Children() {
		if child.Kind == "dns" {
			source = &child
		}
	}
	if source == nil {
		return
	}
	if source.Result != lxtrace.Reused {
		switch stageAttrValue(source.Attrs, "type") {
		case C.DNSTypeFakeIP, C.DNSTypeHosts:
			stage.Attr("source", "local")
		default:
			stage.Attr("source", "network")
		}
		return
	}
	stage.Attr("source", "cache")
	for _, attr := range source.Attrs {
		if attr.Key == "cache_age" || attr.Key == "stale" {
			stage.Attr(attr.Key, attr.Value)
		}
	}
}

func stageAttrValue(attrs []lxtrace.Attr, key string) string {
	for _, attr := range attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}

// detourType is the protocol type of the node the test goes through directly.
func (t *connectionTest) detourType() string {
	if outbound, isOutbound := t.detour.(adapter.Outbound); isOutbound {
		return outbound.Type()
	}
	return ""
}

// runViaNode is the GetURLViaOutbound path plus the trace: the node's
// DialContext inside a `dial` stage, then the test's HTTP client. The dial
// runs on the test ctx, not on the ctx net/http hands to DialContext: that
// one has its cancellation stripped and is cancelled later by
// CloseIdleConnections, which would turn a timeout into a misfiled
// "context canceled" / lost_race (§5.11 wants FAILED/timeout).
func (t *connectionTest) runViaNode(ctx context.Context, destination M.Socksaddr, result *ConnectionTestResult) {
	dial := func(context.Context) (net.Conn, error) {
		stageCtx, stage := lxtrace.Begin(ctx, stageDial, t.detourTag)
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
		if _, err := dial(ctx); err == nil {
			t.dialFinished.Store(true) // the emit hook may still be queued; the result reads it next
		}
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
		if t.routeCalls.Add(1) > 1 {
			// net/http retries on a new connection after GOAWAY or
			// REFUSED_STREAM. One routed connection per test: a second route
			// would write a second route stage and a second onClose.
			return nil, E.New("connection test: the HTTP client asked for a second connection; one routed connection per test")
		}
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
	t.routeDoneOnce.Do(func() { close(t.routeDone) })
}

// recordRouteError writes the route refusal as a FAILED `route` stage when
// the router refused the connection before the route decision was recorded
// (a reject rule returns before lxtrace.Routed runs).
func (t *connectionTest) recordRouteError(ctx context.Context, destination M.Socksaddr) {
	t.waitRouteClosed(200 * time.Millisecond)
	t.routeAccess.Lock()
	err := t.routeErr
	t.routeAccess.Unlock()
	if err == nil || t.routeSeen.Load() || t.tr.RouteRecorded() {
		return
	}
	_, stage := lxtrace.Begin(ctx, stageRoute, destination.String())
	stage.Attr("network", t.network)
	if t.metadata.Inbound != "" {
		stage.Attr("inbound", t.metadata.Inbound)
	}
	t.attrFakeIPRoute(stage, destination)
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

// beginFirstByte opens the first_byte stage once the request is written and
// the first-hop dial has ended OK (see requestWritten/dialFinished). Called
// from httptrace.WroteRequest and from observe on the dial's end; whichever
// comes second opens the stage.
func (t *connectionTest) beginFirstByte(ctx context.Context) {
	if t.depth != lxtrace.DepthHTTP || !t.requestWritten.Load() || !t.dialFinished.Load() {
		return
	}
	t.httpAccess.Lock()
	defer t.httpAccess.Unlock()
	if t.httpBegun[stageFirstByte] {
		return
	}
	_, t.firstByteStage = lxtrace.Begin(ctx, stageFirstByte, t.host)
	t.httpBegun[stageFirstByte] = true
}

// clientTrace maps the HTTP client's progress to the target_tls, first_byte
// and http stages. The callbacks run on the transport's goroutines.
func (t *connectionTest) clientTrace(ctx context.Context) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			t.requestWritten.Store(true)
			t.beginFirstByte(ctx)
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
		},
		GotFirstResponseByte: func() {
			t.httpAccess.Lock()
			defer t.httpAccess.Unlock()
			if t.firstByteStage != nil {
				t.firstByteStage.End(nil)
				t.firstByteStage = nil
			} else if !t.httpBegun[stageFirstByte] {
				// The response came before the dial's end was observed (or no
				// top-level dial stage exists, e.g. a truncated trace): the
				// stage is recorded without a measured wait.
				_, stage := lxtrace.Begin(ctx, stageFirstByte, t.host)
				stage.Attr("start", "late")
				stage.End(nil)
				t.httpBegun[stageFirstByte] = true
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
	// On a timeout or cancel the HTTP client returns before the pipeline has
	// unwound: a dial may still be in flight and pathFailed not yet set. The
	// running stages are then left to Finish, which closes them FAILED with
	// the cause so the deepest one is the root (§5.11); the stages that never
	// began are SKIPPED, not FAILED with the client's ctx error.
	timedOut := ctx.Err() != nil && !pathFailed

	t.httpAccess.Lock()
	defer t.httpAccess.Unlock()
	failed := false
	closeRunning := func(stage *lxtrace.Stage, stageErr error) {
		if stage == nil {
			return
		}
		if timedOut {
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
	if !pathFailed && !failed && !timedOut {
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
	snapshot = t.observe(snapshot)
	if t.sendFailed.Load() {
		return
	}
	err := t.stream.Send(&ConnectionTestProgress{Event: &ConnectionTestProgress_Stage{Stage: connectionTestStageToProto(snapshot)}})
	if err != nil {
		t.sendFailed.Store(true)
		t.cancel(err)
	}
}

// observe returns the snapshot to send, with the attributes synthesis added
// to an already-ended stage merged in. Synthesis runs BEFORE dialDone is
// closed: the handler finishes the trace once dialDone is closed, so any
// stage synthesized after that point would race Finish.
func (t *connectionTest) observe(snapshot lxtrace.StageSnapshot) lxtrace.StageSnapshot {
	if snapshot.Result == lxtrace.Running {
		return snapshot
	}
	switch snapshot.Kind {
	case stageRoute:
		t.routeSeen.Store(true)
		if snapshot.Result == lxtrace.Failed && snapshot.Parent == 0 {
			t.pathFailed.Store(true)
		}
	case stageDial, stageDetour:
		if snapshot.Parent == 0 && snapshot.Result == lxtrace.Failed {
			t.pathFailed.Store(true)
		}
		t.httpAccess.Lock()
		already := t.synthesized[snapshot.ID]
		t.synthesized[snapshot.ID] = true
		t.httpAccess.Unlock()
		if !already {
			// The header target is the hop's own destination (SPEC 139 §4):
			// the test's target for the first hop, the address handed to the
			// detour for a detour.
			target, firstHop := t.target, true
			if snapshot.Kind == stageDetour {
				target, firstHop = snapshotAttr(snapshot, "destination"), false
			}
			if extra := t.synthesizeAfterDial(t.ctx, snapshot, target, firstHop); len(extra) > 0 {
				snapshot.Attrs = append(append([]lxtrace.Attr(nil), snapshot.Attrs...), extra...)
			}
		}
		if snapshot.Parent == 0 && snapshot.Kind == stageDial && snapshot.Result == lxtrace.OK {
			t.dialFinished.Store(true)
			t.beginFirstByte(t.ctx)
			t.dialDoneOnce.Do(func() { close(t.dialDone) })
		}
	}
	return snapshot
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

// findingState gathers the core state the findings (SPEC 139 §6) are computed
// against: the source, the TUN inbounds it enters, the fakeip store, the route
// rule table and the node types. It only reads: the test writes no shared
// state (§5.7).
func (t *connectionTest) findingState(summary lxtrace.Summary, result *ConnectionTestResult) lxtrace.FindingState {
	state := lxtrace.FindingState{
		Host:      t.host,
		ConnectIP: t.connectIP,
		Completed: result.RootCause == 0 && (t.depth != lxtrace.DepthHTTP || result.HttpStatus != 0),
		FakeIP:    t.fakeIPState(),
		NodeTypes: t.nodeTypes(summary),
	}
	if source := t.request.Source; source != nil {
		state.PackageName = source.PackageName
	}
	if owner := t.metadata.ProcessInfo; owner != nil && owner.UserId > 0 {
		state.UID = owner.UserId
	}
	var options *option.Options
	if t.instance.runningConfig != "" {
		// The rule options and the TUN options come from the config snapshot,
		// as GetRunningConfig returns it (SPEC 037): the live objects keep them
		// private.
		parsed, err := parseConfig(t.instance.ctx, t.instance.runningConfig)
		if err == nil {
			options = &parsed
		}
	}
	if options != nil {
		state.TUN = connectionTestTUNs(options, t.metadata.Inbound)
	}
	if t.router != nil {
		state.RouteRules = connectionTestRouteRules(t.router, options)
		state.QUICRejectedBy = t.quicRejectedBy()
	}
	return state
}

// connectionTestTUNs returns the TUN inbounds the source enters: the source
// inbound when it is a TUN, every TUN when no inbound was given.
func connectionTestTUNs(options *option.Options, sourceInbound string) []lxtrace.TUNState {
	var tuns []lxtrace.TUNState
	for _, inbound := range options.Inbounds {
		if inbound.Type != C.TypeTun || sourceInbound != "" && inbound.Tag != sourceInbound {
			continue
		}
		tunOptions, isTUN := inbound.Options.(*option.TunInboundOptions)
		if !isTUN {
			continue
		}
		tun := lxtrace.TUNState{
			Tag:             inbound.Tag,
			IncludePackage:  tunOptions.IncludePackage,
			ExcludePackage:  tunOptions.ExcludePackage,
			IncludeUID:      tunOptions.IncludeUID,
			ExcludeUID:      tunOptions.ExcludeUID,
			IncludeUIDRange: tunOptions.IncludeUIDRange,
			ExcludeUIDRange: tunOptions.ExcludeUIDRange,
		}
		for _, prefixes := range [][]netip.Prefix{tunOptions.Address, tunOptions.Inet6Address} {
			for _, prefix := range prefixes {
				if prefix.Addr().Is6() {
					tun.HasIPv6 = true
				}
			}
		}
		tuns = append(tuns, tun)
	}
	return tuns
}

// connectionTestRouteRules lists the route rules as the route stage names them
// (description, action) with the conditions of each that need the destination
// address. The router builds one rule per configured rule, in order, so the
// options line up by index; on a mismatch the conditions are left out.
func connectionTestRouteRules(router adapter.Router, options *option.Options) []lxtrace.RouteRule {
	rules := router.Rules()
	var configured []option.Rule
	if options != nil && options.Route != nil && len(options.Route.Rules) == len(rules) {
		configured = options.Route.Rules
	}
	routeRules := make([]lxtrace.RouteRule, 0, len(rules))
	for i, rule := range rules {
		routeRule := lxtrace.RouteRule{Description: rule.String()}
		if action := rule.Action(); action != nil {
			routeRule.Action = action.String()
		}
		if configured != nil {
			routeRule.AddressConditions = ruleAddressConditions(router, configured[i], nil)
		}
		routeRules = append(routeRules, routeRule)
	}
	return routeRules
}

func ruleAddressConditions(router adapter.Router, rule option.Rule, conditions []string) []string {
	if rule.Type == C.RuleTypeLogical {
		for _, subRule := range rule.LogicalOptions.Rules {
			conditions = ruleAddressConditions(router, subRule, conditions)
		}
		return conditions
	}
	options := rule.DefaultOptions.RawDefaultRule
	if len(options.IPCIDR) > 0 {
		conditions = appendCondition(conditions, "ip_cidr")
	}
	if options.IPIsPrivate {
		conditions = appendCondition(conditions, "ip_is_private")
	}
	if len(options.GeoIP) > 0 {
		conditions = appendCondition(conditions, "geoip")
	}
	if !options.RuleSetIPCIDRMatchSource && !options.Deprecated_RulesetIPCIDRMatchSource {
		for _, tag := range options.RuleSet {
			if ruleSet, loaded := router.RuleSet(tag); loaded && ruleSet.Metadata().ContainsIPCIDRRule {
				conditions = appendCondition(conditions, "rule_set="+tag)
			}
		}
	}
	return conditions
}

func appendCondition(conditions []string, condition string) []string {
	for _, existing := range conditions {
		if existing == condition {
			return conditions
		}
	}
	return append(conditions, condition)
}

// quicRejectedBy names the route rule that rejects udp/443 to the test's host,
// the way a browser's QUIC attempt would be routed (quic sniffed, the host as
// its SNI): rules are matched in order, actions are not applied. A TCP https
// test through the router only; empty otherwise.
func (t *connectionTest) quicRejectedBy() string {
	if t.network != N.NetworkTCP || !t.https || t.port != 443 || t.depth == lxtrace.DepthDNS {
		return ""
	}
	metadata := t.metadata
	metadata.Network = N.NetworkUDP
	metadata.Protocol = C.ProtocolQUIC
	metadata.Destination = M.ParseSocksaddrHostPort(t.host, 443)
	if metadata.Destination.IsFqdn() {
		metadata.Domain = metadata.Destination.Fqdn
	}
	for i, rule := range t.router.Rules() {
		metadata.ResetRuleCache()
		if !rule.Match(&metadata) {
			continue
		}
		action := rule.Action()
		if action == nil {
			continue
		}
		switch typed := action.(type) {
		case *R.RuleActionReject:
			text := "route[" + strconv.Itoa(i) + "]"
			if description := rule.String(); description != "" {
				text += " " + description
			}
			return text + " => " + typed.String()
		case *R.RuleActionBypass:
			if typed.Outbound != "" {
				return ""
			}
		}
		switch action.Type() {
		case C.RuleActionTypeRoute, C.RuleActionTypeHijackDNS:
			return ""
		}
	}
	return ""
}

// fakeIPStore returns the store of the fakeip DNS server, nil without one.
func (t *connectionTest) fakeIPStore() adapter.FakeIPStore {
	if t.instance.ctx == nil {
		return nil
	}
	manager := service.FromContext[adapter.DNSTransportManager](t.instance.ctx)
	if manager == nil {
		return nil
	}
	transport := manager.FakeIP()
	if transport == nil {
		return nil
	}
	return transport.Store()
}

// fakeIPState reads the fakeip store without allocating a record.
func (t *connectionTest) fakeIPState() *lxtrace.FakeIPState {
	store := t.fakeIPStore()
	if store == nil {
		return nil
	}
	storeState, loaded := fakeip.LxStateOf(store)
	if !loaded {
		return nil
	}
	state := &lxtrace.FakeIPState{
		Inet4Range: storeState.Inet4Range,
		Inet6Range: storeState.Inet6Range,
		Persisted:  storeState.Persisted,
		TTL:        C.DefaultDNSTTL * time.Second,
		Inet4Wrap:  lxtrace.FakeIPWrap(storeState.Inet4Wrap),
		Inet6Wrap:  lxtrace.FakeIPWrap(storeState.Inet6Wrap),
	}
	if t.connectIP.IsValid() && store.Contains(t.connectIP) {
		state.ConnectIPDomain, state.ConnectIPKnown = fakeip.LxDomainOf(store, t.connectIP)
		state.HostAddress, _ = fakeip.LxAddressOf(store, t.host, t.connectIP.Is6())
	}
	return state
}

// attrFakeIPRoute marks a route refusal for a fakeip destination the store
// has no record for: the router's "missing fakeip record" (SPEC 139 §8.7).
func (t *connectionTest) attrFakeIPRoute(stage *lxtrace.Stage, destination M.Socksaddr) {
	if stage == nil || !destination.IsIP() {
		return
	}
	store := t.fakeIPStore()
	if store == nil || !store.Contains(destination.Addr) {
		return
	}
	stage.Attr("domain_origin", "fakeip")
	if _, known := fakeip.LxDomainOf(store, destination.Addr); !known {
		stage.Attr("fakeip", "no_record")
	}
}

// nodeTypes maps the tag of every dial and detour stage to its outbound or
// endpoint type.
func (t *connectionTest) nodeTypes(summary lxtrace.Summary) map[string]string {
	types := make(map[string]string)
	for _, stage := range summary.Stages {
		if stage.Kind != stageDial && stage.Kind != stageDetour {
			continue
		}
		if _, known := types[stage.Subject]; known || stage.Subject == "" {
			continue
		}
		if t.instance.outboundManager != nil {
			if outbound, loaded := t.instance.outboundManager.Outbound(stage.Subject); loaded {
				types[stage.Subject] = outbound.Type()
				continue
			}
		}
		if t.instance.endpointManager != nil {
			if endpoint, loaded := t.instance.endpointManager.Get(stage.Subject); loaded {
				types[stage.Subject] = endpoint.Type()
			}
		}
	}
	return types
}

func connectionTestFindingsToProto(findings []lxtrace.Finding) []*ConnectionTestFinding {
	if len(findings) == 0 {
		return nil
	}
	result := make([]*ConnectionTestFinding, 0, len(findings))
	for _, finding := range findings {
		converted := &ConnectionTestFinding{
			Code:   finding.Code,
			Level:  ConnectionTestFindingLevel(finding.Level),
			Stages: finding.Stages,
		}
		if len(finding.Attrs) > 0 {
			converted.Attrs = make([]*ConnectionTestAttr, 0, len(finding.Attrs))
			for _, attr := range finding.Attrs {
				converted.Attrs = append(converted.Attrs, &ConnectionTestAttr{Key: attr.Key, Value: attr.Value})
			}
		}
		result = append(result, converted)
	}
	return result
}
