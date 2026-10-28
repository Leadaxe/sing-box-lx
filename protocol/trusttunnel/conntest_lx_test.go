//go:build with_lx_command

package trusttunnel

// lx: SPEC 139 — the TrustTunnel hooks as connection-test stages: the session
// stage by protocol, REUSED with its age (reuse=uncontrolled only under
// fresh), and `auth` with the CONNECT status, a refusal included.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/transport/trusttunnel"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

func traceCtxLx(fresh bool) (context.Context, *lxtrace.Trace) {
	trace := lxtrace.New(lxtrace.Options{Fresh: fresh, Probe: true})
	return lxtrace.With(context.Background(), trace), trace
}

func attrLx(stage lxtrace.StageSnapshot, key string) string {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}

func TestConnTestSessionOpen(t *testing.T) {
	server := M.ParseSocksaddr("203.0.113.9:443")
	for protocol, kind := range map[string]string{trusttunnel.ProtocolHTTP3: "quic", trusttunnel.ProtocolHTTP2: "tls"} {
		ctx, trace := traceCtxLx(false)
		ctx = withConnTestLx(ctx)
		hooks := trusttunnel.ContextClientTrace(ctx)
		if hooks == nil {
			t.Fatal("a traced ctx must carry the hooks")
		}
		dialCtx, done := hooks.SessionOpen(ctx, trusttunnel.SessionInfo{Protocol: protocol, Server: server})
		// The socket dialed for the session nests under it.
		_, socket := lxtrace.Begin(dialCtx, "udp", server.String())
		socket.End(nil)
		done(nil)
		stages := trace.Finish(nil).Stages
		if len(stages) != 2 || stages[0].Kind != kind || stages[0].Result != lxtrace.OK || attrLx(stages[0], "protocol") != protocol || stages[1].Parent != stages[0].ID {
			t.Fatalf("%s: stages = %+v, want OK %s with the socket nested", protocol, stages, kind)
		}
	}
}

func TestConnTestSessionReused(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		ctx, trace := traceCtxLx(fresh)
		ctx = withConnTestLx(ctx)
		trusttunnel.ContextClientTrace(ctx).SessionReused(ctx, trusttunnel.SessionInfo{
			Protocol: trusttunnel.ProtocolHTTP2,
			Server:   M.ParseSocksaddr("203.0.113.9:443"),
			Age:      75 * time.Second,
		})
		stages := trace.Finish(nil).Stages
		if len(stages) != 1 || stages[0].Kind != "tls" || stages[0].Result != lxtrace.Reused || attrLx(stages[0], "age") != "75" || attrLx(stages[0], "session") != "existing" {
			t.Fatalf("fresh=%v: stages = %+v, want REUSED tls age=75", fresh, stages)
		}
		want := ""
		if fresh {
			want = "uncontrolled"
		}
		if attrLx(stages[0], "reuse") != want {
			t.Fatalf("fresh=%v: reuse = %q, want %q", fresh, attrLx(stages[0], "reuse"), want)
		}
	}
}

func TestConnTestAuthStatus(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		err     error
		result  lxtrace.Result
		code    string
		refused string
	}{
		{"accepted", http.StatusOK, nil, lxtrace.OK, "200", ""},
		{"refused credentials", http.StatusProxyAuthRequired, trusttunnel.NewStatusError(http.StatusProxyAuthRequired, nil), lxtrace.Failed, "407", ""},
		{"upstream failure", http.StatusBadGateway, trusttunnel.NewStatusError(http.StatusBadGateway, nil), lxtrace.Failed, "502", ""},
		{"session closed for auth", 0, E.Cause1(trusttunnel.ErrAuthenticationRequired, E.New("GOAWAY")), lxtrace.Failed, "", "authentication_required"},
		{"no answer", 0, E.New("trusttunnel: CONNECT example.com:443: timeout"), lxtrace.Failed, "", ""},
	}
	for _, testCase := range cases {
		ctx, trace := traceCtxLx(false)
		ctx = withConnTestLx(ctx)
		trusttunnel.ContextClientTrace(ctx).Connect(ctx, "example.com:443")(testCase.status, testCase.err)
		stages := trace.Finish(nil).Stages
		if len(stages) != 1 || stages[0].Kind != "auth" || stages[0].Subject != "example.com:443" {
			t.Fatalf("%s: stages = %+v, want one auth", testCase.name, stages)
		}
		stage := stages[0]
		if stage.Result != testCase.result || attrLx(stage, "status") != testCase.code || attrLx(stage, "refused") != testCase.refused {
			t.Fatalf("%s: auth = %+v, want %v status=%q refused=%q", testCase.name, stage, testCase.result, testCase.code, testCase.refused)
		}
	}
}

func TestConnTestNoTraceLx(t *testing.T) {
	ctx := context.Background()
	if allocs := testing.AllocsPerRun(100, func() {
		if withConnTestLx(ctx) != ctx {
			t.Fatal("no trace, no hooks")
		}
	}); allocs != 0 {
		t.Fatalf("withConnTestLx allocates %v without a trace", allocs)
	}
}
