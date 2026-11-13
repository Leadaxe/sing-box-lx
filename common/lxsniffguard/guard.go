// Package lxsniffguard keeps the path labels of lx.sniff_guard (SPEC 060).
//
// A relay that sniffs the first handshake packet and rewrites the
// destination by the SNI it reads (Xray `sniffing.destOverride` without
// `routeOnly`) sends a node with a cover SNI to a foreign host. The client
// cannot see the sniffer; it learns it from outcomes. Each path (the direct
// path, or a carrier by its tag) has a label per protocol (TLS, QUIC):
// clean dials without protection, suspect dials with the section's method.
// A label moves only on a pair of outcomes on one node: a failure with one
// decision, then a success with the opposite one.
//
// The handshake layer reads the decision from the dial context
// (AttemptFromContext → Attempt.Decide); the outbound wrapper drives the
// retry around the node's dial (Node.Dial). Seams that learn the outcome
// late report it through Node.Report; AmneziaWG reads the label without an
// outcome through Node.Peek.
package lxsniffguard

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service"
)

const (
	// pairLifetime is how long a pending pair waits for the node's next dial.
	pairLifetime = 10 * time.Minute
	// probeWindow is the minimum spacing of descent probes on a suspect key,
	// and of probes of a node whose SNI matches its server.
	probeWindow = 10 * time.Minute
)

// Proto is the handshake protocol a label belongs to.
type Proto uint8

const (
	ProtoTLS Proto = iota + 1
	ProtoQUIC
)

func (p Proto) String() string {
	switch p {
	case ProtoTLS:
		return "tls"
	case ProtoQUIC:
		return "quic"
	}
	return "unknown"
}

func parseProto(s string) (Proto, bool) {
	switch s {
	case "tls":
		return ProtoTLS, true
	case "quic":
		return ProtoQUIC, true
	}
	return 0, false
}

// Path is where a dial went. Direct means the conn is a kernel socket.
// Otherwise the carrier is the nearest non-group outbound recorded by
// adapter.CarrierRecord; an empty CarrierTag with Direct unset is an unknown
// path: no protection, no label, no retry.
type Path struct {
	Direct      bool
	CarrierType string
	CarrierTag  string
}

// NodeInfo is computed once at load from the typed options of a node that
// passed the `nodes` filters.
type NodeInfo struct {
	Type string
	// SNIMismatch grants the right to move path labels (see SNIMismatch).
	// A node whose SNI matches its server only gets a personal opt-out.
	SNIMismatch bool
}

// CarrierInfo is computed once at load for every outbound that can carry a
// dial, by tag.
type CarrierInfo struct {
	Type string
	// Fingerprint identifies the relay behind the tag (type|address|port);
	// a persisted label is applied only to the same relay.
	Fingerprint string
	// Encrypted is false for carriers a local network can see into (socks,
	// http without TLS): their labels reset on a network change and are
	// never persisted.
	Encrypted bool
}

// Cache persists the labels of encrypted carriers (experimental.cache_file).
// Keys are "<carrier tag>|tls" and "<carrier tag>|quic".
type Cache interface {
	LoadSniffGuard() map[string]CacheEntry
	StoreSniffGuard(key string, entry CacheEntry) error
}

type CacheEntry struct {
	Fingerprint string
	State       string
}

type Config struct {
	Policy option.LXSniffGuardResolved
	// Nodes holds only the nodes that passed `nodes`; others are absent.
	Nodes    map[string]NodeInfo
	Carriers map[string]CarrierInfo
	// Cache is nil when persist is off.
	Cache  Cache
	Logger logger.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// labelKey is a path and a protocol. The direct path has an empty tag: a
// carrier always has a non-empty one.
type labelKey struct {
	tag   string
	proto Proto
}

func (k labelKey) String() string {
	if k.tag == "" {
		return k.proto.String() + " path direct"
	}
	return k.proto.String() + " path detour " + k.tag
}

func (k labelKey) cacheKey() string {
	return k.tag + "|" + k.proto.String()
}

type label struct {
	state string
	// probe is the last descent probe on this key.
	probe time.Time
}

// Guard is the label store of one box. A nil *Guard is the feature off.
type Guard struct {
	policy   option.LXSniffGuardResolved
	carriers map[string]CarrierInfo
	cache    Cache
	logger   logger.Logger
	now      func() time.Time
	nodes    map[string]*Node

	access sync.Mutex
	labels map[labelKey]*label
	// epoch counts network changes; an outcome on a local key decided in an
	// older epoch is dropped.
	epoch uint64
}

// New returns the guard of a box, or nil when the policy enables no section.
func New(config Config) *Guard {
	if !config.Policy.Enabled() {
		return nil
	}
	g := &Guard{
		policy:   config.Policy,
		carriers: config.Carriers,
		cache:    config.Cache,
		logger:   config.Logger,
		now:      config.Now,
		nodes:    make(map[string]*Node, len(config.Nodes)),
		labels:   make(map[labelKey]*label),
	}
	if g.carriers == nil {
		g.carriers = map[string]CarrierInfo{}
	}
	if g.logger == nil {
		g.logger = logger.NOP()
	}
	if g.now == nil {
		g.now = time.Now
	}
	for tag, info := range config.Nodes {
		g.nodes[tag] = &Node{guard: g, tag: tag, info: info}
	}
	g.loadCache()
	return g
}

// ContextWith registers g in ctx for FromContext.
func ContextWith(ctx context.Context, g *Guard) context.Context {
	return service.ContextWithPtr(ctx, g)
}

// FromContext returns the guard registered in ctx, or nil.
func FromContext(ctx context.Context) *Guard {
	return service.PtrFromContext[Guard](ctx)
}

// Node returns the handle of a node that passed `nodes`, or nil.
func (g *Guard) Node(tag string) *Node {
	if g == nil {
		return nil
	}
	return g.nodes[tag]
}

// Override reports lx.sniff_guard.override.
func (g *Guard) Override() bool {
	return g != nil && g.policy.Override
}

// NetworkChanged returns the labels of the direct path and of unencrypted
// carriers to their start state and drops the pending pairs and personal
// opt-outs on them. Labels of encrypted carriers describe the relay and stay.
func (g *Guard) NetworkChanged() {
	if g == nil {
		return
	}
	g.access.Lock()
	defer g.access.Unlock()
	g.epoch++
	for key := range g.labels {
		if g.isLocal(key) {
			delete(g.labels, key)
		}
	}
	for _, node := range g.nodes {
		if node.pending != nil && g.isLocal(node.pending.key) {
			node.pending = nil
		}
		for key := range node.optOut {
			if g.isLocal(key) {
				delete(node.optOut, key)
			}
		}
	}
	g.logger.Debug("sniff guard: network changed, labels of local paths reset")
}

func (g *Guard) section(proto Proto) option.LXSniffGuardSectionResolved {
	if proto == ProtoQUIC {
		return g.policy.QUIC
	}
	return g.policy.TLS
}

// resolve returns the label key of a dial and its start state; ok is false
// when the section is off, the path is unknown or the path is `never`.
func (g *Guard) resolve(proto Proto, path Path) (key labelKey, start string, ok bool) {
	if proto != ProtoTLS && proto != ProtoQUIC {
		return
	}
	section := g.section(proto)
	if !section.Enabled() {
		return
	}
	if path.Direct {
		key = labelKey{proto: proto}
		start = section.StartState(option.LXSniffGuardDirect)
	} else {
		if path.CarrierTag == "" {
			return
		}
		carrierType := path.CarrierType
		if carrierType == "" {
			carrierType = g.carriers[path.CarrierTag].Type
		}
		key = labelKey{tag: path.CarrierTag, proto: proto}
		start = section.StartState(carrierType)
	}
	if start != option.LXSniffGuardClean && start != option.LXSniffGuardSuspect {
		return labelKey{}, "", false
	}
	return key, start, true
}

// label returns the label of key, created at start when absent.
func (g *Guard) label(key labelKey, start string) *label {
	l := g.labels[key]
	if l == nil {
		l = &label{state: start}
		g.labels[key] = l
	}
	return l
}

// isLocal reports whether key is a path a network change resets: the direct
// path, or a carrier that is not known to be encrypted.
func (g *Guard) isLocal(key labelKey) bool {
	return key.tag == "" || !g.carriers[key.tag].Encrypted
}

func (g *Guard) decision(proto Proto, protect bool) Decision {
	d := Decision{Protect: protect, Override: g.policy.Override}
	if protect {
		section := g.section(proto)
		d.Method = section.Method
		d.Delay = section.Delay
	}
	return d
}

// LoadCache attaches the cache and reads the persisted labels from it, for a
// box whose cache file opens only after the guard is created. A label learnt
// before the call is newer than the cached one and is kept. Nil-safe on both.
func (g *Guard) LoadCache(cache Cache) {
	if g == nil || cache == nil {
		return
	}
	g.access.Lock()
	defer g.access.Unlock()
	g.cache = cache
	g.loadCache()
}

func (g *Guard) loadCache() {
	if g.cache == nil || !(g.policy.TLS.Persist || g.policy.QUIC.Persist) {
		return
	}
	for cacheKey, entry := range g.cache.LoadSniffGuard() {
		index := strings.LastIndexByte(cacheKey, '|')
		if index <= 0 {
			continue
		}
		tag := cacheKey[:index]
		proto, ok := parseProto(cacheKey[index+1:])
		if !ok {
			continue
		}
		section := g.section(proto)
		if !section.Enabled() || !section.Persist {
			continue
		}
		carrier, ok := g.carriers[tag]
		if !ok || !carrier.Encrypted || carrier.Fingerprint != entry.Fingerprint {
			continue
		}
		start := section.StartState(carrier.Type)
		if start != option.LXSniffGuardClean && start != option.LXSniffGuardSuspect {
			continue
		}
		if entry.State != option.LXSniffGuardClean && entry.State != option.LXSniffGuardSuspect {
			continue
		}
		key := labelKey{tag: tag, proto: proto}
		if _, loaded := g.labels[key]; loaded {
			continue
		}
		g.labels[key] = &label{state: entry.State}
	}
}

// persistable reports whether a transition of key is written to the cache.
func (g *Guard) persistable(key labelKey) bool {
	if g.cache == nil || key.tag == "" || !g.section(key.proto).Persist {
		return false
	}
	return g.carriers[key.tag].Encrypted
}
