//go:build with_gvisor && with_wireguard

// lx: SPEC 030 — fast box shutdown. Stopping an instance with many WireGuard
// endpoints used to hang: box.Close tore endpoints down while the idle/urltest
// tick was still issuing wakes, so each Endpoint.Close blocked on resumeMu
// behind an in-flight ping-wake (device rebuild + handshake), summed serially
// over every endpoint. The fix quiesces the tick and closes all UDP sockets up
// front (router.QuiesceForShutdown), aborts in-flight wakes (the `closing`
// flag), and closes endpoints concurrently.
//
// This is a SMOKE stand, not a red/green regression gate for the resumeMu hang
// (that race — a ping-wake holding resumeMu mid-rebuild while Close runs — is
// covered deterministically by the unit tests in
// protocol/wireguard/endpoint_fast_close_lx_test.go). Here we build N live WG
// endpoints (peers point at dead loopback addresses, so devices stay up with
// busy receive workers) and assert box.Close of all N — now concurrent (SPEC
// 030 fix D) with sockets pre-closed by the router quiesce — returns promptly
// and without panic. The loose ceiling catches a hang or a parallel-close
// deadlock, not CI jitter.
package main

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
)

func TestWGFastClose_LX(t *testing.T) {
	const n = 20
	endpoints := make([]option.Endpoint, 0, n)
	for i := 0; i < n; i++ {
		priv, _ := wgOrderGenKeyPair(t)
		_, peerPub := wgOrderGenKeyPair(t)
		// Each endpoint dials a black-holed peer on loopback (nothing listens),
		// so the device stays up retrying handshakes — a live device with a busy
		// receive worker, which is exactly what makes a naive serial close slow.
		endpoints = append(endpoints, option.Endpoint{
			Type: C.TypeWireGuard,
			Tag:  "wg-" + itoaLX(i),
			Options: &option.WireGuardEndpointOptions{
				MTU:        1408,
				Address:    badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.80." + itoaLX(i) + ".2/32")},
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

	ctx, cancel := context.WithCancel(globalCtx)
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

// itoaLX is a tiny int→string without importing strconv into the test's hot
// build set (keeps the file dependency-light).
func itoaLX(i int) string {
	if i == 0 {
		return "0"
	}
	var b [3]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
