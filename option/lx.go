package option

import (
	"reflect"
	"time"

	"github.com/sagernet/sing-box/schema"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
)

// LXOptions is the root `lx` block: every global knob of the fork, grouped by
// subsystem (lx: SPEC 098). Per-node settings stay in their node objects, as
// upstream does. An empty block and an absent one are equivalent.
//
// There is deliberately no `naive` sub-block yet: its keys arrive with SPEC 096,
// and until then an unknown sub-block is rejected like any unknown key.
type LXOptions struct {
	WG     *LXWGOptions     `json:"wg,omitempty"`
	MASQUE *LXMASQUEOptions `json:"masque,omitempty"`
	// MTUAlign is the path-MTU alignment policy for nodes above IP tunnels
	// (lx: SPEC 120): a mode string, or an object with the mode and the tags
	// to leave alone. Absent = "clamp".
	MTUAlign *LXMTUAlign `json:"mtu_align,omitempty"`
}

// LXWGOptions holds the WG/AWG endpoint knobs. The three idle keys keep the
// SPEC 020 semantics they had as route.lx_idle_*; they act only in builds with
// with_lx_idle_suspend, and the router refuses them at start otherwise.
type LXWGOptions struct {
	// IdleSuspend is the idle threshold after which an endpoint outside the
	// active routing tree is brought Down. 0 / absent disables the idle tick.
	IdleSuspend badoption.Duration `json:"idle_suspend,omitempty"`
	// IdleSuspendReachable is the longer threshold for endpoints inside the
	// tree. Must be >= IdleSuspend and requires it.
	IdleSuspendReachable badoption.Duration `json:"idle_suspend_reachable,omitempty"`
	// IdleTeardown is how long an already-sleeping endpoint sleeps before its
	// device and netstack are torn down. Absent inherits IdleSuspendReachable;
	// an explicit "0" disables teardown, hence the pointer. Requires IdleSuspend.
	IdleTeardown *badoption.Duration `json:"idle_teardown,omitempty"`
	// LazyBuild starts endpoints torn down and builds the device on first dial
	// (SPEC 097). Requires IdleSuspend. Parsed and validated only until 097.
	LazyBuild bool `json:"lazy_build,omitempty"`
	// BuildMax caps the number of simultaneously built devices; 0 = no cap
	// (SPEC 097). Parsed and validated only until 097.
	BuildMax int `json:"build_max,omitempty"`
	// BuildOverflow is what happens when all BuildMax devices carry live
	// connections: "wait" (default) or "build" (SPEC 097). Parsed and validated
	// only until 097.
	BuildOverflow string `json:"build_overflow,omitempty"`
}

// LXMASQUEOptions holds the MASQUE outbound knobs.
type LXMASQUEOptions struct {
	// IdleTimeout is the global default idle window for every masque outbound
	// without its own idle_timeout. A node's own key wins, including an
	// explicit "0" there, which keeps that node's tunnel up.
	IdleTimeout badoption.Duration `json:"idle_timeout,omitempty"`
}

// The lx.mtu_align modes (lx: SPEC 120 §2.3). The parsing side of the same
// table lives in common/lxmtu, which this package must not import.
const (
	LXMTUAlignOff   = "off"
	LXMTUAlignFill  = "fill"
	LXMTUAlignClamp = "clamp"
)

type _LXMTUAlign struct {
	Mode   string                     `json:"mode,omitempty" enum:"off,fill,clamp"`
	Except badoption.Listable[string] `json:"except,omitempty"`
}

// LXMTUAlign is lx.mtu_align in either of its two JSON forms: a bare mode
// string, or {"mode": …, "except": […]}. The string form is the canonical
// output when there are no exceptions.
type LXMTUAlign _LXMTUAlign

func (o LXMTUAlign) MarshalJSON() ([]byte, error) {
	if len(o.Except) == 0 {
		return json.Marshal(o.Mode)
	}
	return json.Marshal(_LXMTUAlign(o))
}

func (o *LXMTUAlign) UnmarshalJSON(bytes []byte) error {
	var mode string
	if err := json.Unmarshal(bytes, &mode); err == nil {
		*o = LXMTUAlign{Mode: mode}
		return validateLXMTUAlignMode(mode)
	}
	if err := json.UnmarshalDisallowUnknownFields(bytes, (*_LXMTUAlign)(o)); err != nil {
		return err
	}
	return validateLXMTUAlignMode(o.Mode)
}

func (o LXMTUAlign) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return builder.Define("LXMTUAlign", func() (*schema.Node, error) {
		objectForm := schema.StrictObject()
		err := builder.FlattenStruct(objectForm, reflect.TypeFor[LXMTUAlign]())
		if err != nil {
			return nil, err
		}
		return schema.AnyOf(schema.StringEnum(LXMTUAlignOff, LXMTUAlignFill, LXMTUAlignClamp), objectForm), nil
	})
}

func validateLXMTUAlignMode(mode string) error {
	switch mode {
	case "", LXMTUAlignOff, LXMTUAlignFill, LXMTUAlignClamp:
		return nil
	}
	return E.New(`lx.mtu_align.mode must be "off", "fill" or "clamp"`)
}

// LXBuildOverflow is the resolved lx.wg.build_overflow.
type LXBuildOverflow uint8

const (
	LXBuildOverflowWait LXBuildOverflow = iota
	LXBuildOverflowBuild
)

// LXResolved carries the resolved `lx` values: aliases folded in, defaults
// applied, validated. It is registered in the box context (service.ContextWithPtr)
// and read by the router, WG endpoints and masque outbounds. A nil *LXResolved
// means every knob is off.
type LXResolved struct {
	WG     LXWGResolved
	MASQUE LXMASQUEResolved
	// MTUAlign always carries a mode: "clamp" when the key is absent.
	MTUAlign LXMTUAlignResolved
}

type LXWGResolved struct {
	IdleSuspend          time.Duration
	IdleSuspendReachable time.Duration
	// IdleTeardown is the effective level-3 window: the explicit value, or
	// IdleSuspendReachable when the key is absent. An explicit "0" stays 0.
	IdleTeardown time.Duration
	// IdleTeardownSet records that idle_teardown was present, which an
	// explicit "0" makes indistinguishable from absent in IdleTeardown alone.
	IdleTeardownSet bool
	LazyBuild       bool
	BuildMax        int
	BuildOverflow   LXBuildOverflow
}

type LXMASQUEResolved struct {
	IdleTimeout time.Duration
}

// LXMTUAlignResolved is the resolved lx.mtu_align (lx: SPEC 120).
type LXMTUAlignResolved struct {
	Mode   string
	Except []string
}

// WGOrZero returns the WG values, or all-off when r is nil.
func (r *LXResolved) WGOrZero() LXWGResolved {
	if r == nil {
		return LXWGResolved{}
	}
	return r.WG
}

// MASQUEOrZero returns the MASQUE values, or all-off when r is nil.
func (r *LXResolved) MASQUEOrZero() LXMASQUEResolved {
	if r == nil {
		return LXMASQUEResolved{}
	}
	return r.MASQUE
}

// MTUAlignOrDefault returns the mtu_align policy, or the default (clamp,
// no exceptions) when r is nil.
func (r *LXResolved) MTUAlignOrDefault() LXMTUAlignResolved {
	if r == nil || r.MTUAlign.Mode == "" {
		return LXMTUAlignResolved{Mode: LXMTUAlignClamp}
	}
	return r.MTUAlign
}

// ResolveLX folds the deprecated route.lx_idle_* aliases into the `lx` block,
// validates it and returns the resolved values plus one warning per alias used.
//
// On success options is left in canonical form: options.LX carries every value
// (nil when nothing is set) and the route.lx_idle_* fields are cleared. Both
// options.LX and options.Route are replaced by fresh copies, never written
// through, so a shallow copy of the caller's Options can be resolved without
// touching the original. On error options is not modified. Resolving a
// canonical Options again is a no-op without warnings.
func ResolveLX(options *Options) (*LXResolved, []string, error) {
	var lx LXOptions
	if options.LX != nil {
		lx = *options.LX
	}
	var wg LXWGOptions
	if lx.WG != nil {
		wg = *lx.WG
	}
	var route RouteOptions
	if options.Route != nil {
		route = *options.Route
	}

	var warnings []string
	aliasUsed := false
	alias := func(key string, legacySet, currentSet, equal bool) error {
		if !legacySet {
			return nil
		}
		if currentSet && !equal {
			return E.New("route.lx_", key, " conflicts with lx.wg.", key)
		}
		warnings = append(warnings, "route.lx_"+key+" is deprecated, use lx.wg."+key)
		aliasUsed = true
		return nil
	}
	err := alias("idle_suspend", route.LXIdleSuspend != 0, wg.IdleSuspend != 0,
		route.LXIdleSuspend == wg.IdleSuspend)
	if err != nil {
		return nil, nil, err
	}
	if route.LXIdleSuspend != 0 {
		wg.IdleSuspend = route.LXIdleSuspend
	}
	err = alias("idle_suspend_reachable", route.LXIdleSuspendReachable != 0, wg.IdleSuspendReachable != 0,
		route.LXIdleSuspendReachable == wg.IdleSuspendReachable)
	if err != nil {
		return nil, nil, err
	}
	if route.LXIdleSuspendReachable != 0 {
		wg.IdleSuspendReachable = route.LXIdleSuspendReachable
	}
	err = alias("idle_teardown", route.LXIdleTeardown != nil, wg.IdleTeardown != nil,
		route.LXIdleTeardown != nil && wg.IdleTeardown != nil && *route.LXIdleTeardown == *wg.IdleTeardown)
	if err != nil {
		return nil, nil, err
	}
	if route.LXIdleTeardown != nil {
		value := *route.LXIdleTeardown
		wg.IdleTeardown = &value
	}

	resolved, err := resolveLXValues(wg, lx.MASQUE, lx.MTUAlign)
	if err != nil {
		return nil, nil, err
	}

	if wg != (LXWGOptions{}) {
		lx.WG = &wg
	} else {
		lx.WG = nil
	}
	if lx.MASQUE != nil && *lx.MASQUE == (LXMASQUEOptions{}) {
		lx.MASQUE = nil
	}
	if lx.MTUAlign != nil && lx.MTUAlign.Mode == "" && len(lx.MTUAlign.Except) == 0 {
		lx.MTUAlign = nil
	}
	if lx.WG == nil && lx.MASQUE == nil && lx.MTUAlign == nil {
		options.LX = nil
	} else {
		options.LX = &lx
	}
	if aliasUsed {
		route.LXIdleSuspend = 0
		route.LXIdleSuspendReachable = 0
		route.LXIdleTeardown = nil
		options.Route = &route
	}
	return resolved, warnings, nil
}

func resolveLXValues(wg LXWGOptions, masque *LXMASQUEOptions, mtuAlign *LXMTUAlign) (*LXResolved, error) {
	var resolved LXResolved
	resolved.MTUAlign.Mode = LXMTUAlignClamp
	if mtuAlign != nil {
		if err := validateLXMTUAlignMode(mtuAlign.Mode); err != nil {
			return nil, err
		}
		if mtuAlign.Mode != "" {
			resolved.MTUAlign.Mode = mtuAlign.Mode
		}
		for _, tag := range mtuAlign.Except {
			if tag == "" {
				return nil, E.New("lx.mtu_align.except: empty tag")
			}
		}
		if len(mtuAlign.Except) > 0 {
			resolved.MTUAlign.Except = append([]string(nil), mtuAlign.Except...)
		}
	}
	if wg.IdleSuspend < 0 {
		return nil, E.New("lx.wg.idle_suspend must be >= 0")
	}
	if wg.IdleSuspendReachable < 0 {
		return nil, E.New("lx.wg.idle_suspend_reachable must be >= 0")
	}
	if wg.IdleTeardown != nil && *wg.IdleTeardown < 0 {
		return nil, E.New("lx.wg.idle_teardown must be >= 0")
	}
	if wg.IdleSuspend == 0 {
		if wg.IdleSuspendReachable > 0 {
			return nil, E.New("lx.wg.idle_suspend_reachable requires lx.wg.idle_suspend")
		}
		if wg.IdleTeardown != nil {
			return nil, E.New("lx.wg.idle_teardown requires lx.wg.idle_suspend")
		}
		if wg.LazyBuild {
			return nil, E.New("lx.wg.lazy_build requires lx.wg.idle_suspend")
		}
	}
	if wg.IdleSuspendReachable > 0 && wg.IdleSuspendReachable < wg.IdleSuspend {
		return nil, E.New("lx.wg.idle_suspend_reachable must be >= lx.wg.idle_suspend")
	}
	if wg.BuildMax < 0 {
		return nil, E.New("lx.wg.build_max must be >= 0")
	}
	switch wg.BuildOverflow {
	case "", "wait":
		resolved.WG.BuildOverflow = LXBuildOverflowWait
	case "build":
		resolved.WG.BuildOverflow = LXBuildOverflowBuild
	default:
		return nil, E.New(`lx.wg.build_overflow must be "wait" or "build"`)
	}
	resolved.WG.IdleSuspend = time.Duration(wg.IdleSuspend)
	resolved.WG.IdleSuspendReachable = time.Duration(wg.IdleSuspendReachable)
	if wg.IdleTeardown != nil {
		resolved.WG.IdleTeardown = time.Duration(*wg.IdleTeardown)
		resolved.WG.IdleTeardownSet = true
	} else {
		resolved.WG.IdleTeardown = time.Duration(wg.IdleSuspendReachable)
	}
	resolved.WG.LazyBuild = wg.LazyBuild
	resolved.WG.BuildMax = wg.BuildMax
	if masque != nil {
		if masque.IdleTimeout < 0 {
			return nil, E.New("lx.masque.idle_timeout must be >= 0")
		}
		resolved.MASQUE.IdleTimeout = time.Duration(masque.IdleTimeout)
	}
	return &resolved, nil
}
