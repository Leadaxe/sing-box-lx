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
