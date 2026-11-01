package oomkiller

import "github.com/sagernet/sing/common/byteformats"

// lx: SPEC 148 §5 — start line naming the active policy mode and the limit
// thresholds, so a field log shows whether the limit is applied and resets
// are armed. Available-mode thresholds depend on each sample and are omitted.
func startLogDetails(config timerConfig, thresholds pressureThresholds) string {
	details := "mode: " + config.policyMode.String()
	switch config.policyMode {
	case policyModeMemoryLimit, policyModeNetworkExtension, policyModeRuntimeLimit:
		details += ", limit: " + byteformats.FormatMemoryBytes(config.memoryLimit) +
			", trigger: " + byteformats.FormatMemoryBytes(thresholds.trigger) +
			", armed: " + byteformats.FormatMemoryBytes(thresholds.armed)
	}
	if config.killerDisabled {
		details += ", report only"
	}
	return details
}
