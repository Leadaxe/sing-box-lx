package lxclosewatch

import (
	"strings"
	"sync"
	"testing"
	"time"

	F "github.com/sagernet/sing/common/format"
)

type recordingLogger struct {
	access sync.Mutex
	lines  []string
}

func (l *recordingLogger) record(args ...any) {
	l.access.Lock()
	l.lines = append(l.lines, F.ToString(args...))
	l.access.Unlock()
}

func (l *recordingLogger) snapshot() []string {
	l.access.Lock()
	defer l.access.Unlock()
	return append([]string(nil), l.lines...)
}

func (l *recordingLogger) Trace(args ...any) { l.record(args...) }
func (l *recordingLogger) Debug(args ...any) { l.record(args...) }
func (l *recordingLogger) Info(args ...any)  { l.record(args...) }
func (l *recordingLogger) Warn(args ...any)  { l.record(args...) }
func (l *recordingLogger) Error(args ...any) { l.record(args...) }
func (l *recordingLogger) Fatal(args ...any) { l.record(args...) }
func (l *recordingLogger) Panic(args ...any) { l.record(args...) }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for ", what)
}

func TestWatchdogDumpsOnceAndReportsCompletion(t *testing.T) {
	lastDump.Store(0)
	logger := &recordingLogger{}
	w := Start(logger, "close test", 50*time.Millisecond, func() []string { return []string{"stuck-a", "stuck-b"} })
	waitFor(t, "dump", func() bool {
		for _, line := range logger.snapshot() {
			if line == "goroutine dump end" {
				return true
			}
		}
		return false
	})
	w.Stop()
	lines := logger.snapshot()
	header := lines[0]
	if !strings.Contains(header, "close test: still running after 0.05s; not returned yet: [stuck-a, stuck-b]") {
		t.Fatalf("unexpected header: %q", header)
	}
	if !strings.Contains(header, "goroutine dump follows in ") {
		t.Fatalf("header must announce the dump: %q", header)
	}
	if end := lines[len(lines)-2]; end != "goroutine dump end" {
		t.Fatalf("dump must be closed by an end marker, got %q", end)
	}
	var sawSelf bool
	for _, line := range lines[1 : len(lines)-2] {
		if !strings.HasPrefix(line, "goroutine dump [") {
			t.Fatalf("unexpected dump line: %q", line)
		}
		if len(line) > chunkSize+64 {
			t.Fatalf("chunk too long for logcat: %d bytes", len(line))
		}
		if strings.Contains(line, "TestWatchdogDumpsOnceAndReportsCompletion") {
			sawSelf = true
		}
	}
	if !sawSelf {
		t.Fatal("dump must contain the test goroutine")
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "close test: completed after ") || !strings.Contains(last, "(watchdog had fired)") {
		t.Fatalf("Stop after fire must log completion, got %q", last)
	}
}

func TestWatchdogSkipsDumpWithinCooldown(t *testing.T) {
	lastDump.Store(time.Now().UnixNano())
	logger := &recordingLogger{}
	w := Start(logger, "close again", 20*time.Millisecond, nil)
	waitFor(t, "header", func() bool { return len(logger.snapshot()) > 0 })
	w.Stop()
	lines := logger.snapshot()
	if len(lines) != 2 || !strings.Contains(lines[0], "goroutine dump skipped") {
		t.Fatalf("expected header-only report within cooldown, got %q", lines)
	}
}

func TestWatchdogStopBeforeThresholdIsSilent(t *testing.T) {
	logger := &recordingLogger{}
	w := Start(logger, "quick close", time.Hour, nil)
	w.Stop()
	time.Sleep(20 * time.Millisecond)
	if lines := logger.snapshot(); len(lines) != 0 {
		t.Fatalf("no log expected, got %q", lines)
	}
}

func TestWatchdogNothingPendingNoDump(t *testing.T) {
	lastDump.Store(0)
	logger := &recordingLogger{}
	w := Start(logger, "racy close", 10*time.Millisecond, func() []string { return nil })
	time.Sleep(50 * time.Millisecond)
	w.Stop()
	lines := logger.snapshot()
	// fire() returned before dumping, but Stop still reports the completion
	// because the timer had fired.
	for _, line := range lines {
		if strings.HasPrefix(line, "goroutine dump") {
			t.Fatalf("no dump expected when nothing is pending, got %q", lines)
		}
	}
}

func TestChunksCutAtLineBoundaries(t *testing.T) {
	text := strings.Repeat("0123456789\n", 10)
	chunks := Chunks(text, 25)
	if strings.Join(chunks, "") != text {
		t.Fatal("chunks must concatenate back to the text")
	}
	for _, chunk := range chunks {
		if len(chunk) > 25 || !strings.HasSuffix(chunk, "\n") {
			t.Fatalf("bad chunk %q", chunk)
		}
	}
	long := Chunks(strings.Repeat("x", 30), 25)
	if len(long) != 2 || len(long[0]) != 25 {
		t.Fatalf("a long line is cut as is, got %q", long)
	}
}
