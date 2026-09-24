//go:build with_lxd

package lxd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testRotator builds a rotator with the redirect stubbed out (the real one
// would hijack the test process's stdout) and an injectable clock.
func testRotator(t *testing.T, maxSizeMB, maxBackups, maxAgeHours int) (*logRotator, *time.Time) {
	t.Helper()
	current := time.Now()
	rotator := newLogRotator(filepath.Join(t.TempDir(), "lxd.log"), maxSizeMB, maxBackups, maxAgeHours)
	rotator.redirect = func(*os.File) error { return nil }
	rotator.now = func() time.Time { return current }
	rotator.releaseHeld = true
	return rotator, &current
}

func mustWrite(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if exists := err == nil; exists != want {
		t.Fatalf("%s: exists=%v, want %v", path, exists, want)
	}
}

func TestLogRotateBySize(t *testing.T) {
	rotator, _ := testRotator(t, 1, 1, 24)
	if err := rotator.Start(); err != nil {
		t.Fatal(err)
	}
	defer rotator.Stop()

	mustWrite(t, rotator.path, 1<<20) // ровно потолок
	rotator.checkOnce()

	requireExists(t, rotator.path+".1", true)
	info, err := os.Stat(rotator.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("current log not fresh after rotation: %d bytes", info.Size())
	}
}

func TestLogRotateByAge(t *testing.T) {
	rotator, clock := testRotator(t, 20, 1, 24)
	if err := rotator.Start(); err != nil {
		t.Fatal(err)
	}
	defer rotator.Stop()

	mustWrite(t, rotator.path, 10)
	rotator.checkOnce()
	requireExists(t, rotator.path+".1", false) // молодой и маленький — не трогаем

	*clock = clock.Add(25 * time.Hour)
	rotator.checkOnce()
	requireExists(t, rotator.path+".1", true)
}

func TestLogBackupsPruned(t *testing.T) {
	rotator, clock := testRotator(t, 20, 2, 24)
	if err := rotator.Start(); err != nil {
		t.Fatal(err)
	}
	defer rotator.Stop()

	for round := 0; round < 3; round++ {
		mustWrite(t, rotator.path, 10)
		*clock = clock.Add(25 * time.Hour)
		rotator.checkOnce()
	}
	requireExists(t, rotator.path+".1", true)
	requireExists(t, rotator.path+".2", true)
	requireExists(t, rotator.path+".3", false) // за пределом maxBackups — удалён
}

func TestStaleLogRotatedOnStart(t *testing.T) {
	rotator, clock := testRotator(t, 20, 1, 24)
	// Файл прошлой жизни: последняя запись позавчера.
	mustWrite(t, rotator.path, 10)
	stale := clock.Add(-48 * time.Hour)
	if err := os.Chtimes(rotator.path, stale, stale); err != nil {
		t.Fatal(err)
	}

	if err := rotator.Start(); err != nil {
		t.Fatal(err)
	}
	defer rotator.Stop()
	requireExists(t, rotator.path+".1", true)
}

func TestMissingLogRecreated(t *testing.T) {
	if logRotateByCopy {
		t.Skip("the held log of the copy strategy cannot be deleted from under the daemon")
	}
	rotator, _ := testRotator(t, 20, 1, 24)
	if err := rotator.Start(); err != nil {
		t.Fatal(err)
	}
	defer rotator.Stop()

	if err := os.Remove(rotator.path); err != nil {
		t.Fatal(err)
	}
	rotator.checkOnce()
	requireExists(t, rotator.path, true)
	requireExists(t, rotator.path+".1", false) // пересоздание — не ротация
}

func TestLogRotatorDefaults(t *testing.T) {
	rotator := newLogRotator("x", 0, 0, 0)
	if rotator.maxSize != defaultLogMaxSizeMB<<20 {
		t.Fatalf("maxSize default: %d", rotator.maxSize)
	}
	if rotator.maxBackups != defaultLogMaxBackups {
		t.Fatalf("maxBackups default: %d", rotator.maxBackups)
	}
	if rotator.maxAge != defaultLogMaxAgeHours*time.Hour {
		t.Fatalf("maxAge default: %v", rotator.maxAge)
	}
}

// TestLogRotateByCopy: the Windows strategy (SPEC 103 §2.12) keeps one
// descriptor and rotates by copying the content to .1 and truncating the
// live file; the descriptor stays the same.
func TestLogRotateByCopy(t *testing.T) {
	rotator, clock := testRotator(t, 1, 2, 24)
	rotator.copyTruncate = true
	if err := rotator.Start(); err != nil {
		t.Fatal(err)
	}
	defer rotator.Stop()
	held := rotator.held
	if held == nil {
		t.Fatal("the copy strategy must hold its descriptor")
	}
	if _, err := held.WriteString("first life\n"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(25 * time.Hour)
	rotator.checkOnce()
	if backup, _ := os.ReadFile(rotator.path + ".1"); string(backup) != "first life\n" {
		t.Fatalf(".1 must hold the rotated content, got %q", backup)
	}
	if info, err := os.Stat(rotator.path); err != nil || info.Size() != 0 {
		t.Fatalf("the live file must be truncated: %v %v", info, err)
	}
	// The writer appends: the next line starts the fresh file.
	if _, err := held.WriteString("second life\n"); err != nil {
		t.Fatal(err)
	}
	if current, _ := os.ReadFile(rotator.path); string(current) != "second life\n" {
		t.Fatalf("the held descriptor must keep writing into the live file, got %q", current)
	}
	if rotator.held != held {
		t.Fatal("rotation must not reopen the descriptor")
	}
	// Size-based, and the backups shift.
	mustWriteAppend(t, held, 1<<20)
	rotator.checkOnce()
	requireExists(t, rotator.path+".2", true)
	if backup, _ := os.ReadFile(rotator.path + ".2"); string(backup) != "first life\n" {
		t.Fatalf(".2 must hold the older backup, got %q", backup)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(rotator.path), ".*rotate-*")); len(leftovers) > 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}
}

func mustWriteAppend(t *testing.T, file *os.File, size int) {
	t.Helper()
	if _, err := file.Write(make([]byte, size)); err != nil {
		t.Fatal(err)
	}
}
