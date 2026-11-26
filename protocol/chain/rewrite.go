package chain

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"reflect"
	"slices"
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

// Rewrite runs П1–П7 and К1–К5 on options. The outbound and endpoint slices
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
	for _, chainEntry := range r.chains {
		if err := r.validatePositions(chainEntry.tag, r.chainOptions(chainEntry).Outbounds); err != nil {
			return nil, err
		}
	}
	if err := r.checkConflicts(); err != nil {
		return nil, err
	}
	intercepted := make(map[string]interception)
	var interceptOrder []string
	for _, chainEntry := range r.chains {
		chainTag := chainEntry.tag
		positions := r.chainOptions(chainEntry).Outbounds
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
			if i == 0 {
				continue // П3: the entry is not taken over
			}
			for _, leaf := range leaves {
				if err := r.checkDialable(chainTag, i, leaf); err != nil {
					return nil, err
				}
				intercepted[leaf.tag] = interception{chain: chainTag, position: i}
				interceptOrder = append(interceptOrder, leaf.tag)
			}
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
			return E.New("chain ", chainTag, ": duplicate outbound ", position, " (position ", i, "): one node or group has one path")
		}
		seen[position] = true
		if r.entries[position] == nil {
			return E.New("chain ", chainTag, ": position ", i, ": outbound not found: ", position)
		}
	}
	return nil
}

// chainPlace is one place of the chains: a position of a chain.
type chainPlace struct {
	chain    string
	position int
}

// checkConflicts is the core's part of К1: a node or group that needs two
// paths — reachable in two chains (any positions), or on two positions >= 1
// of one chain. Reachability counts every member of every group of any depth,
// not the groups' picks; direct and block have no path. A ring (a node that
// closes its own detour through a hop) is not checked here: upstream's
// start-order check reports it as a circular outbound dependency.
func (r *rewriter) checkConflicts() error {
	places := make(map[string][]chainPlace)
	var order []string
	for _, chainEntry := range r.chains {
		for i, position := range r.chainOptions(chainEntry).Outbounds {
			place := chainPlace{chain: chainEntry.tag, position: i}
			r.reach(position, func(tag string) {
				entry := r.entries[tag]
				if entry == nil || entry.typeName == C.TypeDirect || entry.typeName == C.TypeBlock {
					return
				}
				if len(places[tag]) == 0 {
					order = append(order, tag)
				}
				if !slices.Contains(places[tag], place) {
					places[tag] = append(places[tag], place)
				}
			})
		}
	}
	for _, tag := range order {
		first, second, conflict := placesConflict(places[tag])
		if !conflict {
			continue
		}
		return E.New(tag, " is needed on chain ", first.chain, " position ", first.position,
			" and on chain ", second.chain, " position ", second.position, ": one node or group has one path")
	}
	return nil
}

// placesConflict finds two places that need two paths: two chains with at
// least one position >= 1, or two positions >= 1 of one chain. Position 0
// and a position above it of the same chain is a ring, left to upstream; the
// entries of two chains are one place each, not a conflict.
func placesConflict(places []chainPlace) (chainPlace, chainPlace, bool) {
	for i := range places {
		for j := i + 1; j < len(places); j++ {
			a, b := places[i], places[j]
			// Position 0 is not taken over: an entry shared by two chains
			// keeps its one own path. Two places conflict when at least one
			// of them would set the detour.
			if a.position == 0 && b.position == 0 {
				continue
			}
			if a.chain != b.chain || a.position > 0 && b.position > 0 {
				return a, b, true
			}
		}
	}
	return chainPlace{}, chainPlace{}, false
}

// reach calls visit with tag and every tag reachable from it through groups.
func (r *rewriter) reach(tag string, visit func(string)) {
	seen := make(map[string]bool)
	var walk func(string)
	walk = func(tag string) {
		if seen[tag] {
			return
		}
		seen[tag] = true
		visit(tag)
		if entry := r.entries[tag]; entry != nil {
			if members, isGroup := groupMembers(entry.options); isGroup {
				for _, member := range members {
					walk(member)
				}
			}
		}
	}
	walk(tag)
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

// chainLocalPortFields are the fixed local ports a node cannot keep under
// detour (К5): its socket is opened by the hop. WireGuard's is also refused
// by its constructor; the rewrite says so earlier and names the chain.
var chainLocalPortFields = map[string][]string{
	C.TypeWireGuard:   {"listen_port"},
	C.TypeTailscale:   {"listen_port", "relay_server_port"},
	C.TypeOpenConnect: {"dtls_local_port"},
}

// checkDialable is К5: the node must be able to work through detour.
func (r *rewriter) checkDialable(chainTag string, position int, leaf *rewriteEntry) error {
	if _, hasDialer := leaf.options.(option.DialerOptionsWrapper); !hasDialer {
		return E.New("chain ", chainTag, ": position ", position, ": node ", leaf.tag, " (", leaf.typeName, ") cannot dial through detour")
	}
	m, err := optionsToMap(r.ctx, leaf.options)
	if err != nil {
		return E.Cause(err, "chain ", chainTag, ": serialize options of ", leaf.tag)
	}
	var ports []string
	for _, field := range chainLocalPortFields[leaf.typeName] {
		if value, has := m[field]; has && !isZeroJSON(value) {
			ports = append(ports, field)
		}
	}
	if len(ports) > 0 {
		return E.New("chain ", chainTag, ": position ", position, ": node ", leaf.tag, " has ", strings.Join(ports, ", "),
			": a fixed local port does not work with detour, and the chain gives the node one")
	}
	return nil
}

func isZeroJSON(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case bool:
		return !typed
	case string:
		return typed == ""
	case stdjson.Number:
		return typed.String() == "0"
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	}
	return false
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
		rewritten, err := mapToOptions(r.ctx, entry.typeName, m, entry.isEndpoint)
		if err != nil {
			return nil, nil, E.Cause(err, "chain ", place.chain, ": position ", place.position, ": ", entry.tag)
		}
		// The difference is taken after the typed round trip: the canonical
		// form drops what the patch set to a zero value, and the changes must
		// give the effective config (И4).
		after, err := optionsToMap(r.ctx, rewritten)
		if err != nil {
			return nil, nil, E.Cause(err, "chain ", place.chain, ": serialize options of ", entry.tag)
		}
		for _, diff := range diffFlat(before, flatten(after)) {
			diff.By = option.LXConfigChangeBy{Type: option.LXChangeByChainRewrite, Chain: place.chain, Position: place.position, NodeType: entry.typeName}
			changes = append(changes, diff)
		}
		return rewritten, changes, nil
	}
	rewritten, err := mapToOptions(r.ctx, entry.typeName, m, entry.isEndpoint)
	if err != nil {
		return nil, nil, E.Cause(err, "chain ", place.chain, ": position ", place.position, ": ", entry.tag)
	}
	return rewritten, changes, nil
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
