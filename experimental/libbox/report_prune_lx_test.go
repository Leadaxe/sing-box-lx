//go:build darwin || linux || windows

package libbox

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// writeReportDir creates a report directory holding one file of the given size, stamped at
// the given mtime.
func writeReportDir(t *testing.T, parent string, name string, size int, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(parent, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "go.log"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path
}

func reportNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// TestPruneReportsKeepsNewestByCount pins the count limit: with the archive already at the
// cap, pruning leaves room for the report about to be written (keepCount-1), and the
// survivors are the newest ones.
func TestPruneReportsKeepsNewestByCount(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Now().Add(-100 * time.Hour)
	for i := 0; i < 10; i++ {
		writeReportDir(t, dir, "report-"+strconv.Itoa(i), 16, base.Add(time.Duration(i)*time.Hour))
	}

	pruneReports(dir, 4, 1<<30)

	names := reportNames(t, dir)
	if len(names) != 3 {
		t.Fatalf("kept %d reports (%v), want 3 — one slot is reserved for the incoming report", len(names), names)
	}
	for _, name := range names {
		switch name {
		case "report-7", "report-8", "report-9":
		default:
			t.Errorf("kept %q; the three newest (report-7/8/9) were expected", name)
		}
	}
}

// TestPruneReportsHonoursByteBudget pins the byte limit independently of the count limit:
// the count is within cap, so only the size budget may trigger deletion.
func TestPruneReportsHonoursByteBudget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Now().Add(-10 * time.Hour)
	for i := 0; i < 4; i++ {
		writeReportDir(t, dir, "report-"+strconv.Itoa(i), 1000, base.Add(time.Duration(i)*time.Hour))
	}

	pruneReports(dir, 1000, 2500)

	names := reportNames(t, dir)
	if len(names) != 2 {
		t.Fatalf("kept %d reports (%v), want 2 within a 2500-byte budget", len(names), names)
	}
	for _, name := range names {
		if name == "report-0" || name == "report-1" {
			t.Errorf("kept %q; the oldest reports should have gone first", name)
		}
	}
}

// TestPruneReportsOrdersByModTimeNotName is the regression this ordering exists for:
// collision suffixes from nextAvailableReportPath ("…-05-2" vs "…-05-10") break
// lexicographic order, so a name sort would delete the wrong report. Here the
// lexicographically smallest name is the NEWEST report and must survive.
func TestPruneReportsOrdersByModTimeNotName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	newest := "2026-07-15T07-15-05-2"
	oldest := "2026-07-15T07-15-05-10"
	writeReportDir(t, dir, oldest, 16, time.Now().Add(-2*time.Hour))
	writeReportDir(t, dir, newest, 16, time.Now())

	pruneReports(dir, 2, 1<<30)

	names := reportNames(t, dir)
	if len(names) != 1 {
		t.Fatalf("kept %v, want exactly 1", names)
	}
	if names[0] != newest {
		t.Errorf("kept %q, want %q — ordering must follow mtime, not name", names[0], newest)
	}
}

// TestPruneReportsIgnoresLooseFiles guards against pruning eating non-report files that
// share the directory, and against counting them toward the archive.
func TestPruneReportsIgnoresLooseFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stray.log"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReportDir(t, dir, "report-0", 16, time.Now())

	pruneReports(dir, 2, 1<<30)

	if _, err := os.Stat(filepath.Join(dir, "stray.log")); err != nil {
		t.Errorf("loose file was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "report-0")); err != nil {
		t.Errorf("report within the cap was removed: %v", err)
	}
}

// TestPruneReportsMissingDirIsNoop pins the best-effort contract: pruning a directory that
// does not exist yet must not panic or create anything, since it runs before the first
// report is ever written.
func TestPruneReportsMissingDirIsNoop(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent")
	pruneReports(missing, 4, 1024)
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("pruning created the directory: %v", err)
	}
}
