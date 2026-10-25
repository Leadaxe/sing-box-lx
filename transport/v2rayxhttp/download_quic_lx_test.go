//go:build with_xhttp && with_quic

package v2rayxhttp

import (
	stdTLS "crypto/tls"
	"net"
	"net/http"
	"testing"

	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

func init() {
	newHTTP3DownloadServer = func(t *testing.T, handler http.Handler) (M.Socksaddr, *option.OutboundTLSOptions) {
		t.Helper()
		certificate, hash := testCertificate(t)
		socket, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http3.Server{TLSConfig: http3.ConfigureTLSConfig(&stdTLS.Config{Certificates: []stdTLS.Certificate{*certificate}}), Handler: handler}
		go server.Serve(socket)
		t.Cleanup(func() { server.Close(); socket.Close() })
		return M.SocksaddrFromNet(socket.LocalAddr()), &option.OutboundTLSOptions{Enabled: true, ServerName: "localhost", ALPN: []string{"h3"}, CertificatePublicKeySHA256: [][]byte{hash}}
	}
}
