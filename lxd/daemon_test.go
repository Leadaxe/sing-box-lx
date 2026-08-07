//go:build with_lx_command

package lxd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunMissingConfig(t *testing.T) {
	err := Run(context.Background(), Options{
		ConfigPath: filepath.Join(t.TempDir(), "absent.json"),
		Listen:     "127.0.0.1:0",
	})
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestRunBadListen(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), Options{
		ConfigPath: configPath,
		Listen:     "256.0.0.1:99999",
	})
	if err == nil {
		t.Fatal("expected error for bad listen address")
	}
}
