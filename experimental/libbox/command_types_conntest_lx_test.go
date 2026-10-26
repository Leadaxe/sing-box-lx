package libbox

import (
	"bytes"
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/sagernet/sing-box/daemon"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 139 — conversion of the connection test objects and the stream loop of
// CommandClient.StartConnectionTest against a fake stream.

func TestConnectionTestRequestToGRPC_LX(t *testing.T) {
	t.Parallel()
	headers := NewHTTPHeaders()
	headers.Add("User-Agent", "lx")
	headers.Add("Host", "example.org")

	request := NewConnectionTestRequest()
	request.SetLink("https://www.youtube.com/generate_204")
	request.SetOutboundTag("vless-nl")
	request.SetInbound("tun-in")
	request.SetPackageName("com.android.chrome")
	request.SetUID(10123)
	request.SetProcessPath("/usr/bin/curl")
	request.SetSourceIP("192.168.1.20")
	request.SetSourceMAC("aa:bb:cc:dd:ee:ff")
	request.SetAuthUser("alice")
	request.SetAppDNS(true)
	request.SetConnectIP("198.18.0.42")
	request.SetFresh(true)
	request.SetDepth(ConnectionTestDepthConnect)
	request.SetNetwork("udp")
	request.SetTimeout(20000)
	request.SetMaxBytes(4096)
	request.SetHeaders(headers)
	headers.Add("X-Late", "ignored")

	got := request.toGRPC()
	want := &daemon.ConnectionTestRequest{
		Link:        "https://www.youtube.com/generate_204",
		OutboundTag: "vless-nl",
		Source: &daemon.ConnectionTestSource{
			Inbound:     "tun-in",
			PackageName: "com.android.chrome",
			Uid:         10123,
			ProcessPath: "/usr/bin/curl",
			SourceIP:    "192.168.1.20",
			SourceMAC:   "aa:bb:cc:dd:ee:ff",
			AuthUser:    "alice",
		},
		AppDNS:    true,
		ConnectIP: "198.18.0.42",
		Fresh:     true,
		Depth:     daemon.ConnectionTestDepth_DEPTH_CONNECT,
		Network:   "udp",
		Timeout:   20000,
		MaxBytes:  4096,
		Headers: []*daemon.HttpHeaderPair{
			{Key: "User-Agent", Value: "lx"},
			{Key: "Host", Value: "example.org"},
		},
	}
	if !connectionTestRequestEqual(got, want) {
		t.Fatalf("request:\n got %v\nwant %v", got, want)
	}

	empty := NewConnectionTestRequest()
	empty.SetLink("http://example.org")
	empty.SetTimeout(-1)
	empty.SetMaxBytes(-1)
	empty.SetHeaders(headers)
	empty.SetHeaders(nil)
	got = empty.toGRPC()
	if got.Source != nil || got.Timeout != 0 || got.MaxBytes != 0 || got.Headers != nil || got.Depth != daemon.ConnectionTestDepth_DEPTH_HTTP {
		t.Fatalf("empty request: %v", got)
	}
}

func connectionTestRequestEqual(a, b *daemon.ConnectionTestRequest) bool {
	if a.Link != b.Link || a.OutboundTag != b.OutboundTag || a.AppDNS != b.AppDNS || a.ConnectIP != b.ConnectIP ||
		a.Fresh != b.Fresh || a.Depth != b.Depth || a.Network != b.Network || a.Timeout != b.Timeout || a.MaxBytes != b.MaxBytes {
		return false
	}
	if (a.Source == nil) != (b.Source == nil) || len(a.Headers) != len(b.Headers) {
		return false
	}
	if a.Source != nil {
		as, bs := a.Source, b.Source
		if as.Inbound != bs.Inbound || as.PackageName != bs.PackageName || as.Uid != bs.Uid || as.ProcessPath != bs.ProcessPath ||
			as.SourceIP != bs.SourceIP || as.SourceMAC != bs.SourceMAC || as.AuthUser != bs.AuthUser {
			return false
		}
	}
	for i := range a.Headers {
		if a.Headers[i].Key != b.Headers[i].Key || a.Headers[i].Value != b.Headers[i].Value {
			return false
		}
	}
	return true
}

func TestConnectionTestStageFromGRPC_LX(t *testing.T) {
	t.Parallel()
	stage := connectionTestStageFromGRPC(&daemon.ConnectionTestStage{
		Id: 7, Parent: 3, Kind: "tcp_connect", Subject: "203.0.113.50:8388",
		StartMs: 24, DurationMs: 37, Result: daemon.ConnectionTestStageResult_STAGE_FAILED,
		Error: "connection refused",
		Attrs: []*daemon.ConnectionTestAttr{{Key: "local", Value: "10.0.0.2:51000"}, {Key: "cause", Value: "timeout"}},
	})
	if stage.ID() != 7 || stage.Parent() != 3 || stage.Kind() != "tcp_connect" || stage.Subject() != "203.0.113.50:8388" ||
		stage.StartMs() != 24 || stage.DurationMs() != 37 || stage.Result() != ConnectionTestStageFailed ||
		stage.ErrorMessage() != "connection refused" {
		t.Fatalf("stage: %+v", stage)
	}
	attrs := stage.Attrs()
	if attrs.Len() != 2 {
		t.Fatalf("attrs len %d", attrs.Len())
	}
	if attr := attrs.Next(); attr.Key() != "local" || attr.Value() != "10.0.0.2:51000" {
		t.Fatalf("attr 0: %+v", attr)
	}
	if attr := attrs.Next(); attr.Key() != "cause" || attr.Value() != "timeout" || attrs.HasNext() {
		t.Fatalf("attr 1: %+v", attr)
	}

	bare := connectionTestStageFromGRPC(&daemon.ConnectionTestStage{Id: 1})
	if bare.Result() != ConnectionTestStageRunning || bare.Attrs().HasNext() {
		t.Fatalf("bare stage: %+v", bare)
	}
}

func TestConnectionTestResultFromGRPC_LX(t *testing.T) {
	t.Parallel()
	result := connectionTestResultFromGRPC(&daemon.ConnectionTestResult{
		Error:     "",
		RootCause: 12,
		Findings: []*daemon.ConnectionTestFinding{{
			Code:   "dependency_outside_route",
			Level:  daemon.ConnectionTestFindingLevel_FINDING_PROBLEM,
			Stages: []uint32{4, 9},
			Attrs:  []*daemon.ConnectionTestAttr{{Key: "node", Value: "ss-pl"}},
		}, {
			Code: "partially_reused",
		}},
		HttpStatus:      204,
		Body:            []byte("ok"),
		Truncated:       true,
		ContentType:     "text/plain",
		ElapsedMs:       842,
		StagesTruncated: true,
	})
	if result.ErrorMessage() != "" || result.RootCause() != 12 || result.HTTPStatus() != 204 ||
		!bytes.Equal(result.Body(), []byte("ok")) || !result.Truncated() || result.ContentType() != "text/plain" ||
		result.ElapsedMs() != 842 || !result.StagesTruncated() {
		t.Fatalf("result: %+v", result)
	}
	findings := result.Findings()
	if findings.Len() != 2 {
		t.Fatalf("findings len %d", findings.Len())
	}
	first := findings.Next()
	if first.Code() != "dependency_outside_route" || first.Level() != ConnectionTestFindingProblem {
		t.Fatalf("finding 0: %+v", first)
	}
	stages := first.Stages()
	if stages.Next() != 4 || stages.Next() != 9 || stages.HasNext() {
		t.Fatal("finding stages")
	}
	if attr := first.Attrs().Next(); attr.Key() != "node" || attr.Value() != "ss-pl" {
		t.Fatalf("finding attr: %+v", attr)
	}
	second := findings.Next()
	if second.Code() != "partially_reused" || second.Level() != ConnectionTestFindingInfo || second.Stages().HasNext() || findings.HasNext() {
		t.Fatalf("finding 1: %+v", second)
	}

	failed := connectionTestResultFromGRPC(&daemon.ConnectionTestResult{Error: "busy"})
	if failed.ErrorMessage() != "busy" || failed.Findings().HasNext() || failed.Body() != nil {
		t.Fatalf("failed result: %+v", failed)
	}
}

// The gomobile surface of the new objects stays within what the bridge binds.
func TestConnectionTestGomobileSignatures_LX(t *testing.T) {
	t.Parallel()
	allowed := func(kind reflect.Type) bool {
		switch kind.Kind() {
		case reflect.Int32, reflect.Int64, reflect.Bool, reflect.String, reflect.Interface:
			return true
		case reflect.Slice:
			return kind.Elem().Kind() == reflect.Uint8
		case reflect.Pointer:
			return kind.Elem().Kind() == reflect.Struct
		}
		return false
	}
	for _, value := range []any{
		&ConnectionTestRequest{}, &ConnectionTestStage{}, &ConnectionTestAttr{},
		&ConnectionTestFinding{}, &ConnectionTestResult{}, &ConnectionTestSession{},
	} {
		valueType := reflect.TypeOf(value)
		for i := 0; i < valueType.NumMethod(); i++ {
			method := valueType.Method(i)
			for j := 1; j < method.Type.NumIn(); j++ {
				if !allowed(method.Type.In(j)) {
					t.Errorf("%s.%s takes %s", valueType, method.Name, method.Type.In(j))
				}
			}
			for j := 0; j < method.Type.NumOut(); j++ {
				if !allowed(method.Type.Out(j)) {
					t.Errorf("%s.%s returns %s", valueType, method.Name, method.Type.Out(j))
				}
			}
		}
	}
	if _, isError := any(&ConnectionTestStage{}).(error); isError {
		t.Error("ConnectionTestStage implements error")
	}
	if _, isError := any(&ConnectionTestResult{}).(error); isError {
		t.Error("ConnectionTestResult implements error")
	}
}

type connectionTestStream struct {
	grpc.ClientStream
	ctx    context.Context
	events []*daemon.ConnectionTestProgress
	err    error
}

func (s *connectionTestStream) Recv() (*daemon.ConnectionTestProgress, error) {
	if len(s.events) > 0 {
		event := s.events[0]
		s.events = s.events[1:]
		return event, nil
	}
	if s.err != nil {
		return nil, s.err
	}
	<-s.ctx.Done()
	return nil, status.FromContextError(s.ctx.Err()).Err()
}

type connectionTestClient struct {
	daemon.StartedServiceClient
	request *daemon.ConnectionTestRequest
	events  []*daemon.ConnectionTestProgress
	recvErr error
	openErr error
}

func (c *connectionTestClient) StartConnectionTest(ctx context.Context, request *daemon.ConnectionTestRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[daemon.ConnectionTestProgress], error) {
	c.request = request
	if c.openErr != nil {
		return nil, c.openErr
	}
	return &connectionTestStream{ctx: ctx, events: c.events, err: c.recvErr}, nil
}

type connectionTestRecorder struct {
	access  sync.Mutex
	stages  []*ConnectionTestStage
	results []*ConnectionTestResult
	errors  []string
}

func (r *connectionTestRecorder) OnStage(stage *ConnectionTestStage) {
	r.access.Lock()
	defer r.access.Unlock()
	r.stages = append(r.stages, stage)
}

func (r *connectionTestRecorder) OnResult(result *ConnectionTestResult) {
	r.access.Lock()
	defer r.access.Unlock()
	r.results = append(r.results, result)
}

func (r *connectionTestRecorder) OnError(message string) {
	r.access.Lock()
	defer r.access.Unlock()
	r.errors = append(r.errors, message)
}

func stageEvent(id, parent uint32, result daemon.ConnectionTestStageResult) *daemon.ConnectionTestProgress {
	return &daemon.ConnectionTestProgress{Event: &daemon.ConnectionTestProgress_Stage{Stage: &daemon.ConnectionTestStage{
		Id: id, Parent: parent, Kind: "dial", Result: result,
	}}}
}

func resultEvent(result *daemon.ConnectionTestResult) *daemon.ConnectionTestProgress {
	return &daemon.ConnectionTestProgress{Event: &daemon.ConnectionTestProgress_Result{Result: result}}
}

func TestCommandClientStartConnectionTest_LX(t *testing.T) {
	t.Parallel()
	request := NewConnectionTestRequest()
	request.SetLink("https://example.org")
	request.SetOutboundTag("node")

	t.Run("stages then result", func(t *testing.T) {
		t.Parallel()
		fake := &connectionTestClient{events: []*daemon.ConnectionTestProgress{
			stageEvent(1, 0, daemon.ConnectionTestStageResult_STAGE_RUNNING),
			{},
			stageEvent(1, 0, daemon.ConnectionTestStageResult_STAGE_OK),
			resultEvent(&daemon.ConnectionTestResult{HttpStatus: 204}),
			stageEvent(2, 1, daemon.ConnectionTestStageResult_STAGE_OK),
		}}
		client := NewCommandClient(nil, &CommandClientOptions{})
		client.grpcClient = fake
		recorder := new(connectionTestRecorder)
		session, err := client.StartConnectionTest(request, recorder)
		if err != nil {
			t.Fatal(err)
		}
		<-session.closeDone
		if fake.request.Link != "https://example.org" || fake.request.OutboundTag != "node" {
			t.Fatalf("request: %v", fake.request)
		}
		if len(recorder.stages) != 2 || recorder.stages[0].Result() != ConnectionTestStageRunning || recorder.stages[1].Result() != ConnectionTestStageOK {
			t.Fatalf("stages: %+v", recorder.stages)
		}
		if len(recorder.results) != 1 || recorder.results[0].HTTPStatus() != 204 || len(recorder.errors) != 0 {
			t.Fatalf("results %+v, errors %v", recorder.results, recorder.errors)
		}
		session.Cancel()
		session.Close()
	})

	t.Run("test error arrives in OnResult", func(t *testing.T) {
		t.Parallel()
		fake := &connectionTestClient{events: []*daemon.ConnectionTestProgress{
			resultEvent(&daemon.ConnectionTestResult{Error: "busy"}),
		}}
		client := NewCommandClient(nil, &CommandClientOptions{})
		client.grpcClient = fake
		recorder := new(connectionTestRecorder)
		session, err := client.StartConnectionTest(request, recorder)
		if err != nil {
			t.Fatal(err)
		}
		session.Close()
		if len(recorder.results) != 1 || recorder.results[0].ErrorMessage() != "busy" || len(recorder.errors) != 0 {
			t.Fatalf("results %+v, errors %v", recorder.results, recorder.errors)
		}
	})

	t.Run("transport failure goes to OnError", func(t *testing.T) {
		t.Parallel()
		fake := &connectionTestClient{
			events:  []*daemon.ConnectionTestProgress{stageEvent(1, 0, daemon.ConnectionTestStageResult_STAGE_RUNNING)},
			recvErr: status.Error(codes.Unimplemented, "method StartConnectionTest not implemented"),
		}
		client := NewCommandClient(nil, &CommandClientOptions{})
		client.grpcClient = fake
		recorder := new(connectionTestRecorder)
		session, err := client.StartConnectionTest(request, recorder)
		if err != nil {
			t.Fatal(err)
		}
		<-session.closeDone
		if len(recorder.stages) != 1 || len(recorder.results) != 0 || len(recorder.errors) != 1 {
			t.Fatalf("stages %d, results %d, errors %v", len(recorder.stages), len(recorder.results), recorder.errors)
		}
	})

	t.Run("cancel is silent", func(t *testing.T) {
		t.Parallel()
		fake := &connectionTestClient{events: []*daemon.ConnectionTestProgress{
			stageEvent(1, 0, daemon.ConnectionTestStageResult_STAGE_RUNNING),
		}}
		client := NewCommandClient(nil, &CommandClientOptions{})
		client.grpcClient = fake
		recorder := new(connectionTestRecorder)
		session, err := client.StartConnectionTest(request, recorder)
		if err != nil {
			t.Fatal(err)
		}
		session.Cancel()
		session.Close()
		recorder.access.Lock()
		defer recorder.access.Unlock()
		if len(recorder.results) != 0 || len(recorder.errors) != 0 {
			t.Fatalf("results %+v, errors %v", recorder.results, recorder.errors)
		}
	})

	t.Run("open failure and nil request", func(t *testing.T) {
		t.Parallel()
		fake := &connectionTestClient{openErr: status.Error(codes.Unavailable, "down")}
		client := NewCommandClient(nil, &CommandClientOptions{})
		client.grpcClient = fake
		if session, err := client.StartConnectionTest(request, new(connectionTestRecorder)); session != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("session %v, err %v", session, err)
		}
		if session, err := client.StartConnectionTest(nil, new(connectionTestRecorder)); session != nil || err == nil {
			t.Fatalf("nil request: session %v, err %v", session, err)
		}
	})
}
