package option

// AmneziaWGOptions carries the AmneziaWG (AWG) obfuscation parameters that
// extend a plain WireGuard endpoint into an AmneziaWG 2.0 client.
//
// It is a downstream (sing-box-lx) addition; upstream sing-box has no AWG
// support. The struct is always present in the option model so that configs
// parse identically with and without the `with_awg` build tag — the tag only
// gates whether these values are actually applied to the device (see
// transport/wireguard/device_awg.go vs device_stub_awg.go). Without the tag a
// config that sets any AWG field is rejected with an explicit
// "awg support not built" error rather than silently downgrading to plain
// WireGuard (which would defeat the obfuscation).
//
// Field semantics (AmneziaWG wire format, see the amneziawg-go IpcSet keys):
//   - Jc/Jmin/Jmax: junk packet count and min/max sizes sent before the
//     handshake.
//   - S1/S2: extra junk prepended to the init / response handshake messages.
//   - H1..H4: magic header values overriding the four WireGuard message types.
//   - I1..I5: AmneziaWG 2.0 "controlled packet sequence" (CPS) packets. These
//     are case-sensitive strings (UPPERCASE keywords like <b 0x...>, <c>, <t>,
//     <r N>) and the order matters; I1 is typically a real protocol snapshot
//     (e.g. a QUIC Initial). They map 1:1 to the amneziawg-go i1..i5 keys.
type AmneziaWGOptions struct {
	Jc   uint32 `json:"jc,omitempty"`
	Jmin uint32 `json:"jmin,omitempty"`
	Jmax uint32 `json:"jmax,omitempty"`
	S1   uint32 `json:"s1,omitempty"`
	S2   uint32 `json:"s2,omitempty"`
	S3   uint32 `json:"s3,omitempty"`
	S4   uint32 `json:"s4,omitempty"`
	H1   uint32 `json:"h1,omitempty"`
	H2   uint32 `json:"h2,omitempty"`
	H3   uint32 `json:"h3,omitempty"`
	H4   uint32 `json:"h4,omitempty"`
	I1   string `json:"i1,omitempty"`
	I2   string `json:"i2,omitempty"`
	I3   string `json:"i3,omitempty"`
	I4   string `json:"i4,omitempty"`
	I5   string `json:"i5,omitempty"`
}

// IsSet reports whether any AmneziaWG obfuscation parameter has been
// configured. It is used by the device builder/stub to decide whether AWG
// handling is required for this endpoint.
func (o AmneziaWGOptions) IsSet() bool {
	return o != AmneziaWGOptions{}
}
