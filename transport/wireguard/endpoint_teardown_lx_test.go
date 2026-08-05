//go:build with_gvisor

// lx:begin idle-suspend

package wireguard

import (
	"context"
	"net/netip"
	"testing"

	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

func mustSocksaddr(s string) M.Socksaddr { return M.ParseSocksaddr(s) }

// newTeardownTestEndpoint builds a REAL endpoint (gVisor stack device included,
// no network I/O) so the teardown → rebuild cycle exercises the actual one-shot
// objects rather than a nil stub. Peers use a literal IP, so no resolver runs.
func newTeardownTestEndpoint(t *testing.T) *Endpoint {
	t.Helper()
	e, err := NewEndpoint(EndpointOptions{
		Context:    context.Background(),
		Logger:     logger.NOP(),
		Name:       "wg-test",
		MTU:        1408,
		Address:    []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")},
		PrivateKey: "iOx8sYFBnQjKMkTfTBaLZ5+DEHU4S3vzcSLp+HDaOWc=",
		Peers: []PeerOptions{{
			Endpoint:   mustSocksaddr("192.0.2.1:51820"),
			PublicKey:  "eBpqZlJmSVBGNW9BOEZ4S3lRZTNQd0RhbWFnZTBTNTA=",
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
		}},
	})
	if err != nil {
		// No Skip inside the test: a silent skip once hid this whole file from
		// the suite. The stack device needs `with_gvisor` (see NewDevice), so
		// the requirement lives in this file's build tag instead — every real
		// test run (CI test.yml, Makefile, AAR) carries that tag, and here a
		// construction failure is a real failure.
		t.Fatalf("endpoint construction failed: %v", err)
	}
	return e
}

// TestTeardownRebuildCycle: Teardown releases the tun device (and with it the
// gVisor netstack — the ~5.9 MB level-3 targets), Rebuild installs a fresh one.
// The one-shot nature of the closed objects (closeOnce + closed channels) is
// exactly why a rebuild must create a NEW device instead of reusing it, so this
// pins that a second cycle works too.
func TestTeardownRebuildCycle(t *testing.T) {
	e := newTeardownTestEndpoint(t)
	t.Cleanup(func() { _ = e.Close() })

	if e.TornDown() {
		t.Fatal("a fresh endpoint must not report torn down")
	}
	first := e.tunDevice

	e.Teardown()
	if !e.TornDown() || e.tunDevice != nil {
		t.Fatal("Teardown must release the tun device (netstack freed)")
	}
	if !e.suspended.Load() {
		t.Fatal("a torn-down endpoint must stay flagged suspended (pause-wake must not touch it)")
	}

	if err := e.Rebuild(); err != nil {
		t.Fatalf("rebuild after teardown: %v", err)
	}
	if e.TornDown() || e.tunDevice == nil {
		t.Fatal("Rebuild must install a fresh tun device")
	}
	if e.tunDevice == first {
		t.Fatal("Rebuild must NOT reuse the closed device (its Close runs under sync.Once)")
	}
	if e.suspended.Load() {
		t.Fatal("Rebuild must clear the suspended flag")
	}

	// A second cycle must work identically — the recipe survives.
	e.Teardown()
	if err := e.Rebuild(); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
}

// TestPortAddressesSurviveTeardown: the L3 layer may ask for port addresses at
// any moment, including while the endpoint is torn down — served from the cache,
// never from the (possibly nil) tun device.
func TestPortAddressesSurviveTeardown(t *testing.T) {
	e := newTeardownTestEndpoint(t)
	t.Cleanup(func() { _ = e.Close() })
	v4Before, _ := e.PortAddresses()
	if !v4Before.IsValid() {
		t.Fatal("precondition: live endpoint must report a v4 port address")
	}
	e.Teardown()
	v4After, _ := e.PortAddresses() // must not panic
	if v4After != v4Before {
		t.Fatalf("torn-down endpoint must keep reporting its addresses: %v != %v", v4After, v4Before)
	}
}

// TestRebuildKeepsAttachedReturnPath: sing-tun attaches its L3 return path once;
// it knows nothing about our teardown/rebuild cycle, so the fresh wrapper must
// inherit the attachment or the L3 downlink silently breaks after a rebuild.
func TestRebuildKeepsAttachedReturnPath(t *testing.T) {
	e := newTeardownTestEndpoint(t)
	t.Cleanup(func() { _ = e.Close() })
	attached := &returnPathState{headroom: 4}
	e.returnDevice.state.Store(attached)

	e.Teardown()
	if err := e.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if got := e.returnDevice.state.Load(); got != attached {
		t.Fatal("rebuild must carry the attached return path over to the new wrapper")
	}
}

// TestTeardownAfterPartialRebuild: the failure path of a rebuild rolls back via
// Teardown over a half-rebuilt endpoint (tun device recreated, wg device never
// started) — that must be clean and leave the endpoint properly torn down.
func TestTeardownAfterPartialRebuild(t *testing.T) {
	e := newTeardownTestEndpoint(t)
	t.Cleanup(func() { _ = e.Close() })
	e.Teardown()
	if err := e.Rebuild(); err != nil { // half-rebuilt: no Start ran
		t.Fatal(err)
	}
	e.Teardown() // the rollback the protocol layer performs on a Start failure
	if !e.TornDown() {
		t.Fatal("rollback must land back in the clean torn-down state")
	}
	if err := e.Rebuild(); err != nil { // and the retry works from scratch
		t.Fatal(err)
	}
}

// TestRebuildWithoutTeardownIsNoop: Rebuild on a live endpoint must not swap a
// working device out from under active traffic.
func TestRebuildWithoutTeardownIsNoop(t *testing.T) {
	e := newTeardownTestEndpoint(t)
	t.Cleanup(func() { _ = e.Close() })
	live := e.tunDevice
	if err := e.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if e.tunDevice != live {
		t.Fatal("Rebuild on a live endpoint must be a no-op")
	}
}

// TestCloseAfterTeardownIdempotent: box shutdown over an already torn-down
// endpoint must not panic or double-close.
func TestCloseAfterTeardownIdempotent(t *testing.T) {
	e := newTeardownTestEndpoint(t)
	e.Teardown()
	if err := e.Close(); err != nil {
		t.Fatalf("Close after Teardown: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("double Close: %v", err)
	}
}

// TestDialTornDownEndpointErrors: reaching the transport with no device must
// fail cleanly (the protocol layer rebuilds first; this is the backstop).
func TestDialTornDownEndpointErrors(t *testing.T) {
	e := newTeardownTestEndpoint(t)
	t.Cleanup(func() { _ = e.Close() })
	e.Teardown()
	if _, err := e.DialContext(context.Background(), "tcp", mustSocksaddr("192.0.2.9:80")); err == nil {
		t.Fatal("dialing a torn-down endpoint must return an error, not panic")
	}
}

// lx:end idle-suspend
