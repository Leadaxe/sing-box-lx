package shadowtls

import (
	"context"
	"net"
	"time"

	"github.com/sagernet/sing-shadowtls"
)

// boundTLSHandshakeLx gives the ShadowTLS v3 handshake over a plain (non-uTLS)
// TLS config the same ceiling tls.ClientHandshake applies on every other path:
// the config's handshake timeout (C.TCPTimeout unless set). The library's
// DefaultTLSHandshakeFunc runs HandshakeContext on the caller's context alone,
// and routed connections dial without a deadline, so a server that accepts the
// socket and stays silent kept the handshake waiting forever.
//
// The context is only the handshake's: the conn outlives it, and HandshakeContext
// stops watching the context once it returns.
func boundTLSHandshakeLx(handshake shadowtls.TLSHandshakeFunc, timeout time.Duration) shadowtls.TLSHandshakeFunc {
	if timeout <= 0 {
		return handshake
	}
	return func(ctx context.Context, conn net.Conn, sessionIDGenerator shadowtls.TLSSessionIDGeneratorFunc) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return handshake(ctx, conn, sessionIDGenerator)
	}
}
