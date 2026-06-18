//go:build with_awg && with_utls

// Browser-fingerprinted ClientHello for ip=quic + ib=chrome|firefox, built with
// uTLS (github.com/metacubex/utls) so the decoy carries a real browser's JA3/JA4
// instead of our generic ClientHello. Only compiled with the with_utls tag; the
// stub file handles the !with_utls case (falls back to the generic CH).
//
// We drive uTLS in QUIC mode (UQUICClient) so the emitted ClientHello is a QUIC
// ClientHello — it carries quic_transport_params and we force ALPN h3, unlike a
// TCP-TLS ClientHello. The post-quantum hybrid key_share (X25519MLKEM768) that a
// 2024+ browser sends is ~1.2 KB and would not fit one QUIC Initial, so we strip
// it (the reality client does the same) and pin a pre-PQ fingerprint version
// (Chrome 120 / Firefox 120, ~550–620 B). Honest consequence: the JA3 matches a
// late-2023 browser, not a current PQ-enabled one.
//
// NOTE: this is a forward-looking knob against a future JA3/JA4-classifying DPI.
// On the DPI this feature targets, ip=quic already passes on fragmentation alone
// (no fingerprint check), so the default ib="" keeps the device-proven generic
// ~294 B ClientHello; ib=chrome/firefox opts into the larger uTLS one, whose
// different size re-shapes the fragmentation and is not itself device-tested.
package wireguard

import (
	"context"

	utls "github.com/metacubex/utls"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
)

// browserHelloID maps the ib hint to a uTLS fingerprint pinned to a pre-PQ
// version (so the ClientHello fits a single QUIC Initial after PQ stripping).
func browserHelloID(browser string) (utls.ClientHelloID, bool) {
	switch browser {
	case masqueBrowserChrome:
		return utls.HelloChrome_120, true
	case masqueBrowserFirefox:
		return utls.HelloFirefox_120, true
	default:
		return utls.ClientHelloID{}, false
	}
}

// buildBrowserClientHello returns the raw ClientHello bytes for a QUIC Initial
// fingerprinted as the given browser, with ALPN forced to h3 and the PQ hybrid
// key_share stripped so it fits one Initial. Falls back to the generic CH for an
// unsupported browser (should not happen — the dispatcher only routes chrome/firefox).
func buildBrowserClientHello(sni, browser string) ([]byte, error) {
	id, ok := browserHelloID(browser)
	if !ok {
		return nil, E.New("amneziawg: no uTLS fingerprint for browser ", browser)
	}

	spec, err := utls.UTLSIdToSpec(id)
	if err != nil {
		return nil, E.Cause(err, "amneziawg: uTLS spec for ", browser)
	}

	// QUIC requires TLS 1.3 only; the parrot spec advertises 1.0–1.3 for TCP
	// compatibility, which uTLS rejects in QUIC mode. Pin it to 1.3.
	spec.TLSVersMin = utls.VersionTLS13
	spec.TLSVersMax = utls.VersionTLS13
	for _, ext := range spec.Extensions {
		switch e := ext.(type) {
		case *utls.ALPNExtension:
			e.AlpnProtocols = []string{"h3"} // QUIC, not h2
		case *utls.SupportedVersionsExtension:
			e.Versions = []uint16{utls.VersionTLS13}
		case *utls.SupportedCurvesExtension:
			e.Curves = common.Filter(e.Curves, notPQCurve)
		case *utls.KeyShareExtension:
			e.KeyShares = common.Filter(e.KeyShares, func(k utls.KeyShare) bool { return notPQCurve(k.Group) })
		}
	}

	cfg := &utls.Config{ServerName: sni, MinVersion: utls.VersionTLS13}
	q := utls.UQUICClient(&utls.QUICConfig{TLSConfig: cfg}, utls.HelloCustom)
	defer q.Close()
	if err := q.ApplyPreset(&spec); err != nil {
		return nil, E.Cause(err, "amneziawg: uTLS ApplyPreset")
	}
	// QUIC transport parameters are mandatory in the ClientHello; the decoy never
	// negotiates, so an opaque plausible blob is enough (same intent as the
	// generic CH's quic_transport_params).
	q.SetTransportParameters(quicDecoyTransportParams())
	if err := q.Start(context.Background()); err != nil {
		return nil, E.Cause(err, "amneziawg: uTLS QUIC start")
	}

	// The Initial-level CRYPTO data uTLS wants to send IS the ClientHello.
	var hello []byte
	for {
		ev := q.NextEvent()
		if ev.Kind == utls.QUICNoEvent {
			break
		}
		if ev.Kind == utls.QUICWriteData && ev.Level == utls.QUICEncryptionLevelInitial {
			hello = append(hello, ev.Data...)
		}
	}
	if len(hello) == 0 || hello[0] != 0x01 {
		return nil, E.New("amneziawg: uTLS produced no ClientHello")
	}
	return hello, nil
}

// notPQCurve reports whether a curve is NOT a post-quantum hybrid (which we strip
// to keep the ClientHello inside one QUIC Initial).
func notPQCurve(c utls.CurveID) bool {
	return c != utls.X25519MLKEM768 && c != utls.X25519Kyber768Draft00
}

// quicDecoyTransportParams returns an opaque plausible QUIC transport-parameters
// blob for the decoy ClientHello (not negotiated; presence + plausibility only).
func quicDecoyTransportParams() []byte {
	return buildQUICTransportParams()
}
