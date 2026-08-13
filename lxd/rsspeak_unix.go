//go:build with_lx_command && (linux || darwin)

package lxd

import (
	"runtime"
	"syscall"
)

// peakRSS returns the high-water mark of the process's resident size, in
// bytes. It only ever grows — that is the point of the field, and exactly why
// it is reported separately from currentRSS (SPEC 065): a caller graphing
// ru_maxrss cannot tell a leak from a one-off spike.
//
// ru_maxrss units differ by platform: bytes on darwin, kilobytes elsewhere —
// the same normalization box.rusageMaxRSS does.
func peakRSS() int64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return rssUnsupported
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
		return int64(usage.Maxrss)
	}
	return int64(usage.Maxrss) << 10
}
