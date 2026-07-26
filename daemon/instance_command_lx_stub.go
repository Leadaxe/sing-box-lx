//go:build !with_lx_command

package daemon

import "github.com/sagernet/sing-box/option"

// captureRunningConfig is a no-op without with_lx_command: the snapshot exists only to
// serve the GetRunningConfig RPC (SPEC 037), which is stubbed out in this build — no
// reason to hold a serialized config copy per instance. Keeps tag-less builds
// behaviourally (and memory-wise) equivalent to upstream.
func captureRunningConfig(options option.Options) string {
	return ""
}
