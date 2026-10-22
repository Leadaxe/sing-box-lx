//go:build with_gvisor && with_wireguard

// lx: SPECS/TASKS/030-FAST_BOX_SHUTDOWN
//
// Fast box shutdown, end to end. Stopping an instance with many WireGuard
// endpoints used to hang: box.Close tore endpoints down while the idle/urltest
// tick was still issuing wakes, so each Endpoint.Close blocked on resumeMu
// behind an in-flight ping-wake (device rebuild + handshake), summed serially
// over every endpoint. The fix quiesces the tick, marks every endpoint closing
// and releases every UDP socket up front (router.QuiesceForShutdown → the
// endpoints' QuiesceForShutdown), aborts in-flight wakes (the `closing` flag),
// and closes endpoints concurrently.
//
// This is a SMOKE stand, not a red/green gate for the mechanisms: the closing
// gate is pinned in protocol/wireguard/endpoint_fast_close_lx_test.go and
// endpoint_quiesce_lx_test.go, the socket release in
// transport/wireguard/endpoint_quiesce_lx_test.go, the router walk in
// route/quiesce_shutdown_lx_test.go. Here we build N live WG endpoints (peers
// point at dead loopback addresses, so devices stay up with busy receive
// workers) and assert that box.Close of all N returns promptly and without
// panic; the duration is logged for the SPEC 030 record. The loose ceiling
// catches a hang or a parallel-close deadlock, not CI jitter.
//
// Lives here rather than in test/ for the same reason as the stand next to it:
// the test/ module resolves the fork submodules from the proxy and does not
// build, so a stand there never runs.
package startclose

import (
	"context"
	"net/netip"
	"strconv"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
)

func TestWGFastClose_LX(t *testing.T) {
	const n = 20
	endpoints := make([]option.Endpoint, 0, n)
	for i := 0; i < n; i++ {
		priv, _ := genKeyPair(t)
		_, peerPub := genKeyPair(t)
		// Each endpoint dials a black-holed peer on loopback (nothing listens),
		// so the device stays up retrying handshakes — a live device with a busy
		// receive worker, which is exactly what makes a naive serial close slow.
		endpoints = append(endpoints, option.Endpoint{
			Type: C.TypeWireGuard,
			Tag:  "wg-" + strconv.Itoa(i),
			Options: &option.WireGuardEndpointOptions{
				MTU:        1408,
				Address:    badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.80." + strconv.Itoa(i) + ".2/32")},
				PrivateKey: priv,
				Peers: []option.WireGuardPeer{
					{
						Address:    "127.0.0.1",
						Port:       uint16(20000 + i),
						PublicKey:  peerPub,
						AllowedIPs: badoption.Listable[netip.Prefix]{netip.MustParsePrefix("0.0.0.0/0")},
					},
				},
			},
		})
	}

	ctx, cancel := context.WithCancel(include.Context(context.Background()))
	defer cancel()
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Log:       &option.LogOptions{Level: "warning"},
			Endpoints: endpoints,
			Outbounds: []option.Outbound{{Type: C.TypeDirect}},
		},
	})
	require.NoError(t, err)
	require.NoError(t, instance.Start())

	// Let the devices come up and their receive workers get busy on the dead peers.
	time.Sleep(500 * time.Millisecond)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- instance.Close() }()

	select {
	case closeErr := <-done:
		elapsed := time.Since(start)
		require.NoError(t, closeErr)
		t.Logf("box.Close of %d WG endpoints took %v", n, elapsed)
		// Generous ceiling: a hang is 10s+; the fix closes in well under a second.
		require.Less(t, elapsed, 4*time.Second, "box.Close hung — endpoints not closed promptly")
	case <-time.After(15 * time.Second):
		t.Fatal("box.Close did not return within 15s — hung")
	}
}
