package option

// lx: FEATURE 020 / SPEC 164 — test outbound `impair`.

// ImpairOutboundOptions is the JSON schema of the `impair` test outbound. The
// dialer block is the one every dialing outbound has (`detour`,
// `bind_interface`, `routing_mark`, `domain_resolver`, ...): without `detour`
// the outbound dials the destination itself, like `direct`; with it, through
// that outbound. The link keys at the top level apply to both directions;
// `up` (what this end sends) and `down` (what it receives) override them per
// direction.
type ImpairOutboundOptions struct {
	DialerOptions
	ImpairLinkOptions
	Up       *ImpairLinkOptions    `json:"up,omitempty"`
	Down     *ImpairLinkOptions    `json:"down,omitempty"`
	Stall    *ImpairStallOptions   `json:"stall,omitempty"`
	Schedule []ImpairScheduleEntry `json:"schedule,omitempty"`
	Start    string                `json:"start,omitempty"`
	Profile  string                `json:"profile,omitempty"`
	Seed     *int64                `json:"seed,omitempty"`
}

// ImpairLinkOptions are the bottleneck parameters of one direction. A key
// left out keeps the value of the layer below (defaults < profile < top level
// < up/down; a schedule entry changes the running value).
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
}

// ImpairVaryOptions makes the rate jump to a uniformly drawn value in
// [min, max] every `every`.
type ImpairVaryOptions struct {
	Min   string `json:"min,omitempty"`
	Max   string `json:"max,omitempty"`
	Every string `json:"every,omitempty"`
}

// ImpairStallOptions adds random silent outages of the whole link (both
// directions): the gaps between them are exponential with mean `every`, each
// lasts `duration`.
type ImpairStallOptions struct {
	Every    string `json:"every,omitempty"`
	Duration string `json:"duration,omitempty"`
}

// ImpairScheduleEntry changes the link at a moment: link keys, an outage
// on/off, optionally limited to one direction.
type ImpairScheduleEntry struct {
	At        string `json:"at,omitempty"`
	Direction string `json:"direction,omitempty"`
	ImpairLinkOptions
	Outage *bool `json:"outage,omitempty"`
	Reset  bool  `json:"reset,omitempty"`
}
