package option

import (
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

// XmuxRange is an XMUX range option. Our XHTTP options express ranges as the
// "min-max" string (see V2RayXHTTPOptions), but xmux sections travel inside
// subscription configs authored against Xray and sing-box-extended, which write
// them as a two-element array. Accepting all three spellings keeps those configs
// loadable without changing what they mean:
//
//	"600-900"   our canonical string form
//	"900"       a single integer, equivalent to "900-900"
//	600         a bare JSON number, likewise
//	[600, 900]  the Xray / sing-box-extended array form
//
// The zero value marshals back as the empty string, so an unset field stays
// absent from a re-serialized config. See SPECS/TASKS/059-XHTTP_XMUX.
type XmuxRange string

// MarshalJSON emits the canonical string form.
func (r XmuxRange) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(r))
}

// UnmarshalJSON accepts the string, bare-number and [min,max] array spellings,
// normalizing all of them to the "min-max" string form.
func (r *XmuxRange) UnmarshalJSON(content []byte) error {
	var stringValue string
	if err := json.Unmarshal(content, &stringValue); err == nil {
		*r = XmuxRange(strings.TrimSpace(stringValue))
		return nil
	}
	var numberValue int
	if err := json.Unmarshal(content, &numberValue); err == nil {
		*r = XmuxRange(strconv.Itoa(numberValue))
		return nil
	}
	var arrayValue []int
	if err := json.Unmarshal(content, &arrayValue); err != nil {
		return E.New("invalid xmux range: expected \"min-max\", a number, or [min,max]")
	}
	switch len(arrayValue) {
	case 1:
		*r = XmuxRange(strconv.Itoa(arrayValue[0]))
	case 2:
		*r = XmuxRange(strconv.Itoa(arrayValue[0]) + "-" + strconv.Itoa(arrayValue[1]))
	default:
		return E.New("invalid xmux range: array form takes 1 or 2 elements, got ", len(arrayValue))
	}
	return nil
}
