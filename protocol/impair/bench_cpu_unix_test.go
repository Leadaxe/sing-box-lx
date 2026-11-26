//go:build unix

package impair

import (
	"syscall"
	"time"
)

// cpuTime is the CPU time of the process so far (user + system).
func cpuTime() (time.Duration, bool) {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return 0, false
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano()), true
}
