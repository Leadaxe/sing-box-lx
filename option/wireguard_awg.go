package option

import (
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

// MagicHeader is one AmneziaWG magic-header parameter (H1..H4): either a
// single uint32 or an inclusive "min-max" range (AWG 2.0 export format, e.g.
// "43613244-384550127"). It holds the canonical spec string understood by the
// device layer (newMagicHeader in the vendored wireguard-go): "N" for a single
// value, "N-M" for a range, "" for unset.
//
// The underlying type is string so AmneziaWGOptions stays comparable (IsSet
// relies on o != AmneziaWGOptions{}) and the zero value means "not set", as
// the zero uint32 did before.
type MagicHeader string

// UnmarshalJSON accepts a JSON number (backward compatible with the previous
// uint32 field: "h1": 1234567890) or a JSON string "N" / "N-M". The single
// value 0 (and "", "0") normalizes to unset, preserving the previous
// zero-value semantics.
func (h *MagicHeader) UnmarshalJSON(data []byte) error {
	var spec string
	if len(data) > 0 && data[0] == '"' {
		err := json.Unmarshal(data, &spec)
		if err != nil {
			return err
		}
	} else {
		var value uint32
		err := json.Unmarshal(data, &value)
		if err != nil {
			return E.New("invalid magic header ", string(data), ": expected uint32 or \"min-max\" string")
		}
		spec = strconv.FormatUint(uint64(value), 10)
	}
	normalized, err := normalizeMagicHeader(spec)
	if err != nil {
		return err
	}
	*h = normalized
	return nil
}

// MarshalJSON keeps type fidelity with the previous uint32 field: a single
// value marshals back to a JSON number, only a range becomes a JSON string.
func (h MagicHeader) MarshalJSON() ([]byte, error) {
	normalized, err := normalizeMagicHeader(string(h))
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return []byte("0"), nil
	}
	if !strings.Contains(string(normalized), "-") {
		return []byte(normalized), nil
	}
	return json.Marshal(string(normalized))
}

// Spec re-validates the header and returns the canonical IpcSet value (""
// when unset). The device layer calls it so options constructed in code
// (libbox/launcher, bypassing JSON) are still checked before IpcSet.
func (h MagicHeader) Spec() (string, error) {
	normalized, err := normalizeMagicHeader(string(h))
	return string(normalized), err
}

// normalizeMagicHeader validates spec as "N" or "N-M" (both uint32, N ≤ M)
// and returns the canonical form: "" for unset/zero, "N" for a single value
// (including "N-N"), "N-M" for a range — mirroring magic-header.go in the
// vendored wireguard-go.
func normalizeMagicHeader(spec string) (MagicHeader, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", nil
	}
	parts := strings.Split(spec, "-")
	if len(parts) > 2 {
		return "", E.New("invalid magic header ", strconv.Quote(spec), ": expected uint32 or \"min-max\"")
	}
	start, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 32)
	if err != nil {
		return "", E.New("invalid magic header ", strconv.Quote(spec), ": parse ", strconv.Quote(parts[0]), ": ", err)
	}
	end := start
	if len(parts) == 2 {
		end, err = strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
		if err != nil {
			return "", E.New("invalid magic header ", strconv.Quote(spec), ": parse ", strconv.Quote(parts[1]), ": ", err)
		}
	}
	if end < start {
		return "", E.New("invalid magic header ", strconv.Quote(spec), ": range start > end")
	}
	if end == 0 {
		return "", nil
	}
	if start == end {
		return MagicHeader(strconv.FormatUint(start, 10)), nil
	}
	return MagicHeader(strconv.FormatUint(start, 10) + "-" + strconv.FormatUint(end, 10)), nil
}

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
//   - H1..H4: magic header values overriding the four WireGuard message types;
//     each is a single uint32 or an inclusive "min-max" range (AWG 2.0).
//   - I1..I5: AmneziaWG 2.0 "controlled packet sequence" (CPS) packets. These
//     are case-sensitive strings (UPPERCASE keywords like <b 0x...>, <c>, <t>,
//     <r N>) and the order matters; I1 is typically a real protocol snapshot
//     (e.g. a QUIC Initial). They map 1:1 to the amneziawg-go i1..i5 keys.
//   - Id/Ip/Ib: WireSock-style declarative masquerade sugar over I1. Instead of
//     hand-writing an I1 CPS string, the user names a masquerade domain (Id),
//     protocol (Ip: quic|dns|stun|sip) and browser (Ib), and the device layer
//     (transport/wireguard/masque_awg.go) generates the I1 CPS string for them.
//     They are mutually exclusive with an explicit I1. See SPECS/TASKS/009-*.
type AmneziaWGOptions struct {
	Jc   uint32      `json:"jc,omitempty"`
	Jmin uint32      `json:"jmin,omitempty"`
	Jmax uint32      `json:"jmax,omitempty"`
	S1   uint32      `json:"s1,omitempty"`
	S2   uint32      `json:"s2,omitempty"`
	S3   uint32      `json:"s3,omitempty"`
	S4   uint32      `json:"s4,omitempty"`
	H1   MagicHeader `json:"h1,omitempty"`
	H2   MagicHeader `json:"h2,omitempty"`
	H3   MagicHeader `json:"h3,omitempty"`
	H4   MagicHeader `json:"h4,omitempty"`
	I1   string      `json:"i1,omitempty"`
	I2   string      `json:"i2,omitempty"`
	I3   string      `json:"i3,omitempty"`
	I4   string      `json:"i4,omitempty"`
	I5   string      `json:"i5,omitempty"`
	Id   string      `json:"id,omitempty"` // masquerade domain; required for ip=quic/dns/sip, optional for stun
	Ip   string      `json:"ip,omitempty"` // masquerade protocol: quic | dns | stun | sip
	Ib   string      `json:"ib,omitempty"` // masquerade browser: chrome | firefox | curl (limited effect, see masque_awg.go)
}

// IsSet reports whether any AmneziaWG obfuscation parameter has been
// configured. It is used by the device builder/stub to decide whether AWG
// handling is required for this endpoint.
func (o AmneziaWGOptions) IsSet() bool {
	return o != AmneziaWGOptions{}
}
