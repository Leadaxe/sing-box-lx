//go:build with_lx_command

package lxd

import (
	"net/http"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strconv"
	"sync/atomic"
	"time"
)

// The pprof plane serves the daemon's own profiles over the admin listener.
//
// Why not net/http/pprof: that package answers with an HTML index and serves
// /debug/pprof/* reflectively. Our consumer is the launcher, so profile names
// are an explicit whitelist and an unknown one is a JSON 404 — never an index
// page. /symbol and /cmdline are deliberately absent: symbolization happens on
// the developer's machine against a binary with DWARF, and cmdline would leak
// the daemon's full command line for nothing.
//
// Why no separate debug listener: box's experimental.debug.listen opens a
// second, entirely unauthenticated port. These routes ride the existing mTLS
// listener behind the same client-certificate pin as the rest of the admin
// plane (SPEC 065). That pin is a fair trade — the same certificate already
// authorizes POST /admin/apply, i.e. running arbitrary config in the daemon.

// snapshotProfiles are served straight from the runtime's continuously
// maintained tables: a GET serializes what is already there and returns in
// milliseconds. Nothing has to be started in advance — heap sampling
// (MemProfileRate, one sample per 512 KiB) runs from process start.
//
// block and mutex are the exception: they are shaped like snapshots but stay
// empty until a rate is set, because their accounting timestamps every
// synchronization operation. Hence the POST routes below.
var snapshotProfiles = []string{"heap", "allocs", "goroutine", "threadcreate", "block", "mutex"}

const (
	defaultProfileSeconds = 30
	// maxProfileSeconds is bounded by our own infrastructure, not by CPU cost:
	// the admin server runs with IdleTimeout = 120s, so a longer recording
	// would die on that timeout instead of returning. A 400 says so plainly.
	maxProfileSeconds = 120
)

// recordingProfile guards the two interval recorders. runtime.StartCPUProfile
// fails outright while a profile is running, so a second caller is refused
// immediately with 409 rather than queued: waiting 30 seconds behind a mutex
// is worse than a clear answer.
type recordingProfile struct {
	running atomic.Bool
}

func (r *recordingProfile) tryAcquire() bool { return r.running.CompareAndSwap(false, true) }
func (r *recordingProfile) release()         { r.running.Store(false) }

// profilePlane owns the mutable pprof state: whether a recording is in flight,
// and the block/mutex rates the operator turned on. Rates deliberately live in
// memory only — they never reach daemon.json and never survive a restart.
type profilePlane struct {
	cpu   recordingProfile
	trace recordingProfile

	blockRate     atomic.Int64
	mutexFraction atomic.Int64
}

func (c *controller) pprofHandler(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/pprof", c.handleProfileList)
	mux.HandleFunc("GET /admin/pprof/{name}", c.handleProfile)
	mux.HandleFunc("POST /admin/pprof/block", c.handleBlockRate)
	mux.HandleFunc("POST /admin/pprof/mutex", c.handleMutexFraction)
}

type profileEntry struct {
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Enabled bool   `json:"enabled"`
	Hint    string `json:"hint,omitempty"`
}

// handleProfileList answers "which profiles are worth fetching, and why is
// this one empty" in one request, so the client never has to consult docs to
// learn that block/mutex need enabling first.
func (c *controller) handleProfileList(writer http.ResponseWriter, request *http.Request) {
	entries := make([]profileEntry, 0, len(snapshotProfiles))
	for _, name := range snapshotProfiles {
		entry := profileEntry{Name: name, Enabled: true}
		if profile := pprof.Lookup(name); profile != nil {
			entry.Count = profile.Count()
		}
		switch name {
		case "block":
			if c.profiles.blockRate.Load() == 0 {
				entry.Enabled = false
				entry.Hint = "POST /admin/pprof/block?rate=10000"
			}
		case "mutex":
			if c.profiles.mutexFraction.Load() == 0 {
				entry.Enabled = false
				entry.Hint = "POST /admin/pprof/mutex?fraction=100"
			}
		}
		entries = append(entries, entry)
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"profiles":            entries,
		"cpu_profile_running": c.profiles.cpu.running.Load(),
	})
}

func (c *controller) handleProfile(writer http.ResponseWriter, request *http.Request) {
	name := request.PathValue("name")
	switch name {
	case "profile":
		c.serveCPUProfile(writer, request)
		return
	case "trace":
		c.serveTrace(writer, request)
		return
	}
	profile := lookupSnapshot(name)
	if profile == nil {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "no such profile"})
		return
	}
	debug := parseDebug(request)
	if debug > 0 {
		// debug=1 is a readable table; debug=2 on `goroutine` is the full
		// stack dump — the single most useful view when the daemon is wedged.
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		setProfileDownloadHeaders(writer, name)
	}
	_ = profile.WriteTo(writer, debug)
}

// lookupSnapshot resolves a name against the whitelist before touching pprof,
// so an arbitrary registered profile cannot be pulled by guessing its name.
func lookupSnapshot(name string) *pprof.Profile {
	for _, allowed := range snapshotProfiles {
		if allowed == name {
			return pprof.Lookup(name)
		}
	}
	return nil
}

func parseDebug(request *http.Request) int {
	value, err := strconv.Atoi(request.URL.Query().Get("debug"))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func setProfileDownloadHeaders(writer http.ResponseWriter, name string) {
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Disposition",
		`attachment; filename="`+name+"-"+time.Now().UTC().Format("20060102T150405Z")+`.pb.gz"`)
}

// parseSeconds bounds the recording window. Out-of-range is a 400 rather than
// a silent clamp: a caller asking for 10 minutes should learn it cannot have
// them, not receive two silently.
func parseSeconds(request *http.Request) (time.Duration, error) {
	raw := request.URL.Query().Get("seconds")
	if raw == "" {
		return defaultProfileSeconds * time.Second, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maxProfileSeconds {
		return 0, errBadSeconds
	}
	return time.Duration(value) * time.Second, nil
}

var errBadSeconds = &secondsError{}

type secondsError struct{}

func (e *secondsError) Error() string {
	return "seconds must be between 1 and " + strconv.Itoa(maxProfileSeconds)
}

// serveCPUProfile records for the requested window and streams the result.
//
// NOTE: this holds the response open for `seconds`. It works because the admin
// http.Server sets no WriteTimeout — do not add one without giving these two
// routes an exemption, or long profiles will be cut off mid-write with no
// error the client can interpret.
func (c *controller) serveCPUProfile(writer http.ResponseWriter, request *http.Request) {
	duration, err := parseSeconds(request)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if !c.profiles.cpu.tryAcquire() {
		writeJSON(writer, http.StatusConflict, map[string]any{"error": "CPU profile already running"})
		return
	}
	defer c.profiles.cpu.release()
	setProfileDownloadHeaders(writer, "cpu")
	if err = pprof.StartCPUProfile(writer); err != nil {
		// The header is already written, so this can only be reported as a
		// truncated body — but the guard above makes it near-unreachable.
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	sleepOrAbort(request, duration)
	pprof.StopCPUProfile()
}

func (c *controller) serveTrace(writer http.ResponseWriter, request *http.Request) {
	duration, err := parseSeconds(request)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if !c.profiles.trace.tryAcquire() {
		writeJSON(writer, http.StatusConflict, map[string]any{"error": "trace already running"})
		return
	}
	defer c.profiles.trace.release()
	setProfileDownloadHeaders(writer, "trace")
	if err = trace.Start(writer); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	sleepOrAbort(request, duration)
	trace.Stop()
}

// sleepOrAbort waits out the recording window but gives up early if the client
// disconnects — otherwise an aborted request would keep the profiler engaged
// (and the 409 guard held) for the full window with nobody to receive it.
func sleepOrAbort(request *http.Request, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-request.Context().Done():
	}
}

// handleBlockRate turns block profiling on or off. POST, not GET: it changes
// the process's accounting behaviour, and that cost is paid on every blocking
// operation until it is turned back off.
//
// The rate is in NANOSECONDS — sample a blocking event lasting that long on
// average. 1 records everything (very expensive), 10000 is the usual working
// choice, 0 disables.
func (c *controller) handleBlockRate(writer http.ResponseWriter, request *http.Request) {
	rate, err := parseNonNegative(request, "rate")
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	runtime.SetBlockProfileRate(int(rate))
	c.profiles.blockRate.Store(rate)
	writeJSON(writer, http.StatusOK, map[string]any{"profile": "block", "rate": rate, "enabled": rate > 0})
}

// handleMutexFraction turns mutex profiling on or off. The parameter is a
// FRACTION — record roughly 1/n contention events — not a duration like
// block's rate. Note that 0 stops recording but does NOT clear what was
// already collected, so a profile fetched after disabling is still readable.
func (c *controller) handleMutexFraction(writer http.ResponseWriter, request *http.Request) {
	fraction, err := parseNonNegative(request, "fraction")
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	runtime.SetMutexProfileFraction(int(fraction))
	c.profiles.mutexFraction.Store(fraction)
	writeJSON(writer, http.StatusOK, map[string]any{"profile": "mutex", "fraction": fraction, "enabled": fraction > 0})
}

func parseNonNegative(request *http.Request, key string) (int64, error) {
	raw := request.URL.Query().Get(key)
	if raw == "" {
		return 0, &missingParam{key}
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value < 0 {
		return 0, &missingParam{key}
	}
	return value, nil
}

type missingParam struct{ key string }

func (e *missingParam) Error() string { return e.key + " must be a non-negative integer" }
