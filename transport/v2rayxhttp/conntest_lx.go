package v2rayxhttp

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/sagernet/sing-box/common/lxtrace"
	E "github.com/sagernet/sing/common/exceptions"
)

// xhttpTrace is the `transport` stage of one XHTTP dial in a connection test
// (SPEC 139 §4). nil without a trace; every method is nil-safe, so the dial
// paths never check for it.
//
// The stage ends when DialContext returns. The response whose status decides
// the conn (the stream-one POST, the download GET of the split modes) often
// arrives later — the dial does not wait for it (SPEC 002/077) — so the status
// is added to the stage when it is known, and a late refusal (non-200, failed
// round trip) is recorded as a failed `transport_response` child: it ends
// before the read error reaches the stages above, so it is the root cause.
type xhttpTrace struct {
	ctx   context.Context // carries stage as the current stage
	stage *lxtrace.Stage
}

// traceDial opens the stage. Without a trace it costs one ctx.Value lookup.
func (c *Client) traceDial(ctx context.Context) *xhttpTrace {
	if lxtrace.From(ctx) == nil {
		return nil
	}
	stageCtx, stage := lxtrace.Begin(ctx, "transport", "xhttp")
	if stage == nil {
		return nil
	}
	mode := c.resolvedMode()
	stage.Attr("mode", mode)
	if mode == modeStreamOne {
		stage.Attr("method", c.meta.uplinkHTTPMethod)
	} else {
		// the status reported is the download GET's; uploads use the uplink method
		stage.Attr("method", http.MethodGet)
		stage.Attr("upload_method", c.meta.uplinkHTTPMethod)
	}
	stage.Attr("path", c.path)
	stage.Attr("http_version", c.httpVersion.String())
	return &xhttpTrace{ctx: stageCtx, stage: stage}
}

// resolvedMode is the mode dialMode dials: "auto" resolves as there.
func (c *Client) resolvedMode() string {
	if c.mode != modeAuto {
		return c.mode
	}
	if c.realityEnabled {
		return modeStreamOne
	}
	return modePacketUp
}

func (t *xhttpTrace) end(err error) {
	if t == nil {
		return
	}
	t.stage.End(err)
}

// response records the outcome of the round trip that decides the conn.
func (t *xhttpTrace) response(response *http.Response, err error) {
	if t == nil {
		return
	}
	if err == nil {
		t.stage.Attr("http_status", strconv.Itoa(response.StatusCode))
		if response.StatusCode == http.StatusOK {
			return
		}
		err = E.New("unexpected status: ", response.Status)
	} else if errors.Is(err, context.Canceled) {
		// the conn was closed before the response: not a refusal
		return
	}
	_, child := lxtrace.Begin(t.ctx, "transport_response", "xhttp")
	if response != nil {
		child.Attr("http_status", strconv.Itoa(response.StatusCode))
	}
	child.End(err)
}
