//go:build with_awg

package wireguard

import (
	"strings"

	"github.com/sagernet/sing-box/option"
	F "github.com/sagernet/sing/common/format"
)

// awgIpcLines renders the AmneziaWG 2.0 device-global obfuscation parameters as
// amneziawg-go IpcSet config lines, ready to be appended to the WireGuard
// device configuration (after private_key/listen_port, before peer sections).
//
// Output format (one key per line, leading "\n", no trailing newline):
//
//	\njc=<n>\njmin=<n>\njmax=<n>\ns1=<n>\ns2=<n>\nh1=<n>\nh2=<n>\nh3=<n>\nh4=<n>
//	\ni1=<str>\ni2=<str>...
//
// Numeric keys are emitted only when non-zero. The I1..I5 keys are emitted only
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
	writeUint("jc", o.Jc)
	writeUint("jmin", o.Jmin)
	writeUint("jmax", o.Jmax)
	writeUint("s1", o.S1)
	writeUint("s2", o.S2)
	writeUint("s3", o.S3)
	writeUint("s4", o.S4)
	writeUint("h1", o.H1)
	writeUint("h2", o.H2)
	writeUint("h3", o.H3)
	writeUint("h4", o.H4)
	writeStr("i1", o.I1)
	writeStr("i2", o.I2)
	writeStr("i3", o.I3)
	writeStr("i4", o.I4)
	writeStr("i5", o.I5)
	return b.String(), nil
}
