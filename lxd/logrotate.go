//go:build with_lxd

package lxd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// Rotation defaults: "about a day of history" — rotate by age every 24 hours,
// keep the current file plus one backup. The size ceiling is a safety net for
// a log gone wild in an error loop (a bounded file cannot eat the disk
// overnight, which is exactly what launchd's plain append redirect would do).
const (
	defaultLogMaxSizeMB    = 1
	defaultLogMaxBackups   = 1
	defaultLogMaxAgeHours  = 24
	logRotateCheckInterval = time.Minute
)

// DefaultLogPath returns the daemon's default log file for a given state dir:
// in the support directory beside it (…/sing-box-lxd/lxd.log next to
// …/sing-box-lxd/state). daemon.json's log_file overrides it; GET /admin/info
// advertises the ACTUAL path either way, so the launcher never hard-codes it.
func DefaultLogPath(stateDir string) string {
	abs := stateDir
	if a, err := filepath.Abs(stateDir); err == nil {
		abs = a
	}
	return filepath.Join(filepath.Dir(abs), "lxd.log")
}

// logRotator makes the daemon own its log file instead of launchd: it re-opens
// the file onto the process's stdout/stderr (dup2), so EVERYTHING the process
// emits — daemon log, core log, runtime panics — lands in one rotated file.
// The plist's StandardOutPath keeps pointing at the same path purely as the
// crash channel for output produced before this takes over (early flag/config
// errors); after the dup2 the launchd-opened descriptor is gone from the
// process.
type logRotator struct {
	path       string
	maxSize    int64
	maxBackups int
	maxAge     time.Duration

	// Test seams: the clock and the redirect mechanism.
	now      func() time.Time
	redirect func(*os.File) error

	openedAt time.Time
	stop     chan struct{}
	done     chan struct{}
}

// newLogRotator applies the defaults: zero or negative limits mean "default",
// there is deliberately no "unlimited" knob (an unbounded service log is the
// exact failure mode this exists to prevent).
func newLogRotator(path string, maxSizeMB, maxBackups, maxAgeHours int) *logRotator {
	if maxSizeMB <= 0 {
		maxSizeMB = defaultLogMaxSizeMB
	}
	if maxBackups <= 0 {
		maxBackups = defaultLogMaxBackups
	}
	if maxAgeHours <= 0 {
		maxAgeHours = defaultLogMaxAgeHours
	}
	return &logRotator{
		path:       path,
		maxSize:    int64(maxSizeMB) << 20,
		maxBackups: maxBackups,
		maxAge:     time.Duration(maxAgeHours) * time.Hour,
		now:        time.Now,
		redirect:   redirectStdIO,
	}
}

// Start rotates a stale leftover, opens and redirects, then begins the
// background age/size watch.
func (r *logRotator) Start() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return E.Cause(err, "create log directory")
	}
	// A file last written longer than maxAge ago is a previous life's history:
	// back it up so the fresh tail starts clean. (mtime, not birth time —
	// portable, and for an idle file they agree.)
	if info, err := os.Stat(r.path); err == nil && info.Size() > 0 && r.now().Sub(info.ModTime()) >= r.maxAge {
		r.shiftBackups()
	}
	if err := r.openAndRedirect(); err != nil {
		return err
	}
	r.stop = make(chan struct{})
	r.done = make(chan struct{})
	go r.loop()
	return nil
}

func (r *logRotator) Stop() {
	close(r.stop)
	<-r.done
}

func (r *logRotator) loop() {
	defer close(r.done)
	ticker := time.NewTicker(logRotateCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.checkOnce()
		}
	}
}

// checkOnce rotates when the current file reached the size or age limit, and
// heals a log file deleted out from under the daemon (re-created on the next
// tick instead of writing into an unlinked inode forever).
func (r *logRotator) checkOnce() {
	info, err := os.Stat(r.path)
	if os.IsNotExist(err) {
		_ = r.openAndRedirect()
		return
	}
	rotateBySize := err == nil && info.Size() >= r.maxSize
	rotateByAge := r.now().Sub(r.openedAt) >= r.maxAge
	if !rotateBySize && !rotateByAge {
		return
	}
	r.shiftBackups()
	// A failed re-open is not reportable — the log IS the reporting channel.
	// Writers still hold the old descriptor (renames follow open files), so
	// output keeps landing in the .1 backup rather than vanishing.
	_ = r.openAndRedirect()
}

// openAndRedirect opens the log for append and points fd 1/2 at it. The local
// descriptor is closed afterwards: after dup2 the process's stdout/stderr ARE
// the file, a private copy would only leak.
func (r *logRotator) openAndRedirect() error {
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return E.Cause(err, "open log file")
	}
	err = r.redirect(file)
	_ = file.Close()
	if err != nil {
		return E.Cause(err, "redirect stdio to log file")
	}
	r.openedAt = r.now()
	return nil
}

// shiftBackups sends the current file to .1, shifting older backups up and
// dropping the one past maxBackups. Concurrent writers lose nothing: renames
// follow open descriptors, their lines land in the rotated backup.
func (r *logRotator) shiftBackups() {
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.maxBackups))
	for i := r.maxBackups - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	_ = os.Rename(r.path, r.path+".1")
}
