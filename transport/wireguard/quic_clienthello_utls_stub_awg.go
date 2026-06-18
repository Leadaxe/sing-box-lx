//go:build with_awg && !with_utls

// Fallback for ib=chrome|firefox when the build lacks the with_utls tag: there
// is no uTLS to produce a real browser fingerprint, so we degrade gracefully to
// the generic ClientHello (the same one ib="" uses). The ib hint is still
// accepted and validated upstream; it simply has no effect without with_utls.
// (LX_TAGS includes with_utls, so release builds take the real path in
// quic_clienthello_utls_awg.go; this only covers a with_awg-without-with_utls build.)
package wireguard

import (
	"crypto/ecdh"
	"crypto/rand"
)

// buildBrowserClientHello (no-uTLS build) ignores the browser and returns the
// generic ClientHello with fresh per-call random + ephemeral x25519, matching
// what buildInitialPacket would have produced for ib="".
func buildBrowserClientHello(sni, browser string) ([]byte, error) {
	_ = browser
	var tlsRandom [32]byte
	if _, err := rand.Read(tlsRandom[:]); err != nil {
		return nil, err
	}
	ecKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return buildGenericClientHello(sni, tlsRandom, ecKey.PublicKey().Bytes())
}
