package lxmtu

import (
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

// Mode is the lx.mtu_align policy (SPEC 120 §2.3). The zero value is the
// default, clamp.
type Mode uint8

const (
	// ModeClamp fills absent fields and lowers explicit values that do not
	// fit (default).
	ModeClamp Mode = iota
	// ModeFill fills absent fields; an explicit value that does not fit is
	// kept and reported.
	ModeFill
	// ModeOff touches nothing.
	ModeOff
)

const (
	modeClampName = "clamp"
	modeFillName  = "fill"
	modeOffName   = "off"
)

// ModeNames lists the accepted spellings, for error messages.
var ModeNames = []string{modeOffName, modeFillName, modeClampName}

// ParseMode parses a mode name; an empty string is the default (clamp).
func ParseMode(name string) (Mode, error) {
	switch name {
	case "", modeClampName:
		return ModeClamp, nil
	case modeFillName:
		return ModeFill, nil
	case modeOffName:
		return ModeOff, nil
	}
	return ModeClamp, E.New("unknown mtu_align mode ", strconv.Quote(name), " (expected one of ", strings.Join(ModeNames, ", "), ")")
}

func (m Mode) String() string {
	switch m {
	case ModeFill:
		return modeFillName
	case ModeOff:
		return modeOffName
	}
	return modeClampName
}

// Policy is a mode plus the tags that are never touched in any mode.
type Policy struct {
	Mode   Mode
	Except map[string]bool
}

// NewPolicy builds a Policy from the resolved option values.
func NewPolicy(mode string, except []string) (Policy, error) {
	parsed, err := ParseMode(mode)
	if err != nil {
		return Policy{}, err
	}
	policy := Policy{Mode: parsed}
	if len(except) > 0 {
		policy.Except = make(map[string]bool, len(except))
		for _, tag := range except {
			policy.Except[tag] = true
		}
	}
	return policy, nil
}

// Excepted reports whether tag is exempt from alignment.
func (p Policy) Excepted(tag string) bool {
	return p.Except[tag]
}

// Decision is the outcome of aligning one field of one node. A decision with
// Changed is a value we wrote; one with only Warning is a value we did not
// touch but want to be seen in the log.
type Decision struct {
	Tag   string
	Field string
	// Configured is the value before alignment: the explicit one, or the
	// type's default (Explicit false) when the field was absent; 0 when the
	// field was absent and the type has no default we know of.
	Configured uint32
	Effective  uint32
	// Explicit reports that Configured came from the config, not a default.
	Explicit bool
	Changed  bool
	// Reason explains Effective: the capacity below, the overhead subtracted
	// and (for QUIC) the PMTUD flag.
	Reason string
	// Warning is a line for the Warn log, empty when there is nothing to say.
	Warning string
}

// Apply is the mode table of SPEC 120 §2.3 for one field. configured is the
// value in effect before alignment (0 = absent without a default); explicit
// reports that it was set in the config; limit is the largest value that fits
// the path; reason describes where the limit comes from. No mode ever raises
// a value.
func (p Policy) Apply(tag, field string, configured uint32, explicit bool, limit uint32, reason string) Decision {
	decision := Decision{Tag: tag, Field: field, Configured: configured, Effective: configured, Explicit: explicit}
	if p.Mode == ModeOff {
		return decision
	}
	if p.Excepted(tag) {
		decision.Reason = "kept (excepted)"
		return decision
	}
	if configured == 0 {
		decision.Effective = limit
		decision.Changed = true
		decision.Reason = reason
		return decision
	}
	if configured <= limit {
		decision.Reason = "fits (" + reason + ")"
		return decision
	}
	if explicit && p.Mode == ModeFill {
		decision.Reason = "kept (explicit, fill)"
		decision.Warning = tag + " " + field + " " + strconv.Itoa(int(configured)) + " does not fit (" + reason + "; limit " + strconv.Itoa(int(limit)) + ")"
		return decision
	}
	decision.Effective = limit
	decision.Changed = true
	decision.Reason = reason
	return decision
}

// String renders the Info log line of a changed decision (SPEC 120 §4):
//
//	hy2-us initial_packet_size → 1232 (detour warp[masque] mtu 1280 − 48 ipv6; pmtud off)
//	wg-exit mtu 1420 → 1200 clamped (limited by warp[masque] mtu 1280 via sel −80 ipv6)
//
// A decision that changed nothing renders its reason, or its warning.
func (d Decision) String() string {
	var b strings.Builder
	b.WriteString(d.Tag)
	b.WriteByte(' ')
	b.WriteString(d.Field)
	if !d.Changed {
		if d.Warning != "" {
			return d.Warning
		}
		b.WriteString(" ")
		b.WriteString(strconv.Itoa(int(d.Effective)))
		if d.Reason != "" {
			b.WriteString(" (")
			b.WriteString(d.Reason)
			b.WriteByte(')')
		}
		return b.String()
	}
	if d.Configured != 0 {
		b.WriteByte(' ')
		b.WriteString(strconv.Itoa(int(d.Configured)))
	}
	b.WriteString(" → ")
	b.WriteString(strconv.Itoa(int(d.Effective)))
	if d.Explicit {
		b.WriteString(" clamped")
	} else if d.Configured != 0 {
		b.WriteString(" (default)")
	}
	if d.Reason != "" {
		b.WriteString(" (")
		b.WriteString(d.Reason)
		b.WriteByte(')')
	}
	return b.String()
}

// ChainReason is the mtu_reason text for ChainInfo: the reason with the mode
// prefix of SPEC 120 §4 (`clamped:` / `filled:` / `kept (explicit, fill)`).
func (d Decision) ChainReason() string {
	switch {
	case d.Changed && d.Explicit:
		return "clamped: " + d.Reason
	case d.Changed:
		return "filled: " + d.Reason
	case d.Warning != "" && d.Reason != "":
		return d.Reason + ": " + d.Warning
	case d.Warning != "":
		return d.Warning
	}
	return d.Reason
}
