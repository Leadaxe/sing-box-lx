//go:build with_lxd

package lxd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreLastGoodRoundtrip(t *testing.T) {
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, loaded, _ := stateStore.LoadLastGood(); loaded {
		t.Fatal("empty store must not report last-good")
	}
	if err = stateStore.SaveLastGood(`{"v": 1}`); err != nil {
		t.Fatal(err)
	}
	content, loaded, _ := stateStore.LoadLastGood()
	if !loaded || content != `{"v": 1}` {
		t.Fatal("roundtrip mismatch")
	}
	entries, err := os.ReadDir(stateStore.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatal("temp file leaked:", entry.Name())
		}
	}
}

func TestStorePendingLifecycle(t *testing.T) {
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := stateStore.PendingSHA(); pending {
		t.Fatal("fresh store must not be pending")
	}
	if err = stateStore.SetPending("abc123"); err != nil {
		t.Fatal(err)
	}
	sha, pending := stateStore.PendingSHA()
	if !pending || sha != "abc123" {
		t.Fatal("pending marker mismatch")
	}
	stateStore.ClearPending()
	if _, pending = stateStore.PendingSHA(); pending {
		t.Fatal("pending marker must be removable")
	}
}

func TestReadOptionalDistinguishesErrorFromAbsence(t *testing.T) {
	dir := t.TempDir()

	// Absent file is the normal "no value" answer, not an error.
	content, exists, err := readOptional(filepath.Join(dir, "absent"))
	if content != "" || exists || err != nil {
		t.Fatal("absent file must read as (\"\", false, nil), got:", content, exists, err)
	}

	// An unreadable file is a real error and must not masquerade as absence
	// (spec 057: a ReadFile failure is not "no file").
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	locked := filepath.Join(dir, "locked")
	if err = os.WriteFile(locked, []byte("secret"), 0o000); err != nil {
		t.Fatal(err)
	}
	content, exists, err = readOptional(locked)
	if err == nil {
		t.Fatal("permission failure must surface as an error, not absence")
	}
	if content != "" || exists {
		t.Fatal("failed read must not report content or existence")
	}
}

func TestWasRunningTristate(t *testing.T) {
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Fresh state: no intent was ever recorded.
	if running, recorded := stateStore.WasRunning(); running || recorded {
		t.Fatal("fresh store must report (false, false)")
	}
	if err = stateStore.SetWasRunning(true); err != nil {
		t.Fatal(err)
	}
	if running, recorded := stateStore.WasRunning(); !running || !recorded {
		t.Fatal("after SetWasRunning(true) expected (true, true)")
	}
	if err = stateStore.SetWasRunning(false); err != nil {
		t.Fatal(err)
	}
	if running, recorded := stateStore.WasRunning(); running || !recorded {
		t.Fatal("after SetWasRunning(false) expected (false, true)")
	}
	// "stopped" is written as "0", not expressed by deleting the file:
	// bootstrap must be able to tell an operator's stop from fresh state.
	raw, err := os.ReadFile(stateStore.runStatePath())
	if err != nil {
		t.Fatal("was_running file must exist after SetWasRunning(false):", err)
	}
	if strings.TrimSpace(string(raw)) != "0" {
		t.Fatal("expected \"0\" marker, got:", string(raw))
	}
}

func TestWriteAtomicOverwriteAndErrorCleanup(t *testing.T) {
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Overwrite: the atomic rename must replace previous content in place.
	if err = stateStore.SaveLastGood(`{"v": 1}`); err != nil {
		t.Fatal(err)
	}
	if err = stateStore.SaveLastGood(`{"v": 2}`); err != nil {
		t.Fatal(err)
	}
	content, loaded, _ := stateStore.LoadLastGood()
	if !loaded || content != `{"v": 2}` {
		t.Fatal("overwrite must yield the latest content, got:", content)
	}

	// Failed rename must clean up its temp file: a directory squatting on the
	// destination path makes the rename fail after the temp was written.
	if err = os.Mkdir(stateStore.candidatePath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = stateStore.StageCandidate("x"); err == nil {
		t.Fatal("rename onto a directory must fail")
	}
	entries, err := os.ReadDir(stateStore.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatal("temp file leaked after failed write:", entry.Name())
		}
	}

	// A vanished state directory is a hard error, not a silent success.
	gone, err := newStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(gone.dir); err != nil {
		t.Fatal(err)
	}
	if err = gone.SaveLastGood("x"); err == nil {
		t.Fatal("write into a removed state dir must fail")
	}
}
