//go:build with_lx_command

package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/certificate"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	boxservice "github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport/local"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing-box/protocol/group"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"

	"google.golang.org/grpc"
)

// lx: SPECS/TASKS/139-CONNECTION_TEST — handler contract tests. Through a
// node they reuse the 058 probe stand (a dialer counted per call); through
// the router they build a real box with a direct outbound and a reject rule,
// so the route stage, the pipe and the first-hop dial are the production
// ones. The transports themselves (VLESS, WG, …) are stand work.

// recordingStream is the gRPC stream of a test: it records every message and
// is cancelled through its context.
type recordingStream struct {
	grpc.ServerStream
	ctx    context.Context
	access sync.Mutex
	events []*ConnectionTestProgress
}

func newRecordingStream(ctx context.Context) *recordingStream {
	return &recordingStream{ctx: ctx}
}

func (s *recordingStream) Context() context.Context { return s.ctx }

func (s *recordingStream) Send(event *ConnectionTestProgress) error {
	s.access.Lock()
	defer s.access.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *recordingStream) stages() []*ConnectionTestStage {
	s.access.Lock()
	defer s.access.Unlock()
	var stages []*ConnectionTestStage
	for _, event := range s.events {
		if stage := event.GetStage(); stage != nil {
			stages = append(stages, stage)
		}
	}
	return stages
}

// finalStages returns the last snapshot of every stage, in id order.
func (s *recordingStream) finalStages() []*ConnectionTestStage {
	byID := make(map[uint32]*ConnectionTestStage)
	var order []uint32
	for _, stage := range s.stages() {
		if _, seen := byID[stage.Id]; !seen {
			order = append(order, stage.Id)
		}
		byID[stage.Id] = stage
	}
	result := make([]*ConnectionTestStage, 0, len(order))
	for _, id := range order {
		result = append(result, byID[id])
	}
	return result
}

func (s *recordingStream) result() *ConnectionTestResult {
	s.access.Lock()
	defer s.access.Unlock()
	if len(s.events) == 0 {
		return nil
	}
	return s.events[len(s.events)-1].GetResult()
}

func kindsOf(stages []*ConnectionTestStage) []string {
	kinds := make([]string, 0, len(stages))
	for _, stage := range stages {
		kinds = append(kinds, stage.Kind)
	}
	return kinds
}

func attrOf(stage *ConnectionTestStage, key string) string {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}

func findStage(stages []*ConnectionTestStage, kind string) *ConnectionTestStage {
	for _, stage := range stages {
		if stage.Kind == kind {
			return stage
		}
	}
	return nil
}

// Through a node (DEPTH_HTTP): the dial is a top-level stage, the exchange
// stages follow it, the result carries the HTTP answer.
func TestStartConnectionTest_ViaNode_HTTP_LX(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	dialer := &probeDialer{}
	service := newProbeService(dialer)
	stream := newRecordingStream(context.Background())
	err := service.StartConnectionTest(&ConnectionTestRequest{Link: server.URL, OutboundTag: "node"}, stream)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	result := stream.result()
	if result == nil {
		t.Fatal("the stream must end with a result")
	}
	if result.Error != "" {
		t.Fatalf("result.error = %q, want empty", result.Error)
	}
	if dialer.dials != 1 {
		t.Fatalf("dials = %d, want 1 through the node", dialer.dials)
	}
	if result.HttpStatus != http.StatusNoContent {
		t.Fatalf("httpStatus = %d, want 204", result.HttpStatus)
	}
	if result.RootCause != 0 {
		t.Fatalf("rootCause = %d, want 0 on a completed path", result.RootCause)
	}
	stages := stream.finalStages()
	kinds := strings.Join(kindsOf(stages), ",")
	if kinds != "dial,first_byte,http" {
		t.Fatalf("stage kinds = %s, want dial,first_byte,http", kinds)
	}
	for _, stage := range stages {
		if stage.Result != ConnectionTestStageResult_STAGE_OK {
			t.Fatalf("stage %s result = %v, want OK", stage.Kind, stage.Result)
		}
		if stage.Parent != 0 {
			t.Fatalf("stage %s parent = %d, want top level", stage.Kind, stage.Parent)
		}
	}
	dial := stages[0]
	if dial.Subject != "node" || attrOf(dial, "type") != "probe" {
		t.Fatalf("dial stage = %v, want subject node, type probe", dial)
	}
	if attrOf(stages[2], "status") != "204" {
		t.Fatalf("http stage status attr = %q, want 204", attrOf(stages[2], "status"))
	}
	// Every stage is streamed twice: RUNNING, then its end, with the same id.
	all := stream.stages()
	if len(all) != 2*len(stages) {
		t.Fatalf("streamed %d stage events for %d stages, want begin+end each", len(all), len(stages))
	}
	if all[0].Result != ConnectionTestStageResult_STAGE_RUNNING || all[0].Id != all[1].Id {
		t.Fatalf("first events = %v / %v, want RUNNING then end with the same id", all[0], all[1])
	}
}

// Through a node to an https target whose certificate is not trusted: the
// path is fine, the exchange fails at target_tls, and that is the root cause;
// the stages after it are SKIPPED, not FAILED.
func TestStartConnectionTest_ViaNode_TargetTLSFailure_LX(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {}))
	defer server.Close()

	service := newProbeService(&probeDialer{})
	stream := newRecordingStream(context.Background())
	err := service.StartConnectionTest(&ConnectionTestRequest{Link: server.URL, OutboundTag: "node"}, stream)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	result := stream.result()
	if result == nil || result.Error != "" {
		t.Fatalf("result = %v, want a result without error", result)
	}
	stages := stream.finalStages()
	kinds := strings.Join(kindsOf(stages), ",")
	if kinds != "dial,target_tls,first_byte,http" {
		t.Fatalf("stage kinds = %s, want dial,target_tls,first_byte,http", kinds)
	}
	tlsStage := stages[1]
	if tlsStage.Result != ConnectionTestStageResult_STAGE_FAILED || tlsStage.Error == "" {
		t.Fatalf("target_tls = %v, want FAILED with the certificate error", tlsStage)
	}
	if result.RootCause != tlsStage.Id {
		t.Fatalf("rootCause = %d, want the target_tls stage %d", result.RootCause, tlsStage.Id)
	}
	for _, stage := range stages[2:] {
		if stage.Result != ConnectionTestStageResult_STAGE_SKIPPED {
			t.Fatalf("stage %s after the failure = %v, want SKIPPED", stage.Kind, stage.Result)
		}
	}
	if result.HttpStatus != 0 || len(result.Body) != 0 {
		t.Fatal("a broken path must leave the HTTP fields empty")
	}
}

// A node whose dial fails: the dial stage is the root cause and the HTTP
// stages are SKIPPED placeholders.
func TestStartConnectionTest_ViaNode_DialFailure_LX(t *testing.T) {
	service := newProbeService(&probeDialer{fail: errors.New("connection refused")})
	stream := newRecordingStream(context.Background())
	err := service.StartConnectionTest(&ConnectionTestRequest{Link: "http://example.invalid/", OutboundTag: "node"}, stream)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	result := stream.result()
	stages := stream.finalStages()
	kinds := strings.Join(kindsOf(stages), ",")
	if kinds != "dial,first_byte,http" {
		t.Fatalf("stage kinds = %s, want dial,first_byte,http", kinds)
	}
	if stages[0].Result != ConnectionTestStageResult_STAGE_FAILED || !strings.Contains(stages[0].Error, "connection refused") {
		t.Fatalf("dial = %v, want FAILED connection refused", stages[0])
	}
	if result.RootCause != stages[0].Id {
		t.Fatalf("rootCause = %d, want dial %d", result.RootCause, stages[0].Id)
	}
	if stages[1].Result != ConnectionTestStageResult_STAGE_SKIPPED || stages[2].Result != ConnectionTestStageResult_STAGE_SKIPPED {
		t.Fatal("stages after a failed dial must be SKIPPED")
	}
}

// Invalid requests are one result with error and no stages (§3.2).
func TestStartConnectionTest_InvalidRequest_LX(t *testing.T) {
	service := newProbeService(&probeDialer{})
	for name, request := range map[string]*ConnectionTestRequest{
		"malformed url":        {Link: "::not a url", OutboundTag: "node"},
		"scheme":               {Link: "ftp://example.invalid", OutboundTag: "node"},
		"unknown node":         {Link: "http://example.invalid", OutboundTag: "missing"},
		"udp beyond route":     {Link: "http://example.invalid", OutboundTag: "node", Network: "udp"},
		"bad connectIP":        {Link: "http://example.invalid", OutboundTag: "node", ConnectIP: "nope"},
		"no router":            {Link: "http://example.invalid"},
		"dns depth needs dns":  {Link: "http://example.invalid", OutboundTag: "node", Depth: ConnectionTestDepth_DEPTH_DNS},
		"appDNS without a dns": {Link: "http://example.invalid", OutboundTag: "node", AppDNS: true},
	} {
		t.Run(name, func(t *testing.T) {
			stream := newRecordingStream(context.Background())
			err := service.StartConnectionTest(request, stream)
			if err != nil {
				t.Fatalf("transport error: %v", err)
			}
			if len(stream.events) != 1 {
				t.Fatalf("events = %d, want exactly one result", len(stream.events))
			}
			result := stream.result()
			if result == nil || result.Error == "" {
				t.Fatalf("result = %v, want an error", result)
			}
		})
	}
}

// Not STARTED is a transport error, as for URLTestOutbound.
func TestStartConnectionTest_NotStarted_LX(t *testing.T) {
	service := &StartedService{serviceStatus: &ServiceStatus{Status: ServiceStatus_IDLE}}
	err := service.StartConnectionTest(&ConnectionTestRequest{Link: "http://example.invalid"}, newRecordingStream(context.Background()))
	if err == nil {
		t.Fatal("expected a transport error when the service is not started")
	}
}

// The fifth concurrent test is refused with "busy" (§5.10).
func TestStartConnectionTest_Busy_LX(t *testing.T) {
	block := make(chan struct{})
	// One service (and dialer) per goroutine: the slots are package-level,
	// the probe dialer's counter is not concurrency-safe.
	service := newProbeService(&probeDialer{block: block})

	var running sync.WaitGroup
	for i := 0; i < connectionTestConcurrency; i++ {
		running.Add(1)
		go func() {
			defer running.Done()
			stream := newRecordingStream(context.Background())
			newProbeService(&probeDialer{block: block}).StartConnectionTest(&ConnectionTestRequest{Link: "http://example.invalid/", OutboundTag: "node", Timeout: 5000}, stream)
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(connectionTestSlots) < connectionTestConcurrency {
		if time.Now().After(deadline) {
			t.Fatal("the four tests did not take their slots")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stream := newRecordingStream(context.Background())
	err := service.StartConnectionTest(&ConnectionTestRequest{Link: "http://example.invalid/", OutboundTag: "node"}, stream)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if result := stream.result(); result == nil || result.Error != "busy" {
		t.Fatalf("result = %v, want busy", result)
	}
	close(block)
	running.Wait()
	if len(connectionTestSlots) != 0 {
		t.Fatalf("slots still held after the tests ended: %d", len(connectionTestSlots))
	}
}

// Cancellation by the client closes the connections, sends no result and
// leaves no goroutine behind.
func TestStartConnectionTest_CancelLeavesNothing_LX(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	service := newProbeService(&probeDialer{block: block})

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	stream := newRecordingStream(ctx)
	done := make(chan error, 1)
	go func() {
		done <- service.StartConnectionTest(&ConnectionTestRequest{Link: "http://example.invalid/", OutboundTag: "node"}, stream)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if stage := findStage(stream.stages(), "dial"); stage != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the dial stage never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handler returned %v on cancellation, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not return after cancellation")
	}
	if stream.result() != nil {
		t.Fatal("a cancelled test must not send a result")
	}
	for i := 0; i < 200; i++ {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines: %d before, %d after settling", before, runtime.NumGoroutine())
}

// routerService builds a real box (direct outbound, a reject rule for
// reject.test, final → direct) and a STARTED service over it, so the router
// path is exercised against the production route code.
func routerService(t *testing.T) (*StartedService, func()) {
	t.Helper()
	// include.Context would import service/api → daemon (an import cycle in a
	// daemon test), so the registries are the minimum the stand needs.
	outboundRegistry := outbound.NewRegistry()
	direct.RegisterOutbound(outboundRegistry)
	group.RegisterSelector(outboundRegistry)
	dnsRegistry := dns.NewTransportRegistry()
	local.RegisterTransport(dnsRegistry)
	ctx := box.Context(context.Background(), inbound.NewRegistry(), outboundRegistry, endpoint.NewRegistry(), dnsRegistry, boxservice.NewRegistry(), certificate.NewRegistry())
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Log: &option.LogOptions{Disabled: true},
			Outbounds: []option.Outbound{
				{Type: C.TypeDirect, Tag: "direct"},
				{Type: C.TypeSelector, Tag: "sel", Options: &option.SelectorOutboundOptions{Outbounds: []string{"direct"}}},
			},
			Route: &option.RouteOptions{
				Final: "sel",
				Rules: []option.Rule{{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						RawDefaultRule: option.RawDefaultRule{Domain: []string{"reject.test"}},
						RuleAction:     option.RuleAction{Action: C.RuleActionTypeReject, RejectOptions: option.RejectActionOptions{Method: C.RuleActionRejectMethodDefault}},
					},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("box.New: %v", err)
	}
	if err := instance.Start(); err != nil {
		t.Fatalf("box.Start: %v", err)
	}
	service := &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance: &Instance{
			ctx:                   ctx,
			urlTestHistoryStorage: urltest.NewHistoryStorage(),
			outboundManager:       service.FromContext[adapter.OutboundManager](ctx),
			endpointManager:       service.FromContext[adapter.EndpointManager](ctx),
			router:                service.FromContext[adapter.Router](ctx),
		},
	}
	return service, func() { instance.Close() }
}

// Through the router at DEPTH_ROUTE: the route stage is recorded with the
// rule, the selector is unfolded, nothing is dialed, and the result is clean.
func TestStartConnectionTest_ViaRouter_DepthRoute_LX(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer server.Close()
	service, closeBox := routerService(t)
	defer closeBox()

	stream := newRecordingStream(context.Background())
	err := service.StartConnectionTest(&ConnectionTestRequest{Link: server.URL, Depth: ConnectionTestDepth_DEPTH_ROUTE}, stream)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	result := stream.result()
	if result == nil || result.Error != "" {
		t.Fatalf("result = %v, want a clean result", result)
	}
	stages := stream.finalStages()
	route := findStage(stages, "route")
	if route == nil {
		t.Fatalf("no route stage in %v", kindsOf(stages))
	}
	if route.Result != ConnectionTestStageResult_STAGE_OK || attrOf(route, "rule") != "final" || attrOf(route, "outbound") != "sel" {
		t.Fatalf("route stage = %v, want OK final → sel", route)
	}
	if group := findStage(stages, "group"); group == nil || attrOf(group, "selected") != "direct" {
		t.Fatalf("selector unfold missing or wrong: %v", group)
	}
	if findStage(stages, "dial") != nil || findStage(stages, "http") != nil {
		t.Fatalf("DEPTH_ROUTE must not dial: %v", kindsOf(stages))
	}
	if hits != 0 {
		t.Fatalf("the target was reached %d times at DEPTH_ROUTE", hits)
	}
	if result.RootCause != 0 {
		t.Fatalf("rootCause = %d, want 0", result.RootCause)
	}
}

// Through the router, a reject rule: the router refuses before the route
// decision is traced, the handler records the refusal as a FAILED route
// stage, and that stage is the root cause.
func TestStartConnectionTest_ViaRouter_Reject_LX(t *testing.T) {
	service, closeBox := routerService(t)
	defer closeBox()

	for _, depth := range []ConnectionTestDepth{ConnectionTestDepth_DEPTH_ROUTE, ConnectionTestDepth_DEPTH_HTTP} {
		t.Run(depth.String(), func(t *testing.T) {
			stream := newRecordingStream(context.Background())
			err := service.StartConnectionTest(&ConnectionTestRequest{Link: "http://reject.test/", Depth: depth}, stream)
			if err != nil {
				t.Fatalf("transport error: %v", err)
			}
			result := stream.result()
			if result == nil || result.Error != "" {
				t.Fatalf("result = %v, want a result without a call error", result)
			}
			route := findStage(stream.finalStages(), "route")
			if route == nil || route.Result != ConnectionTestStageResult_STAGE_FAILED {
				t.Fatalf("route stage = %v, want FAILED", route)
			}
			if result.RootCause != route.Id {
				t.Fatalf("rootCause = %d, want route %d", result.RootCause, route.Id)
			}
			for _, stage := range stream.finalStages() {
				if stage.Kind != "route" && stage.Result == ConnectionTestStageResult_STAGE_FAILED {
					t.Fatalf("stage %s is FAILED after the refusal, want SKIPPED", stage.Kind)
				}
			}
		})
	}
}

// Through the router at DEPTH_HTTP: the route decision, the pipe and the
// direct outbound carry the request to the target.
func TestStartConnectionTest_ViaRouter_HTTP_LX(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Write([]byte("through-the-router"))
	}))
	defer server.Close()
	service, closeBox := routerService(t)
	defer closeBox()

	stream := newRecordingStream(context.Background())
	err := service.StartConnectionTest(&ConnectionTestRequest{
		Link:   server.URL,
		Source: &ConnectionTestSource{SourceIP: "192.0.2.10", ProcessPath: "/usr/bin/curl"},
	}, stream)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	result := stream.result()
	if result == nil || result.Error != "" {
		t.Fatalf("result = %v, want a clean result", result)
	}
	if result.HttpStatus != http.StatusOK || string(result.Body) != "through-the-router" {
		t.Fatalf("result = %d %q, want 200 through-the-router", result.HttpStatus, result.Body)
	}
	stages := stream.finalStages()
	route := findStage(stages, "route")
	if route == nil || attrOf(route, "outbound") != "sel" || attrOf(route, "process") != "/usr/bin/curl" {
		t.Fatalf("route stage = %v, want outbound sel with the given process", route)
	}
	if !strings.HasPrefix(attrOf(route, "source"), "192.0.2.10:") {
		t.Fatalf("route source = %q, want the given source IP", attrOf(route, "source"))
	}
	httpStage := findStage(stages, "http")
	if httpStage == nil || httpStage.Result != ConnectionTestStageResult_STAGE_OK || attrOf(httpStage, "status") != "200" {
		t.Fatalf("http stage = %v, want OK 200", httpStage)
	}
	if result.RootCause != 0 {
		t.Fatalf("rootCause = %d, want 0", result.RootCause)
	}
}

// UDP at DEPTH_ROUTE: the packet pipe reaches the route decision without a
// payload and the route stage names the outbound.
func TestStartConnectionTest_ViaRouter_UDPRoute_LX(t *testing.T) {
	service, closeBox := routerService(t)
	defer closeBox()

	stream := newRecordingStream(context.Background())
	err := service.StartConnectionTest(&ConnectionTestRequest{
		Link:    "https://udp.test/",
		Network: "udp",
		Depth:   ConnectionTestDepth_DEPTH_ROUTE,
		Timeout: 5000,
	}, stream)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	result := stream.result()
	if result == nil || result.Error != "" {
		t.Fatalf("result = %v, want a clean result", result)
	}
	route := findStage(stream.finalStages(), "route")
	if route == nil || attrOf(route, "network") != "udp" || attrOf(route, "outbound") != "sel" {
		t.Fatalf("route stage = %v, want udp → sel", route)
	}
}

// The idle packet conn honours read deadlines and unblocks on Close.
func TestIdlePacketConn_DeadlineAndClose_LX(t *testing.T) {
	conn := newIdlePacketConn(M.SocksaddrFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 1))
	conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	_, err := conn.ReadPacket(nil)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read with a past deadline = %v, want a deadline error", err)
	}
	conn.SetReadDeadline(time.Time{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		conn.Close()
	}()
	_, err = conn.ReadPacket(nil)
	if err == nil {
		t.Fatal("read after Close must fail")
	}
	if addr := conn.LocalAddr(); addr == nil {
		t.Fatal("LocalAddr must not be nil")
	}
}
