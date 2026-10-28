package trusttunnel

// lx: SPEC 139 — connection-test stages of a TrustTunnel dial, recorded
// through the transport's ClientTrace hooks: the session as `quic` (HTTP/3)
// or `tls` (HTTP/2, the TCP dial nested in it), REUSED with its age when the
// dial goes over the live session, and one `auth` per CONNECT with the
// response status. The test does not open a session of its own (SPEC 139
// §5.6), so a reused one under `fresh` is marked reuse=uncontrolled. Without
// a trace in ctx the hook costs one ctx.Value lookup and is not attached.

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/transport/trusttunnel"
)

// connTestTraceLx is stateless: every hook works on the ctx it is given.
var connTestTraceLx = &trusttunnel.ClientTrace{
	SessionReused: func(ctx context.Context, info trusttunnel.SessionInfo) {
		_, stage := lxtrace.Begin(ctx, sessionKindLx(info.Protocol), info.Server.String())
		stage.Attr("protocol", info.Protocol)
		stage.Attr("session", "existing")
		stage.Attr("age", strconv.FormatInt(int64(info.Age/time.Second), 10))
		if lxtrace.Fresh(ctx) {
			stage.Attr("reuse", "uncontrolled")
		}
		stage.Mark(lxtrace.Reused)
	},
	SessionOpen: func(ctx context.Context, info trusttunnel.SessionInfo) (context.Context, func(error)) {
		stageCtx, stage := lxtrace.Begin(ctx, sessionKindLx(info.Protocol), info.Server.String())
		stage.Attr("protocol", info.Protocol)
		return stageCtx, stage.End
	},
	Connect: func(ctx context.Context, authority string) func(int, error) {
		_, stage := lxtrace.Begin(ctx, "auth", authority)
		return func(status int, err error) {
			if status != 0 {
				stage.Attr("status", strconv.Itoa(status))
			} else if errors.Is(err, trusttunnel.ErrAuthenticationRequired) {
				// The server closed the session with the authentication error
				// code instead of answering 407.
				stage.Attr("refused", "authentication_required")
			}
			stage.End(err)
		}
	},
}

// withConnTestLx attaches the hooks when ctx carries a connection test.
func withConnTestLx(ctx context.Context) context.Context {
	if lxtrace.From(ctx) == nil {
		return ctx
	}
	return trusttunnel.WithClientTrace(ctx, connTestTraceLx)
}

func sessionKindLx(protocol string) string {
	if protocol == trusttunnel.ProtocolHTTP3 {
		return "quic"
	}
	return "tls"
}
