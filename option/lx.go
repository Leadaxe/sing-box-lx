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
	// TrustTunnel holds the trusttunnel outbound knobs (lx: SPEC 133).
	TrustTunnel *LXTrustTunnelOptions `json:"trusttunnel,omitempty"`
	// MTUAlign is the path-MTU alignment policy for nodes above IP tunnels
	// (lx: SPEC 120): a mode string, or an object with the mode and the tags
	// to leave alone. Absent = "clamp".
	MTUAlign   *LXMTUAlign          `json:"mtu_align,omitempty"`
	SniffGuard *LXSniffGuardOptions `json:"sniff_guard,omitempty"` // lx: SPEC 060
	// QUICSniffDecoy is removed (lx: SPEC 060): any value fails the load.
	QUICSniffDecoy *LXRemovedQUICSniffDecoy `json:"quic_sniff_decoy,omitempty"`
	// AWGMasquerade is removed (lx: SPEC 153 → 060): any value fails the load.
	AWGMasquerade *LXRemovedAWGMasquerade `json:"awg_masquerade,omitempty"`
}

// UnmarshalJSON decodes the block as a plain struct and fails on any
// presence of a removed key, null included: a null leaves a pointer field
// nil without calling the removed type's UnmarshalJSON.
func (o *LXOptions) UnmarshalJSON(content []byte) error {
	var keys map[string]json.RawMessage
	if json.Unmarshal(content, &keys) == nil {
		if _, present := keys["quic_sniff_decoy"]; present {
			return E.New(errLXQUICSniffDecoyRemoved)
		}
		if _, present := keys["awg_masquerade"]; present {
			return E.New(errLXAWGMasqueradeRemoved)
		}
	}
	return json.UnmarshalDisallowUnknownFields(content, (*lxOptionsPlain)(o))
}

// lxOptionsPlain is LXOptions without its methods.
type lxOptionsPlain LXOptions

func (o LXOptions) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return builder.Define("LXOptions", func() (*schema.Node, error) {
		node := schema.StrictObject()
		err := builder.FlattenStruct(node, reflect.TypeFor[lxOptionsPlain]())
		if err != nil {
			return nil, err
		}
		return node, nil
	})
}

// LXWGOptions holds the WG/AWG endpoint knobs (SPEC 098 §2.1). Only the idle
// sub-block needs a with_lx_idle_suspend build; buffers and connected_socket
// act in every build.
type LXWGOptions struct {
	Idle    *LXWGIdleOptions    `json:"idle,omitempty"`
	Buffers *LXWGBuffersOptions `json:"buffers,omitempty"` // SPEC 158 §9
	// ConnectedSocket connects the tunnel socket to the server address in
	// single-peer mode (SPEC 160). Linux and Android; elsewhere it is accepted
	// and changes nothing.
	ConnectedSocket bool `json:"connected_socket,omitempty"`

	// The flat idle keys moved into lx.wg.idle (owner 2026-11-23) with no
	// compatibility period: any value fails the load and names the new key.
	MovedIdleSuspend          *LXMovedWGIdleKey `json:"idle_suspend,omitempty"`
	MovedIdleSuspendReachable *LXMovedWGIdleKey `json:"idle_suspend_reachable,omitempty"`
	MovedIdleTeardown         *LXMovedWGIdleKey `json:"idle_teardown,omitempty"`
	MovedLazyBuild            *LXMovedWGIdleKey `json:"lazy_build,omitempty"`
	MovedBuildMax             *LXMovedWGIdleKey `json:"build_max,omitempty"`
	MovedBuildOverflow        *LXMovedWGIdleKey `json:"build_overflow,omitempty"`
}

// lxWGMovedKeys maps each flat key to its name in lx.wg.idle.
var lxWGMovedKeys = [...]struct{ old, new string }{
	{"idle_suspend", "suspend"},
	{"idle_suspend_reachable", "suspend_reachable"},
	{"idle_teardown", "teardown"},
	{"lazy_build", "lazy_build"},
	{"build_max", "build_max"},
	{"build_overflow", "build_overflow"},
}

// UnmarshalJSON checks the moved keys by presence, null included, so the
// error names the exact new path; the guard type alone could not tell which
// key it sits under.
func (o *LXWGOptions) UnmarshalJSON(content []byte) error {
	var keys map[string]json.RawMessage
	if json.Unmarshal(content, &keys) == nil {
		for _, key := range lxWGMovedKeys {
			if _, present := keys[key.old]; present {
				return E.New("lx.wg.", key.old, " is moved to lx.wg.idle.", key.new)
			}
		}
	}
	return json.UnmarshalDisallowUnknownFields(content, (*lxWGOptionsPlain)(o))
}

// lxWGOptionsPlain is LXWGOptions without its methods.
type lxWGOptionsPlain LXWGOptions

func (o LXWGOptions) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return builder.Define("LXWGOptions", func() (*schema.Node, error) {
		node := schema.StrictObject()
		err := builder.FlattenStruct(node, reflect.TypeFor[lxWGOptionsPlain]())
		if err != nil {
			return nil, err
		}
		return node, nil
	})
}

// LXMovedWGIdleKey is the guard on a flat lx.wg idle key. LXWGOptions catches
// the key by name first; this error is the fallback for a direct decode.
type LXMovedWGIdleKey struct{}

const errLXWGIdleKeyMoved = "the lx.wg idle keys are moved to lx.wg.idle"

func (LXMovedWGIdleKey) UnmarshalJSON([]byte) error {
	return E.New(errLXWGIdleKeyMoved)
}

func (LXMovedWGIdleKey) MarshalJSON() ([]byte, error) {
	return nil, E.New(errLXWGIdleKeyMoved)
}

func (LXMovedWGIdleKey) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return &schema.Node{Deprecated: true}, nil
}

// LXWGIdleOptions holds the idle keys: suspend, suspend_reachable and teardown
// carry the SPEC 020 semantics, lazy_build, build_max and build_overflow are
// SPEC 097. All six act only in builds with with_lx_idle_suspend; without the
// tag the router refuses them at start.
type LXWGIdleOptions struct {
	// Suspend is the idle threshold after which an endpoint outside the
	// active routing tree is brought Down. 0 / absent disables the idle tick.
	Suspend badoption.Duration `json:"suspend,omitempty"`
	// SuspendReachable is the longer threshold for endpoints inside the
	// tree. Must be >= Suspend and requires it.
	SuspendReachable badoption.Duration `json:"suspend_reachable,omitempty"`
	// Teardown is how long an already-sleeping endpoint sleeps before its
	// device and netstack are torn down. Absent inherits SuspendReachable;
	// an explicit "0" disables teardown, hence the pointer. Requires Suspend.
	Teardown *badoption.Duration `json:"teardown,omitempty"`
	// LazyBuild starts endpoints torn down and builds the device on first dial
	// (SPEC 097). Requires Suspend. An endpoint with listen_port is never
	// lazy.
	LazyBuild bool `json:"lazy_build,omitempty"`
	// BuildMax caps the number of simultaneously built devices; 0 = no cap
	// (SPEC 097). Does not require LazyBuild.
	BuildMax int `json:"build_max,omitempty"`
	// BuildOverflow is what happens when all BuildMax devices carry live
	// connections: "wait" (default) waits within the dial deadline, "build"
	// builds above the cap (SPEC 097).
	BuildOverflow string `json:"build_overflow,omitempty"`
}

// LXWGBuffersOptions holds the tunnel buffer knobs (SPEC 158 §9.1). Both
// fields are pointers so that a default written out is told apart from an
// invalid zero, and canonicalizes away.
type LXWGBuffersOptions struct {
	// ReadDatagramCount is the number of datagrams reserved per socket read,
	// 1–128; absent = 128, as upstream.
	ReadDatagramCount *int `json:"read_datagram_count,omitempty"`
	// SendByMTU sizes the buffers read from the stack by the tunnel MTU
	// instead of 64 KiB; absent = true. false is a diagnostic switch.
	SendByMTU *bool `json:"send_by_mtu,omitempty"`
}

// lxWGReadDatagramCountMax is the upstream batch size and the default.
const lxWGReadDatagramCountMax = 128

// LXMASQUEOptions holds the MASQUE outbound knobs.
type LXMASQUEOptions struct {
	// IdleTimeout is the global default idle window for every masque outbound
	// without its own idle_timeout. A node's own key wins, including an
	// explicit "0" there, which keeps that node's tunnel up.
	IdleTimeout badoption.Duration `json:"idle_timeout,omitempty"`
}

// LXTrustTunnelOptions holds the trusttunnel outbound knobs (lx: SPEC 133).
type LXTrustTunnelOptions struct {
	// IdleTimeout is the global default idle window for every trusttunnel
	// outbound without its own idle_timeout; 0 / absent = 5 minutes. A node's
	// own key wins, including an explicit "0" there, which disables teardown.
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

// LXRemovedAWGMasquerade is the guard on the removed lx.awg_masquerade (SPEC
// 153, removed by SPEC 060, owner 2026-11-07): any value is a load error, so a
// config that still carries the key fails with the reason instead of an
// unknown-field message. The masquerade burst is sent on every path.
type LXRemovedAWGMasquerade struct{}

const errLXAWGMasqueradeRemoved = "lx.awg_masquerade is removed: the AmneziaWG masquerade burst is sent on every path"

func (LXRemovedAWGMasquerade) UnmarshalJSON([]byte) error {
	return E.New(errLXAWGMasqueradeRemoved)
}

func (LXRemovedAWGMasquerade) MarshalJSON() ([]byte, error) {
	return nil, E.New(errLXAWGMasqueradeRemoved)
}

func (LXRemovedAWGMasquerade) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return &schema.Node{Deprecated: true}, nil
}

// LXRemovedQUICSniffDecoy is the guard on the removed lx.quic_sniff_decoy
// (SPEC 060): any value is a load error that names its replacement. There is
// no backward compatibility (owner 2026-11-13).
type LXRemovedQUICSniffDecoy struct{}

const errLXQUICSniffDecoyRemoved = "lx.quic_sniff_decoy is removed: the QUIC sniff decoy is configured by lx.sniff_guard.quic"

func (LXRemovedQUICSniffDecoy) UnmarshalJSON([]byte) error {
	return E.New(errLXQUICSniffDecoyRemoved)
}

func (LXRemovedQUICSniffDecoy) MarshalJSON() ([]byte, error) {
	return nil, E.New(errLXQUICSniffDecoyRemoved)
}

func (LXRemovedQUICSniffDecoy) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return &schema.Node{Deprecated: true}, nil
}

// LXBuildOverflow is the resolved lx.wg.idle.build_overflow.
type LXBuildOverflow uint8

const (
	LXBuildOverflowWait LXBuildOverflow = iota
	LXBuildOverflowBuild
)

// LXResolved carries the resolved `lx` values: aliases folded in, defaults
// applied, validated. It is registered in the box context (service.ContextWithPtr)
// and read by the router, WG endpoints, and masque and trusttunnel outbounds.
// A nil *LXResolved means every knob is at its default.
type LXResolved struct {
	WG     LXWGResolved
	MASQUE LXMASQUEResolved
	// TrustTunnel carries the raw global window: 0 means "unset", the outbound
	// applies its own default.
	TrustTunnel LXTrustTunnelResolved
	// MTUAlign always carries a mode: "clamp" when the key is absent.
	MTUAlign   LXMTUAlignResolved
	SniffGuard LXSniffGuardResolved // lx: SPEC 060
}

// LXWGResolved keeps the field names of the flat layout for the idle values,
// so the router and endpoints did not change with the move to lx.wg.idle.
type LXWGResolved struct {
	IdleSuspend          time.Duration
	IdleSuspendReachable time.Duration
	// IdleTeardown is the effective level-3 window: the explicit value, or
	// IdleSuspendReachable when the key is absent. An explicit "0" stays 0.
	IdleTeardown time.Duration
	// IdleTeardownSet records that lx.wg.idle.teardown was present, which an
	// explicit "0" makes indistinguishable from absent in IdleTeardown alone.
	IdleTeardownSet bool
	LazyBuild       bool
	BuildMax        int
	BuildOverflow   LXBuildOverflow
	// ReadDatagramCount is 128 when lx.wg.buffers.read_datagram_count is absent.
	ReadDatagramCount int
	// SendFullSize is lx.wg.buffers.send_by_mtu set to false. It is the
	// inverse of the key so that the zero value is the default behaviour.
	SendFullSize    bool
	ConnectedSocket bool
}

// lxWGDefaults is the WG part of an absent block: the idle keys off, the
// buffer keys at the upstream behaviour.
func lxWGDefaults() LXWGResolved {
	return LXWGResolved{ReadDatagramCount: lxWGReadDatagramCountMax}
}

type LXMASQUEResolved struct {
	IdleTimeout time.Duration
}

type LXTrustTunnelResolved struct {
	IdleTimeout time.Duration
}

// LXMTUAlignResolved is the resolved lx.mtu_align (lx: SPEC 120).
type LXMTUAlignResolved struct {
	Mode   string
	Except []string
}

// WGOrZero returns the WG values, or the defaults when r is nil: the idle
// keys off, the buffer keys at their non-zero defaults.
func (r *LXResolved) WGOrZero() LXWGResolved {
	if r == nil {
		return lxWGDefaults()
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

// TrustTunnelOrZero returns the trusttunnel values, or all-unset when r is nil.
func (r *LXResolved) TrustTunnelOrZero() LXTrustTunnelResolved {
	if r == nil {
		return LXTrustTunnelResolved{}
	}
	return r.TrustTunnel
}

// MTUAlignOrDefault returns the mtu_align policy, or the default (clamp,
// no exceptions) when r is nil.
func (r *LXResolved) MTUAlignOrDefault() LXMTUAlignResolved {
	if r == nil || r.MTUAlign.Mode == "" {
		return LXMTUAlignResolved{Mode: LXMTUAlignClamp}
	}
	return r.MTUAlign
}

// ResolveLX validates the root `lx` block and returns the resolved values.
//
// On success options is left in canonical form: empty sub-blocks are dropped
// and options.LX is nil when nothing is set. options.LX is replaced by a fresh
// copy, never written through, so a shallow copy of the caller's Options can
// be resolved without touching the original. On error options is not
// modified. Resolving a canonical Options again is a no-op.
func ResolveLX(options *Options) (*LXResolved, error) {
	var lx LXOptions
	if options.LX != nil {
		lx = *options.LX
	}
	var wg LXWGOptions
	if lx.WG != nil {
		wg = *lx.WG
	}

	resolved, err := resolveLXValues(wg, lx.MASQUE, lx.TrustTunnel, lx.MTUAlign)
	if err != nil {
		return nil, err
	}
	lx.AWGMasquerade = nil
	lx.QUICSniffDecoy = nil
	wg = canonicalLXWG(wg)
	// lx:begin SPEC 060
	if lx.SniffGuard != nil {
		if err := lx.SniffGuard.validate(); err != nil {
			return nil, err
		}
	}
	resolved.SniffGuard = resolveLXSniffGuard(lx.SniffGuard)
	lx.SniffGuard = lx.SniffGuard.canonical()
	// lx:end SPEC 060

	if wg != (LXWGOptions{}) {
		lx.WG = &wg
	} else {
		lx.WG = nil
	}
	if lx.MASQUE != nil && *lx.MASQUE == (LXMASQUEOptions{}) {
		lx.MASQUE = nil
	}
	if lx.TrustTunnel != nil && *lx.TrustTunnel == (LXTrustTunnelOptions{}) {
		lx.TrustTunnel = nil
	}
	if lx.MTUAlign != nil && lx.MTUAlign.Mode == "" && len(lx.MTUAlign.Except) == 0 {
		lx.MTUAlign = nil
	}
	if lx.WG == nil && lx.MASQUE == nil && lx.TrustTunnel == nil && lx.MTUAlign == nil &&
		lx.SniffGuard == nil { // lx: SPEC 060
		options.LX = nil
	} else {
		options.LX = &lx
	}
	return resolved, nil
}

func resolveLXValues(wg LXWGOptions, masque *LXMASQUEOptions, trustTunnel *LXTrustTunnelOptions, mtuAlign *LXMTUAlign) (*LXResolved, error) {
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
	resolved.WG = lxWGDefaults()
	var idle LXWGIdleOptions
	if wg.Idle != nil {
		idle = *wg.Idle
	}
	if idle.Suspend < 0 {
		return nil, E.New("lx.wg.idle.suspend must be >= 0")
	}
	if idle.SuspendReachable < 0 {
		return nil, E.New("lx.wg.idle.suspend_reachable must be >= 0")
	}
	if idle.Teardown != nil && *idle.Teardown < 0 {
		return nil, E.New("lx.wg.idle.teardown must be >= 0")
	}
	if idle.Suspend == 0 {
		if idle.SuspendReachable > 0 {
			return nil, E.New("lx.wg.idle.suspend_reachable requires lx.wg.idle.suspend")
		}
		if idle.Teardown != nil {
			return nil, E.New("lx.wg.idle.teardown requires lx.wg.idle.suspend")
		}
		if idle.LazyBuild {
			return nil, E.New("lx.wg.idle.lazy_build requires lx.wg.idle.suspend")
		}
	}
	if idle.SuspendReachable > 0 && idle.SuspendReachable < idle.Suspend {
		return nil, E.New("lx.wg.idle.suspend_reachable must be >= lx.wg.idle.suspend")
	}
	if idle.BuildMax < 0 {
		return nil, E.New("lx.wg.idle.build_max must be >= 0")
	}
	switch idle.BuildOverflow {
	case "", "wait":
		resolved.WG.BuildOverflow = LXBuildOverflowWait
	case "build":
		resolved.WG.BuildOverflow = LXBuildOverflowBuild
	default:
		return nil, E.New(`lx.wg.idle.build_overflow must be "wait" or "build"`)
	}
	resolved.WG.IdleSuspend = time.Duration(idle.Suspend)
	resolved.WG.IdleSuspendReachable = time.Duration(idle.SuspendReachable)
	if idle.Teardown != nil {
		resolved.WG.IdleTeardown = time.Duration(*idle.Teardown)
		resolved.WG.IdleTeardownSet = true
	} else {
		resolved.WG.IdleTeardown = time.Duration(idle.SuspendReachable)
	}
	resolved.WG.LazyBuild = idle.LazyBuild
	resolved.WG.BuildMax = idle.BuildMax
	if wg.Buffers != nil {
		if count := wg.Buffers.ReadDatagramCount; count != nil {
			if *count < 1 || *count > lxWGReadDatagramCountMax {
				return nil, E.New("lx.wg.buffers.read_datagram_count must be between 1 and 128")
			}
			resolved.WG.ReadDatagramCount = *count
		}
		if wg.Buffers.SendByMTU != nil {
			resolved.WG.SendFullSize = !*wg.Buffers.SendByMTU
		}
	}
	resolved.WG.ConnectedSocket = wg.ConnectedSocket
	if masque != nil {
		if masque.IdleTimeout < 0 {
			return nil, E.New("lx.masque.idle_timeout must be >= 0")
		}
		resolved.MASQUE.IdleTimeout = time.Duration(masque.IdleTimeout)
	}
	if trustTunnel != nil {
		if trustTunnel.IdleTimeout < 0 {
			return nil, E.New("lx.trusttunnel.idle_timeout must be >= 0")
		}
		resolved.TrustTunnel.IdleTimeout = time.Duration(trustTunnel.IdleTimeout)
	}
	return &resolved, nil
}

// canonicalLXWG returns wg with fresh sub-block pointers, defaults written out
// dropped and empty sub-blocks removed. It never writes through the caller's
// pointers. The moved-key guards are dropped like the removed root keys.
func canonicalLXWG(wg LXWGOptions) LXWGOptions {
	canonical := LXWGOptions{ConnectedSocket: wg.ConnectedSocket}
	if wg.Idle != nil && *wg.Idle != (LXWGIdleOptions{}) {
		idle := *wg.Idle
		canonical.Idle = &idle
	}
	if wg.Buffers != nil {
		buffers := *wg.Buffers
		if buffers.ReadDatagramCount != nil && *buffers.ReadDatagramCount == lxWGReadDatagramCountMax {
			buffers.ReadDatagramCount = nil
		}
		if buffers.SendByMTU != nil && *buffers.SendByMTU {
			buffers.SendByMTU = nil
		}
		if buffers != (LXWGBuffersOptions{}) {
			canonical.Buffers = &buffers
		}
	}
	return canonical
}
