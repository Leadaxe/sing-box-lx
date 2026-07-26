//go:build !with_lx_idle_suspend

// lx:begin idle-suspend
package route

import (
	"testing"
	"time"
)

// TestStartIdleSuspend_stubErrorsWhenOptionSet: without the build tag, a config
// that sets lx_idle_suspend must fail loudly at start, not silently no-op.
func TestStartIdleSuspend_stubErrorsWhenOptionSet(t *testing.T) {
	r := &Router{idleSuspend: 30 * time.Second}
	if err := r.startIdleSuspend(); err == nil {
		t.Fatal("expected an error when lx_idle_suspend is set in a build without with_lx_idle_suspend")
	}
}

// TestStartIdleSuspend_stubErrorsOnExplicitTeardownZero: an explicit
// lx_idle_teardown of "0" (the teardown kill switch) resolves to a zero window,
// so the stub must key off "the option was present", not off its magnitude —
// otherwise a config carrying the switch would silently no-op in a build without
// the tag, which is exactly the silence this stub exists to prevent.
func TestStartIdleSuspend_stubErrorsOnExplicitTeardownZero(t *testing.T) {
	r := &Router{idleTeardown: 0, idleTeardownSet: true}
	if err := r.startIdleSuspend(); err == nil {
		t.Fatal(`expected an error when an explicit lx_idle_teardown "0" is set in a build without with_lx_idle_suspend`)
	}
}

// TestStartIdleSuspend_stubNoopWhenUnset: without the option, the stub is a clean
// no-op (no error) — desktop builds with no lx_idle_suspend are unaffected.
func TestStartIdleSuspend_stubNoopWhenUnset(t *testing.T) {
	r := &Router{} // idleSuspend == 0
	if err := r.startIdleSuspend(); err != nil {
		t.Fatalf("stub must be a no-op when the option is unset, got %v", err)
	}
	r.stopIdleSuspend() // must not panic
}

// lx:end idle-suspend
