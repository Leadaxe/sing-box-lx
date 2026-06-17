//go:build with_awg

package wireguard

import (
	"strings"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
)

// awgIpcLines renders the AmneziaWG 2.0 device-global obfuscation parameters as
// amneziawg-go IpcSet config lines, ready to be appended to the WireGuard
// device configuration (after private_key/listen_port, before peer sections).
//
// Output format (one key per line, leading "\n", no trailing newline):
//
//	\njc=<n>\njmin=<n>\njmax=<n>\ns1=<n>\ns2=<n>\nh1=<spec>\nh2=<spec>\nh3=<spec>\nh4=<spec>
//	\ni1=<str>\ni2=<str>...
//
// Numeric keys are emitted only when non-zero. The h1..h4 values are magic
// header specs — a single uint32 ("N") or an inclusive range ("N-M", AWG 2.0)
// — emitted in the exact format newMagicHeader expects on the uapi side;
// unset headers are omitted. The I1..I5 keys are emitted only
// when non-empty and are written verbatim — their value is case-sensitive
// (uppercase keywords) and must not be normalised. Returns "" when no AWG
// parameter is set, so a plain WireGuard endpoint produces byte-identical
// config to upstream even in a `with_awg` build.
//
// These keys correspond to the AmneziaWG handshake-obfuscation knobs parsed by
// amneziawg-go's device.IpcSetOperation. With `with_awg` the wireguard-go
// dependency is replaced by amnezia-vpn/amneziawg-go, which understands them;
// upstream wireguard-go would reject the unknown keys at IpcSet time.
func awgIpcLines(o option.AmneziaWGOptions) (string, error) {
	if !o.IsSet() {
		return "", nil
	}
	if err := validateJunk(o); err != nil {
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
	writeMagic := func(key string, value option.MagicHeader) error {
		spec, err := value.Spec()
		if err != nil {
			return E.Cause(err, key)
		}
		writeStr(key, spec)
		return nil
	}
	// WireSock-style id/ip/ib masquerade is sugar over I1 (and, for ip=quic, I2):
	// it generates the CPS strings. masqueI1 returns "" when no masquerade is set,
	// and errors on a conflict with an explicit I1 or on invalid id/ip/ib. When
	// set, its output is used as the i1 value below.
	i1 := o.I1
	i2 := o.I2
	masque, err := masqueI1(o)
	if err != nil {
		return "", err
	}
	if masque != "" {
		i1 = masque
		// ip=quic also fills i2 with a second independent fragmented Initial
		// (developing-session decoy). A user-supplied i2 alongside id/ip/ib is
		// ambiguous, exactly like the i1 conflict masqueI1 already rejects.
		masque2, err := masqueI2(o)
		if err != nil {
			return "", err
		}
		if masque2 != "" {
			if o.I2 != "" {
				return "", E.New("amneziawg: id/ip/ib masquerade (ip=quic) fills i2; an explicit i2 conflicts with it")
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
		if err := writeMagic(header.key, header.value); err != nil {
			return "", err
		}
	}
	writeStr("i1", i1)
	writeStr("i2", i2)
	writeStr("i3", o.I3)
	writeStr("i4", o.I4)
	writeStr("i5", o.I5)
	return b.String(), nil
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
