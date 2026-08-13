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
