package masque

import (
	"context"
	"sync"
	"testing"
)

// newBareSession builds a session with no real device/ipConn/closer — enough to
// exercise the teardown/generation logic (all three are nil-guarded).
func newBareSession() *session {
	ctx, cancel := context.WithCancel(context.Background())
	return &session{ctx: ctx, cancel: cancel}
}

// TestTeardownGenerationGuard: tearing down a stale session must NOT clear a
// newer current session (this was the class of bug behind the reconnect defect).
func TestTeardownGenerationGuard(t *testing.T) {
	t.Parallel()
	o := &Outbound{}

	old := newBareSession()
	o.sess = old

	// Simulate reconnect: a new session becomes current.
	fresh := newBareSession()
	o.runMu.Lock()
	o.sess = fresh
	o.runMu.Unlock()

	// Late teardown of the OLD session must not touch the fresh one.
	o.teardownSession(old)

	o.runMu.Lock()
	got := o.sess
	o.runMu.Unlock()
	if got != fresh {
		t.Fatal("teardown of stale session cleared the current (fresh) session")
	}

	// Tearing down the fresh (current) session clears it → next dial rebuilds.
	o.teardownSession(fresh)
	o.runMu.Lock()
	got = o.sess
	o.runMu.Unlock()
	if got != nil {
		t.Fatal("teardown of current session must clear o.sess (enables reconnect)")
	}
}

// TestTeardownIdempotent: both pumps + Close may race to tear a session down;
// it must run once and not panic (double cancel / double clear).
func TestTeardownIdempotent(t *testing.T) {
	t.Parallel()
	o := &Outbound{}
	s := newBareSession()
	o.sess = s

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o.teardownSession(s)
		}()
	}
	wg.Wait()

	o.runMu.Lock()
	got := o.sess
	o.runMu.Unlock()
	if got != nil {
		t.Fatal("concurrent teardown must end with o.sess == nil")
	}
}

// TestClosePreventsNewSession: after Close, ensureSession must refuse to build.
func TestClosePreventsNewSession(t *testing.T) {
	t.Parallel()
	o := &Outbound{}
	_ = o.Close()
	if _, err := o.ensureSession(context.Background()); err == nil {
		t.Fatal("ensureSession must fail after Close")
	}
}
