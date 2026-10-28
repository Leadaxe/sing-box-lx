//go:build with_lx_command

package fakeip

// lx: SPEC 139 — read access to the fakeip store for the connection test
// findings (fakeip_unknown_address, fakeip_address_reassigned,
// fakeip_not_persisted, fakeip_pool_wrapped, fakeip_ipv6_without_tun_ipv6)
// and the pool-wrap counter fed by the one-line insertion in Create. Every
// accessor only reads: a connection test never allocates or rewrites a fakeip
// record (SPEC 139 §5.7).

import (
	"net/netip"
	"runtime"
	"sync"
	"time"
	"weak"

	"github.com/sagernet/sing-box/adapter"
)

// LxWrap is the pool-wrap history of one address family.
type LxWrap struct {
	Count    uint64        // wraps since the store started
	Last     time.Time     // time of the last wrap; zero without one
	Interval time.Duration // time the last full pass over the range took
}

// LxState is a read-only view of a fakeip store.
type LxState struct {
	Inet4Range netip.Prefix
	Inet6Range netip.Prefix
	Persisted  bool // records live in the cache file and survive a restart
	Inet4Wrap  LxWrap
	Inet6Wrap  LxWrap
}

// LxStateOf returns the view of store; false when store is not this package's
// store or has not started.
func LxStateOf(store adapter.FakeIPStore) (LxState, bool) {
	s, isStore := store.(*Store)
	if !isStore || s.storage == nil {
		return LxState{}, false
	}
	state := LxState{
		Inet4Range: s.inet4Range,
		Inet6Range: s.inet6Range,
	}
	_, inMemory := s.storage.(*MemoryStorage)
	state.Persisted = !inMemory
	if wraps := lxLoadWraps(s); wraps != nil {
		wraps.access.Lock()
		state.Inet4Wrap = wraps.inet4.wrap
		state.Inet6Wrap = wraps.inet6.wrap
		wraps.access.Unlock()
	}
	return state, true
}

// LxAddressOf returns the address the store currently holds for domain in the
// given family, without allocating one.
func LxAddressOf(store adapter.FakeIPStore, domain string, isIPv6 bool) (netip.Addr, bool) {
	s, isStore := store.(*Store)
	if !isStore || s.storage == nil {
		return netip.Addr{}, false
	}
	return s.storage.FakeIPLoadDomain(domain, isIPv6)
}

// LxDomainOf returns the domain the store holds for address.
func LxDomainOf(store adapter.FakeIPStore, address netip.Addr) (string, bool) {
	s, isStore := store.(*Store)
	if !isStore || s.storage == nil {
		return "", false
	}
	return s.storage.FakeIPLoad(address)
}

type lxFamilyWraps struct {
	passStart time.Time // first allocation of the current pass
	wrap      LxWrap
}

type lxStoreWraps struct {
	access sync.Mutex
	inet4  lxFamilyWraps
	inet6  lxFamilyWraps
}

// lxWraps holds the wrap history per store. Keys are weak, so a store dropped
// on reload takes its entry with it.
var lxWraps sync.Map // weak.Pointer[Store] → *lxStoreWraps

func lxLoadWraps(s *Store) *lxStoreWraps {
	wraps, _ := lxWraps.Load(weak.Make(s))
	typed, _ := wraps.(*lxStoreWraps)
	return typed
}

// lxNoteCreate runs deferred in Create, under addressAccess, with the current
// addresses as they were before the allocation. A current address that moved
// backwards means the allocation jumped to the start of the range: the pool
// wrapped, and every older record of that family may now name a new domain.
func lxNoteCreate(s *Store, previous4, previous6 netip.Addr) {
	wraps := lxLoadWraps(s)
	if wraps == nil {
		key := weak.Make(s)
		loaded, isLoaded := lxWraps.LoadOrStore(key, &lxStoreWraps{})
		if !isLoaded {
			runtime.AddCleanup(s, func(key weak.Pointer[Store]) { lxWraps.Delete(key) }, key)
		}
		wraps = loaded.(*lxStoreWraps)
	}
	now := time.Now()
	wraps.access.Lock()
	defer wraps.access.Unlock()
	if s.inet4Current != previous4 {
		wraps.inet4.note(now, s.inet4Current.Less(previous4))
	}
	if s.inet6Current != previous6 {
		wraps.inet6.note(now, s.inet6Current.Less(previous6))
	}
}

// note counts a wrap. The interval is measured only between two wraps: the
// first pass after start may have begun anywhere in the range (a persisted
// store resumes from its saved current address), so its length says nothing
// about how fast the pool turns over.
func (f *lxFamilyWraps) note(now time.Time, wrapped bool) {
	if !wrapped {
		return
	}
	f.wrap.Count++
	f.wrap.Last = now
	if !f.passStart.IsZero() {
		f.wrap.Interval = now.Sub(f.passStart)
	}
	f.passStart = now
}
