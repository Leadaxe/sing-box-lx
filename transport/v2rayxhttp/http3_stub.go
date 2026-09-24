//go:build !with_xhttp || !with_quic

package v2rayxhttp

import (
	"time"

	"github.com/sagernet/sing-box/common/tls"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lx: SPEC 104 — without quic-go a config the version rule sends to HTTP/3 is
// refused at load.
func newHTTP3Transport(N.Dialer, M.Socksaddr, tls.Config, time.Duration, func(string)) (func() xmuxConn, error) {
	return nil, E.New(`v2ray-xhttp: HTTP/3 (tls.alpn ["h3"]) requires the with_quic build tag`)
}

func hideQUICError(err error) error {
	return err
}
