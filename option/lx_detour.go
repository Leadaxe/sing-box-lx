package option

import (
	"bytes"
	"slices"

	"github.com/sagernet/sing-box/schema"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

// lx: SPEC 165 — lx.detour, what the core does to a node that dials through
// `detour`: the path-MTU alignment (SPEC 120/144, moved here from
// lx.mtu_align) and the strip filters, which keep a node from using a
// mechanism that needs a kernel socket when its conn is not one. One
// except_outbounds list serves both.
//
// The block is true (also when absent), false or an object (ALGORITHM
// Р1–Р8); strip and strip.filters are true, false or an object as well. An
// object of filters replaces the catalog whole: a filter it does not name is
// off.

// The strip catalog (Р7), every filter on by default.
const (
	// LXDetourStripTLSFragment: the ClientHello split, which inside a tunnel
	// sleeps fragment_fallback_delay between its pieces.
	LXDetourStripTLSFragment = "tls.fragment"
	// LXDetourStripTLSSpoof: the forged ClientHello sent as a raw TCP packet.
	LXDetourStripTLSSpoof = "tls.spoof"
	// LXDetourStripTLSKernel: kTLS (kernel_tx, kernel_rx).
	LXDetourStripTLSKernel = "tls.kernel"
	// LXDetourStripTLSEngine: the apple engine, replaced by go.
	LXDetourStripTLSEngine = "tls.engine"
)

// LXDetour is lx.detour as written. The written form is kept for the running
// config (Н1); Resolve gives the values, Expanded the form of the effective
// config (Р9).
type LXDetour struct {
	// Bool is set when the block is written as true or false.
	Bool *bool
	// MTUAlign is the mode, "" when the key is absent.
	MTUAlign        string
	Strip           *LXDetourStrip
	ExceptOutbounds []string
	// expanded writes every key, an empty except_outbounds included (Р9).
	expanded bool
}

// LXDetourStrip is lx.detour.strip as written.
type LXDetourStrip struct {
	Bool    *bool
	Filters *LXDetourFilters
}

// LXDetourFilters is lx.detour.strip.filters as written.
type LXDetourFilters struct {
	Bool *bool
	// TLS is the tls section of the object form; nil when absent.
	TLS *LXDetourTLSFilters
}

// LXDetourTLSFilters is lx.detour.strip.filters.tls.
type LXDetourTLSFilters struct {
	Fragment *bool
	Spoof    *bool
	Kernel   *bool
	Engine   *bool
}

func (f *LXDetourTLSFilters) field(key string) **bool {
	switch key {
	case "fragment":
		return &f.Fragment
	case "spoof":
		return &f.Spoof
	case "kernel":
		return &f.Kernel
	case "engine":
		return &f.Engine
	}
	return nil
}

var lxDetourTLSFilterKeys = []string{"fragment", "spoof", "kernel", "engine"}

// LXDetourStripResolved is the set of filters in effect.
type LXDetourStripResolved struct {
	TLSFragment bool
	TLSSpoof    bool
	TLSKernel   bool
	TLSEngine   bool
}

func lxDetourStripCatalog() LXDetourStripResolved {
	return LXDetourStripResolved{TLSFragment: true, TLSSpoof: true, TLSKernel: true, TLSEngine: true}
}

// Any reports whether a filter is on.
func (s LXDetourStripResolved) Any() bool {
	return s != LXDetourStripResolved{}
}

// LXDetourResolved is the resolved lx.detour.
type LXDetourResolved struct {
	// MTUAlign is always set: clamp when the key is absent.
	MTUAlign string
	// Strip is the set of strip filters in effect.
	Strip           LXDetourStripResolved
	ExceptOutbounds []string
}

// Excepted reports whether tag is in except_outbounds.
func (r LXDetourResolved) Excepted(tag string) bool {
	return slices.Contains(r.ExceptOutbounds, tag)
}

// lxDetourRemovedMTUAlign is the load error of the old root key (Р8).
const lxDetourRemovedMTUAlign = "lx.mtu_align is moved to lx.detour.mtu_align (lx.mtu_align.except is lx.detour.except_outbounds)"

// LXMovedMTUAlign is the guard on the moved lx.mtu_align: any value fails the
// load and names the new place.
type LXMovedMTUAlign struct{}

func (LXMovedMTUAlign) UnmarshalJSON([]byte) error {
	return E.New(lxDetourRemovedMTUAlign)
}

func (LXMovedMTUAlign) MarshalJSON() ([]byte, error) {
	return nil, E.New(lxDetourRemovedMTUAlign)
}

func (LXMovedMTUAlign) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return &schema.Node{Deprecated: true}, nil
}

func (o *LXDetour) UnmarshalJSON(content []byte) error {
	*o = LXDetour{}
	if value, isBool := parseLXDetourBool(content); isBool {
		o.Bool = &value
		return nil
	}
	keys, err := lxDetourObject(content, "lx.detour")
	if err != nil {
		return err
	}
	for key, raw := range keys {
		switch key {
		case "mtu_align":
			var mode string
			if err := json.Unmarshal(raw, &mode); err != nil {
				return E.New(`lx.detour.mtu_align must be "off", "fill" or "clamp"`)
			}
			if err := validateLXDetourMTUAlign(mode); err != nil {
				return err
			}
			o.MTUAlign = mode
		case "strip":
			strip, err := parseLXDetourStrip(raw)
			if err != nil {
				return err
			}
			o.Strip = strip
		case "except_outbounds":
			var tags []string
			if err := json.Unmarshal(raw, &tags); err != nil {
				var tag string
				if json.Unmarshal(raw, &tag) != nil {
					return E.New("lx.detour.except_outbounds must be a list of tags")
				}
				tags = []string{tag}
			}
			for _, tag := range tags {
				if tag == "" {
					return E.New("lx.detour.except_outbounds: empty tag")
				}
			}
			o.ExceptOutbounds = tags
		default:
			return E.New("lx.detour: unknown key ", key)
		}
	}
	return nil
}

func parseLXDetourStrip(content []byte) (*LXDetourStrip, error) {
	if value, isBool := parseLXDetourBool(content); isBool {
		return &LXDetourStrip{Bool: &value}, nil
	}
	keys, err := lxDetourObject(content, "lx.detour.strip")
	if err != nil {
		return nil, err
	}
	strip := &LXDetourStrip{}
	for key, raw := range keys {
		if key != "filters" {
			return nil, E.New("lx.detour.strip: unknown key ", key)
		}
		filters, err := parseLXDetourFilters(raw)
		if err != nil {
			return nil, err
		}
		strip.Filters = filters
	}
	return strip, nil
}

func parseLXDetourFilters(content []byte) (*LXDetourFilters, error) {
	if value, isBool := parseLXDetourBool(content); isBool {
		return &LXDetourFilters{Bool: &value}, nil
	}
	keys, err := lxDetourObject(content, "lx.detour.strip.filters")
	if err != nil {
		return nil, err
	}
	filters := &LXDetourFilters{}
	for key, raw := range keys {
		if key != "tls" {
			return nil, E.New("lx.detour.strip.filters: unknown key ", key)
		}
		tlsKeys, err := lxDetourObject(raw, "lx.detour.strip.filters.tls")
		if err != nil {
			return nil, err
		}
		filters.TLS = &LXDetourTLSFilters{}
		for tlsKey, tlsRaw := range tlsKeys {
			target := filters.TLS.field(tlsKey)
			if target == nil {
				return nil, E.New("lx.detour.strip.filters.tls: unknown key ", tlsKey)
			}
			value, isBool := parseLXDetourBool(tlsRaw)
			if !isBool {
				return nil, E.New("lx.detour.strip.filters.tls.", tlsKey, " must be true or false")
			}
			*target = &value
		}
	}
	return filters, nil
}

func parseLXDetourBool(content []byte) (value bool, isBool bool) {
	switch string(bytes.TrimSpace(content)) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

func lxDetourObject(content []byte, path string) (map[string]json.RawMessage, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(content, &keys); err != nil || keys == nil {
		return nil, E.New(path, " must be true, false or an object")
	}
	return keys, nil
}

func validateLXDetourMTUAlign(mode string) error {
	switch mode {
	case LXMTUAlignOff, LXMTUAlignFill, LXMTUAlignClamp:
		return nil
	}
	return E.New(`lx.detour.mtu_align must be "off", "fill" or "clamp"`)
}

// MarshalJSON writes the block as it was written (Н1).
func (o LXDetour) MarshalJSON() ([]byte, error) {
	if o.Bool != nil {
		return json.Marshal(*o.Bool)
	}
	object := make(map[string]any)
	if o.MTUAlign != "" {
		object["mtu_align"] = o.MTUAlign
	}
	if o.Strip != nil {
		object["strip"] = o.Strip.written()
	}
	if len(o.ExceptOutbounds) > 0 || o.expanded {
		object["except_outbounds"] = o.ExceptOutbounds
	}
	return json.Marshal(object)
}

func (s *LXDetourStrip) written() any {
	if s.Bool != nil {
		return *s.Bool
	}
	object := make(map[string]any)
	if s.Filters != nil {
		object["filters"] = s.Filters.written()
	}
	return object
}

func (f *LXDetourFilters) written() any {
	if f.Bool != nil {
		return *f.Bool
	}
	object := make(map[string]any)
	if f.TLS != nil {
		tls := make(map[string]any)
		for _, key := range lxDetourTLSFilterKeys {
			if value := *f.TLS.field(key); value != nil {
				tls[key] = *value
			}
		}
		object["tls"] = tls
	}
	return object
}

func (o LXDetour) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return builder.Define("LXDetour", func() (*schema.Node, error) {
		return &schema.Node{AnyOf: []*schema.Node{{Type: "boolean"}, {Type: "object", AdditionalProperties: schema.LooseObject()}}}, nil
	})
}

// Resolve applies Р1–Р7. A nil block is an absent one: true.
func (o *LXDetour) Resolve() LXDetourResolved {
	resolved := LXDetourResolved{MTUAlign: LXMTUAlignClamp, Strip: lxDetourStripCatalog()}
	if o == nil {
		return resolved
	}
	if o.Bool != nil {
		if !*o.Bool {
			resolved.MTUAlign = LXMTUAlignOff
			resolved.Strip = LXDetourStripResolved{}
		}
		return resolved
	}
	if o.MTUAlign != "" {
		resolved.MTUAlign = o.MTUAlign
	}
	resolved.Strip = o.Strip.resolve()
	if len(o.ExceptOutbounds) > 0 {
		resolved.ExceptOutbounds = slices.Clone(o.ExceptOutbounds)
	}
	return resolved
}

// resolve is Р4–Р6 for the strip value: absent, true, an object without
// filters and filters true are the catalog.
func (s *LXDetourStrip) resolve() LXDetourStripResolved {
	if s == nil {
		return lxDetourStripCatalog()
	}
	if s.Bool != nil {
		if *s.Bool {
			return lxDetourStripCatalog()
		}
		return LXDetourStripResolved{}
	}
	filters := s.Filters
	if filters == nil {
		return lxDetourStripCatalog()
	}
	if filters.Bool != nil {
		if *filters.Bool {
			return lxDetourStripCatalog()
		}
		return LXDetourStripResolved{}
	}
	if filters.TLS == nil {
		return LXDetourStripResolved{}
	}
	on := func(value *bool) bool { return value != nil && *value }
	return LXDetourStripResolved{
		TLSFragment: on(filters.TLS.Fragment),
		TLSSpoof:    on(filters.TLS.Spoof),
		TLSKernel:   on(filters.TLS.Kernel),
		TLSEngine:   on(filters.TLS.Engine),
	}
}

// ExpandedLXDetour is the effective-config form of a resolved block (Р9):
// every key and every filter written out.
func ExpandedLXDetour(resolved LXDetourResolved) *LXDetour {
	strip := resolved.Strip
	except := resolved.ExceptOutbounds
	if except == nil {
		except = []string{}
	}
	tls := &LXDetourTLSFilters{Fragment: &strip.TLSFragment, Spoof: &strip.TLSSpoof, Kernel: &strip.TLSKernel, Engine: &strip.TLSEngine}
	return &LXDetour{
		MTUAlign:        resolved.MTUAlign,
		Strip:           &LXDetourStrip{Filters: &LXDetourFilters{TLS: tls}},
		ExceptOutbounds: except,
		expanded:        true,
	}
}
