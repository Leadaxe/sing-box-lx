//go:build with_lx_command

package lxd

import (
	"context"
	"testing"
)

func TestRunBadListen(t *testing.T) {
	err := Run(context.Background(), Options{
		Listen:   "256.0.0.1:99999",
		StateDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for bad listen address")
	}
}
