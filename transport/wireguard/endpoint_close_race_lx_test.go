package wireguard

// lx: issue #75 — the swappable endpoint fields (tunDevice, device, allowedIPs,
// returnDevice) are written by Close/Teardown/Rebuild/Start and read by the
// dial, routing and bind-update paths with no lock in common. These tests pin
// that under `-race`; they use the fake tun device, so they need neither
// with_gvisor nor a network.

import (
	"context"
	"net/netip"
	"sync"
	"testing"

	M "github.com/sagernet/sing/common/metadata"
)

const closeRaceRounds = 20

// raceTogether releases every fn at once and waits for all of them.
func raceTogether(fns ...func()) {
	var (
		start = make(chan struct{})
		group sync.WaitGroup
	)
	for _, fn := range fns {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			fn()
		}()
	}
	close(start)
	group.Wait()
}

func newBuiltFakeEndpoint(t *testing.T) *Endpoint {
	t.Helper()
	e := newLazyTransportEndpoint(t, false, "10.0.0.2/32")
	if err := e.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return e
}

// Close of an endpoint whose wireguard device is already gone (a second Close,
// or a built-but-never-started one) clears tunDevice while a dial that already
// passed the protocol layer's gate reads it twice.
func TestCloseRace_tunDeviceAgainstDial(t *testing.T) {
	defer SetNewDeviceFnForTest(func(DeviceOptions) (Device, error) { return newFakeDevice(), nil })()
	destination := M.ParseSocksaddr("192.0.2.9:443")
	for range closeRaceRounds {
		e := newBuiltFakeEndpoint(t)
		raceTogether(
			func() { _, _ = e.DialContext(context.Background(), "tcp", destination) },
			func() { _, _ = e.ListenPacket(context.Background(), destination) },
			func() { _ = e.Close() },
		)
	}
}

// Teardown (idle tick or build budget, under resumeMu only) clears device and
// allowedIPs while BindUpdate (under bindAccess), Lookup and WritePackets (no
// lock) read them.
func TestCloseRace_deviceAndAllowedIPsAgainstTeardown(t *testing.T) {
	defer SetNewDeviceFnForTest(func(DeviceOptions) (Device, error) { return newFakeDevice(), nil })()
	address := netip.MustParseAddr("192.0.2.9")
	for range closeRaceRounds {
		e := newBuiltFakeEndpoint(t)
		// The fake tun device never reports EventUp, so the wireguard device
		// stays down and opens no socket.
		if err := e.Start(false); err != nil {
			t.Fatal(err)
		}
		raceTogether(
			func() { _ = e.BindUpdate() },
			func() { _ = e.Lookup(address) },
			func() { _ = e.WritePackets([][]byte{nil}) },
			func() { e.Teardown() },
		)
		_ = e.Close()
	}
}

// Rebuild copies the attached return path into a fresh wrapper and swaps
// returnDevice; an AttachReturn that lands on the old wrapper after the copy
// reports success and is lost. sing-tun attaches once per port and never
// again, so the L3 downlink of that endpoint stays broken.
func TestCloseRace_attachReturnAgainstRebuild(t *testing.T) {
	defer SetNewDeviceFnForTest(func(DeviceOptions) (Device, error) { return newFakeDevice(), nil })()
	lost := 0
	for range closeRaceRounds {
		e := newLazyTransportEndpoint(t, false, "10.0.0.2/32")
		var attachErr error
		raceTogether(
			func() { attachErr = e.AttachReturn(fakeReturn{}) },
			func() { _ = e.Rebuild() },
		)
		if attachErr == nil && e.returnDevice.state.Load() == nil {
			lost++
		}
		_ = e.Close()
	}
	if lost > 0 {
		t.Errorf("AttachReturn succeeded but the rebuilt endpoint has no return path in %d of %d rounds", lost, closeRaceRounds)
	}
}
