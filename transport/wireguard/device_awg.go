//go:build with_awg

package wireguard

import (
	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
)

// awgHeaderCipherKeySize / awgHeaderCipherNonceSize mirror the vendored
// wireguard-go's HeaderCipherKeySize / HeaderCipherNonceSize: the header
// protection key is 32 bytes and its per-datagram nonce is the first 12 bytes
// of the S1–S4 padding, so each padding must be at least that long.
const (
	awgHeaderCipherKeySize   = 32
	awgHeaderCipherNonceSize = 12
)

// awgIpcLines renders the AmneziaWG device-global obfuscation parameters as
// amneziawg-go IpcSet config lines, ready to be appended to the WireGuard
// device configuration (after private_key/listen_port, before peer sections).
//
// Output format (one key per line, leading "\n", no trailing newline):
//
//	\njc=<n>\njmin=<n>\njmax=<n>\ns1=<n>\ns2=<n>\ns3=<n>\ns4=<n>\nh1=<spec>\nh2=<spec>\nh3=<spec>\nh4=<spec>
//	\ni1=<str>\ni2=<str>...
//	\nheader_protection_key=<hex>\ncontent_padding_addition=<spec>\nrekey_after_time=<spec>...
//	\nrandom_trailers=1\ndisable_cookies=1
//
// Numeric keys are emitted only when non-zero. The h1..h4 values are magic
// header specs — a single uint32 ("N") or an inclusive range ("N-M", AWG 2.0)
// — emitted in the exact format UintRange.FromString expects on the uapi
// side; unset headers are omitted. The I1..I5 keys are emitted only when
// non-empty and are written verbatim — their value is case-sensitive
// (uppercase keywords) and must not be normalised. The AWG 3.x keys follow:
// the header protection key is converted from the config's base64 (the
// `awg genkey` / .conf form) to the uapi hex form, the ranged knobs use the
// same "N" / "N-M" spec as the headers, the two switches are emitted only
// when on. Returns "" when no AWG parameter is set, so a plain WireGuard
// endpoint produces byte-identical config to upstream even in a `with_awg`
// build.
//
// These keys correspond to the AmneziaWG handshake-obfuscation knobs parsed by
// amneziawg-go's device.IpcSetOperation (v3 for the AWG 3.x keys). With
// `with_awg` the wireguard-go dependency is replaced by the vendored fork,
// which understands them; upstream wireguard-go would reject the unknown keys
// at IpcSet time.
func awgIpcLines(o option.AmneziaWGOptions) (string, error) {
	if !o.IsSet() {
		return "", nil
	}
	if err := validateJunk(o); err != nil {
		return "", err
	}
	headerKeyHex, err := awgHeaderProtectionKeyHex(o)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	writeUint := func(key string, value uint32) {
		if value != 0 {
			b.WriteString("\n")
			b.WriteString(key)
			b.WriteString("=")
			b.WriteString(F.ToString(value))
		}
	}
	writeStr := func(key, value string) {
		if value != "" {
			b.WriteString("\n")
			b.WriteString(key)
			b.WriteString("=")
			b.WriteString(value)
		}
	}
	writeRange := func(key string, value option.AWGRange) error {
		spec, err := value.Spec()
		if err != nil {
			return E.Cause(err, key)
		}
		writeStr(key, spec)
		return nil
	}
	// WireSock-style id/ip/ib masquerade is sugar over I1 (and, for ip=quic and
	// ip=sip, also I2): masqueI1I2 generates both CPS strings in one pass. It
	// returns "" for both when no masquerade is set, and errors on a conflict
	// with an explicit I1 or on invalid id/ip/ib. When set, its output is used
	// as the i1/i2 values below.
	i1 := o.I1
	i2 := o.I2
	masque, masque2, err := masqueI1I2(o)
	if err != nil {
		return "", err
	}
	if masque != "" {
		i1 = masque
		// ip=sip fills i2 with the matching 100 Trying of the INVITE dialog (quic
		// and dns/stun are single-packet and leave i2 empty). A user-supplied i2
		// alongside an i2-filling sugar profile is ambiguous, exactly like the i1
		// conflict masqueI1 already rejects.
		if masque2 != "" {
			if o.I2 != "" {
				return "", E.New("amneziawg: id/ip/ib masquerade (ip=sip) fills i2; an explicit i2 conflicts with it")
			}
			i2 = masque2
		}
	}

	writeUint("jc", o.Jc)
	writeUint("jmin", o.Jmin)
	writeUint("jmax", o.Jmax)
	writeUint("s1", o.S1)
	writeUint("s2", o.S2)
	writeUint("s3", o.S3)
	writeUint("s4", o.S4)
	for _, header := range []struct {
		key   string
		value option.MagicHeader
	}{{"h1", o.H1}, {"h2", o.H2}, {"h3", o.H3}, {"h4", o.H4}} {
		if err := writeRange(header.key, header.value); err != nil {
			return "", err
		}
	}
	writeStr("i1", i1)
	writeStr("i2", i2)
	writeStr("i3", o.I3)
	writeStr("i4", o.I4)
	writeStr("i5", o.I5)

	// AmneziaWG 3.x (SPEC 080)
	writeStr("header_protection_key", headerKeyHex)
	for _, knob := range []struct {
		key   string
		value option.AWGRange
	}{
		{"content_padding_addition", o.ContentPaddingAddition},
		{"rekey_after_time", o.RekeyAfterTime},
		{"rekey_timeout", o.RekeyTimeout},
		{"reject_after_time", o.RejectAfterTime},
		{"keepalive_timeout", o.KeepaliveTimeout},
		{"max_handshake_attempts", o.MaxHandshakeAttempts},
	} {
		if err := writeRange(knob.key, knob.value); err != nil {
			return "", err
		}
	}
	if o.RandomTrailers {
		writeStr("random_trailers", "1")
	}
	if o.DisableCookies {
		writeStr("disable_cookies", "1")
	}
	return b.String(), nil
}

// awgHeaderProtectionKeyHex validates the AWG 3.x header_protection_key and
// returns it in the uapi hex form ("" when unset). The config carries the key
// as base64, the way `awg genkey` prints it and the .conf / Amnezia export
// store it; the device expects hex like every other key. A key needs every
// S1–S4 padding to carry the 12-byte header cipher nonce, which the vendored
// device also enforces at IpcSet — checking here turns the late device error
// into a `sing-box check` failure naming the field.
func awgHeaderProtectionKeyHex(o option.AmneziaWGOptions) (string, error) {
	if o.HeaderProtectionKey == "" {
		return "", nil
	}
	key, err := base64.StdEncoding.DecodeString(o.HeaderProtectionKey)
	if err != nil {
		return "", E.Cause(err, "amneziawg: decode header_protection_key (expected base64, as printed by `awg genkey`)")
	}
	if len(key) != awgHeaderCipherKeySize {
		return "", E.New("amneziawg: header_protection_key must decode to ", awgHeaderCipherKeySize, " bytes, got ", len(key))
	}
	zero := true
	for _, c := range key {
		if c != 0 {
			zero = false
			break
		}
	}
	if zero {
		return "", E.New("amneziawg: header_protection_key is all zeros (the device treats that as \"off\"); omit the field instead")
	}
	for _, padding := range []struct {
		key   string
		value uint32
	}{{"s1", o.S1}, {"s2", o.S2}, {"s3", o.S3}, {"s4", o.S4}} {
		if padding.value < awgHeaderCipherNonceSize {
			return "", E.New("amneziawg: ", padding.key, "=", F.ToString(padding.value), " is too short for header_protection_key: each of s1-s4 must be at least ",
				awgHeaderCipherNonceSize, " bytes (the padding carries the header cipher nonce)")
		}
	}
	return hex.EncodeToString(key), nil
}

// awgKeepaliveSpec validates the per-peer persistent_keepalive_interval and
// returns its canonical IpcSet spec: "N" (plain WireGuard), "min-max" (AWG
// 3.x — the device re-picks the interval from the range at every arming) or
// "" when off.
func awgKeepaliveSpec(keepalive option.AWGRange) (string, error) {
	return keepalive.Spec()
}

// validateJunk rejects a jmin/jmax junk-size range with jmin > jmax before it
// reaches the device, so a bad config fails at endpoint build / `sing-box check`
// with a clear error instead of panicking later at handshake time. The vendored
// amneziawg-go (device/send.go) sizes each junk packet rand(0..jmax-jmin)+jmin
// before a handshake initiation; jmax < jmin makes rand.Int's argument <= 0 and
// panics in the retransmit-timer goroutine. amneziawg-go's uapi checks each
// field > 0 individually but not jmin <= jmax, which is why we add it here.
//
// Only this crash case is guarded: jc>0 without sizes (sends empty junk) or
// sizes without jc (junk never sent) are wasteful but harmless and stay allowed,
// to keep the diff minimal and avoid rejecting a working real-server config.
func validateJunk(o option.AmneziaWGOptions) error {
	if o.Jmin > o.Jmax {
		return E.New("amneziawg: jmin (", F.ToString(o.Jmin), ") must be <= jmax (", F.ToString(o.Jmax), ")")
	}
	return nil
}
