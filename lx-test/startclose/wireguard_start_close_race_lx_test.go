//go:build with_gvisor && with_wireguard

// lx: SPECS/TASKS/070-WG_START_CLOSE_RACE_CRASH
//
// Stop during start must not crash the process. The daemon deliberately allows
// CloseService while instance.Start() is still running (field crash: a
// 1755-outbound profile, user pressed stop mid-start; Box.Close nil'd a WG
// endpoint's tun device under the feet of its in-flight transport Start →
// SIGSEGV). This stand races Box.Start against Box.Close over live WG
// endpoints with a swept delay, so the close lands in every phase of the
// start. The sweep asserts no panic and no hang; it also hammers Box.Close's
// concurrent-idempotency CAS — the external close races the start-error-path
// close inside Box.Start.
//
// Run PLAIN, without -race: the detector additionally reports three known
// upstream Start×Close races outside the WG endpoint (Box.debugHTTPServer,
// sing-tun's defaultInterfaceMonitor, route.Router fields) — the residual
// class SPEC 070 documents as out of scope, none of which has a field crash.
// The WG regression itself is pinned deterministically red/green by the unit
// test in protocol/wireguard/endpoint_start_close_race_lx_test.go.
//
// Lives here rather than in test/ for the same reason as lx-test/zombie: the
// test/ module resolves the fork submodules from the proxy and does not build.
package startclose

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/curve25519"
)

func genKeyPair(t *testing.T) (privateKeyBase64, publicKeyBase64 string) {
	t.Helper()
	privateKey := make([]byte, 32)
	_, err := rand.Read(privateKey)
	require.NoError(t, err)
	privateKey[0] &= 248
	privateKey[31] &= 127
	privateKey[31] |= 64
	publicKey, err := curve25519.X25519(privateKey, curve25519.Basepoint)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(privateKey), base64.StdEncoding.EncodeToString(publicKey)
}

func TestWGStartCloseRace_LX(t *testing.T) {
	const endpointCount = 4
	const iterations = 24
	globalCtx := include.Context(context.Background())

	for i := 0; i < iterations; i++ {
		endpoints := make([]option.Endpoint, 0, endpointCount)
		for j := 0; j < endpointCount; j++ {
			priv, _ := genKeyPair(t)
			_, peerPub := genKeyPair(t)
			// Black-holed loopback peers (nothing listens): devices come up and
			// stay busy retrying handshakes, as in the SPEC 030 stand.
			endpoints = append(endpoints, option.Endpoint{
				Type: C.TypeWireGuard,
				Tag:  "wg-race-" + strconv.Itoa(j),
				Options: &option.WireGuardEndpointOptions{
					MTU:        1408,
					Address:    badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.81." + strconv.Itoa(j) + ".2/32")},
					PrivateKey: priv,
					Peers: []option.WireGuardPeer{
						{
							Address:    "127.0.0.1",
							Port:       uint16(21000 + j),
							PublicKey:  peerPub,
							AllowedIPs: badoption.Listable[netip.Prefix]{netip.MustParsePrefix("0.0.0.0/0")},
						},
					},
				},
			})
		}

		ctx, cancel := context.WithCancel(globalCtx)
		instance, err := box.New(box.Options{
			Context: ctx,
			Options: option.Options{
				Log:       &option.LogOptions{Level: "error"},
				Endpoints: endpoints,
				Outbounds: []option.Outbound{{Type: C.TypeDirect}},
			},
		})
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A start interrupted by the racing close may legally fail (and its
			// error path calls Close itself); the assertion is no panic, no race.
			_ = instance.Start()
		}()
		// Sweep the close over the whole start timeline: iteration 0 closes
		// immediately (close-before-start ordering), later ones land mid- and
		// post-start.
		if delay := time.Duration(i) * 500 * time.Microsecond; delay > 0 {
			time.Sleep(delay)
		}
		_ = instance.Close()
		wg.Wait()
		// Second close must be a clean refusal, never a double-close panic.
		_ = instance.Close()
		cancel()
	}
}
