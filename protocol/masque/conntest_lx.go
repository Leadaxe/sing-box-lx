package masque

// lx: SPEC 139 — connection-test stages of the MASQUE tunnel: `quic` for the
// h3 handshake and `auth` for the CONNECT-IP request with the response
// status. The h2 leg's TCP and TLS stages come from the dialer and the shared
// TLS client. A dial over the live tunnel records one REUSED `auth` with the
// tunnel's age: every dial shares the tunnel, and the test does not open a
// tunnel of its own (SPEC 139 §5.6), so under `fresh` it is marked
// reuse=uncontrolled. Without a trace in ctx each helper costs one ctx.Value
// lookup and records nothing.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/common/lxtrace"
)

// traceTunnelReusedLx records a dial that goes through the live tunnel s.
func (o *Outbound) traceTunnelReusedLx(ctx context.Context, s *session) {
	if lxtrace.From(ctx) == nil {
		return
	}
	_, stage := lxtrace.Begin(ctx, "auth", o.server.String())
	stage.Attr("tunnel", "existing")
	stage.Attr("age", strconv.FormatInt(int64(time.Since(s.opened)/time.Second), 10))
	if lxtrace.Fresh(ctx) {
		stage.Attr("reuse", "uncontrolled")
	}
	stage.Mark(lxtrace.Reused)
}

// beginQUICLx opens the `quic` stage of the h3 handshake; nil without a trace.
func (o *Outbound) beginQUICLx(ctx context.Context) *lxtrace.Stage {
	if lxtrace.From(ctx) == nil {
		return nil
	}
	_, stage := lxtrace.Begin(ctx, "quic", o.server.String())
	stage.Attr("sni", o.tlsConfig.ServerName)
	return stage
}

// beginAuthLx opens the `auth` stage of the CONNECT-IP request on the vhttp
// leg (h3 or h2); nil without a trace.
func (o *Outbound) beginAuthLx(ctx context.Context, vhttp string) *lxtrace.Stage {
	if lxtrace.From(ctx) == nil {
		return nil
	}
	_, stage := lxtrace.Begin(ctx, "auth", o.server.String())
	stage.Attr("vhttp", vhttp)
	return stage
}

// endAuthLx closes the `auth` stage with the response status. transport/masque
// accepts any 2xx without reporting the code, so success reads status=2xx; a
// refusal carries the code from its error, and a Cloudflare Access login
// failure (a TLS alert, no HTTP answer) reads refused=access_denied.
func endAuthLx(stage *lxtrace.Stage, err error) {
	if stage == nil {
		return
	}
	if err == nil {
		stage.Attr("status", "2xx")
		stage.End(nil)
		return
	}
	text := err.Error()
	if _, status, found := strings.Cut(text, "server responded with "); found {
		if end := strings.IndexFunc(status, func(r rune) bool { return r < '0' || r > '9' }); end >= 0 {
			status = status[:end]
		}
		if status != "" {
			stage.Attr("status", status)
		}
	} else if strings.Contains(text, "masque: login failed") {
		stage.Attr("refused", "access_denied")
	}
	stage.End(err)
}
