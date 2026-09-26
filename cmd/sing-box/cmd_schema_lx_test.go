package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/schema"

	"github.com/stretchr/testify/require"
)

// TestSchemaGeneratesWithLXTypes guards `sing-box schema` against the lx
// option types: every custom JSON type the fork adds (AWGRange, XmuxRange,
// ChainRewrite) must describe itself, otherwise the generator aborts on the
// first field it meets (issue #30: "unmapped custom JSON type option.AWGRange").
// The registries in include.Context carry whatever the build tags enabled, so
// the test covers exactly the set of features compiled in.
func TestSchemaGeneratesWithLXTypes(t *testing.T) {
	content, err := schema.Generate(include.Context(context.Background()), reflect.TypeFor[option.Options]())
	require.NoError(t, err)
	var root struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(content, &root))
	require.Contains(t, root.Defs, "V2RayTransport")
	if _, wireguard := root.Defs["WireGuardPeer"]; wireguard {
		require.Contains(t, root.Defs, "AWGRange", "AWGRange must describe itself for the WireGuard endpoint")
	}
	if bytes.Contains(content, []byte(`"const": "chain"`)) {
		require.Contains(t, root.Defs, "ChainRewrite", "ChainRewrite must describe itself for the chain outbound")
	}
}
