package trusttunnel

// lx: SPEC 151 — the sniff decoy on the HTTP/3 session of a trusttunnel
// outbound, through the transport's QUICDialHook.

import (
	"context"
	"net"

	"github.com/sagernet/sing-box/common/lxdatagram"
	"github.com/sagernet/sing-box/transport/trusttunnel"
)

type sniffDecoyHook struct {
	decoy *lxdatagram.Decoy
}

// newSniffDecoyHook returns the hook for decoy, or nil when the policy does
// not cover trusttunnel.
func newSniffDecoyHook(decoy *lxdatagram.Decoy) trusttunnel.QUICDialHook {
	if !decoy.Enabled() {
		return nil
	}
	return sniffDecoyHook{decoy: decoy}
}

func (h sniffDecoyHook) Begin(ctx context.Context) trusttunnel.QUICDialAttempt {
	return sniffDecoyAttempt{h.decoy.Begin(ctx)}
}

type sniffDecoyAttempt struct {
	attempt *lxdatagram.Attempt
}

func (a sniffDecoyAttempt) Context() context.Context { return a.attempt.Context }
func (a sniffDecoyAttempt) Dialed(conn net.Conn)     { a.attempt.Dialed(conn, conn) }
func (a sniffDecoyAttempt) Done(err error) bool      { return a.attempt.Done(err) }
