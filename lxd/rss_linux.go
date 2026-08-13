//go:build with_lxd && linux

package lxd

import (
	"os"
	"strconv"
	"strings"
)

// currentRSS reads the process's resident set size from /proc/self/statm —
// the second field, in pages. Plain file read, no cgo: the lxd package stays
// pure Go (SPEC 065).
//
// This is the CURRENT resident size, deliberately distinct from ru_maxrss
// (peakRSS below), which is a high-water mark and never goes down — useless
// for the leak-detection graph this endpoint exists to feed.
func currentRSS() int64 {
	content, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return rssUnsupported
	}
	fields := strings.Fields(string(content))
	if len(fields) < 2 {
		return rssUnsupported
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return rssUnsupported
	}
	return pages * int64(os.Getpagesize())
}
