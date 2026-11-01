//go:build with_gvisor

package tailscale

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lx: SPEC 149 — a disabled endpoint refuses dials before resolving a domain,
// refuses login and logout; a stopped endpoint refuses the switch and login.

func newToggleTestEndpoint(started bool, disabled bool) *Endpoint {
	endpoint := &Endpoint{logger: log.NewNOPFactory().NewLogger("tailscale")}
	endpoint.started.Store(started)
	endpoint.disabled.Store(disabled)
	return endpoint
}

func TestTailscaleDisabledRefusesDials_LX(t *testing.T) {
	endpoint := newToggleTestEndpoint(true, true)
	ctx := context.Background()
	// dnsRouter and server are nil: reaching the lookup or the stack would panic.
	for _, destination := range []M.Socksaddr{M.ParseSocksaddrHostPort("peer.tailnet.ts.net", 443), M.ParseSocksaddrHostPort("100.64.0.2", 443)} {
		for _, network := range []string{N.NetworkTCP, N.NetworkUDP} {
			_, err := endpoint.DialContext(ctx, network, destination)
			if !errors.Is(err, adapter.ErrTailscaleDisabled) {
				t.Fatalf("dial %s %s: %v", network, destination, err)
			}
		}
		_, err := endpoint.ListenPacket(ctx, destination)
		if !errors.Is(err, adapter.ErrTailscaleDisabled) {
			t.Fatalf("listen %s: %v", destination, err)
		}
	}
	if err := endpoint.Logout(ctx); !errors.Is(err, adapter.ErrTailscaleDisabled) {
		t.Fatalf("logout: %v", err)
	}
	if err := endpoint.TailscaleLogin(ctx, "tskey-auth-test"); !errors.Is(err, adapter.ErrTailscaleDisabled) {
		t.Fatalf("login: %v", err)
	}
	if endpoint.Enabled() {
		t.Fatal("Enabled() = true")
	}
	// Already in the target state: no backend call.
	if err := endpoint.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
}

func TestTailscaleToggleNotStarted_LX(t *testing.T) {
	endpoint := newToggleTestEndpoint(false, false)
	for _, enabled := range []bool{false, true} {
		if err := endpoint.SetEnabled(enabled); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("SetEnabled(%v): %v", enabled, err)
		}
	}
	if err := endpoint.TailscaleLogin(context.Background(), ""); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("login: %v", err)
	}
	if !endpoint.Enabled() {
		t.Fatal("Enabled() = false")
	}
}

func TestTailscaleIdleState_LX(t *testing.T) {
	cases := []struct {
		started  bool
		disabled bool
		want     string
	}{
		{false, false, adapter.EndpointStateDown},
		{false, true, adapter.EndpointStateDown},
		{true, true, adapter.EndpointStateDisabled},
		{true, false, adapter.EndpointStateUp},
	}
	for _, tc := range cases {
		if got := newToggleTestEndpoint(tc.started, tc.disabled).IdleState().State; got != tc.want {
			t.Fatalf("started=%v disabled=%v: %q, want %q", tc.started, tc.disabled, got, tc.want)
		}
	}
	// Enabled and started: SetEnabled(true) is a no-op without a backend call.
	if err := newToggleTestEndpoint(true, false).SetEnabled(true); err != nil {
		t.Fatal(err)
	}
}
