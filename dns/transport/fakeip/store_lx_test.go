//go:build with_lx_command

package fakeip

import (
	"context"
	"net/netip"
	"strconv"
	"testing"

	"github.com/sagernet/sing/common/logger"
)

// lx: SPEC 139 — the read-only fakeip view and the pool-wrap counter fed by
// the Create insertion.

func newLxTestStore(t *testing.T, inet4, inet6 string) *Store {
	t.Helper()
	var inet4Range, inet6Range netip.Prefix
	if inet4 != "" {
		inet4Range = netip.MustParsePrefix(inet4)
	}
	if inet6 != "" {
		inet6Range = netip.MustParsePrefix(inet6)
	}
	store := NewStore(context.Background(), logger.NOP(), inet4Range, inet6Range)
	if err := store.Start(); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestLxStoreWrapCounter(t *testing.T) {
	// /29: .2 … .6 are handed out, .7 (broadcast) sends the next one back to .2.
	store := newLxTestStore(t, "198.18.0.0/29", "")
	state, loaded := LxStateOf(store)
	if !loaded || state.Persisted || state.Inet4Range.String() != "198.18.0.0/29" || state.Inet4Wrap.Count != 0 {
		t.Fatalf("fresh store state = %+v, %v", state, loaded)
	}
	var addresses []netip.Addr
	for i := 0; i < 5; i++ {
		address, err := store.Create("d"+strconv.Itoa(i)+".test", false)
		if err != nil {
			t.Fatal(err)
		}
		addresses = append(addresses, address)
	}
	if addresses[0] != netip.MustParseAddr("198.18.0.2") || addresses[4] != netip.MustParseAddr("198.18.0.6") {
		t.Fatalf("addresses = %v", addresses)
	}
	state, _ = LxStateOf(store)
	if state.Inet4Wrap.Count != 0 {
		t.Fatalf("a first pass is no wrap: %+v", state.Inet4Wrap)
	}
	// An existing domain does not allocate.
	if address, _ := store.Create("d0.test", false); address != addresses[0] {
		t.Fatalf("d0.test = %v", address)
	}
	wrapped, err := store.Create("d5.test", false)
	if err != nil {
		t.Fatal(err)
	}
	if wrapped != addresses[0] {
		t.Fatalf("wrapped address = %v, want %v", wrapped, addresses[0])
	}
	state, _ = LxStateOf(store)
	if state.Inet4Wrap.Count != 1 || state.Inet4Wrap.Last.IsZero() || state.Inet6Wrap.Count != 0 {
		t.Fatalf("after the wrap: %+v", state)
	}
	// The first pass may have begun anywhere (a persisted store resumes from
	// its saved address): one wrap gives no interval to judge turnover by.
	if state.Inet4Wrap.Interval != 0 {
		t.Fatalf("an interval from the first wrap alone: %+v", state.Inet4Wrap)
	}

	// The address now names the new domain; the old domain has no address.
	if domain, known := LxDomainOf(store, addresses[0]); !known || domain != "d5.test" {
		t.Fatalf("domain of %v = %q, %v", addresses[0], domain, known)
	}
	if _, known := LxAddressOf(store, "d0.test", false); known {
		t.Fatal("d0.test still has an address after its record was reused")
	}
	if address, known := LxAddressOf(store, "d1.test", false); !known || address != addresses[1] {
		t.Fatalf("d1.test = %v, %v", address, known)
	}
	if _, known := LxDomainOf(store, netip.MustParseAddr("198.18.0.6")); !known {
		t.Fatal("198.18.0.6 lost its record")
	}
}

func TestLxStoreReadsDoNotAllocate(t *testing.T) {
	store := newLxTestStore(t, "198.18.0.0/15", "fc00::/18")
	if _, known := LxAddressOf(store, "never.test", true); known {
		t.Fatal("unknown domain reported")
	}
	if _, known := LxDomainOf(store, netip.MustParseAddr("198.18.0.9")); known {
		t.Fatal("unknown address reported")
	}
	if _, known := LxAddressOf(store, "never.test", false); known {
		t.Fatal("a read allocated a record")
	}
	state, _ := LxStateOf(store)
	if state.Inet6Range.String() != "fc00::/18" || state.Inet4Wrap.Count != 0 {
		t.Fatalf("state = %+v", state)
	}
	// Not started, or not this package's store: no view.
	if _, loaded := LxStateOf(NewStore(context.Background(), logger.NOP(), netip.MustParsePrefix("198.18.0.0/15"), netip.Prefix{})); loaded {
		t.Fatal("a store that has not started has no view")
	}
	if _, loaded := LxStateOf(nil); loaded {
		t.Fatal("nil store has a view")
	}
}
