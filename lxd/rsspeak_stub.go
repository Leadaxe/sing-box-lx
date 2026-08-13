//go:build with_lxd && !(linux || darwin)

package lxd

// peakRSS has no portable source outside unix; windows would need
// GetProcessMemoryInfo, which the lxd package does not carry (SPEC 065).
func peakRSS() int64 { return rssUnsupported }
