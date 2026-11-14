package v2rayxhttp

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

// lx: SPEC 060 — the request context of an XHTTP conn carries the dial's
// sniff guard attempt and carrier record over the transport context, so the
// TLS handshake of the pooled HTTP connection is the node's.
func TestSniffGuardRequestContext(t *testing.T) {
	guard := lxsniffguard.New(lxsniffguard.Config{
		Policy: option.LXSniffGuardResolved{TLS: option.LXSniffGuardSectionResolved{
			Method:      option.LXSniffGuardTLSRecordFragment,
			Delay:       300 * time.Millisecond,
			DetourTypes: map[string]string{option.LXSniffGuardWildcard: option.LXSniffGuardSuspect},
		}},
		Nodes: map[string]lxsniffguard.NodeInfo{"node": {Type: "vless", SNIMismatch: true}},
	})
	type transportKey struct{}
	transportCtx := context.WithValue(context.Background(), transportKey{}, "transport")
	err := guard.Node("node").Dial(context.Background(), func(dialCtx context.Context) error {
		dialCtx, record := adapter.ContextWithCarrierRecord(dialCtx)
		requestCtx := sniffGuardRequestContext(dialCtx, transportCtx)
		require.Same(t, lxsniffguard.AttemptFromContext(dialCtx), lxsniffguard.AttemptFromContext(requestCtx))
		require.Same(t, record, adapter.CarrierRecordFromContext(requestCtx))
		require.Equal(t, "transport", requestCtx.Value(transportKey{}), "still the transport context")
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, transportCtx, sniffGuardRequestContext(context.Background(), transportCtx), "nothing to carry")
}
