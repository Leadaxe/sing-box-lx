//go:build with_awg && with_utls

// Browser-fingerprinted ClientHello for ip=quic + ib=chrome|chrome-full|firefox,
// built with uTLS (the utls fork submodule) so the decoy carries a real
// browser's JA3/JA4 instead of our generic ClientHello. Only compiled with the
// with_utls tag; the stub file handles the !with_utls case (falls back to the
// generic CH).
//
// We drive uTLS in QUIC mode (UQUICClient) so the emitted ClientHello is a QUIC
// ClientHello — it carries quic_transport_params and we force ALPN h3, unlike a
// TCP-TLS ClientHello.
//
// chrome: Chrome 155 with the post-quantum hybrid key_share (X25519MLKEM768)
// stripped. The hybrid share is ~1.2 KB; without it the ClientHello is ~470
// bytes and fits one 1250-byte Initial. This is what Chrome 155 sends with
// PostQuantumKeyAgreementEnabled=false.
//
// chrome-full: Chrome 155 as shipped, hybrid share kept (~1.75 KB ClientHello).
// It does not fit a 1250-byte Initial; a real Chrome spreads it over three
// Initials, but a multi-packet start is dropped on the WARP path (LxBox §618),
// so the generator emits ONE oversized Initial (~2 KB) that the IP layer
// fragments. Trade-off: the fingerprint is current Chrome, the datagram is not.
//
// firefox: HelloFirefox_148 reshaped on the Firefox 149 capture, PQ stripped,
// one CRYPTO frame; the neqo header (3-byte SCID, pn_len 2, zero fill after the
// packet) is applied by quic_initial_awg.go.
package wireguard

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"sort"

	utls "github.com/metacubex/utls"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
)

// browserHelloID maps the ib hint to a uTLS fingerprint.
func browserHelloID(browser string) (utls.ClientHelloID, bool) {
	switch browser {
	case masqueBrowserChrome, masqueBrowserChromeFull:
		return utls.HelloChrome_155, true
	case masqueBrowserFirefox:
		return utls.HelloFirefox_148, true
	default:
		return utls.ClientHelloID{}, false
	}
}

// buildBrowserClientHello returns the raw ClientHello bytes for a QUIC Initial
// fingerprinted as the given browser. The uTLS presets describe the browser's
// TCP ClientHello; a browser's QUIC ClientHello is a different message (TLS 1.3
// only, quic_transport_parameters present, a different extension set), so the
// preset is reshaped per browser against a capture — see chromeQUICSpec and
// firefoxQUICSpec. scid is the Initial's Source Connection ID (it is echoed in
// initial_source_connection_id). The PQ hybrid key_share is stripped for every
// browser except chrome-full.
func buildBrowserClientHello(sni, browser string, scid []byte) ([]byte, error) {
	id, ok := browserHelloID(browser)
	if !ok {
		return nil, E.New("amneziawg: no uTLS fingerprint for browser ", browser)
	}

	spec, err := utls.UTLSIdToSpec(id)
	if err != nil {
		return nil, E.Cause(err, "amneziawg: uTLS spec for ", browser)
	}
	keepPQ := browser == masqueBrowserChromeFull
	switch browser {
	case masqueBrowserChrome, masqueBrowserChromeFull:
		err = chromeQUICSpec(&spec, keepPQ, scid)
	default:
		err = firefoxQUICSpec(&spec, scid)
	}
	if err != nil {
		return nil, err
	}

	cfg := &utls.Config{ServerName: sni, MinVersion: utls.VersionTLS13}
	q := utls.UQUICClient(&utls.QUICConfig{TLSConfig: cfg}, utls.HelloCustom)
	defer q.Close()
	if err := q.ApplyPreset(&spec); err != nil {
		return nil, E.Cause(err, "amneziawg: uTLS ApplyPreset")
	}
	// uTLS ignores SetTransportParameters for a preset-built ClientHello; the
	// transport parameters travel inside the QUICTransportParametersExtension
	// the spec functions above add. Start still requires a non-nil value.
	q.SetTransportParameters([]byte{})
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

// tls13CipherSuites is the whole cipher list of Chrome's QUIC ClientHello:
// QUIC is TLS 1.3 only, and Chrome does not GREASE it there (Chrome 133/147).
var tls13CipherSuites = []uint16{
	utls.TLS_AES_128_GCM_SHA256,
	utls.TLS_AES_256_GCM_SHA384,
	utls.TLS_CHACHA20_POLY1305_SHA256,
}

// chromeQUICSignatureAlgorithms is the signature_algorithms list of Chrome's
// QUIC ClientHello (identical in the Chrome 133 and 147 captures).
var chromeQUICSignatureAlgorithms = []utls.SignatureScheme{
	utls.ECDSAWithP256AndSHA256,
	utls.PSSWithSHA256,
	utls.PKCS1WithSHA256,
	utls.ECDSAWithP384AndSHA384,
	utls.PSSWithSHA384,
	utls.PKCS1WithSHA384,
	utls.PSSWithSHA512,
	utls.PKCS1WithSHA512,
	utls.PKCS1WithSHA1,
}

// chromeQUICSpec reshapes the HelloChrome_155 TCP preset into Chrome's QUIC
// ClientHello, after captures of Chrome 133 (LxBox §618) and Chrome 147
// (testdata/chrome_147_initial.bin):
//
//   - cipher_suites: the three TLS 1.3 suites, no GREASE, no TLS 1.2 suites;
//   - supported_groups / key_share: X25519MLKEM768 (chrome-full only), X25519,
//     P-256, P-384 — no GREASE group or share;
//   - extensions kept: server_name, supported_groups, ALPN (h3),
//     signature_algorithms (capture list), key_share, psk_key_exchange_modes,
//     supported_versions (1.3), compress_certificate, ALPS (h3), GREASE ECH;
//     everything else the TCP preset has is absent over QUIC (GREASE extensions,
//     trust_anchors, ec_point_formats, status_request, SCT, session_ticket,
//     extended_master_secret, renegotiation_info, padding);
//   - quic_transport_parameters added at a random position (the preset's
//     extension order is already Chrome-shuffled).
//
// pre_shared_key / early_data of the 133 capture belong to a repeat visit and
// are left out: the decoy is a first visit.
func chromeQUICSpec(spec *utls.ClientHelloSpec, keepPQ bool, scid []byte) error {
	spec.TLSVersMin = utls.VersionTLS13
	spec.TLSVersMax = utls.VersionTLS13
	spec.CipherSuites = append([]uint16(nil), tls13CipherSuites...)
	keepGroup := func(c utls.CurveID) bool {
		return uint16(c) != utls.GREASE_PLACEHOLDER && (keepPQ || notPQCurve(c))
	}
	kept := make([]utls.TLSExtension, 0, len(spec.Extensions)+1)
	for _, ext := range spec.Extensions {
		switch e := ext.(type) {
		case *utls.SNIExtension, *utls.PSKKeyExchangeModesExtension, *utls.UtlsCompressCertExtension,
			*utls.GREASEEncryptedClientHelloExtension:
		case *utls.ALPNExtension:
			e.AlpnProtocols = []string{"h3"}
		case *utls.ApplicationSettingsExtensionNew:
			e.SupportedProtocols = []string{"h3"}
		case *utls.SupportedVersionsExtension:
			e.Versions = []uint16{utls.VersionTLS13}
		case *utls.SignatureAlgorithmsExtension:
			e.SupportedSignatureAlgorithms = append([]utls.SignatureScheme(nil), chromeQUICSignatureAlgorithms...)
		case *utls.SupportedCurvesExtension:
			e.Curves = common.Filter(e.Curves, keepGroup)
		case *utls.KeyShareExtension:
			e.KeyShares = common.Filter(e.KeyShares, func(k utls.KeyShare) bool { return keepGroup(k.Group) })
			if !keepPQ {
				for i := range e.KeyShares {
					e.KeyShares[i].Data = nil // drop any reuse-of-hybrid marker
				}
			}
		default:
			continue
		}
		kept = append(kept, ext)
	}
	tps, err := chromeTransportParameters(scid)
	if err != nil {
		return err
	}
	spec.Extensions, err = insertExtensionRandomly(kept, &utls.QUICTransportParametersExtension{TransportParameters: tps})
	return err
}

// chromeTransportParameters is the transport_parameters list of Chrome's QUIC
// ClientHello. Values are the Chrome 133/147 captures'; the order is shuffled
// per call because quiche shuffles it (the two captures differ). The GREASE
// parameter gets a fresh id and 0–15 random bytes; initial_rtt (0x3127,
// microseconds) is sent on every connection and drawn from a plausible range.
func chromeTransportParameters(scid []byte) (utls.TransportParameters, error) {
	greaseLen, err := randInt(16)
	if err != nil {
		return nil, err
	}
	rttMs, err := randInt(120 - 12 + 1)
	if err != nil {
		return nil, err
	}
	version, err := versionInformation()
	if err != nil {
		return nil, err
	}
	tps := utls.TransportParameters{
		&utls.GREASETransportParameter{Length: uint16(greaseLen)},
		&utls.FakeQUICTransportParameter{Id: 0x3127, Val: appendQUICVarint(nil, uint64((12+rttMs)*1000))}, // initial_rtt
		utls.MaxDatagramFrameSize(65536),
		version,
		utls.MaxUDPPayloadSize(1472),
		utls.InitialMaxStreamsUni(103),
		utls.InitialMaxStreamDataUni(6291456),
		utls.InitialMaxStreamDataBidiRemote(6291456),
		utls.InitialMaxData(15728640),
		utls.InitialSourceConnectionID(append([]byte(nil), scid...)),
		utls.InitialMaxStreamDataBidiLocal(6291456),
		utls.MaxIdleTimeout(30000),
		utls.InitialMaxStreamsBidi(100),
	}
	for i := len(tps) - 1; i > 0; i-- {
		j, err := randInt(i + 1)
		if err != nil {
			return nil, err
		}
		tps[i], tps[j] = tps[j], tps[i]
	}
	return tps, nil
}

// firefoxQUICSignatureAlgorithms is the signature_algorithms list of Firefox's
// QUIC ClientHello (Firefox 149 capture, testdata/firefox_149_initial.bin).
var firefoxQUICSignatureAlgorithms = []utls.SignatureScheme{
	utls.ECDSAWithP256AndSHA256,
	utls.ECDSAWithP384AndSHA384,
	utls.ECDSAWithP521AndSHA512,
	utls.ECDSAWithSHA1,
	utls.PSSWithSHA256,
	utls.PSSWithSHA384,
	utls.PSSWithSHA512,
	utls.PKCS1WithSHA256,
	utls.PKCS1WithSHA384,
	utls.PKCS1WithSHA512,
	utls.PKCS1WithSHA1,
}

// firefoxQUICSpec reshapes the HelloFirefox_148 TCP preset into Firefox's QUIC
// ClientHello after the Firefox 149 capture (testdata/firefox_149_initial.bin):
// the three TLS 1.3 suites in NSS order; extensions server_name,
// extended_master_secret, renegotiation_info, supported_groups, ALPN (h3),
// status_request, delegated_credentials, key_share, supported_versions,
// signature_algorithms (capture list), psk_key_exchange_modes,
// record_size_limit, compress_certificate, GREASE ECH and
// quic_transport_parameters — ec_point_formats, SCT, session_ticket and padding
// are absent over QUIC, FFDHE groups too; psk_key_exchange_modes (absent from
// the TCP preset, present over QUIC) is added, and the list is sorted into the
// capture's order. The PQ hybrid is stripped so the ClientHello fits one
// Initial; groups keep X25519, P-256, P-384, P-521 as captured.
func firefoxQUICSpec(spec *utls.ClientHelloSpec, scid []byte) error {
	spec.TLSVersMin = utls.VersionTLS13
	spec.TLSVersMax = utls.VersionTLS13
	spec.CipherSuites = []uint16{utls.TLS_AES_128_GCM_SHA256, utls.TLS_CHACHA20_POLY1305_SHA256, utls.TLS_AES_256_GCM_SHA384}
	kept := make([]utls.TLSExtension, 0, len(spec.Extensions)+1)
	for _, ext := range spec.Extensions {
		switch e := ext.(type) {
		case *utls.SNIExtension, *utls.ExtendedMasterSecretExtension, *utls.RenegotiationInfoExtension,
			*utls.StatusRequestExtension, *utls.FakeDelegatedCredentialsExtension,
			*utls.PSKKeyExchangeModesExtension, *utls.FakeRecordSizeLimitExtension,
			*utls.UtlsCompressCertExtension, *utls.GREASEEncryptedClientHelloExtension:
		case *utls.ALPNExtension:
			e.AlpnProtocols = []string{"h3"}
		case *utls.SupportedVersionsExtension:
			e.Versions = []uint16{utls.VersionTLS13}
		case *utls.SignatureAlgorithmsExtension:
			e.SupportedSignatureAlgorithms = append([]utls.SignatureScheme(nil), firefoxQUICSignatureAlgorithms...)
		case *utls.SupportedCurvesExtension:
			// Firefox offers no FFDHE groups over QUIC (capture: X25519, P-256, P-384, P-521).
			e.Curves = common.Filter(e.Curves, func(c utls.CurveID) bool { return notPQCurve(c) && c < 256 })
		case *utls.KeyShareExtension:
			e.KeyShares = common.Filter(e.KeyShares, func(k utls.KeyShare) bool { return notPQCurve(k.Group) })
			// The preset marks the X25519 share to reuse the hybrid share's
			// X25519 half (SPEC 086); with the hybrid gone, let uTLS generate it.
			for i := range e.KeyShares {
				e.KeyShares[i].Data = nil
			}
		default:
			continue
		}
		kept = append(kept, ext)
	}
	version, err := versionInformation()
	if err != nil {
		return err
	}
	// neqo writes its transport parameters in ascending id order (as captured).
	tps := utls.TransportParameters{
		utls.MaxIdleTimeout(30000),
		utls.InitialMaxData(25165824),
		utls.InitialMaxStreamDataBidiLocal(12582912),
		utls.InitialMaxStreamDataBidiRemote(1048576),
		utls.InitialMaxStreamDataUni(1048576),
		utls.InitialMaxStreamsBidi(100),
		utls.InitialMaxStreamsUni(100),
		utls.MaxAckDelay(20),
		utls.ActiveConnectionIDLimit(8),
		utls.InitialSourceConnectionID(append([]byte(nil), scid...)),
		version,
		&utls.FakeQUICTransportParameter{Id: 0xff02de1a, Val: appendQUICVarint(nil, 1000)}, // min_ack_delay (draft)
		utls.MaxDatagramFrameSize(65535),
	}
	kept = append(kept,
		&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
		&utls.QUICTransportParametersExtension{TransportParameters: tps})
	// NSS orders the QUIC ClientHello differently from the TCP one; sort into
	// the Firefox 149 capture order.
	sort.SliceStable(kept, func(i, j int) bool { return firefoxQUICExtOrder(kept[i]) < firefoxQUICExtOrder(kept[j]) })
	spec.Extensions = kept
	return nil
}

// firefoxQUICExtOrder is the position of an extension in the Firefox 149 QUIC
// ClientHello: extended_master_secret, delegated_credentials,
// record_size_limit, ALPN, status_request, signature_algorithms,
// renegotiation_info, compress_certificate, server_name, key_share,
// supported_versions, supported_groups, psk_key_exchange_modes,
// quic_transport_parameters, ECH.
func firefoxQUICExtOrder(ext utls.TLSExtension) int {
	switch ext.(type) {
	case *utls.ExtendedMasterSecretExtension:
		return 0
	case *utls.FakeDelegatedCredentialsExtension:
		return 1
	case *utls.FakeRecordSizeLimitExtension:
		return 2
	case *utls.ALPNExtension:
		return 3
	case *utls.StatusRequestExtension:
		return 4
	case *utls.SignatureAlgorithmsExtension:
		return 5
	case *utls.RenegotiationInfoExtension:
		return 6
	case *utls.UtlsCompressCertExtension:
		return 7
	case *utls.SNIExtension:
		return 8
	case *utls.KeyShareExtension:
		return 9
	case *utls.SupportedVersionsExtension:
		return 10
	case *utls.SupportedCurvesExtension:
		return 11
	case *utls.PSKKeyExchangeModesExtension:
		return 12
	case *utls.QUICTransportParametersExtension:
		return 13
	case *utls.GREASEEncryptedClientHelloExtension:
		return 14
	default:
		return 99
	}
}

// versionInformation builds the version_information transport parameter:
// chosen v1, available [GREASE, v1]. The GREASE version is drawn here as a
// proper 0x?a?a?a?a (the uTLS helper ORs 0x0a0a0a0a over an unmasked random
// value, which lands on the GREASE pattern only 1 time in 256).
func versionInformation() (*utls.VersionInformation, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	grease := (binary.BigEndian.Uint32(b[:]) & 0xf0f0f0f0) | 0x0a0a0a0a
	if grease == utls.VERSION_GREASE {
		grease |= 0x10000000 // keep uTLS from re-drawing the exact constant
	}
	return &utls.VersionInformation{ChoosenVersion: utls.VERSION_1, AvailableVersions: []uint32{grease, utls.VERSION_1}}, nil
}

// insertExtensionRandomly puts ext at a random position of the list.
func insertExtensionRandomly(exts []utls.TLSExtension, ext utls.TLSExtension) ([]utls.TLSExtension, error) {
	pos, err := randInt(len(exts) + 1)
	if err != nil {
		return nil, err
	}
	out := make([]utls.TLSExtension, 0, len(exts)+1)
	out = append(out, exts[:pos]...)
	out = append(out, ext)
	return append(out, exts[pos:]...), nil
}

// notPQCurve reports whether a curve is NOT a post-quantum hybrid (which we strip
// to keep the ClientHello inside one QUIC Initial).
func notPQCurve(c utls.CurveID) bool {
	return c != utls.X25519MLKEM768 && c != utls.X25519Kyber768Draft00
}
