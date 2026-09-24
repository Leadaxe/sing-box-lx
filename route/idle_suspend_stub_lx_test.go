//go:build !with_lx_idle_suspend

// lx:begin idle-suspend
package route

import (
	"strings"
	"testing"
	"time"
)

// TestStartIdleSuspend_stubErrorsWhenOptionSet: without the build tag, a config
// that sets lx.wg.idle_suspend must fail loudly at start, not silently no-op.
func TestStartIdleSuspend_stubErrorsWhenOptionSet(t *testing.T) {
	r := &Router{idleSuspend: 30 * time.Second}
	if err := r.startIdleSuspend(); err == nil {
		t.Fatal("expected an error when lx.wg.idle_suspend is set in a build without with_lx_idle_suspend")
	}
}

// TestStartIdleSuspend_stubErrorsOnExplicitTeardownZero: an explicit
// lx.wg.idle_teardown of "0" (the teardown kill switch) resolves to a zero window,
// so the stub must key off "the option was present", not off its magnitude —
// otherwise a config carrying the switch would silently no-op in a build without
// the tag, which is exactly the silence this stub exists to prevent.
func TestStartIdleSuspend_stubErrorsOnExplicitTeardownZero(t *testing.T) {
	r := &Router{idleTeardown: 0, idleTeardownSet: true}
	if err := r.startIdleSuspend(); err == nil {
		t.Fatal(`expected an error when an explicit lx.wg.idle_teardown "0" is set in a build without with_lx_idle_suspend`)
	}
}

// TestStartIdleSuspend_stubNoopWhenUnset: without the option, the stub is a clean
// no-op (no error) — desktop builds with no lx.wg.* keys are unaffected.
// TestSelectionRefs_stub: without the tag there is no build budget to rank
// victims for (SPEC 097).
func TestSelectionRefs_stub(t *testing.T) {
	r := &Router{}
	if manual, auto := r.SelectionRefs("any"); manual != 0 || auto != 0 {
		t.Fatalf("stub SelectionRefs: (%d, %d), want (0, 0)", manual, auto)
	}
}

func TestStartIdleSuspend_stubNoopWhenUnset(t *testing.T) {
	r := &Router{} // idleSuspend == 0
	if err := r.startIdleSuspend(); err != nil {
		t.Fatalf("stub must be a no-op when the option is unset, got %v", err)
	}
	r.stopIdleSuspend() // must not panic
}

// lx: SPEC 098 — the gate covers every lx.wg.* key, read from the context the
// way box.New builds it, and names the new key path.
func TestStartIdleSuspend_stubRejectsLXWGKeys(t *testing.T) {
	for _, content := range []string{
		`{"lx":{"wg":{"idle_suspend":"30s"}}}`,
		`{"lx":{"wg":{"idle_suspend":"30s","idle_teardown":"0"}}}`,
		`{"route":{"lx_idle_suspend":"30s"}}`,
		`{"lx":{"wg":{"idle_suspend":"30s","lazy_build":true}}}`,
		`{"lx":{"wg":{"build_max":2}}}`,
		`{"lx":{"wg":{"build_overflow":"build"}}}`,
	} {
		err := routerFromConfig(t, content).startIdleSuspend()
		if err == nil {
			t.Fatalf("%s: expected the build-tag error", content)
		}
		if !strings.HasPrefix(err.Error(), "lx.wg.* is set but this build lacks idle-suspend support") ||
			!strings.Contains(err.Error(), "-tags with_lx_idle_suspend") {
			t.Fatalf("%s: unexpected text %q", content, err)
		}
	}
	for _, content := range []string{
		`{}`,
		`{"lx":{"wg":{"build_overflow":"wait"}}}`,
		`{"lx":{"masque":{"idle_timeout":"5m"}}}`,
	} {
		if err := routerFromConfig(t, content).startIdleSuspend(); err != nil {
			t.Fatalf("%s: nothing to gate, got %v", content, err)
		}
	}
}

// lx:end idle-suspend
