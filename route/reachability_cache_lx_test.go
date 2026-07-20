//go:build with_lx_idle_suspend

// lx:begin idle-suspend
package route

import (
	"testing"

	"github.com/sagernet/sing-box/adapter"
)

// countingManager is a minimal adapter.OutboundManager that records how many
// times the walk reads it (via Default), so a test can tell a recompute from a
// cache hit. Only the three methods the walk uses are implemented; the rest come
// from the embedded nil interface and must never be called.
type countingManager struct {
	adapter.OutboundManager
	defaultCalls int
}

func (m *countingManager) Default() adapter.Outbound {
	m.defaultCalls++
	return &stubOutbound{tag: "final"}
}

func (m *countingManager) Outbound(tag string) (adapter.Outbound, bool) {
	return &stubOutbound{tag: tag}, true
}
func (m *countingManager) Outbounds() []adapter.Outbound { return nil }

func TestReachCache_recomputesOnlyWhenDirty(t *testing.T) {
	mgr := &countingManager{}
	r := &Router{outbound: mgr}
	r.reachDirty.Store(true) // initial state, as NewRouter sets it

	// First call: dirty → recompute (1 walk).
	r.reachableOutbounds()
	if mgr.defaultCalls != 1 {
		t.Fatalf("first call should walk once, got %d walks", mgr.defaultCalls)
	}

	// Subsequent calls without an event: served from cache, no further walk.
	r.reachableOutbounds()
	r.reachableOutbounds()
	if mgr.defaultCalls != 1 {
		t.Fatalf("cached calls must not walk again, got %d walks", mgr.defaultCalls)
	}

	// After an event invalidates: next call recomputes once more.
	r.InvalidateReachability()
	r.reachableOutbounds()
	if mgr.defaultCalls != 2 {
		t.Fatalf("after invalidation, expected a second walk, got %d", mgr.defaultCalls)
	}
	// And then caches again.
	r.reachableOutbounds()
	if mgr.defaultCalls != 2 {
		t.Fatalf("should be cached again after recompute, got %d", mgr.defaultCalls)
	}
}

func TestReachCache_resultIsCorrect(t *testing.T) {
	mgr := &countingManager{}
	r := &Router{outbound: mgr}
	r.reachDirty.Store(true)
	got := r.reachableOutbounds()
	if !got["final"] {
		t.Fatalf("final outbound must be reachable, got %v", keys(got))
	}
}

func TestReachCache_invalidateIsLockFree(t *testing.T) {
	// InvalidateReachability must be safe to call concurrently (it is a lone atomic
	// store). This is a smoke test that it does not deadlock / panic under churn.
	r := &Router{outbound: &countingManager{}}
	r.reachDirty.Store(true)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			r.InvalidateReachability()
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		r.reachableOutbounds()
	}
	<-done
}

// lx:end idle-suspend
