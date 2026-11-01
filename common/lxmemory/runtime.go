// Package lxmemory reads the memory figure the Go runtime holds from the OS.
//
// lx: SPEC 148 — the core embedded into another process (LxBox on Android)
// shares its RSS with code it does not own; the runtime figure is the part
// debug.SetMemoryLimit governs.
package lxmemory

import "runtime/metrics"

const (
	metricTotal    = "/memory/classes/total:bytes"
	metricReleased = "/memory/classes/heap/released:bytes"
)

// Runtime returns the memory mapped by the Go runtime minus the heap memory
// already returned to the OS: heap including idle spans, goroutine stacks and
// runtime metadata. It does not stop the world. Zero if the metrics are not
// supported by the runtime.
func Runtime() uint64 {
	samples := [2]metrics.Sample{
		{Name: metricTotal},
		{Name: metricReleased},
	}
	metrics.Read(samples[:])
	if samples[0].Value.Kind() != metrics.KindUint64 || samples[1].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	total := samples[0].Value.Uint64()
	released := samples[1].Value.Uint64()
	if released >= total {
		return 0
	}
	return total - released
}
