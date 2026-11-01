package oomkiller

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/byteformats"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/memory"

	"github.com/stretchr/testify/require"
)

const testRuntimeLimit = 200 << 20

func memoryBytes(t *testing.T, value uint64) *byteformats.MemoryBytes {
	t.Helper()
	var memoryBytes byteformats.MemoryBytes
	require.NoError(t, memoryBytes.UnmarshalJSON([]byte(strconv.FormatUint(value, 10))))
	require.Equal(t, value, memoryBytes.Value())
	return &memoryBytes
}

func newRuntimeLimitTestTimer(t *testing.T, options option.OOMKillerServiceOptions) *adaptiveTimer {
	t.Helper()
	config, err := buildTimerConfig(options, testRuntimeLimit, policyModeRuntimeLimit, true)
	require.NoError(t, err)
	return newAdaptiveTimer(log.NewNOPFactory().Logger(), nil, nil, nil, config)
}

func TestRuntimeLimitPolicyResolve(t *testing.T) {
	ctx := context.Background()

	limit, mode := resolvePolicyMode(ctx, option.OOMKillerServiceOptions{MemoryLimitHeap: true, MemoryLimitOverride: testRuntimeLimit})
	require.Equal(t, uint64(testRuntimeLimit), limit)
	require.Equal(t, policyModeRuntimeLimit, mode)
	require.Equal(t, "runtime_limit", mode.String())

	limit, mode = resolvePolicyMode(ctx, option.OOMKillerServiceOptions{MemoryLimitHeap: true, MemoryLimit: memoryBytes(t, testRuntimeLimit)})
	require.Equal(t, uint64(testRuntimeLimit), limit)
	require.Equal(t, policyModeRuntimeLimit, mode)

	upstreamLimit, upstreamMode := resolvePolicyMode(ctx, option.OOMKillerServiceOptions{})
	limit, mode = resolvePolicyMode(ctx, option.OOMKillerServiceOptions{MemoryLimitHeap: true})
	require.Equal(t, upstreamLimit, limit)
	require.Equal(t, upstreamMode, mode)
	require.NotEqual(t, policyModeRuntimeLimit, mode)

	limit, mode = resolvePolicyMode(ctx, option.OOMKillerServiceOptions{MemoryLimitOverride: testRuntimeLimit})
	require.Equal(t, uint64(testRuntimeLimit), limit)
	require.Equal(t, policyModeMemoryLimit, mode)
}

func TestRuntimeLimitSample(t *testing.T) {
	previous := readRuntimeMemory
	readRuntimeMemory = func() uint64 { return 42 << 20 }
	defer func() { readRuntimeMemory = previous }()

	sample := readMemorySample(policyModeRuntimeLimit)
	require.Equal(t, uint64(42<<20), sample.usage)
	if sample.availableKnown {
		require.True(t, memory.AvailableAvailable())
		require.NotZero(t, sample.available)
	} else {
		require.Zero(t, sample.available)
	}
}

func TestMemoryLimitSampleStaysRSS(t *testing.T) {
	if !memory.TotalAvailable() {
		t.Skip("process memory is not available on this platform")
	}
	previous := readRuntimeMemory
	readRuntimeMemory = func() uint64 { return 1 }
	defer func() { readRuntimeMemory = previous }()

	sample := readMemorySample(policyModeMemoryLimit)
	require.Greater(t, sample.usage, uint64(1))
	require.False(t, sample.availableKnown)
}

func TestRuntimeLimitThresholds(t *testing.T) {
	timer := newRuntimeLimitTestTimer(t, option.OOMKillerServiceOptions{})
	require.Equal(t, uint64(195<<20), timer.limitThresholds.trigger)
	require.Equal(t, uint64(190<<20), timer.limitThresholds.armed)
	require.False(t, timer.explicitSafetyMargin)

	sample := func(usage uint64, available uint64) memorySample {
		return memorySample{usage: usage, available: available, availableKnown: true}
	}
	step := func(from pressureState, s memorySample) pressureState {
		timer.state = from
		return timer.nextState(s)
	}

	require.Equal(t, pressureStateNormal, step(pressureStateNormal, sample(42<<20, 1<<30)))
	require.Equal(t, pressureStateTriggered, step(pressureStateNormal, sample(195<<20, 1<<30)))
	require.Equal(t, pressureStateArmed, step(pressureStateNormal, sample(190<<20, 1<<30)))
	require.Equal(t, pressureStateTriggered, step(pressureStateTriggered, sample(192<<20, 1<<30)))
	require.Equal(t, pressureStateNormal, step(pressureStateTriggered, sample(150<<20, 1<<30)))

	// The available watch stays on: low available memory at small usage triggers.
	require.Equal(t, pressureStateTriggered, step(pressureStateNormal, sample(42<<20, 1<<20)))
	// Without an explicit safety_margin the available thresholds are the available-mode
	// default (32 MiB at this usage), not the 5 MiB memory-limit default.
	require.Equal(t, uint64(32<<20), timer.availableThresholdsRuntimeLimit(sample(42<<20, 0)).trigger)
	require.Equal(t, pressureStateTriggered, step(pressureStateNormal, sample(42<<20, 20<<20)))
	require.Equal(t, pressureStateArmed, step(pressureStateNormal, sample(42<<20, 50<<20)))
	// Unknown available memory: only the runtime figure counts.
	require.Equal(t, pressureStateNormal, step(pressureStateNormal, memorySample{usage: 42 << 20}))
	require.Equal(t, pressureStateTriggered, step(pressureStateNormal, memorySample{usage: 196 << 20}))

	require.True(t, timer.belowTrigger(sample(42<<20, 1<<30)))
	require.False(t, timer.belowTrigger(sample(196<<20, 1<<30)))
	require.False(t, timer.belowTrigger(sample(42<<20, 1<<20)))
	require.True(t, timer.belowTrigger(memorySample{usage: 42 << 20}))

	details := timer.logDetails(sample(150<<20, 1<<30))
	require.Contains(t, details, "headroom")
	require.Contains(t, details, "available")
}

func TestRuntimeLimitExplicitSafetyMargin(t *testing.T) {
	timer := newRuntimeLimitTestTimer(t, option.OOMKillerServiceOptions{SafetyMargin: memoryBytes(t, 64<<20)})
	require.True(t, timer.explicitSafetyMargin)
	require.Equal(t, uint64(136<<20), timer.limitThresholds.trigger)
	thresholds := timer.availableThresholdsRuntimeLimit(memorySample{usage: 42 << 20})
	require.Equal(t, uint64(64<<20), thresholds.trigger)
	require.Equal(t, uint64(128<<20), thresholds.armed)

	timer.state = pressureStateNormal
	require.Equal(t, pressureStateTriggered, timer.nextState(memorySample{usage: 42 << 20, available: 60 << 20, availableKnown: true}))
}

func TestRuntimeLimitPoll(t *testing.T) {
	previous := readRuntimeMemory
	readRuntimeMemory = func() uint64 { return 196 << 20 }
	defer func() { readRuntimeMemory = previous }()

	// Intervals far beyond the test, so only the direct poll below runs.
	timer := newRuntimeLimitTestTimer(t, option.OOMKillerServiceOptions{
		MinInterval: badoption.Duration(time.Hour),
		MaxInterval: badoption.Duration(time.Hour),
	})
	timer.start(nil)
	defer timer.stop()
	timer.poll()

	timer.access.Lock()
	state := timer.state
	timer.access.Unlock()
	require.Equal(t, pressureStateTriggered, state)
}
