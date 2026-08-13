//go:build with_lx_command && !linux

package lxd

// currentRSS is unsupported outside linux: the honest darwin implementation
// needs task_info(MACH_TASK_BASIC_INFO) through cgo, and the lxd package is
// pure Go by construction. Servers are linux; darwin is a dev host, where
// `inuse_bytes` and `rss_peak_bytes` answer the same questions well enough
// (SPEC 065).
func currentRSS() int64 { return rssUnsupported }
