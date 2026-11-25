package chain

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

// lx: SPEC 165 — a chain is a rewrite of `detour` (ALGORITHM §4.1–4.2).
//
// Before any node is created, every node a chain reaches on a position i >= 1
// (members of groups of any depth included) gets `detour` = the hop of
// position i-1, and the hops "<chain>#<i>" are appended to the outbounds. A
// node keeps one instance and one path; the group on a position checks the
// very instances that carry its traffic.

// HopTag is the tag of hop i of a chain. PUBLIC CONTRACT (SPEC 073/165):
// clients address the positions by these tags.
func HopTag(chain string, index int) string {
	return chain + "#" + strconv.Itoa(index)
}

// rewriteEntry is one node of the config, outbound or endpoint (one tag
// namespace).
type rewriteEntry struct {
	tag        string
	typeName   string
	options    any
	isEndpoint bool
	index      int
}

// interception is the node's place under П2.
type interception struct {
	chain    string
	position int
}

type rewriter struct {
	ctx     context.Context
	options *option.Options
	entries map[string]*rewriteEntry
	order   []*rewriteEntry
	chains  []*rewriteEntry
}

// Rewrite runs П1–П7 and К1–К6 on options. The outbound and endpoint slices
// are replaced by new ones and every rewritten node gets a fresh options
// struct, so the caller's structs are never written through. It returns the
// changes per node tag in the order they were made.
func Rewrite(ctx context.Context, options *option.Options) (map[string][]option.LXConfigChange, error) {
	r := &rewriter{ctx: ctx, options: options, entries: make(map[string]*rewriteEntry)}
	for i, outbound := range options.Outbounds {
		if outbound.Type == C.TypeChainHop {
			return nil, E.New("outbound ", outbound.Tag, ": type ", C.TypeChainHop, " is internal and cannot be used in a config")
		}
		r.add(&rewriteEntry{tag: outbound.Tag, typeName: outbound.Type, options: outbound.Options, index: i})
	}
	for i, endpoint := range options.Endpoints {
		r.add(&rewriteEntry{tag: endpoint.Tag, typeName: endpoint.Type, options: endpoint.Options, isEndpoint: true, index: i})
	}
	if len(r.chains) == 0 {
		return nil, nil
	}
	return r.run()
}

func (r *rewriter) add(entry *rewriteEntry) {
	if _, duplicate := r.entries[entry.tag]; duplicate || entry.tag == "" {
		// Duplicate and empty tags are reported by the managers at start.
		return
	}
	r.entries[entry.tag] = entry
	r.order = append(r.order, entry)
	if entry.typeName == C.TypeChain {
		r.chains = append(r.chains, entry)
	}
}

func (r *rewriter) chainOptions(entry *rewriteEntry) *option.ChainOutboundOptions {
	chainOptions, _ := entry.options.(*option.ChainOutboundOptions)
	if chainOptions == nil {
		return &option.ChainOutboundOptions{}
	}
	return chainOptions
}

func (r *rewriter) run() (map[string][]option.LXConfigChange, error) {
	intercepted := make(map[string]interception)
	entryOf := make(map[string][]string) // node tag -> chains reaching it on position 0
	var interceptOrder []string
	for _, chainEntry := range r.chains {
		chainTag := chainEntry.tag
		positions := r.chainOptions(chainEntry).Outbounds
		if err := r.validatePositions(chainTag, positions); err != nil {
			return nil, err
		}
		for i := range positions {
			hopTag := HopTag(chainTag, i)
			if _, taken := r.entries[hopTag]; taken {
				return nil, E.New("chain ", chainTag, ": tag ", hopTag, " is reserved for the chain hop but already used")
			}
		}
		for i, position := range positions {
			leaves, err := r.leavesOf(chainTag, i, position)
			if err != nil {
				return nil, err
			}
			for _, leaf := range leaves {
				if i == 0 {
					entryOf[leaf.tag] = append(entryOf[leaf.tag], chainTag)
					continue
				}
				if previous, loaded := intercepted[leaf.tag]; loaded {
					// К1
					return nil, E.New("node ", leaf.tag, " is on position ", previous.position, " of chain ", previous.chain,
						" and on position ", i, " of chain ", chainTag, ": a node can be taken over by one chain position only")
				}
				if err := r.checkDialable(chainTag, i, leaf); err != nil {
					return nil, err
				}
				intercepted[leaf.tag] = interception{chain: chainTag, position: i}
				interceptOrder = append(interceptOrder, leaf.tag)
			}
		}
	}
	// К2
	for _, tag := range interceptOrder {
		if chains := entryOf[tag]; len(chains) > 0 {
			place := intercepted[tag]
			return nil, E.New("node ", tag, " is on position ", place.position, " of chain ", place.chain,
				" and on the entry (position 0) of chain ", chains[0], ": a node taken over by a chain cannot be an entry")
		}
	}

	outbounds := append([]option.Outbound(nil), r.options.Outbounds...)
	endpoints := append([]option.Endpoint(nil), r.options.Endpoints...)
	changes := make(map[string][]option.LXConfigChange)
	chainsByTag := make(map[string]*rewriteEntry, len(r.chains))
	for _, chainEntry := range r.chains {
		chainsByTag[chainEntry.tag] = chainEntry
	}
	for _, tag := range interceptOrder {
		entry := r.entries[tag]
		place := intercepted[tag]
		rewritten, nodeChanges, err := r.rewriteNode(entry, place, r.chainOptions(chainsByTag[place.chain]).Rewrite)
		if err != nil {
			return nil, err
		}
		if entry.isEndpoint {
			endpoints[entry.index].Options = rewritten
		} else {
			outbounds[entry.index].Options = rewritten
		}
		entry.options = rewritten
		changes[tag] = nodeChanges
	}
	// П1, П6: the hops, in chain order.
	for _, chainEntry := range r.chains {
		positions := r.chainOptions(chainEntry).Outbounds
		for i, position := range positions {
			hop := &option.ChainHopOutboundOptions{Chain: chainEntry.tag, Position: i, Outbound: position}
			if i > 0 {
				hop.Below = HopTag(chainEntry.tag, i-1)
			}
			hopTag := HopTag(chainEntry.tag, i)
			outbounds = append(outbounds, option.Outbound{Type: C.TypeChainHop, Tag: hopTag, Options: hop})
			r.entries[hopTag] = &rewriteEntry{tag: hopTag, typeName: C.TypeChainHop, options: hop}
		}
	}
	if err := r.checkCycles(); err != nil {
		return nil, err
	}
	r.options.Outbounds = outbounds
	r.options.Endpoints = endpoints
	return changes, nil
}

func (r *rewriter) validatePositions(chainTag string, positions []string) error {
	if len(positions) < 2 {
		return E.New("chain ", chainTag, ": at least 2 outbounds required")
	}
	seen := make(map[string]bool, len(positions))
	for i, position := range positions {
		switch {
		case position == "":
			return E.New("chain ", chainTag, ": empty outbound tag at position ", i)
		case position == chainTag:
			return E.New("chain ", chainTag, ": position ", i, " is the chain itself")
		case seen[position]:
			return E.New("chain ", chainTag, ": duplicate outbound ", position, " (position ", i, ")")
		}
		seen[position] = true
		if r.entries[position] == nil {
			return E.New("chain ", chainTag, ": position ", i, ": outbound not found: ", position)
		}
	}
	return nil
}

// groupMembers returns the members of a group node, ok false for a non-group.
func groupMembers(options any) ([]string, bool) {
	switch group := options.(type) {
	case *option.SelectorOutboundOptions:
		return group.Outbounds, true
	case *option.URLTestOutboundOptions:
		return group.Outbounds, true
	}
	return nil, false
}

// leavesOf is the reachable nodes of a position (ALGORITHM §3): the position
// itself when it is a node, every leaf of a group of any depth otherwise;
// direct and block are left out. A chain is a leaf allowed only on position
// 0 (К4).
func (r *rewriter) leavesOf(chainTag string, position int, tag string) ([]*rewriteEntry, error) {
	var leaves []*rewriteEntry
	seen := make(map[string]bool)
	var walk func(tag string, via string) error
	walk = func(tag string, via string) error {
		if seen[tag] {
			return nil
		}
		seen[tag] = true
		entry := r.entries[tag]
		if entry == nil {
			return E.New("chain ", chainTag, ": position ", position, ": group ", via, ": member not found: ", tag)
		}
		if members, isGroup := groupMembers(entry.options); isGroup {
			for _, member := range members {
				if err := walk(member, tag); err != nil {
					return err
				}
			}
			return nil
		}
		switch entry.typeName {
		case C.TypeDirect, C.TypeBlock:
			return nil
		case C.TypeChain:
			if tag == chainTag {
				return E.New("chain ", chainTag, ": position ", position, " contains the chain itself")
			}
			if position > 0 {
				return E.New("chain ", chainTag, ": position ", position, " (", tag, "): a nested chain is only allowed on position 0")
			}
		}
		leaves = append(leaves, entry)
		return nil
	}
	if err := walk(tag, tag); err != nil {
		return nil, err
	}
	return leaves, nil
}

// checkDialable is К5: the node must be able to dial through detour.
func (r *rewriter) checkDialable(chainTag string, position int, leaf *rewriteEntry) error {
	if _, hasDialer := leaf.options.(option.DialerOptionsWrapper); !hasDialer {
		return E.New("chain ", chainTag, ": position ", position, ": node ", leaf.tag, " (", leaf.typeName, ") cannot dial through detour")
	}
	if wireGuard, isWireGuard := leaf.options.(*option.WireGuardEndpointOptions); isWireGuard && wireGuard.ListenPort != 0 {
		return E.New("chain ", chainTag, ": position ", position, ": node ", leaf.tag, " has listen_port, which does not work with detour")
	}
	return nil
}

// rewriteNode applies П2 and П5 to a copy of the node's options.
func (r *rewriter) rewriteNode(entry *rewriteEntry, place interception, rewrite option.ChainRewrite) (any, []option.LXConfigChange, error) {
	m, err := optionsToMap(r.ctx, entry.options)
	if err != nil {
		return nil, nil, E.Cause(err, "chain ", place.chain, ": serialize options of ", entry.tag)
	}
	hopTag := HopTag(place.chain, place.position-1)
	change := option.LXConfigChange{
		Field: "detour",
		To:    hopTag,
		By:    option.LXConfigChangeBy{Type: option.LXChangeByChain, Chain: place.chain, Position: place.position},
	}
	if previous, has := m["detour"]; has {
		change.From = previous
	}
	changes := []option.LXConfigChange{change}
	m["detour"] = hopTag
	if patch, has := rewrite[entry.typeName]; has {
		patchMap, isObject := patch.(map[string]any)
		if !isObject {
			return nil, nil, E.New("chain ", place.chain, ": rewrite[", entry.typeName, "] must be a JSON object")
		}
		if _, touchesDetour := patchMap["detour"]; touchesDetour {
			return nil, nil, E.New("chain ", place.chain, ": rewrite[", entry.typeName, "] cannot change detour")
		}
		before := flatten(m)
		patchMap = normalizeNumbers(patchMap)
		mergePatch(m, patchMap)
		for _, diff := range diffFlat(before, flatten(m)) {
			diff.By = option.LXConfigChangeBy{Type: option.LXChangeByChainRewrite, Chain: place.chain, Position: place.position, NodeType: entry.typeName}
			changes = append(changes, diff)
		}
	}
	rewritten, err := mapToOptions(r.ctx, entry.typeName, m, entry.isEndpoint)
	if err != nil {
		return nil, nil, E.Cause(err, "chain ", place.chain, ": position ", place.position, ": ", entry.tag)
	}
	return rewritten, changes, nil
}

// checkCycles is К6: a cycle through the rewritten detour graph is reported
// with the chain and the nodes on it, not only hop tags.
func (r *rewriter) checkCycles() error {
	const (
		unvisited = iota
		visiting
		done
	)
	state := make(map[string]int, len(r.entries))
	var stack []string
	var visit func(tag string) []string
	visit = func(tag string) []string {
		entry := r.entries[tag]
		if entry == nil {
			return nil
		}
		switch state[tag] {
		case visiting:
			start := 0
			for i, onStack := range stack {
				if onStack == tag {
					start = i
				}
			}
			return append(append([]string(nil), stack[start:]...), tag)
		case done:
			return nil
		}
		state[tag] = visiting
		stack = append(stack, tag)
		for _, next := range dependenciesOf(entry) {
			if cycle := visit(next); cycle != nil {
				return cycle
			}
		}
		stack = stack[:len(stack)-1]
		state[tag] = done
		return nil
	}
	for _, entry := range r.order {
		cycle := visit(entry.tag)
		if cycle == nil {
			continue
		}
		var chainTag string
		var nodes []string
		for _, tag := range cycle {
			if hop, isHop := r.entries[tag].options.(*option.ChainHopOutboundOptions); isHop {
				if chainTag == "" {
					chainTag = hop.Chain
				}
				continue
			}
			nodes = append(nodes, tag)
		}
		if chainTag == "" {
			// Not ours: the start-up dependency check reports it.
			continue
		}
		return E.New("chain ", chainTag, ": detour cycle through ", strings.Join(nodes, " → "))
	}
	return nil
}

func dependenciesOf(entry *rewriteEntry) []string {
	switch options := entry.options.(type) {
	case *option.ChainHopOutboundOptions:
		if options.Below != "" {
			return []string{options.Outbound, options.Below}
		}
		return []string{options.Outbound}
	case *option.ChainOutboundOptions:
		if len(options.Outbounds) == 0 {
			return nil
		}
		return []string{HopTag(entry.tag, len(options.Outbounds)-1)}
	}
	if members, isGroup := groupMembers(entry.options); isGroup {
		return members
	}
	if wrapper, ok := entry.options.(option.DialerOptionsWrapper); ok {
		if detour := wrapper.TakeDialerOptions().Detour; detour != "" {
			return []string{detour}
		}
	}
	return nil
}

// flatten maps every leaf of m (a non-object value) to its dotted path.
func flatten(m map[string]any) map[string]any {
	flat := make(map[string]any)
	var walk func(prefix string, value map[string]any)
	walk = func(prefix string, value map[string]any) {
		for key, child := range value {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if object, isObject := child.(map[string]any); isObject {
				walk(path, object)
				continue
			}
			flat[path] = child
		}
	}
	walk("", m)
	return flat
}

// diffFlat returns one change per leaf that differs, sorted by path.
func diffFlat(before, after map[string]any) []option.LXConfigChange {
	var changes []option.LXConfigChange
	for path, value := range after {
		previous, had := before[path]
		if had && reflect.DeepEqual(previous, value) {
			continue
		}
		change := option.LXConfigChange{Field: path, To: value}
		if had {
			change.From = previous
		}
		changes = append(changes, change)
	}
	for path, previous := range before {
		if _, kept := after[path]; !kept {
			changes = append(changes, option.LXConfigChange{Field: path, From: previous})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Field < changes[j].Field })
	return changes
}

// normalizeNumbers re-decodes a patch so its numbers are json.Number, like
// the node map, and compare equal to them.
func normalizeNumbers(patch map[string]any) map[string]any {
	data, err := stdjson.Marshal(patch)
	if err != nil {
		return patch
	}
	decoder := stdjson.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var normalized map[string]any
	if decoder.Decode(&normalized) != nil {
		return patch
	}
	return normalized
}

func optionsToMap(ctx context.Context, options any) (map[string]any, error) {
	data, err := json.MarshalContext(ctx, options)
	if err != nil {
		return nil, err
	}
	decoder := stdjson.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var m map[string]any
	if err := decoder.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		m = make(map[string]any)
	}
	return m, nil
}

func mapToOptions(ctx context.Context, typeName string, m map[string]any, isEndpoint bool) (any, error) {
	data, err := stdjson.Marshal(m)
	if err != nil {
		return nil, err
	}
	var (
		options any
		loaded  bool
	)
	if isEndpoint {
		registry := service.FromContext[option.EndpointOptionsRegistry](ctx)
		if registry == nil {
			return nil, E.New("missing endpoint options registry")
		}
		options, loaded = registry.CreateOptions(typeName)
	} else {
		registry := service.FromContext[option.OutboundOptionsRegistry](ctx)
		if registry == nil {
			return nil, E.New("missing outbound options registry")
		}
		options, loaded = registry.CreateOptions(typeName)
	}
	if !loaded {
		return nil, E.New("unknown type: ", typeName)
	}
	if err := json.UnmarshalContextDisallowUnknownFields(ctx, data, options); err != nil {
		return nil, E.Cause(err, "decode ", typeName, " options")
	}
	return options, nil
}

// mergePatch — RFC 7396: null удаляет ключ, объект сливается рекурсивно,
// остальное заменяет.
func mergePatch(target map[string]any, patch map[string]any) {
	for key, value := range patch {
		if value == nil {
			delete(target, key)
			continue
		}
		if patchObject, isObject := value.(map[string]any); isObject {
			targetObject, ok := target[key].(map[string]any)
			if !ok {
				targetObject = make(map[string]any)
			}
			mergePatch(targetObject, patchObject)
			target[key] = targetObject
			continue
		}
		target[key] = value
	}
}
