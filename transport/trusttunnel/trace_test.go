package trusttunnel

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing/common/auth"
	M "github.com/sagernet/sing/common/metadata"
)

type traceEvent struct {
	kind   string
	info   SessionInfo
	target string
	status int
	err    error
}

type traceRecorder struct {
	access sync.Mutex
	events []traceEvent
}

func (r *traceRecorder) add(event traceEvent) {
	r.access.Lock()
	r.events = append(r.events, event)
	r.access.Unlock()
}

func (r *traceRecorder) take() []traceEvent {
	r.access.Lock()
	defer r.access.Unlock()
	events := r.events
	r.events = nil
	return events
}

func (r *traceRecorder) trace() *ClientTrace {
	return &ClientTrace{
		SessionReused: func(ctx context.Context, info SessionInfo) {
			r.add(traceEvent{kind: "reused", info: info})
		},
		SessionOpen: func(ctx context.Context, info SessionInfo) (context.Context, func(error)) {
			return ctx, func(err error) { r.add(traceEvent{kind: "open", info: info, err: err}) }
		},
		Connect: func(ctx context.Context, authority string) func(int, error) {
			return func(status int, err error) {
				r.add(traceEvent{kind: "connect", target: authority, status: status, err: err})
			}
		},
	}
}

func testTrace(t *testing.T, protocol string) {
	server := startTestServer(t, protocol)
	echo := startEchoServer(t)
	recorder := &traceRecorder{}
	ctx := WithClientTrace(testContext(t), recorder.trace())

	client := server.newClient(t, protocol, testPassword, &testDialer{}, nil)
	conn, err := client.DialContext(ctx, echo)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	events := recorder.take()
	if len(events) != 2 || events[0].kind != "open" || events[0].info.Protocol != protocol || events[0].info.Server != server.address || events[0].err != nil {
		t.Fatalf("first dial: %+v, want a new session then CONNECT", events)
	}
	if events[1].kind != "connect" || events[1].target != echo.String() || events[1].status != http.StatusOK || events[1].err != nil {
		t.Fatalf("first dial CONNECT: %+v, want 200", events[1])
	}

	conn, err = client.DialContext(ctx, echo)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	events = recorder.take()
	if len(events) != 2 || events[0].kind != "reused" || events[0].info.Age <= 0 || events[1].status != http.StatusOK {
		t.Fatalf("second dial: %+v, want the reused session with its age, then 200", events)
	}

	// A refused CONNECT reports the status with the error.
	_, err = client.DialContext(ctx, closedPort(t))
	events = recorder.take()
	if err == nil || len(events) != 2 || events[1].status != http.StatusBadGateway || events[1].err == nil {
		t.Fatalf("deferred failure: %v %+v, want 502 with the error", err, events)
	}

	wrong := server.newClient(t, protocol, "wrong", &testDialer{}, nil)
	_, err = wrong.DialContext(ctx, echo)
	events = recorder.take()
	if err == nil || len(events) != 2 || events[1].status != http.StatusProxyAuthRequired || !IsAuthenticationRequired(events[1].err) {
		t.Fatalf("wrong password: %v %+v, want 407", err, events)
	}

	// _check is a CONNECT too.
	if err = client.Check(ctx); err != nil {
		t.Fatal(err)
	}
	events = recorder.take()
	if len(events) != 2 || events[1].target != AuthorityCheck || events[1].status != http.StatusOK {
		t.Fatalf("check: %+v, want CONNECT _check 200", events)
	}

	server.service.UpdateUsers([]auth.User{})
	if err = client.Check(ctx); err == nil {
		t.Fatal("check with a removed user must fail")
	}
	events = recorder.take()
	if len(events) == 0 || events[len(events)-1].kind != "connect" || events[len(events)-1].err == nil {
		t.Fatalf("refused check: %+v, want a failed CONNECT", events)
	}
}

// Without a trace the hooks are no-ops and allocate nothing.
func TestClientTraceNil(t *testing.T) {
	ctx := context.Background()
	if ContextClientTrace(ctx) != nil {
		t.Fatal("no trace expected")
	}
	var trace *ClientTrace
	if allocs := testing.AllocsPerRun(100, func() {
		trace = ContextClientTrace(ctx)
		trace.sessionReused(ctx, ProtocolHTTP2, M.Socksaddr{}, time.Time{})
		dialCtx, done := trace.sessionOpen(ctx, ProtocolHTTP2, M.Socksaddr{})
		done(nil)
		trace.connect(dialCtx, "example.com:443")(http.StatusOK, nil)
	}); allocs != 0 {
		t.Fatalf("nil trace allocates %v", allocs)
	}
	empty := &ClientTrace{}
	dialCtx, done := empty.sessionOpen(ctx, ProtocolHTTP2, M.Socksaddr{})
	done(errors.New("x"))
	empty.connect(dialCtx, "x")(0, nil)
}
