//go:build !unix

package impair

import "time"

// cpuTime is unknown here (no getrusage): the benchmarks run without the
// CPU metrics.
func cpuTime() (time.Duration, bool) { return 0, false }
