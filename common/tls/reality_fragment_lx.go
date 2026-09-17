//go:build with_utls

package tls

import (
	"net"

	tf "github.com/sagernet/sing-box/common/tlsfragment"
)

// wrapRealityClientHelloFragment applies the fragmentation flags already
// parsed by newUTLSClient. REALITY creates utls.UClient itself instead of using
// UTLSClientConfig.Client, so it must perform the same transport wrapping.
// lx: SPEC 060.
func wrapRealityClientHelloFragment(conn net.Conn, client *UTLSClientConfig) net.Conn {
	if client.fragment || client.recordFragment {
		return tf.NewConn(conn, client.ctx, client.fragment, client.recordFragment, client.fragmentFallbackDelay)
	}
	return conn
}
