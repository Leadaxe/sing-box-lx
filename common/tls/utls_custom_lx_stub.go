//go:build with_utls && !with_lx_utls_custom

package tls

import (
	"errors"

	utls "github.com/metacubex/utls"
)

func validateLXCustomClientHello([]byte) error {
	return errors.New("custom uTLS ClientHello is not included in this build, rebuild with -tags with_lx_utls_custom")
}

func applyLXCustomClientHello(*utls.UConn, []byte) error {
	return errors.New("custom uTLS ClientHello is not included in this build, rebuild with -tags with_lx_utls_custom")
}
