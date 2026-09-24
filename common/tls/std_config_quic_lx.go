package tls

import (
	E "github.com/sagernet/sing/common/exceptions"
)

// ErrECHOverQUIC is returned by STDConfigForQUIC for a config with ECH.
var ErrECHOverQUIC = E.New("ECH is not supported over HTTP/3")

// lxSTDConvertible is implemented by client configs whose STDConfig() refuses
// but that can still describe themselves as a crypto/tls config (uTLS).
type lxSTDConvertible interface {
	lxSTDConfig() (*STDConfig, error)
}

// STDConfigForQUIC returns a crypto/tls client config for a QUIC dial built from
// config (lx: SPEC 104). It first asks STDConfig(); when that refuses, it unwraps
// kTLS and converts a uTLS config, reporting fromUTLS = true (the fingerprint is
// then not applied). ECH is refused with ErrECHOverQUIC. The returned config is
// a clone: changing it does not touch config.
func STDConfigForQUIC(config Config) (stdConfig *STDConfig, fromUTLS bool, err error) {
	for {
		if kTLSConfig, isKTLS := config.(*KTLSClientConfig); isKTLS {
			config = kTLSConfig.Config
			continue
		}
		break
	}
	if _, isECH := config.(*ECHClientConfig); isECH {
		return nil, false, ErrECHOverQUIC
	}
	if echConfig, isECH := config.(ECHCapableConfig); isECH && len(echConfig.ECHConfigList()) > 0 {
		return nil, false, ErrECHOverQUIC
	}
	stdConfig, err = config.STDConfig()
	if err == nil {
		return stdConfig.Clone(), false, nil
	}
	if convertible, isConvertible := config.(lxSTDConvertible); isConvertible {
		stdConfig, err = convertible.lxSTDConfig()
		if err != nil {
			return nil, false, err
		}
		return stdConfig, true, nil
	}
	return nil, false, err
}
