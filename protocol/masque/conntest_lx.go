package masque

// lx: SPEC 139 — connection-test stage of a dial through the masque-client
// endpoint. The tunnel is opened by the endpoint's own loop, not by the dial,
// so a test sees one `auth` stage:
//
//   - the tunnel is up: REUSED, with its age and HTTP version. The test does
//     not open a tunnel of its own (SPEC 139 §5.6), so under `fresh` the stage
//     is marked reuse=uncontrolled.
//   - the tunnel is not up: the stage spans the wait for it (an on_demand
//     endpoint is woken by the dial) and ends with the endpoint's last connect
//     error or with the version the tunnel came up on. The QUIC handshake and
//     the CONNECT request run inside the loop and are not split out here.
//
// Without a trace in ctx this costs one ctx.Value lookup.

import (
	"context"
	"strconv"
	"time"

	"github.com/sagernet/sing-box/common/lxtrace"
)

func (c *ClientEndpoint) waitReadyTracedLx(ctx context.Context) error {
	if lxtrace.From(ctx) == nil {
		return c.waitReady(ctx)
	}
	_, stage := lxtrace.Begin(ctx, "auth", c.Tag())
	if c.client.Ready() {
		if age, vhttp, ok := c.client.TunnelInfoLx(); ok {
			stage.Attr("vhttp", vhttp)
			stage.Attr("age", strconv.FormatInt(int64(age/time.Second), 10))
		}
		stage.Attr("tunnel", "existing")
		if lxtrace.Fresh(ctx) {
			stage.Attr("reuse", "uncontrolled")
		}
		stage.Mark(lxtrace.Reused)
		return nil
	}
	stage.Attr("tunnel", "new")
	err := c.waitReady(ctx)
	if err == nil {
		if _, vhttp, ok := c.client.TunnelInfoLx(); ok {
			stage.Attr("vhttp", vhttp)
		}
	}
	stage.End(err)
	return err
}
