//go:build with_lx_command

package lxd

import (
	"os"
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
