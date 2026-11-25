package option

import (
	"bytes"
	"reflect"

	"github.com/sagernet/sing-box/schema"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
)

// impairOff is how a value is turned off: the config decoder drops empty
// arrays and objects, so [] and {} cannot mean "off": false does.
const impairOff = `false`

// impairOffOr decodes false into off, anything else into value; true, [] and {}
// are refused, since in a config file they would silently vanish.
func impairOffOr(content []byte, off *bool, value any) error {
	trimmed := bytes.TrimSpace(content)
	switch string(trimmed) {
	case impairOff:
		*off = true
		return nil
	case "true":
		return E.New(`true has no meaning here: write the value, or false to turn it off`)
	}
	if len(trimmed) > 0 && (trimmed[0] == '[' || trimmed[0] == '{') {
		var probe any
		if json.Unmarshal(trimmed, &probe) == nil {
			switch v := probe.(type) {
			case []any:
				if len(v) == 0 {
					return E.New(`empty []: write false to turn it off`)
				}
			case map[string]any:
				if len(v) == 0 {
					return E.New(`empty {}: write false to turn it off`)
				}
			}
		}
	}
	return json.UnmarshalDisallowUnknownFields(content, value)
}

// lx: FEATURE 020 / SPEC 164 — test outbound `impair`.

// ImpairOutboundOptions is the JSON schema of the `impair` test outbound. The
// dialer block is the one every dialing outbound has (`detour`,
// `bind_interface`, `routing_mark`, `domain_resolver`, `connect_timeout`,
// ...): without `detour` the outbound dials the destination itself, like
// `direct`; with it, through that outbound.
//
// One object says how the link is degraded, link_params, and it is the same
// everywhere: the outbound's own line, a sublink, a schedule entry, the
// SetImpair command and a profile in lx.impair_profiles.
type ImpairOutboundOptions struct {
	DialerOptions
	LinkParams     *ImpairLinkParams     `json:"link_params,omitempty"`
	Sublinks       []ImpairSublink       `json:"sublinks,omitempty"`
	Schedule       []ImpairScheduleEntry `json:"schedule,omitempty"`
	ScheduleRepeat string                `json:"schedule_repeat,omitempty"`
	Start          string                `json:"start,omitempty"`
	Seed           *int64                `json:"seed,omitempty"`
}

// ImpairLinkParams is how a line is degraded. With `profile` the line starts
// from that template (every other value back to its default) and the other
// keys tune it; without it the keys change only themselves. The link keys at
// the top apply to both directions, `up` (what this end sends) and `down`
// (what it receives) override them per direction.
type ImpairLinkParams struct {
	Profile string `json:"profile,omitempty"`
	ImpairLinkOptions
	Up    *ImpairLinkOptions  `json:"up,omitempty"`
	Down  *ImpairLinkOptions  `json:"down,omitempty"`
	Stall *ImpairStallOptions `json:"stall,omitempty"`
	// Reset breaks the connections of the line when an outage begins in the
	// same link_params; in a sublink it refuses the matched connections.
	Reset bool `json:"reset,omitempty"`
}

// ImpairLinkOptions are the parameters of one direction's line.
type ImpairLinkOptions struct {
	Rate        string             `json:"rate,omitempty"`
	Queue       string             `json:"queue,omitempty"`
	Delay       string             `json:"delay,omitempty"`
	Jitter      string             `json:"jitter,omitempty"`
	Reorder     *bool              `json:"reorder,omitempty"`
	Loss        string             `json:"loss,omitempty"`
	LossBurst   *float64           `json:"loss_burst,omitempty"`
	StreamStall string             `json:"stream_stall,omitempty"`
	Vary        *ImpairVaryOptions `json:"vary,omitempty"`
	Outage      *bool              `json:"outage,omitempty"`
}

// ImpairVaryOptions makes the rate jump to a uniformly drawn value in
// [min, max] every `every`; false turns the swings off.
type ImpairVaryOptions struct {
	Min   string `json:"min,omitempty"`
	Max   string `json:"max,omitempty"`
	Every string `json:"every,omitempty"`
	Off   bool   `json:"-"`
}

type impairVaryPlain ImpairVaryOptions

func (o ImpairVaryOptions) MarshalJSON() ([]byte, error) {
	if o.Off {
		return []byte(impairOff), nil
	}
	return json.Marshal(impairVaryPlain(o))
}

func (o *ImpairVaryOptions) UnmarshalJSON(content []byte) error {
	return impairOffOr(content, &o.Off, (*impairVaryPlain)(o))
}

// ImpairStallOptions adds random silent outages of the whole line (both
// directions): the gaps between them are exponential with mean `every`, each
// lasts `duration`; false turns them off.
type ImpairStallOptions struct {
	Every    string `json:"every,omitempty"`
	Duration string `json:"duration,omitempty"`
	Off      bool   `json:"-"`
}

type impairStallPlain ImpairStallOptions

func (o ImpairStallOptions) MarshalJSON() ([]byte, error) {
	if o.Off {
		return []byte(impairOff), nil
	}
	return json.Marshal(impairStallPlain(o))
}

func (o *ImpairStallOptions) UnmarshalJSON(content []byte) error {
	return impairOffOr(content, &o.Off, (*impairStallPlain)(o))
}

// ImpairSublink is a second stage after the outbound's line for the
// connections that match: both keys are required, and match needs at least
// one field.
type ImpairSublink struct {
	Match      *ImpairMatch      `json:"match,omitempty"`
	LinkParams *ImpairLinkParams `json:"link_params,omitempty"`
}

// ImpairMatch selects connections when they open: the fields AND together,
// the values of one field OR. every and share pick among the connections
// that match the other fields.
type ImpairMatch struct {
	Domain       badoption.Listable[string] `json:"domain,omitempty"`
	DomainSuffix badoption.Listable[string] `json:"domain_suffix,omitempty"`
	IPCIDR       badoption.Listable[string] `json:"ip_cidr,omitempty"`
	Port         badoption.Listable[uint16] `json:"port,omitempty"`
	Network      badoption.Listable[string] `json:"network,omitempty"`
	Every        int                        `json:"every,omitempty"`
	Share        string                     `json:"share,omitempty"`
}

// ImpairScheduleEntry changes the outbound at a moment: its line
// (link_params), its sublinks (the whole list; false removes them), or
// both. SetImpair takes the same object without `at`.
type ImpairScheduleEntry struct {
	At         string             `json:"at,omitempty"`
	LinkParams *ImpairLinkParams  `json:"link_params,omitempty"`
	Sublinks   *ImpairSublinkList `json:"sublinks,omitempty"`
}

// ImpairSublinkList is the sublinks of a schedule entry: a list that
// replaces the running one, or false (no sublinks).
type ImpairSublinkList struct {
	List []ImpairSublink
	Off  bool
}

func (l ImpairSublinkList) MarshalJSON() ([]byte, error) {
	if l.Off {
		return []byte(impairOff), nil
	}
	return json.Marshal(l.List)
}

func (l *ImpairSublinkList) UnmarshalJSON(content []byte) error {
	return impairOffOr(content, &l.Off, &l.List)
}

// ImpairProfile is a named link template of lx.impair_profiles.
type ImpairProfile struct {
	LinkParams *ImpairLinkParams `json:"link_params,omitempty"`
}

func (ImpairVaryOptions) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return builder.Define("ImpairVaryOptions", func() (*schema.Node, error) {
		objectForm := schema.StrictObject()
		if err := builder.FlattenStruct(objectForm, reflect.TypeFor[impairVaryPlain]()); err != nil {
			return nil, err
		}
		return schema.AnyOf(impairOffSchema(), objectForm), nil
	})
}

func (ImpairStallOptions) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return builder.Define("ImpairStallOptions", func() (*schema.Node, error) {
		objectForm := schema.StrictObject()
		if err := builder.FlattenStruct(objectForm, reflect.TypeFor[impairStallPlain]()); err != nil {
			return nil, err
		}
		return schema.AnyOf(impairOffSchema(), objectForm), nil
	})
}

func (ImpairSublinkList) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return builder.Define("ImpairSublinkList", func() (*schema.Node, error) {
		element, err := builder.Describe(reflect.TypeFor[ImpairSublink]())
		if err != nil {
			return nil, err
		}
		return schema.AnyOf(impairOffSchema(), &schema.Node{Type: "array", Items: element}), nil
	})
}

// impairOffSchema is the JSON false that turns a value off (a const false
// would be dropped by omitempty, an enum keeps it).
func impairOffSchema() *schema.Node {
	return &schema.Node{Type: "boolean", Enum: []any{false}}
}
