//go:build with_lxd

package lxd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDaemonConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Absent file: not an error, found=false.
	if _, found, err := LoadDaemonConfig(dir); err != nil || found {
		t.Fatalf("absent daemon.json: found=%v err=%v, want false nil", found, err)
	}

	saved := DaemonConfig{Listen: ListenAddress("127.0.0.1:19091"), TLS: true, Secret: "s3cret"}
	if err := SaveDaemonConfig(dir, saved); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := LoadDaemonConfig(dir)
	if err != nil || !found {
		t.Fatalf("load after save: found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(loaded, saved) {
		t.Fatalf("round-trip mismatch: %+v != %+v", loaded, saved)
	}

	// The file holds the secret — must be 0600.
	info, err := os.Stat(daemonConfigPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("daemon.json permissions = %o, want 600", perm)
	}

	// A present but corrupt file is a loud error, not a silent fallback:
	// booting with different settings than configured would be worse.
	if err = os.WriteFile(daemonConfigPath(dir), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = LoadDaemonConfig(dir); err == nil || !strings.Contains(err.Error(), "daemon.json") {
		t.Fatalf("corrupt daemon.json must error mentioning the file, got %v", err)
	}
}

func TestSaveDaemonConfigCreatesStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	if err := SaveDaemonConfig(dir, DaemonConfig{Listen: ListenAddress("127.0.0.1:1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(daemonConfigPath(dir)); err != nil {
		t.Fatal("daemon.json not created in a fresh state dir:", err)
	}
}

func TestGenerateSecretShape(t *testing.T) {
	first, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || first == second {
		t.Fatalf("secrets must be 64 hex chars and unique: %q %q", first, second)
	}
}
