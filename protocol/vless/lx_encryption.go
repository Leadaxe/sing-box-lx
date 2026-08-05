package vless

// lx: parsing for the VLESS `encryption` spec string (SPEC 032). Kept in its own
// file so the upstream-owned outbound.go carries only the two-line wiring, and a
// future upstream implementation of this feature conflicts there rather than in
// the parser.

import (
	"context"
	"encoding/base64"
	"net"
	"strings"
	"time"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
)

// wrapEncryption performs the post-quantum handshake over an already-dialed
// conn, returning it unchanged when the layer is not configured. It sits above
// the transport/TLS and below the vless client, which stays unaware of it.
// A failed handshake closes the conn: the caller only propagates the error.
//
// lx: 050 — Handshake takes a bare net.Conn (the wire format is fixed by SPEC
// 032 and upstream Xray, so it grows no context parameter) and internally writes
// fragmented padding with sleeps in between. On a half-alive node those writes
// block forever, which is how URL tests turned into goroutines that outlived the
// box. The dial deadline is therefore applied to the conn for the duration of
// the handshake and cleared afterwards, so the caller's context governs it.
//
// WRITE side only, deliberately. The hang this guards against is a blocked Write
// into an upload body nobody reads, so a read deadline buys nothing here — and it
// would cost correctness: an XHTTP read deadline is one-shot (it closes the
// late-bound download body, and clearing it cannot reopen that), so a handshake
// that overran the dial deadline but still succeeded would hand back a conn whose
// download side is already dead. SetDeadline covers both directions, hence the
// narrower call.
func (h *vlessDialer) wrapEncryption(ctx context.Context, conn net.Conn) (net.Conn, error) {
	if h.encryption == nil {
		return conn, nil
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetWriteDeadline(deadline); err == nil {
			defer conn.SetWriteDeadline(time.Time{})
		}
	}
	encryptedConn, err := h.encryption.Handshake(conn)
	if err != nil {
		common.Close(conn)
		return nil, E.Cause(err, "encryption handshake")
	}
	return encryptedConn, nil
}

// encryptionPrefix is the only handshake method that exists today.
const encryptionPrefix = "mlkem768x25519plus"

// xorMode selects how the layer looks on the wire: AEAD headers shaped like
// TLSv1.3 (native), XOR-ed by public key (xorpub), or fully random.
const (
	xorModeNative uint32 = 0
	xorModeXorPub uint32 = 1
	xorModeRandom uint32 = 2
)

// keyLenX25519 and keyLenMLKEM768 are the two accepted public-key sizes; a
// segment decoding to anything else is a malformed key rather than padding.
const (
	keyLenX25519   = 32
	keyLenMLKEM768 = 1184
)

// paddingSegmentMaxLen bounds how long a segment may be and still be read as a
// padding block. Keys are base64 of 32 or 1184 bytes, so they are always longer;
// padding blocks look like "100-111-1111".
const paddingSegmentMaxLen = 20

type clientEncryptionConfig struct {
	keys    [][]byte
	xorMode uint32
	seconds uint32
	padding string
}

// parseClientEncryption reads the spec string:
//
//	mlkem768x25519plus.<native|xorpub|random>.<0rtt|1rtt>[.<padding>…].<key>[.<key>…]
//
// Errors name the offending segment so a bad config fails at check/start time
// with something actionable, rather than silently failing to connect later.
func parseClientEncryption(raw string) (clientEncryptionConfig, error) {
	var cfg clientEncryptionConfig
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cfg, E.New("empty encryption string")
	}
	parts := strings.Split(raw, ".")
	if len(parts) < 4 {
		return cfg, E.New("invalid encryption string: expected at least method.appearance.rtt.key, got ", len(parts), " segments")
	}
	if parts[0] != encryptionPrefix {
		return cfg, E.New("unsupported encryption method: ", parts[0], " (only ", encryptionPrefix, " exists)")
	}
	switch parts[1] {
	case "native":
		cfg.xorMode = xorModeNative
	case "xorpub":
		cfg.xorMode = xorModeXorPub
	case "random":
		cfg.xorMode = xorModeRandom
	default:
		return cfg, E.New("unknown encryption appearance: ", parts[1], " (expected native|xorpub|random)")
	}
	switch parts[2] {
	case "0rtt":
		cfg.seconds = 1
	case "1rtt":
		cfg.seconds = 0
	default:
		return cfg, E.New("unknown encryption RTT mode: ", parts[2], " (expected 0rtt|1rtt)")
	}

	// Remaining segments are padding blocks first, then keys. Once a key is seen
	// the padding phase is over — that is how the reference implementation tells
	// the two apart, since both are dot-separated and otherwise unlabelled.
	paddingPhase := true
	var paddingParts []string
	for _, segment := range parts[3:] {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return cfg, E.New("empty segment in encryption string")
		}
		if paddingPhase && len(segment) < paddingSegmentMaxLen {
			paddingParts = append(paddingParts, segment)
			continue
		}
		data, err := base64.RawURLEncoding.DecodeString(segment)
		if err != nil {
			return cfg, E.New("invalid encryption key (not base64url): ", segment)
		}
		if len(data) != keyLenX25519 && len(data) != keyLenMLKEM768 {
			return cfg, E.New("invalid encryption key length: ", len(data), " (expected ", keyLenX25519, " or ", keyLenMLKEM768, ")")
		}
		cfg.keys = append(cfg.keys, data)
		paddingPhase = false
	}
	if len(cfg.keys) == 0 {
		return cfg, E.New("no encryption keys in encryption string")
	}
	if len(paddingParts) > 0 {
		cfg.padding = strings.Join(paddingParts, ".")
	}
	return cfg, nil
}
