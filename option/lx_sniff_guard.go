package option

import (
	"bytes"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/schema"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
)

// lx: SPEC 060 — the root lx.sniff_guard block: makes the first handshake
// packet unreadable to an SNI sniffer on the path, learning from dial outcomes
// on which paths that is needed.

const (
	LXSniffGuardNever   = "never"
	LXSniffGuardClean   = "clean"
	LXSniffGuardSuspect = "suspect"

	LXSniffGuardTLSRecordFragment = "record_fragment"
	LXSniffGuardTLSFragment       = "fragment"
	LXSniffGuardQUICDecoy         = "decoy_first_byte"
	LXSniffGuardDelay             = "delay" // shared by tls and quic
	LXSniffGuardOff               = "off"

	LXSniffGuardDirect   = "direct" // map key of the direct path
	LXSniffGuardWildcard = "*"
)

// LXSniffGuardDefaultDelay is the pause of the delay method when the section
// has no delay key: above the 200ms budget of the Xray sniffer.
const LXSniffGuardDefaultDelay = 300 * time.Millisecond

var (
	lxSniffGuardTLSMethods  = []string{LXSniffGuardTLSRecordFragment, LXSniffGuardTLSFragment, LXSniffGuardDelay, LXSniffGuardOff}
	lxSniffGuardQUICMethods = []string{LXSniffGuardQUICDecoy, LXSniffGuardDelay, LXSniffGuardOff}
	lxSniffGuardStates      = []string{LXSniffGuardNever, LXSniffGuardClean, LXSniffGuardSuspect}
	// lxSniffGuardGroupTypes can be neither a protected node type nor a
	// carrier type: a group is always resolved to the member it picked.
	lxSniffGuardGroupTypes = []string{"selector", "urltest"}
)

// lxSniffGuardDefaultDetourTypes is the start-state map an absent detour_type
// stands for; the block map and the section map are laid over it.
func lxSniffGuardDefaultDetourTypes() map[string]string {
	return map[string]string{
		LXSniffGuardDirect:   LXSniffGuardClean,
		"ssh":                LXSniffGuardClean,
		"socks":              LXSniffGuardClean,
		LXSniffGuardWildcard: LXSniffGuardSuspect,
	}
}

type _LXSniffGuardOptions struct {
	Nodes      *LXSniffGuardNodesOptions `json:"nodes,omitempty"`
	DetourType LXSniffGuardDetourTypes   `json:"detour_type,omitempty"`
	Override   bool                      `json:"override,omitempty"`
	TLS        *LXSniffGuardTLSOptions   `json:"tls,omitempty"`
	QUIC       *LXSniffGuardQUICOptions  `json:"quic,omitempty"`
}

// LXSniffGuardOptions is lx.sniff_guard (lx: SPEC 060). Without a method in
// either section the block is equivalent to an absent one.
type LXSniffGuardOptions _LXSniffGuardOptions

// LXSniffGuardNodesOptions selects the protected nodes; a node is protected
// when it passes both filters. A nil block is the default: sni_mismatch on,
// no outbound_type filter.
type LXSniffGuardNodesOptions struct {
	// SNIMismatch limits protection to nodes whose SNI differs from server.
	// nil = true: a node with its own name passes a sniffer unprotected
	// anyway; false covers every node.
	SNIMismatch *bool `json:"sni_mismatch,omitempty"`
	// OutboundType limits protection to the listed types; nil = no filter.
	OutboundType LXSniffGuardOutboundTypes `json:"outbound_type,omitempty"`
}

// LXSniffGuardTLSOptions is lx.sniff_guard.tls.
type LXSniffGuardTLSOptions struct {
	Method     string                  `json:"method,omitempty" enum:"record_fragment,fragment,delay,off"`
	Delay      *badoption.Duration     `json:"delay,omitempty"`
	Persist    bool                    `json:"persist,omitempty"`
	DetourType LXSniffGuardDetourTypes `json:"detour_type,omitempty"`
}

// LXSniffGuardQUICOptions is lx.sniff_guard.quic.
type LXSniffGuardQUICOptions struct {
	Method     string                  `json:"method,omitempty" enum:"decoy_first_byte,delay,off"`
	Delay      *badoption.Duration     `json:"delay,omitempty"`
	Persist    bool                    `json:"persist,omitempty"`
	DetourType LXSniffGuardDetourTypes `json:"detour_type,omitempty"`
}

// LXSniffGuardDetourTypes maps a carrier type (or "*") to its start state:
// never, clean or suspect. Unknown types are rejected by the box, which knows
// the registries of the build.
type LXSniffGuardDetourTypes map[string]string

// LXSniffGuardOutboundTypes is nodes.outbound_type: a non-empty list of types,
// or false, which like an absent key means no filter.
type LXSniffGuardOutboundTypes []string

const errLXSniffGuardForm = "lx.sniff_guard must be an object: {nodes, detour_type, override, tls, quic}"

// lxSniffGuardRaw keeps the sub-objects raw so that each is decoded strictly
// and its errors name its own key.
type lxSniffGuardRaw struct {
	Nodes      json.RawMessage         `json:"nodes"`
	DetourType LXSniffGuardDetourTypes `json:"detour_type"`
	Override   bool                    `json:"override"`
	TLS        json.RawMessage         `json:"tls"`
	QUIC       json.RawMessage         `json:"quic"`
}

type lxSniffGuardNodesRaw struct {
	SNIMismatch  *bool           `json:"sni_mismatch"`
	OutboundType json.RawMessage `json:"outbound_type"`
}

func (o *LXSniffGuardOptions) UnmarshalJSON(content []byte) error {
	if !lxSniffGuardIsObject(content) {
		return E.New(errLXSniffGuardForm)
	}
	var raw lxSniffGuardRaw
	if err := json.UnmarshalDisallowUnknownFields(content, &raw); err != nil {
		return E.Cause(err, "lx.sniff_guard")
	}
	options := LXSniffGuardOptions{DetourType: raw.DetourType, Override: raw.Override}
	if !lxSniffGuardIsAbsent(raw.Nodes) {
		if !lxSniffGuardIsObject(raw.Nodes) {
			return E.New("lx.sniff_guard.nodes must be an object: {sni_mismatch, outbound_type}")
		}
		var nodesRaw lxSniffGuardNodesRaw
		if err := json.UnmarshalDisallowUnknownFields(raw.Nodes, &nodesRaw); err != nil {
			return E.Cause(err, "lx.sniff_guard.nodes")
		}
		outboundTypes, err := parseLXSniffGuardOutboundTypes(nodesRaw.OutboundType)
		if err != nil {
			return err
		}
		options.Nodes = &LXSniffGuardNodesOptions{SNIMismatch: nodesRaw.SNIMismatch, OutboundType: outboundTypes}
	}
	if !lxSniffGuardIsAbsent(raw.TLS) {
		if !lxSniffGuardIsObject(raw.TLS) {
			return E.New("lx.sniff_guard.tls must be an object: {method, delay, persist, detour_type}")
		}
		options.TLS = new(LXSniffGuardTLSOptions)
		if err := json.UnmarshalDisallowUnknownFields(raw.TLS, options.TLS); err != nil {
			return E.Cause(err, "lx.sniff_guard.tls")
		}
	}
	if !lxSniffGuardIsAbsent(raw.QUIC) {
		if !lxSniffGuardIsObject(raw.QUIC) {
			return E.New("lx.sniff_guard.quic must be an object: {method, delay, persist, detour_type}")
		}
		options.QUIC = new(LXSniffGuardQUICOptions)
		if err := json.UnmarshalDisallowUnknownFields(raw.QUIC, options.QUIC); err != nil {
			return E.Cause(err, "lx.sniff_guard.quic")
		}
	}
	if err := options.validate(); err != nil {
		return err
	}
	*o = options
	return nil
}

func (o LXSniffGuardOptions) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return builder.Define("LXSniffGuardOptions", func() (*schema.Node, error) {
		node := schema.StrictObject()
		err := builder.FlattenStruct(node, reflect.TypeFor[_LXSniffGuardOptions]())
		if err != nil {
			return nil, err
		}
		return node, nil
	})
}

func (o *LXSniffGuardOutboundTypes) UnmarshalJSON(content []byte) error {
	outboundTypes, err := parseLXSniffGuardOutboundTypes(content)
	if err != nil {
		return err
	}
	*o = outboundTypes
	return nil
}

func (o LXSniffGuardOutboundTypes) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return builder.Define("LXSniffGuardOutboundTypes", func() (*schema.Node, error) {
		list := &schema.Node{Type: "array", Items: schema.StringNode()}
		return schema.AnyOf(&schema.Node{Const: false}, list), nil
	})
}

func (o LXSniffGuardDetourTypes) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return builder.Define("LXSniffGuardDetourTypes", func() (*schema.Node, error) {
		return &schema.Node{Type: "object", AdditionalProperties: schema.StringEnum(lxSniffGuardStates...)}, nil
	})
}

// parseLXSniffGuardOutboundTypes reads outbound_type from the raw JSON: the
// empty list must be caught here, since a later re-marshal of the options
// collapses [] into an absent key, which means "no filter".
func parseLXSniffGuardOutboundTypes(content []byte) (LXSniffGuardOutboundTypes, error) {
	const key = "lx.sniff_guard.nodes.outbound_type"
	trimmed := bytes.TrimSpace(content)
	switch string(trimmed) {
	case "", "null", "false":
		return nil, nil
	case "true":
		return nil, E.New(key, ": true is not a filter; list the types, or use false for no filter")
	}
	if !bytes.HasPrefix(trimmed, []byte("[")) {
		return nil, E.New(key, " must be a list of outbound types or false")
	}
	var outboundTypes []string
	if err := json.Unmarshal(trimmed, &outboundTypes); err != nil {
		return nil, E.Cause(err, key)
	}
	if len(outboundTypes) == 0 {
		return nil, E.New(key, ": empty list; list the types, or use false for no filter")
	}
	result := LXSniffGuardOutboundTypes(outboundTypes)
	if err := result.validate(); err != nil {
		return nil, err
	}
	return result, nil
}

func (o LXSniffGuardOutboundTypes) validate() error {
	const key = "lx.sniff_guard.nodes.outbound_type"
	for _, outboundType := range o {
		if outboundType == "" {
			return E.New(key, ": empty type")
		}
		if slices.Contains(lxSniffGuardGroupTypes, outboundType) {
			return E.New(key, ": ", strconv.Quote(outboundType), " is a group; a group is never dialled itself, name the member types")
		}
	}
	return nil
}

func (o LXSniffGuardDetourTypes) validate(key string) error {
	for carrierType, state := range o {
		if carrierType == "" {
			return E.New(key, ": empty type")
		}
		if slices.Contains(lxSniffGuardGroupTypes, carrierType) {
			return E.New(key, ": ", strconv.Quote(carrierType), " is a group; name the member types, a group is resolved to the member it picks")
		}
		if !slices.Contains(lxSniffGuardStates, state) {
			return E.New(key, ".", carrierType, ` must be "never", "clean" or "suspect"`)
		}
	}
	return nil
}

func validateLXSniffGuardSection(key string, methods []string, method string, delay *badoption.Duration, detourType LXSniffGuardDetourTypes) error {
	if method != "" && !slices.Contains(methods, method) {
		quoted := make([]string, 0, len(methods))
		for _, m := range methods {
			quoted = append(quoted, strconv.Quote(m))
		}
		return E.New(key, ".method must be one of ", strings.Join(quoted, ", "))
	}
	if delay != nil && *delay <= 0 {
		return E.New(key, ".delay must be > 0")
	}
	return detourType.validate(key + ".detour_type")
}

// validate checks everything that does not need the build registries or the
// rest of the config; unknown types and persist without cache_file are
// checked by the box.
func (o LXSniffGuardOptions) validate() error {
	if o.Nodes != nil && len(o.Nodes.OutboundType) > 0 {
		if err := o.Nodes.OutboundType.validate(); err != nil {
			return err
		}
	}
	if err := o.DetourType.validate("lx.sniff_guard.detour_type"); err != nil {
		return err
	}
	if o.TLS != nil {
		if err := validateLXSniffGuardSection("lx.sniff_guard.tls", lxSniffGuardTLSMethods, o.TLS.Method, o.TLS.Delay, o.TLS.DetourType); err != nil {
			return err
		}
	}
	if o.QUIC != nil {
		if err := validateLXSniffGuardSection("lx.sniff_guard.quic", lxSniffGuardQUICMethods, o.QUIC.Method, o.QUIC.Delay, o.QUIC.DetourType); err != nil {
			return err
		}
	}
	return nil
}

func lxSniffGuardIsObject(content []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(content), []byte("{"))
}

func lxSniffGuardIsAbsent(content []byte) bool {
	trimmed := bytes.TrimSpace(content)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

// sniMismatchOnly is the sni_mismatch filter with its default: on unless the
// key is false.
func (o *LXSniffGuardNodesOptions) sniMismatchOnly() bool {
	return o == nil || o.SNIMismatch == nil || *o.SNIMismatch
}

func lxSniffGuardMethodSet(method string) bool {
	return method != "" && method != LXSniffGuardOff
}

func (o *LXSniffGuardOptions) tlsEnabled() bool {
	return o != nil && o.TLS != nil && lxSniffGuardMethodSet(o.TLS.Method)
}

func (o *LXSniffGuardOptions) quicEnabled() bool {
	return o != nil && o.QUIC != nil && lxSniffGuardMethodSet(o.QUIC.Method)
}

// canonical returns the block without what an absent key would mean
// anyway, or nil when no section has a method. o is not written through.
func (o *LXSniffGuardOptions) canonical() *LXSniffGuardOptions {
	if !o.tlsEnabled() && !o.quicEnabled() {
		return nil
	}
	result := *o
	if result.Nodes != nil {
		nodes := *result.Nodes
		if nodes.sniMismatchOnly() {
			nodes.SNIMismatch = nil
		}
		if nodes.SNIMismatch == nil && len(nodes.OutboundType) == 0 {
			result.Nodes = nil
		} else {
			result.Nodes = &nodes
		}
	}
	if len(result.DetourType) == 0 {
		result.DetourType = nil
	}
	if o.tlsEnabled() {
		section := *o.TLS
		if len(section.DetourType) == 0 {
			section.DetourType = nil
		}
		result.TLS = &section
	} else {
		result.TLS = nil
	}
	if o.quicEnabled() {
		section := *o.QUIC
		if len(section.DetourType) == 0 {
			section.DetourType = nil
		}
		result.QUIC = &section
	} else {
		result.QUIC = nil
	}
	return &result
}

// LXSniffGuardResolved is lx.sniff_guard with its defaults applied. The zero
// value is an absent block: the feature is off.
type LXSniffGuardResolved struct {
	SNIMismatchOnly bool     // true unless nodes.sni_mismatch: false
	OutboundTypes   []string // nil = no filter
	Override        bool
	TLS             LXSniffGuardSectionResolved
	QUIC            LXSniffGuardSectionResolved
}

// LXSniffGuardSectionResolved is one section of lx.sniff_guard. The zero value
// is a section without a method.
type LXSniffGuardSectionResolved struct {
	Method  string        // "" = off
	Delay   time.Duration // 300ms when the key is absent
	Persist bool
	// DetourTypes is the effective map: the default (direct, ssh, socks =
	// clean; "*" = suspect), the block map over it, the section map on top.
	DetourTypes map[string]string
}

// Enabled reports whether at least one section has a method.
func (r LXSniffGuardResolved) Enabled() bool {
	return r.TLS.Enabled() || r.QUIC.Enabled()
}

// CoversType reports whether outboundType passes the outbound_type filter.
func (r LXSniffGuardResolved) CoversType(outboundType string) bool {
	return r.OutboundTypes == nil || slices.Contains(r.OutboundTypes, outboundType)
}

// Enabled reports whether the section has a method.
func (s LXSniffGuardSectionResolved) Enabled() bool {
	return s.Method != ""
}

// StartState returns the start state of a path whose carrier is of
// carrierType ("direct" for the direct path): the type's own entry, else the
// "*" entry. A section without a method is never.
func (s LXSniffGuardSectionResolved) StartState(carrierType string) string {
	if !s.Enabled() {
		return LXSniffGuardNever
	}
	if state, loaded := s.DetourTypes[carrierType]; loaded {
		return state
	}
	if state, loaded := s.DetourTypes[LXSniffGuardWildcard]; loaded {
		return state
	}
	return LXSniffGuardSuspect
}

// SniffGuardOrOff returns the resolved lx.sniff_guard, or the zero (off)
// policy when r is nil.
func (r *LXResolved) SniffGuardOrOff() LXSniffGuardResolved {
	if r == nil {
		return LXSniffGuardResolved{}
	}
	return r.SniffGuard
}

// resolveLXSniffGuard applies the defaults. A block without a method in
// either section resolves to the zero value, exactly as an absent one.
func resolveLXSniffGuard(o *LXSniffGuardOptions) LXSniffGuardResolved {
	if !o.tlsEnabled() && !o.quicEnabled() {
		return LXSniffGuardResolved{}
	}
	resolved := LXSniffGuardResolved{Override: o.Override, SNIMismatchOnly: o.Nodes.sniMismatchOnly()}
	if o.Nodes != nil {
		if len(o.Nodes.OutboundType) > 0 {
			resolved.OutboundTypes = append([]string(nil), o.Nodes.OutboundType...)
		}
	}
	if o.tlsEnabled() {
		resolved.TLS = resolveLXSniffGuardSection(o.TLS.Method, o.TLS.Delay, o.TLS.Persist, o.DetourType, o.TLS.DetourType)
	}
	if o.quicEnabled() {
		resolved.QUIC = resolveLXSniffGuardSection(o.QUIC.Method, o.QUIC.Delay, o.QUIC.Persist, o.DetourType, o.QUIC.DetourType)
	}
	return resolved
}

func resolveLXSniffGuardSection(method string, delay *badoption.Duration, persist bool, blockTypes, sectionTypes LXSniffGuardDetourTypes) LXSniffGuardSectionResolved {
	section := LXSniffGuardSectionResolved{
		Method:      method,
		Delay:       LXSniffGuardDefaultDelay,
		Persist:     persist,
		DetourTypes: lxSniffGuardDefaultDetourTypes(),
	}
	if delay != nil {
		section.Delay = time.Duration(*delay)
	}
	for carrierType, state := range blockTypes {
		section.DetourTypes[carrierType] = state
	}
	for carrierType, state := range sectionTypes {
		section.DetourTypes[carrierType] = state
	}
	return section
}
