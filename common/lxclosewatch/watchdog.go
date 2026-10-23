// Package lxclosewatch is the shutdown watchdog of the lx fork (SPEC 030 §8,
// issue #50): when a close pass runs past a threshold, it writes a full
// goroutine dump to the core log once and keeps waiting.
//
// It never interrupts the close it watches. SPEC 030's design — a teardown is
// never abandoned, so no receive worker is left holding a freed netstack —
// stays as is; the watchdog only makes a hung stop observable from the log,
// which is the one channel every client has (pprof may be off, and the
// platform may kill the process before anyone attaches a debugger).
package lxclosewatch

import (
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/logger"
)

// DefaultThreshold is how long a close pass may run before the watchdog
// dumps. It sits well above the per-component StopTimeout (5 s) that upstream's
// taskmonitor already reports, and below what a platform treats as a hung
// service (Android stops the VPN service at ~30 s), so the dump lands in the
// log before the process is killed.
const DefaultThreshold = 20 * time.Second

// chunkSize bounds one log entry of the dump. Android logcat truncates an
// entry at ~4 KiB; chunks are cut at line boundaries below that so no frame
// is lost.
const chunkSize = 3 * 1024

// dumpCooldown suppresses a second full dump shortly after the first one: the
// box-level and the endpoint-level watchdog both fire on the same hang, and
// the goroutine state has not changed in between. The header with the
// pending list is still logged.
const dumpCooldown = DefaultThreshold

var lastDump atomic.Int64 // unix nanoseconds of the last full dump, 0 = never

// Watchdog watches one close pass.
type Watchdog struct {
	logger    logger.Logger
	what      string
	threshold time.Duration
	pending   func() []string
	started   time.Time
	timer     *time.Timer
	access    sync.Mutex
	fired     bool
}

// Start arms a watchdog for the close pass named what. pending, when not nil,
// is asked at fire time for the names of the parts that have not returned
// yet (endpoint tags); it must be safe to call from the timer goroutine.
// A nil logger disarms the watchdog.
func Start(logger logger.Logger, what string, threshold time.Duration, pending func() []string) *Watchdog {
	w := &Watchdog{
		logger:    logger,
		what:      what,
		threshold: threshold,
		pending:   pending,
		started:   time.Now(),
	}
	if logger == nil || threshold <= 0 {
		return w
	}
	w.timer = time.AfterFunc(threshold, w.fire)
	return w
}

// Stop disarms the watchdog. Called when the close pass returns; if the
// watchdog had fired, the completion is logged too, so the log shows the
// hang resolved on its own rather than the process being killed.
func (w *Watchdog) Stop() {
	if w.timer == nil {
		return
	}
	w.timer.Stop()
	w.access.Lock()
	fired := w.fired
	w.access.Unlock()
	if fired {
		w.logger.Warn(w.what, ": completed after ", F.Seconds(time.Since(w.started).Seconds()), "s (watchdog had fired)")
	}
}

func (w *Watchdog) fire() {
	w.access.Lock()
	w.fired = true
	w.access.Unlock()
	var header strings.Builder
	header.WriteString(w.what)
	header.WriteString(": still running after ")
	header.WriteString(F.Seconds(w.threshold.Seconds()))
	header.WriteString("s")
	if w.pending != nil {
		pending := w.pending()
		if len(pending) == 0 {
			// Everything returned between the timer firing and the pass
			// completing; nothing is stuck, so nothing to dump.
			return
		}
		header.WriteString("; not returned yet: [")
		header.WriteString(strings.Join(pending, ", "))
		header.WriteString("]")
	}
	header.WriteString("; goroutines=")
	header.WriteString(F.ToString(runtime.NumGoroutine()))
	now := time.Now()
	last := lastDump.Load()
	if last != 0 && now.Sub(time.Unix(0, last)) < dumpCooldown {
		w.logger.Warn(header.String(), "; goroutine dump skipped (dumped ", F.Seconds(now.Sub(time.Unix(0, last)).Seconds()), "s ago)")
		return
	}
	lastDump.Store(now.UnixNano())
	chunks := Chunks(DumpGoroutines(), chunkSize)
	w.logger.Warn(header.String(), "; goroutine dump follows in ", len(chunks), " parts")
	for i, chunk := range chunks {
		w.logger.Warn("goroutine dump [", i+1, "/", len(chunks), "]\n", chunk)
	}
	w.logger.Warn("goroutine dump end")
}

// DumpGoroutines returns the stacks of every goroutine, as runtime.Stack
// formats them. The buffer grows until the dump fits, up to 64 MiB.
func DumpGoroutines() string {
	size := 1 << 20
	for {
		buf := make([]byte, size)
		n := runtime.Stack(buf, true)
		if n < len(buf) || size >= 64<<20 {
			return string(buf[:n])
		}
		size *= 2
	}
}

// Chunks splits text into pieces of at most size bytes, cutting at line
// boundaries where a line fits; a single line longer than size is cut as is.
func Chunks(text string, size int) []string {
	var chunks []string
	for len(text) > 0 {
		if len(text) <= size {
			chunks = append(chunks, text)
			break
		}
		cut := strings.LastIndexByte(text[:size], '\n')
		if cut <= 0 {
			cut = size
		} else {
			cut++ // keep the newline with the chunk before it
		}
		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	return chunks
}
