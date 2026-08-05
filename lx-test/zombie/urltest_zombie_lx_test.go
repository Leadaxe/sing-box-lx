//go:build with_xhttp

// lx: SPECS/TASKS/050-URLTEST_ZOMBIE_RUN_SURVIVES_RESTART
//
// End-to-end guard for the field symptom: a URL test that reached a half-alive
// node used to keep its goroutine — and the whole outbound slice — alive across
// a full box shutdown, so every Stop → Start added another generation of
// zombies. On device this showed up as testNodes runs aged 100 and 43 minutes
// still holding a 2806-node list from a previous subscription, and as a group
// that had stopped publishing delays ("the ping is lost").
//
// The unit tests in protocol/group and transport/v2rayxhttp pin each level of
// the fix. This stand pins the end state the acceptance criterion is written
// against: after Stop → Start, no test goroutine from the previous session
// survives. It lives in its own package (not test/, whose module needs an
// unrelated `go mod tidy` to build) and drives a real box.New/Start/Close.
package zombie

import (
	"context"
	"net"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
)

// blackHoleListener accepts TCP connections and then does nothing: no reads, no
// writes, no close. This is the half-alive node of the incident — the TCP
// handshake succeeds, so the dial completes and the XHTTP/encryption handshake
// starts writing into a request body that nobody reads.
func blackHoleListener(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	accepted := make(chan net.Conn, 64)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				close(accepted)
				return
			}
			accepted <- conn
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		for conn := range accepted {
			conn.Close()
		}
	})
	return listener.Addr().String()
}

// countTestGoroutines reports how many goroutines sit in the group's test path —
// the same frames dumpStacks() shows on device.
func countTestGoroutines() int {
	buffer := make([]byte, 1<<21)
	buffer = buffer[:runtime.Stack(buffer, true)]
	var count int
	for _, frame := range strings.Split(string(buffer), "\n\n") {
		if strings.Contains(frame, "URLTestGroup).testNodes") ||
			strings.Contains(frame, "urltest.URLTest") ||
			strings.Contains(frame, "encryption.(*ClientInstance).Handshake") {
			count++
		}
	}
	return count
}

// newStandBox builds a box whose urltest group points at a half-alive
// vless+xhttp+encryption node — the protocol stack of the field report.
func newStandBox(t *testing.T, ctx context.Context, serverAddr string) *box.Box {
	t.Helper()
	host, portString, err := net.SplitHostPort(serverAddr)
	require.NoError(t, err)
	portNumber, err := strconv.ParseUint(portString, 10, 16)
	require.NoError(t, err)

	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Log: &option.LogOptions{Level: "warning"},
			Outbounds: []option.Outbound{
				{
					Type: C.TypeVLESS,
					Tag:  "hangs",
					Options: &option.VLESSOutboundOptions{
						ServerOptions: option.ServerOptions{Server: host, ServerPort: uint16(portNumber)},
						UUID:          "b831381d-6324-4d53-ad4f-8cda48b30811",
						// The PQ layer of the incident. Its Handshake writes fragmented
						// padding into the XHTTP upload pipe over a bare net.Conn with no
						// context of its own — that write is what used to block forever.
						Encryption: "mlkem768x25519plus.native.1rtt.AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA",
						Transport: &option.V2RayTransportOptions{
							Type:         C.V2RayTransportTypeXHTTP,
							XHTTPOptions: option.V2RayXHTTPOptions{Mode: "stream-one", Path: "/xhttp/"},
						},
					},
				},
				{
					Type: C.TypeURLTest,
					Tag:  "auto",
					Options: &option.URLTestOutboundOptions{
						Outbounds: []string{"hangs"},
						URL:       "http://127.0.0.1:1/generate_204",
						Interval:  badoption.Duration(time.Minute),
					},
				},
				{Type: C.TypeDirect, Tag: "direct"},
			},
		},
	})
	require.NoError(t, err)
	return instance
}

// TestURLTestZombieDoesNotSurviveRestart is the acceptance criterion: two full
// Stop → Start cycles against a half-alive node must not accumulate test
// goroutines. Before the fix each cycle left its run behind permanently.
func TestURLTestZombieDoesNotSurviveRestart(t *testing.T) {
	globalCtx := include.Context(context.Background())
	serverAddr := blackHoleListener(t)
	baseline := countTestGoroutines()

	// The parent context is deliberately NOT cancelled between cycles: on device
	// a Stop → Start is box.Close() plus a fresh box, while the process-wide
	// context lives on. Cancelling it here would tear the goroutines down through
	// a path the device never takes, and the stand would pass against the bug —
	// verified: with the fix reverted, cancelling hides the leak completely.
	for cycle := 1; cycle <= 2; cycle++ {
		instance := newStandBox(t, globalCtx, serverAddr)
		require.NoError(t, instance.Start())

		// PostStart kicks off CheckOutbounds; give the run time to reach the node
		// and block inside the handshake.
		time.Sleep(1500 * time.Millisecond)

		closed := make(chan error, 1)
		go func() { closed <- instance.Close() }()
		select {
		case err := <-closed:
			require.NoError(t, err)
		case <-time.After(15 * time.Second):
			t.Fatalf("cycle %d: box.Close hung", cycle)
		}

		// Teardown is not instantaneous; poll instead of assuming.
		var leaked int
		for attempt := 0; attempt < 20; attempt++ {
			leaked = countTestGoroutines() - baseline
			if leaked <= 0 {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		require.LessOrEqual(t, leaked, 0,
			"cycle %d: %d test goroutine(s) survived box.Close — the zombie that piles up per restart", cycle, leaked)
	}
}
