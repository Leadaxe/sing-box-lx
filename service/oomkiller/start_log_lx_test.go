package oomkiller

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

func startLogTestConfig(t *testing.T, policyMode policyMode, memoryLimit uint64, killerDisabled bool) timerConfig {
	t.Helper()
	config, err := buildTimerConfig(option.OOMKillerServiceOptions{}, memoryLimit, policyMode, killerDisabled)
	require.NoError(t, err)
	return config
}

func TestStartLogDetails(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		policyMode     policyMode
		memoryLimit    uint64
		killerDisabled bool
		expected       string
	}{
		{"runtime_limit", policyModeRuntimeLimit, testRuntimeLimit, false, "mode: runtime_limit, limit: 200 MB, trigger: 195 MB, armed: 190 MB"},
		{"memory_limit", policyModeMemoryLimit, testRuntimeLimit, false, "mode: memory_limit, limit: 200 MB, trigger: 195 MB, armed: 190 MB"},
		{"network_extension", policyModeNetworkExtension, DefaultAppleNetworkExtensionMemoryLimit, false, "mode: network_extension, limit: 50 MB, trigger: 45 MB, armed: 40 MB"},
		{"available", policyModeAvailable, 0, false, "mode: available"},
		{"runtime_limit report only", policyModeRuntimeLimit, testRuntimeLimit, true, "mode: runtime_limit, limit: 200 MB, trigger: 195 MB, armed: 190 MB, report only"},
		{"available report only", policyModeAvailable, 0, true, "mode: available, report only"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			config := startLogTestConfig(t, testCase.policyMode, testCase.memoryLimit, testCase.killerDisabled)
			timer := newAdaptiveTimer(log.NewNOPFactory().Logger(), nil, nil, nil, config)
			require.Equal(t, testCase.expected, startLogDetails(config, timer.limitThresholds))
		})
	}
}

type startLogBuffer struct {
	access sync.Mutex
	buffer bytes.Buffer
}

func (b *startLogBuffer) Write(p []byte) (int, error) {
	b.access.Lock()
	defer b.access.Unlock()
	return b.buffer.Write(p)
}

func (b *startLogBuffer) String() string {
	b.access.Lock()
	defer b.access.Unlock()
	return b.buffer.String()
}

func TestStartTimerLogsPolicy(t *testing.T) {
	output := &startLogBuffer{}
	factory := log.NewDefaultFactory(context.Background(), log.Formatter{DisableTimestamp: true, DisableColors: true}, output, "", nil, false)
	require.NoError(t, factory.Start())
	defer factory.Close()

	service := &Service{
		logger:      factory.NewLogger("oom-killer"),
		timerConfig: startLogTestConfig(t, policyModeRuntimeLimit, testRuntimeLimit, true),
	}
	require.NoError(t, service.startTimer())
	service.stopTimer()
	require.Contains(t, output.String(), "memory pressure monitoring started, mode: runtime_limit, limit: 200 MB, trigger: 195 MB, armed: 190 MB, report only")
}
