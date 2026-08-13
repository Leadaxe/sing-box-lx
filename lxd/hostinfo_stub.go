//go:build with_lxd && !linux && !darwin

package lxd

import "runtime"

// No host telemetry source on this platform (Windows today). The endpoints
// still answer with the same shape and null fields — the same principle as
// SPEC 066's providers: absence is a state, not a platform complaint, and a
// client needs exactly one branch for "no data".
//
// Note the Linux and darwin implementations also report nothing when a
// particular source is missing on that host, so this is not a special case.

func numCPU() int { return runtime.NumCPU() }

func readStaticInfo() staticInfo { return staticInfo{Arch: runtime.GOARCH} }

func readUptimeSeconds() int64 { return 0 }

func readLoadAverage() (*float64, *float64, *float64) { return nil, nil, nil }

func readCPUTicks() ([]cpuTicks, bool) { return nil, false }

func readMemory() memoryInfo { return memoryInfo{} }

func readThermal() *thermalInfo { return nil }

func readMounts() []mountInfo { return nil }

func markStateDirMount(_ []mountInfo, _ string) {}

func readFD() fdInfo { return fdInfo{} }

func readInterfaces() []rawInterface { return nil }
