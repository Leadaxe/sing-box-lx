// Package dnstrack is the lx DNS-query observability layer (SPEC 018). It mirrors
// common/trafficcontrol for DNS: a process-attributed, structured live stream of DNS
// resolutions that the command server exposes as SubscribeDNSQueries.
//
// Why it exists: hijacked DNS (the norm on an Android VPN) is answered in
// route.hijackDNSStream/Packet BEFORE a connection becomes a trafficcontrol tracker, so
// DNS queries never appear in the connections stream. The only existing egress was the
// text log (dns/client_log.go), which carries no ProcessInfo — forcing LxBox to parse
// log lines and stitch package names by conn_id. This package emits the same data
// structurally, attributed in the core where ProcessInfo is already on the context.
package dnstrack

import (
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/observable"
)

// Source is how the response was produced — the log verb in dns/client_log.go. It is a
// free signal for a profiler (cache vs network hit ratio).
type Source string

const (
	SourceExchanged  Source = "exchanged"
	SourceCached     Source = "cached"
	SourceOptimistic Source = "optimistic"
	SourceRefreshed  Source = "refreshed"
	// No SourceRejected: a rejected resolution (loopback / rejected-cached / SERVFAIL-reject)
	// is folded into SourceFailed at the emit site (dns/client.go via emitFailedQuery), so
	// "rejected" never reaches the wire — see dns/client_log.go logRejectedResponse. Adding it
	// back would create an unreachable case on the client.
	//
	// SourceFailed marks a resolution that produced no usable answer (timeout, loopback,
	// rejected-cached, SERVFAIL-reject). Without it the stream would be blind to DNS
	// failures — the primary "DNS is being throttled" signal. See SPEC 018 пункт 1.
	SourceFailed Source = "failed"
)

// RcodeNoAnswer is the Rcode sentinel when the resolver returned no response at all
// (transport timeout / network error). Distinct from 0 (NOERROR) so a timeout never reads
// as success. SPEC 018 Q1.
const RcodeNoAnswer int32 = -1

// Answer is one resource record of a response, kept in wire order so a CNAME chain
// (cname hops + final A/AAAA) survives intact for the client to reassemble. SPEC 018 Q2.
type Answer struct {
	Name  string
	Type  uint16 // dns.Type
	RData string // textual rdata (CNAME target / IP / …)
	TTL   uint32
}

// QueryEvent is one DNS resolution. Successful resolutions are captured at the
// dns/client_log.go emit point; failures are emitted directly from dns/client.go Exchange
// where response==nil. ProcessInfo comes from the request ctx (same ctx on cache-hit and
// cache-miss paths, so cached queries are attributed too).
type QueryEvent struct {
	Domain      string
	QueryType   uint16 // dns.Type (A/AAAA/HTTPS/…)
	Rcode       int32  // dns.Rcode; RcodeNoAnswer (-1) when there was no response
	TTL         uint32
	Source      Source
	Failed      bool   // true on timeout / loopback / rejected-cached / SERVFAIL-reject
	Error       string // human cause when Failed; "" on success
	ProcessInfo *adapter.ConnectionOwner
	Answers     []Answer // full response.Answer in order; nil unless the subscriber asked for it
	// DNSServer/DNSServerType identify which DNS server (transport) resolved the query —
	// the DNS rule picks a server, not an outbound. Outbound is the channel that server is
	// statically bound to (its detour), resolved through a selector's Now() to the live
	// node; empty on cached/optimistic paths where the query never left the device.
	DNSServer     string
	DNSServerType string
	Outbound      []string
	// SPEC 036 — group probe trace. GroupPath is the group nesting inside-out
	// (empty = the query did not go through a group); Attempts is the probe
	// chronology snapshotted at emit time (race stragglers resolved later are
	// absent by design); Racer marks the query that triggered a race fan-out.
	GroupPath []string
	Attempts  []Attempt
	Racer     bool
}

var _ adapter.LifecycleService = (*Manager)(nil)

// Manager fans one DNS-query stream out to every command-server subscriber via the shared
// observable layer (same as trafficcontrol). The buffer matches connections (256); on
// overflow the oldest events drop — a profiler is an observer, not an audit log.
type Manager struct {
	eventSubscriber *observable.Subscriber[QueryEvent]
	eventObserver   *observable.Observer[QueryEvent]
	subscribers     atomic.Int32 // live SubscribeDNSQueries streams; gates event construction
}

func NewManager() *Manager {
	manager := &Manager{
		eventSubscriber: observable.NewSubscriber[QueryEvent](256),
	}
	manager.eventObserver = observable.NewObserver(manager.eventSubscriber, 64)
	return manager
}

// HasSubscribers reports whether any SubscribeDNSQueries stream is open. The emit sites
// check this BEFORE building a QueryEvent, so when no profiler is attached the DNS hot path
// does zero work (not even constructing the event, let alone resolving the outbound). The
// observable Emit already drops events with no listener, but the construction cost — and
// the Now() outbound resolution — would still be paid; this gate removes it.
func (m *Manager) HasSubscribers() bool {
	if m == nil {
		return false
	}
	return m.subscribers.Load() > 0
}

func (m *Manager) Name() string {
	return "dns query manager"
}

func (m *Manager) Start(stage adapter.StartStage) error {
	return nil
}

func (m *Manager) Close() error {
	return m.eventObserver.Close()
}

// Emit publishes one DNS-query event. Safe to call from the resolver hot path; a nil
// receiver is a no-op so the emit site needs no guard when observability is off.
func (m *Manager) Emit(event QueryEvent) {
	if m == nil {
		return
	}
	m.eventSubscriber.Emit(event)
}

// SubscribeEvents returns a channel of events and a done channel, mirroring
// trafficcontrol.Manager so the command server reuses the same subscription pattern.
func (m *Manager) SubscribeEvents() (observable.Subscription[QueryEvent], <-chan struct{}, error) {
	subscription, done, err := m.eventObserver.Subscribe()
	if err == nil {
		m.subscribers.Add(1)
	}
	return subscription, done, err
}

func (m *Manager) UnSubscribeEvents(subscription observable.Subscription[QueryEvent]) {
	m.eventObserver.UnSubscribe(subscription)
	m.subscribers.Add(-1)
}
