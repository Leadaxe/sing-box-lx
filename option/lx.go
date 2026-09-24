package option

import (
	"time"

	E "github.com/sagernet/sing/common/exceptions"
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

	resolved, err := resolveLXValues(wg, lx.MASQUE)
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
	if lx.WG == nil && lx.MASQUE == nil {
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

func resolveLXValues(wg LXWGOptions, masque *LXMASQUEOptions) (*LXResolved, error) {
	var resolved LXResolved
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
