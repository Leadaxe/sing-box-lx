package trusttunnel

import (
	"context"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// ClientTrace is a set of hooks run at the steps of a client request, in the
// manner of net/http/httptrace. Any hook may be nil. A trace is attached to
// the request context with WithClientTrace and read once per request.
type ClientTrace struct {
	// SessionReused is called when the request goes over the live session.
	SessionReused func(ctx context.Context, info SessionInfo)
	// SessionOpen is called before a new session is dialed. The returned
	// context carries the dial; done is called with its outcome.
	SessionOpen func(ctx context.Context, info SessionInfo) (dialCtx context.Context, done func(err error))
	// Connect is called before CONNECT authority is sent; done is called with
	// the response status (0 when no response arrived) and the error.
	Connect func(ctx context.Context, authority string) (done func(status int, err error))
}

// SessionInfo describes the session a request goes over.
type SessionInfo struct {
	Protocol string
	Server   M.Socksaddr
	// Age is the time since a reused session was opened; zero for a new one.
	Age time.Duration
}

type clientTraceKey struct{}

// WithClientTrace returns a context that runs the hooks of trace for the
// requests made with it.
func WithClientTrace(ctx context.Context, trace *ClientTrace) context.Context {
	return context.WithValue(ctx, clientTraceKey{}, trace)
}

// ContextClientTrace returns the trace attached to ctx, or nil.
func ContextClientTrace(ctx context.Context) *ClientTrace {
	trace, _ := ctx.Value(clientTraceKey{}).(*ClientTrace)
	return trace
}

func (t *ClientTrace) sessionReused(ctx context.Context, protocol string, server M.Socksaddr, opened time.Time) {
	if t == nil || t.SessionReused == nil {
		return
	}
	t.SessionReused(ctx, SessionInfo{Protocol: protocol, Server: server, Age: time.Since(opened)})
}

func (t *ClientTrace) sessionOpen(ctx context.Context, protocol string, server M.Socksaddr) (context.Context, func(error)) {
	if t == nil || t.SessionOpen == nil {
		return ctx, ignoreError
	}
	dialCtx, done := t.SessionOpen(ctx, SessionInfo{Protocol: protocol, Server: server})
	if dialCtx == nil {
		dialCtx = ctx
	}
	if done == nil {
		done = ignoreError
	}
	return dialCtx, done
}

func (t *ClientTrace) connect(ctx context.Context, authority string) func(int, error) {
	if t == nil || t.Connect == nil {
		return ignoreStatus
	}
	done := t.Connect(ctx, authority)
	if done == nil {
		return ignoreStatus
	}
	return done
}

func ignoreError(error) {}

func ignoreStatus(int, error) {}
