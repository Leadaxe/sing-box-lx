//go:build with_awg && with_utls

package wireguard

import (
	"bytes"
	"testing"

	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

// ib=chrome/firefox routes the ClientHello through uTLS (real browser JA3),
// while ib=""/curl keeps the generic ~294B device-proven CH. Every variant must
// still decrypt, carry the SNI, keep the first CRYPTO offset≠0 (I1) and pack
// into the fixed 1250B Initial — the fragment plan adapts to the larger CH.
func TestQUICInitialBrowserFingerprint(t *testing.T) {
	t.Parallel()
	const sni = "www.google.com"

	gen := func(ib string) decodedInitial {
		spec, err := masqueI1(option.AmneziaWGOptions{Ip: "quic", Id: sni, Ib: ib})
		require.NoError(t, err, "ib=%q", ib)
		pkt := obfuscateCPS(t, spec)
		require.Equal(t, quicInitialTotalLen, len(pkt), "ib=%q packet size", ib)
		d := decryptInitial(t, pkt)
		require.Equal(t, sni, extractSNI(t, d.clientHello), "ib=%q SNI (I4)", ib)
		require.NotEqual(t, uint64(0), d.cryptoFrames[0].offset, "ib=%q first CRYPTO offset≠0 (I1)", ib)
		return d
	}

	generic := gen("")
	chrome := gen("chrome")
	firefox := gen("firefox")
	curl := gen("curl")

	// curl falls back to the generic CH (uTLS has no curl-QUIC fingerprint): same
	// length class as ib="" (both the ~294 generic), distinct from chrome/firefox.
	require.Equal(t, len(generic.clientHello), len(curl.clientHello), "curl uses the generic CH")

	// chrome/firefox ClientHellos are the larger uTLS ones and differ from generic
	// and from each other (distinct browser fingerprints).
	require.Greater(t, len(chrome.clientHello), len(generic.clientHello), "chrome CH is the uTLS one")
	require.Greater(t, len(firefox.clientHello), len(generic.clientHello), "firefox CH is the uTLS one")
	require.NotEqual(t, len(chrome.clientHello), len(firefox.clientHello), "chrome and firefox JA3 differ")

	// chrome sends GREASE cipher suites (0x?a?a); firefox does not — a concrete
	// JA3 distinction, asserted on the reassembled ClientHello bytes.
	require.True(t, hasGREASECipher(chrome.clientHello), "chrome ClientHello carries GREASE")
	require.False(t, hasGREASECipher(firefox.clientHello), "firefox ClientHello has no GREASE")
}

// hasGREASECipher reports whether the ClientHello's cipher_suites list contains a
// GREASE value (both bytes equal, low nibble 0xA — RFC 8701). cipher_suites sit
// right after handshake header(4) + legacy_version(2) + random(32) + session_id.
func hasGREASECipher(ch []byte) bool {
	r := bytes.NewReader(ch[4:]) // skip handshake type+len
	skipN(r, 2+32)               // legacy_version + random
	sid, _ := r.ReadByte()       // session_id len
	skipN(r, int(sid))
	hi, _ := r.ReadByte()
	lo, _ := r.ReadByte()
	csLen := int(hi)<<8 | int(lo)
	cs := make([]byte, csLen)
	if _, err := r.Read(cs); err != nil {
		return false
	}
	for i := 0; i+1 < len(cs); i += 2 {
		if cs[i] == cs[i+1] && cs[i]&0x0f == 0x0a {
			return true
		}
	}
	return false
}

func skipN(r *bytes.Reader, n int) {
	for i := 0; i < n; i++ {
		_, _ = r.ReadByte()
	}
}
