package oomkiller

import (
	"github.com/sagernet/sing-box/common/lxmemory"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/byteformats"
	"github.com/sagernet/sing/common/memory"
)

// lx: SPEC 148 — runtime_limit: the memory limit is compared with the memory
// the Go runtime holds from the OS (lxmemory.Runtime), not with the process
// RSS, for a core embedded into another process (LxBox on Android: Go, Flutter
// and Dalvik share one PID). Selected only by the internal MemoryLimitHeap flag
// (SetupOptions.OomMemoryLimitHeap). Unlike memory_limit, the watch on the
// system available memory stays on: the state follows either condition.
const policyModeRuntimeLimit policyMode = 100

var readRuntimeMemory = lxmemory.Runtime

func resolveRuntimeLimitPolicy(options option.OOMKillerServiceOptions) (uint64, bool) {
	if !options.MemoryLimitHeap {
		return 0, false
	}
	if options.MemoryLimitOverride > 0 {
		return options.MemoryLimitOverride, true
	}
	if options.MemoryLimit != nil {
		memoryLimit := options.MemoryLimit.Value()
		if memoryLimit > 0 {
			return memoryLimit, true
		}
	}
	return 0, false
}

func readMemorySampleRuntimeLimit() memorySample {
	sample := memorySample{
		usage: readRuntimeMemory(),
	}
	// os_proc_available_memory reports 0 for a darwin process outside a jetsam
	// limit; a zero reading means no figure, not no memory.
	if memory.AvailableAvailable() {
		sample.available = memory.Available()
		sample.availableKnown = sample.available > 0
	}
	return sample
}

// availableThresholdsRuntimeLimit mirrors availableThresholds without the
// memory-limit default margin: an explicit safety_margin applies to both
// conditions, otherwise the available-mode default derived from usage.
func (t *adaptiveTimer) availableThresholdsRuntimeLimit(sample memorySample) pressureThresholds {
	var triggerMargin uint64
	if t.explicitSafetyMargin {
		triggerMargin = t.safetyMargin
	} else if sample.usage == 0 {
		triggerMargin = defaultAvailableTriggerMarginMin
	} else {
		triggerMargin = max(defaultAvailableTriggerMarginMin, min(sample.usage/4, defaultAvailableTriggerMarginMax))
	}
	return pressureThresholds{
		trigger: triggerMargin,
		armed:   triggerMargin * 2,
	}
}

func (t *adaptiveTimer) nextStateRuntimeLimit(sample memorySample) pressureState {
	shouldTrigger := sample.usage >= t.limitThresholds.trigger
	shouldArm := sample.usage >= t.limitThresholds.armed
	if sample.availableKnown {
		thresholds := t.availableThresholdsRuntimeLimit(sample)
		shouldTrigger = shouldTrigger || sample.available <= thresholds.trigger
		shouldArm = shouldArm || sample.available <= thresholds.armed
	}
	return nextPressureState(t.state, shouldTrigger, shouldArm)
}

func (t *adaptiveTimer) belowTriggerRuntimeLimit(sample memorySample) bool {
	if sample.usage >= t.limitThresholds.trigger {
		return false
	}
	return !sample.availableKnown || sample.available > t.availableThresholdsRuntimeLimit(sample).trigger
}

func (t *adaptiveTimer) logDetailsRuntimeLimit(sample memorySample) string {
	headroom := uint64(0)
	if sample.usage < t.memoryLimit {
		headroom = t.memoryLimit - sample.usage
	}
	details := ", runtime limit: " + byteformats.FormatMemoryBytes(t.memoryLimit) + ", headroom: " + byteformats.FormatMemoryBytes(headroom)
	if sample.availableKnown {
		details += ", available: " + byteformats.FormatMemoryBytes(sample.available)
	}
	return details
}
