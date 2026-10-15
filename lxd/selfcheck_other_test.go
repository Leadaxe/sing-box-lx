//go:build with_lxd && !darwin && !windows

package lxd

import "testing"

// TestSelfCheckOffOutsideServicePlatforms: away from macOS and Windows the
// self-check neither refuses nor warns, root or not (SPEC 132, issue #40).
func TestSelfCheckOffOutsideServicePlatforms(t *testing.T) {
	for _, allowUnsafe := range []bool{false, true} {
		info, warning, err := evaluateSelfCheck(platformSelfCheckEnv(true), allowUnsafe)
		if info != "" || warning != "" || err != nil {
			t.Fatalf("allowUnsafe=%v: info %q, warning %q, error %v", allowUnsafe, info, warning, err)
		}
	}
}
