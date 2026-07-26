// lx: SPEC 033 — DNS group server type. Live box.New coverage: config-level
// plumbing (registration, dependency start order, group as `final`) and the
// badjson `[]`→nil collapse on `servers` (memory: list semantics MUST be
// verified through a live box, not a direct unmarshal).
package main

import (
	"testing"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

func parseOptionsLX(t *testing.T, content string) option.Options {
	t.Helper()
	options, err := json.UnmarshalExtendedContext[option.Options](globalCtx, []byte(content))
	require.NoError(t, err)
	return options
}

func TestDNSGroupBoxStart_LX(t *testing.T) {
	// The group is declared BEFORE its members on purpose: start order must
	// come from dependencies, not from declaration order. The group is also
	// the `final` default server.
	options := parseOptionsLX(t, `{
		"dns": {
			"servers": [
				{"type": "group", "tag": "grp", "servers": ["a", "b"], "down_time": "1s"},
				{"type": "udp", "tag": "a", "server": "127.0.0.1", "server_port": 19653},
				{"type": "udp", "tag": "b", "server": "127.0.0.1", "server_port": 19654}
			],
			"final": "grp"
		},
		"outbounds": [{"type": "direct"}]
	}`)
	instance := startInstance(t, options)
	require.NotNil(t, instance)
}

func TestDNSGroupEmptyServersRejected_LX(t *testing.T) {
	// badjson collapses [] into nil on the re-marshal cycle; both must land
	// in the same "servers is required" constructor error.
	options := parseOptionsLX(t, `{
		"log": {"level": "warning"},
		"dns": {
			"servers": [
				{"type": "group", "tag": "grp", "servers": []}
			]
		},
		"outbounds": [{"type": "direct"}]
	}`)
	_, err := box.New(box.Options{
		Context: globalCtx,
		Options: options,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "servers is required")
}

func TestDNSGroupCycleRejected_LX(t *testing.T) {
	options := parseOptionsLX(t, `{
		"log": {"level": "warning"},
		"dns": {
			"servers": [
				{"type": "group", "tag": "g1", "servers": ["g2"]},
				{"type": "group", "tag": "g2", "servers": ["g1"]}
			],
			"final": "g1"
		},
		"outbounds": [{"type": "direct"}]
	}`)
	instance, err := box.New(box.Options{
		Context: globalCtx,
		Options: options,
	})
	require.NoError(t, err)
	err = instance.Start()
	require.Error(t, err)
	require.Contains(t, err.Error(), "circular server dependency")
	instance.Close()
}
