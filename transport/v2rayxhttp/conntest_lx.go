package v2rayxhttp

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

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

// requestContext is the parent of the conn-scoped request context (SPEC 072):
// parent itself without a trace. With one it is parent — the transport
// lifetime, so cancellation stays the pool's — plus the trace with the
// `transport` stage as the current one. The HTTP layer dials a missing
// connection on the context of the request that needs it (x/net http2
// dialCall, net/http dialConnFor; HTTP/3 alike), so the TCP and TLS dials of
// that connection become children of this stage. Only the requests of this
// conn carry it: a later dial of the same pooled transport made for another
// conn is that conn's.
func (t *xhttpTrace) requestContext(parent context.Context) context.Context {
	if t == nil {
		return parent
	}
	return lxtrace.WithStage(lxtrace.With(parent, lxtrace.From(t.ctx)), t.stage)
}

// acquire takes the pooled connection for one dial. Without a trace it is
// c.xmux.getContext. In a connection test:
//
//   - fresh=true takes a connection outside the pool, so nothing the test
//     opened is ever handed to a production dial (SPEC 139 §5.6, §5.14). It is
//     born retired: the dial's single release (conn Close/fail, or the dial's
//     error path) tears it down — the deferred close of xmuxClient. It has no
//     manager, so its breaker cannot arm the pool's backoff either;
//   - a pooled connection handed out again opens no TCP or TLS, which a REUSED
//     stage with its age says instead of a missing or zero-length one (§5.3).
//
// Without fresh a probe uses the pool like any dial.
func (t *xhttpTrace) acquire(ctx context.Context, c *Client) (*xmuxClient, error) {
	if t == nil {
		return c.xmux.getContext(ctx)
	}
	if lxtrace.Fresh(ctx) {
		t.stage.Attr("pool", "detached")
		return c.xmux.newDetachedClient(), nil
	}
	client, created, err := c.xmux.acquireContext(ctx)
	if err == nil && !created {
		t.reused(c, client)
	}
	return client, err
}

// reused records the handshake a pooled transport did not repeat. HTTP/1.1
// gets none: its long requests (download GET, streamed bodies) each dial their
// own connection (SPEC 104), and that dial is traced for real. On HTTP/2 a
// connection the transport lost is redialed silently and then traced too, next
// to this stage. HTTP/3 reuses a QUIC connection, hence the `quic` kind.
func (t *xhttpTrace) reused(c *Client, client *xmuxClient) {
	kind := "tls"
	switch c.httpVersion {
	case httpVersion11:
		return
	case httpVersion3:
		kind = "quic"
	}
	_, stage := lxtrace.Begin(t.ctx, kind, c.serverName)
	stage.Attr("reuse", "pooled")
	stage.Attr("age", strconv.FormatInt(int64(timeNow().Sub(client.createdAt)/time.Second), 10))
	stage.Mark(lxtrace.Reused)
}

// newDetachedClient opens a connection that never enters the pool, for a
// connection-test dial with fresh=true (see xhttpTrace.acquire).
func (m *xmuxManager) newDetachedClient() *xmuxClient {
	return &xmuxClient{
		conn:         m.newConn(),
		leftUsage:    -1,
		leftRequests: maxInt32,
		createdAt:    timeNow(),
		closed:       true,
	}
}
