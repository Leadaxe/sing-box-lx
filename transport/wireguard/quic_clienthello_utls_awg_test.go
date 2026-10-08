//go:build with_awg && with_utls

package wireguard

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sagernet/sing-box/option"

	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

// ib=chrome/chrome-full/firefox route the ClientHello through uTLS, reshaped
// from the TCP preset into the browser's QUIC ClientHello against the captures
// in testdata/; ib=""/curl keep the generic ~294B CH. Every variant must
// decrypt, carry the SNI and reassemble (I4).
func TestQUICInitialBrowserFingerprint(t *testing.T) {
	t.Parallel()
	const sni = "www.google.com"

	gen := func(ib string) (decodedInitial, int) {
		spec, err := masqueI1(option.AmneziaWGOptions{Ip: "quic", Id: sni, Ib: ib})
		require.NoError(t, err, "ib=%q", ib)
		pkt := obfuscateCPS(t, spec)
		d := decryptInitial(t, pkt)
		require.Equal(t, sni, extractSNI(t, d.clientHello), "ib=%q SNI (I4)", ib)
		return d, len(pkt)
	}

	generic, genericLen := gen("")
	chrome, chromeLen := gen("chrome")
	firefox, firefoxLen := gen("firefox")
	curl, curlLen := gen("curl")
	full, fullLen := gen("chrome-full")

	// curl falls back to the generic CH (uTLS has no curl-QUIC fingerprint).
	require.Equal(t, len(generic.clientHello), len(curl.clientHello), "curl uses the generic CH")
	assertPlainLayout(t, generic)
	assertGenericHeader(t, generic, genericLen)
	assertPlainLayout(t, curl)
	assertGenericHeader(t, curl, curlLen)

	// Chrome (133/147 captures): chaos layout, pn 1, 1250 bytes, the three TLS
	// 1.3 suites, the 11-extension QUIC set, Chrome's transport parameters.
	require.Equal(t, quicInitialTotalLen, chromeLen)
	assertChaosLayout(t, chrome)
	assertChaosLayout(t, full)
	for name, d := range map[string]decodedInitial{"chrome": chrome, "chrome-full": full} {
		require.Equal(t, []uint16{0x1301, 0x1302, 0x1303}, cipherSuites(t, d.clientHello), "%s: TLS 1.3 suites only", name)
		assertExtensionSet(t, d.clientHello, chromeQUICExtensionTypes(), name)
		require.False(t, hasGREASEExtension(t, d.clientHello), "%s: Chrome does not GREASE extensions over QUIC", name)
		assertTransportParameterIDs(t, d.clientHello, chromeTransportParameterIDs(), name)
	}
	require.Equal(t, []uint16{29}, keyShareGroups(t, chrome.clientHello), "chrome: X25519 share only")
	require.Equal(t, []uint16{29, 23, 24}, supportedGroups(t, chrome.clientHello), "chrome: groups without the PQ hybrid")

	// chrome-full keeps the hybrid share first, as Chrome sends it, and grows
	// past one 1250-byte Initial.
	require.Equal(t, []uint16{uint16(utls.X25519MLKEM768), 29}, keyShareGroups(t, full.clientHello), "chrome-full: hybrid share first")
	require.Greater(t, len(full.clientHello), quicInitialTotalLen, "chrome-full CH exceeds one 1250B Initial")
	require.Greater(t, fullLen, quicInitialTotalLen, "chrome-full Initial grows past 1250")
	require.Less(t, fullLen, 4000, "chrome-full Initial stays a single oversized datagram")

	// Firefox (149 capture): plain layout, neqo header, NSS cipher order, the
	// 15-extension QUIC set in capture order, Firefox transport parameters with
	// the SCID echoed.
	assertPlainLayout(t, firefox)
	assertFirefoxHeader(t, firefox, firefoxLen)
	require.Equal(t, []uint16{0x1301, 0x1303, 0x1302}, cipherSuites(t, firefox.clientHello), "firefox: TLS 1.3 suites in NSS order")
	require.Equal(t, firefoxQUICExtensionTypes(), extensionTypes(t, firefox.clientHello), "firefox: extension order as captured")
	require.Equal(t, []uint16{29, 23, 24, 25}, supportedGroups(t, firefox.clientHello), "firefox: groups without the PQ hybrid, no FFDHE")
	require.Equal(t, []uint16{29, 23}, keyShareGroups(t, firefox.clientHello), "firefox: X25519 and P-256 shares")
	assertTransportParameterIDs(t, firefox.clientHello, firefoxTransportParameterIDs(), "firefox")
	require.Equal(t, firefox.scid, transportParameterValue(t, firefox.clientHello, 0x0f), "firefox: initial_source_connection_id echoes the SCID")
}

// TestQUICInitialCaptureParity decrypts the real Chrome 147 / Firefox 149
// Initials in testdata/ with this package's Initial crypto (a known-answer test
// for deriveInitialKeys / header protection / AEAD) and checks that the
// generated profiles carry the same extension and transport-parameter sets.
func TestQUICInitialCaptureParity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file    string
		browser string
		pq      bool
	}{
		{"chrome_147_initial.bin", "chrome-full", true},
		{"firefox_149_initial.bin", "firefox", true},
	} {
		ch, first := decodeCaptureClientHello(t, filepath.Join("testdata", tc.file))
		require.Equal(t, byte(0x01), ch[0], "%s: reassembled ClientHello", tc.file)
		require.NotEmpty(t, extractSNI(t, ch), "%s: SNI present", tc.file)

		var scid []byte
		if tc.browser == "firefox" {
			scid = []byte{1, 2, 3}
		}
		ours, err := buildBrowserClientHello("www.google.com", tc.browser, scid)
		require.NoError(t, err)

		require.Equal(t, cipherSuites(t, ch), cipherSuites(t, ours), "%s: cipher suites", tc.file)
		wantExts := extensionTypes(t, ch)
		if tc.browser == "firefox" {
			require.Equal(t, wantExts, extensionTypes(t, ours), "%s: extension order", tc.file)
			require.Equal(t, 3, len(first.scid), "%s: capture SCID length", tc.file)
			require.Equal(t, 2, first.pnLen, "%s: capture pn_len", tc.file)
		} else {
			require.ElementsMatch(t, wantExts, extensionTypes(t, ours), "%s: extension set", tc.file)
			require.Equal(t, uint64(1), first.packetNumber, "%s: capture first pn", tc.file)
		}
		require.ElementsMatch(t, nonGREASETransportParameterIDs(transportParameterIDs(t, ch)), nonGREASETransportParameterIDs(transportParameterIDs(t, ours)), "%s: transport parameter ids", tc.file)
		if tc.pq {
			require.Equal(t, uint16(utls.X25519MLKEM768), keyShareGroups(t, ch)[0], "%s: capture leads with the hybrid share", tc.file)
		}
	}
}

// decodeCaptureClientHello reads a length-prefixed capture (see testdata/README.md),
// decrypts every Initial with our crypto and returns the reassembled ClientHello
// and the decoded first packet.
func decodeCaptureClientHello(t *testing.T, path string) ([]byte, decodedInitial) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var frags []cryptoFragment
	var first decodedInitial
	for p, n := 0, 0; p+2 <= len(data); n++ {
		l := int(data[p])<<8 | int(data[p+1])
		d := decryptInitialFrames(t, data[p+2:p+2+l])
		if n == 0 {
			first = d
		}
		frags = append(frags, d.cryptoFrames...)
		p += 2 + l
	}
	return reassemble(t, frags), first
}

func chromeQUICExtensionTypes() []uint16 {
	return []uint16{0, 10, 13, 16, 27, 43, 45, 51, 57, 17613, 65037}
}

func firefoxQUICExtensionTypes() []uint16 {
	return []uint16{23, 34, 28, 16, 5, 13, 65281, 27, 0, 51, 43, 10, 45, 57, 65037}
}

func firefoxTransportParameterIDs() []uint64 {
	return []uint64{0x01, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0b, 0x0e, 0x0f, 0x11, 0xff02de1a, 0x20}
}

// assertTransportParameterIDs compares the transport parameter ids as a set
// (Chrome shuffles the order), ignoring GREASE-form ids on both sides: Chrome's
// GREASE parameter is random, and Firefox's draft min_ack_delay id 0xff02de1a
// deliberately has the GREASE form.
func assertTransportParameterIDs(t *testing.T, ch []byte, want []uint64, name string) {
	t.Helper()
	got := transportParameterIDs(t, ch)
	require.ElementsMatch(t, nonGREASETransportParameterIDs(want), nonGREASETransportParameterIDs(got), "%s: transport parameter ids", name)
}

// nonGREASETransportParameterIDs drops ids of the form 27 + 31·N (RFC 9000 §18.1).
func nonGREASETransportParameterIDs(ids []uint64) []uint64 {
	var out []uint64
	for _, id := range ids {
		if id >= 27 && (id-27)%31 == 0 {
			continue
		}
		out = append(out, id)
	}
	return out
}

// transportParameterValue returns the value of the transport parameter id.
func transportParameterValue(t *testing.T, ch []byte, want uint64) []byte {
	t.Helper()
	var value []byte
	walkExtensions(t, ch, func(typ uint16, body []byte) {
		if typ != 57 {
			return
		}
		r := bytes.NewReader(body)
		for r.Len() > 0 {
			id := readVarint(t, r)
			l := int(readVarint(t, r))
			v := make([]byte, l)
			_, _ = io.ReadFull(r, v)
			if id == want {
				value = v
			}
		}
	})
	return value
}

func supportedGroups(t *testing.T, ch []byte) []uint16 {
	t.Helper()
	var groups []uint16
	walkExtensions(t, ch, func(typ uint16, body []byte) {
		if typ != 10 {
			return
		}
		for i := 2; i+1 < len(body); i += 2 {
			groups = append(groups, uint16(body[i])<<8|uint16(body[i+1]))
		}
	})
	return groups
}

// chromeTransportParameterIDs is the Chrome 147 transport-parameter set from
// the capture, without the GREASE parameter (random id); the order is shuffled.
func chromeTransportParameterIDs() []uint64 {
	return []uint64{0x3127, 0x0f, 0x08, 0x05, 0x07, 0x06, 0x01, 0x09, 0x20, 0x11, 0x03, 0x04}
}

// walkExtensions calls fn for every ClientHello extension (type, body).
func walkExtensions(t *testing.T, ch []byte, fn func(typ uint16, body []byte)) {
	t.Helper()
	r := bytes.NewReader(ch[4:])
	skipN(r, 2+32)
	sid, _ := r.ReadByte()
	skipN(r, int(sid))
	csLen := int(readU16(t, r))
	skipN(r, csLen)
	comp, _ := r.ReadByte()
	skipN(r, int(comp))
	extTotal := int(readU16(t, r))
	end := r.Len() - extTotal
	for r.Len() > end {
		extType := readU16(t, r)
		extLen := int(readU16(t, r))
		body := make([]byte, extLen)
		_, err := io.ReadFull(r, body)
		require.NoError(t, err)
		fn(extType, body)
	}
}

func extensionTypes(t *testing.T, ch []byte) []uint16 {
	t.Helper()
	var types []uint16
	walkExtensions(t, ch, func(typ uint16, _ []byte) { types = append(types, typ) })
	return types
}

// assertExtensionSet checks the ClientHello carries exactly the given extension
// types (order is shuffled per connection, so compare as sets).
func assertExtensionSet(t *testing.T, ch []byte, want []uint16, name string) {
	t.Helper()
	got := extensionTypes(t, ch)
	require.ElementsMatch(t, want, got, "%s: extension set", name)
}

func hasGREASEExtension(t *testing.T, ch []byte) bool {
	t.Helper()
	for _, typ := range extensionTypes(t, ch) {
		if typ&0x0f0f == 0x0a0a && typ>>8 == typ&0xff {
			return true
		}
	}
	return false
}

func cipherSuites(t *testing.T, ch []byte) []uint16 {
	t.Helper()
	r := bytes.NewReader(ch[4:])
	skipN(r, 2+32)
	sid, _ := r.ReadByte()
	skipN(r, int(sid))
	csLen := int(readU16(t, r))
	var cs []uint16
	for i := 0; i < csLen; i += 2 {
		cs = append(cs, readU16(t, r))
	}
	return cs
}

// transportParameterIDs lists the ids of the quic_transport_parameters (57)
// extension in wire order.
func transportParameterIDs(t *testing.T, ch []byte) []uint64 {
	t.Helper()
	var ids []uint64
	walkExtensions(t, ch, func(typ uint16, body []byte) {
		if typ != 57 {
			return
		}
		r := bytes.NewReader(body)
		for r.Len() > 0 {
			id := readVarint(t, r)
			l := readVarint(t, r)
			skipN(r, int(l))
			ids = append(ids, id)
		}
	})
	return ids
}

// keyShareGroups returns the named groups listed in the ClientHello key_share
// extension (0x0033), in order.
func keyShareGroups(t *testing.T, ch []byte) []uint16 {
	t.Helper()
	var groups []uint16
	walkExtensions(t, ch, func(typ uint16, body []byte) {
		if typ != 0x0033 {
			return
		}
		p := 2 // client_shares length
		for p+4 <= len(body) {
			g := uint16(body[p])<<8 | uint16(body[p+1])
			l := int(body[p+2])<<8 | int(body[p+3])
			groups = append(groups, g)
			p += 4 + l
		}
	})
	return groups
}

func skipN(r *bytes.Reader, n int) {
	for i := 0; i < n; i++ {
		_, _ = r.ReadByte()
	}
}
