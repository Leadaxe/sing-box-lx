//go:build with_lx_command

package masque

// lx: SPEC 139 — the MASQUE stages of a connection test: `auth` with the
// CONNECT-IP status (including a refusal), `quic`, and a REUSED `auth` for a
// dial over the live tunnel.

import (
	"context"
	stdtls "crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/lxtrace"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

func traceCtxLx(fresh bool) (context.Context, *lxtrace.Trace) {
	trace := lxtrace.New(lxtrace.Options{Fresh: fresh, Probe: true})
	return lxtrace.With(context.Background(), trace), trace
}

func onlyStageLx(t *testing.T, trace *lxtrace.Trace) lxtrace.StageSnapshot {
	t.Helper()
	stages := trace.Finish(nil).Stages
	if len(stages) != 1 {
		t.Fatalf("stages = %+v, want one", stages)
	}
	return stages[0]
}

func attrLx(stage lxtrace.StageSnapshot, key string) string {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}

func traceTestOutboundLx() *Outbound {
	return &Outbound{
		server:    M.ParseSocksaddr("162.159.198.1:443"),
		tlsConfig: &stdtls.Config{ServerName: "consumer-masque.cloudflareclient.com"},
	}
}

func TestConnTestAuth_status(t *testing.T) {
	o := traceTestOutboundLx()
	cases := []struct {
		name    string
		err     error
		result  lxtrace.Result
		status  string
		refused string
	}{
		{"accepted", nil, lxtrace.OK, "2xx", ""},
		{"h3 refused", E.Cause(E.New("connect-ip: server responded with ", 403), "masque: dial connect-ip"), lxtrace.Failed, "403", ""},
		{"h2 refused", E.New("connect-ip: server responded with ", 407), lxtrace.Failed, "407", ""},
		{"access denied", E.New("masque: login failed — verify the TLS key/cert is enrolled with Cloudflare Access"), lxtrace.Failed, "", "access_denied"},
		{"no answer", errors.New("masque: CONNECT-IP timed out: timeout: no recent network activity"), lxtrace.Failed, "", ""},
	}
	for _, testCase := range cases {
		ctx, trace := traceCtxLx(false)
		endAuthLx(o.beginAuthLx(ctx, "h3"), testCase.err)
		stage := onlyStageLx(t, trace)
		if stage.Kind != "auth" || stage.Subject != "162.159.198.1:443" || attrLx(stage, "vhttp") != "h3" {
			t.Fatalf("%s: stage = %+v, want auth on the server, vhttp=h3", testCase.name, stage)
		}
		if stage.Result != testCase.result || attrLx(stage, "status") != testCase.status || attrLx(stage, "refused") != testCase.refused {
			t.Fatalf("%s: stage = %+v, want result %v status=%q refused=%q", testCase.name, stage, testCase.result, testCase.status, testCase.refused)
		}
	}
}

func TestConnTestQUIC(t *testing.T) {
	o := traceTestOutboundLx()
	ctx, trace := traceCtxLx(false)
	o.beginQUICLx(ctx).End(context.DeadlineExceeded)
	stage := onlyStageLx(t, trace)
	if stage.Kind != "quic" || stage.Result != lxtrace.Failed || attrLx(stage, "sni") != "consumer-masque.cloudflareclient.com" {
		t.Fatalf("stage = %+v, want FAILED quic with the SNI", stage)
	}
}

// A dial over the live tunnel: REUSED auth with the tunnel's age; under
// fresh it is marked reuse=uncontrolled, without it it is not.
func TestConnTestTunnelReused(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		o := traceTestOutboundLx()
		o.sess = &session{opened: time.Now().Add(-42 * time.Second)}
		ctx, trace := traceCtxLx(fresh)
		s, err := o.ensureSession(ctx)
		if err != nil || s != o.sess {
			t.Fatalf("ensureSession = %v, %v; want the live session", s, err)
		}
		stage := onlyStageLx(t, trace)
		if stage.Kind != "auth" || stage.Result != lxtrace.Reused || attrLx(stage, "age") != "42" || attrLx(stage, "tunnel") != "existing" {
			t.Fatalf("fresh=%v: stage = %+v, want REUSED auth age=42", fresh, stage)
		}
		want := ""
		if fresh {
			want = "uncontrolled"
		}
		if attrLx(stage, "reuse") != want {
			t.Fatalf("fresh=%v: reuse = %q, want %q", fresh, attrLx(stage, "reuse"), want)
		}
	}
}

func TestConnTestNoTraceLx(t *testing.T) {
	o := traceTestOutboundLx()
	ctx := context.Background()
	s := &session{opened: time.Now()}
	if allocs := testing.AllocsPerRun(100, func() {
		o.traceTunnelReusedLx(ctx, s)
		o.beginQUICLx(ctx).End(nil)
		endAuthLx(o.beginAuthLx(ctx, "h2"), nil)
	}); allocs != 0 {
		t.Fatalf("no-trace helpers allocate %v", allocs)
	}
}
