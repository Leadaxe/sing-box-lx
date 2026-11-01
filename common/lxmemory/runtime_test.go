package lxmemory

import (
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
)

var sink []byte

func TestRuntimeNonZero(t *testing.T) {
	require.NotZero(t, Runtime())
}

func TestRuntimeCoversHeapInuse(t *testing.T) {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	require.GreaterOrEqual(t, Runtime(), memStats.HeapInuse+memStats.StackInuse)
}

func TestRuntimeGrowsWithLiveSlice(t *testing.T) {
	debug.FreeOSMemory()
	before := Runtime()
	const size = 64 << 20
	sink = make([]byte, size)
	for i := 0; i < len(sink); i += 4096 {
		sink[i] = 1
	}
	after := Runtime()
	runtime.KeepAlive(sink)
	require.GreaterOrEqual(t, after, before+48<<20, "before %d, after %d", before, after)
	sink = nil
}
