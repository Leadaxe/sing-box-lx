package wireguard

// lx: SPEC 097 — the lazy transport endpoint: no device until Rebuild, and
// everything that may be asked before it is served without one.

import (
	"context"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"

	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/wireguard-go/device"
	wgTun "github.com/sagernet/wireguard-go/tun"
)

func newLazyTransportEndpoint(t *testing.T, system bool, addresses ...string) *Endpoint {
	t.Helper()
	var prefixes []netip.Prefix
	for _, address := range addresses {
		prefixes = append(prefixes, netip.MustParsePrefix(address))
	}
	e, err := NewEndpoint(EndpointOptions{
		Context:    context.Background(),
		Logger:     logger.NOP(),
		System:     system,
		Name:       "wg-lazy",
		MTU:        1408,
		Address:    prefixes,
		PrivateKey: "iOx8sYFBnQjKMkTfTBaLZ5+DEHU4S3vzcSLp+HDaOWc=",
		Peers: []PeerOptions{{
			Endpoint:   M.ParseSocksaddr("192.0.2.1:51820"),
			PublicKey:  "eBpqZlJmSVBGNW9BOEZ4S3lRZTNQd0RhbWFnZTBTNTA=",
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
		}},
		LazyDevice: true,
	})
	if err != nil {
		t.Fatalf("lazy endpoint construction failed: %v", err)
	}
	return e
}

// fakeDevice is a tun device without a network stack, for rebuild tests that
// must not depend on with_gvisor.
type fakeDevice struct {
	closeOnce sync.Once
	events    chan wgTun.Event
	closed    chan struct{}
}

func newFakeDevice() *fakeDevice {
	return &fakeDevice{events: make(chan wgTun.Event), closed: make(chan struct{})}
}

func (d *fakeDevice) File() *os.File { return nil }
func (d *fakeDevice) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	<-d.closed
	return 0, os.ErrClosed
}
func (d *fakeDevice) Write(bufs [][]byte, offset int) (int, error) { return len(bufs), nil }
func (d *fakeDevice) MTU() (int, error)                            { return 1408, nil }
func (d *fakeDevice) Name() (string, error)                        { return "fake", nil }
func (d *fakeDevice) Events() <-chan wgTun.Event                   { return d.events }
func (d *fakeDevice) BatchSize() int                               { return 1 }
func (d *fakeDevice) Close() error {
	d.closeOnce.Do(func() {
		close(d.closed)
		close(d.events)
	})
	return nil
}

func (d *fakeDevice) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return nil, os.ErrInvalid
}

func (d *fakeDevice) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}
func (d *fakeDevice) Start() error                    { return nil }
func (d *fakeDevice) SetDevice(device *device.Device) {}
func (d *fakeDevice) Inet4Address() netip.Addr        { return netip.MustParseAddr("10.0.0.2") }
func (d *fakeDevice) Inet6Address() netip.Addr        { return netip.Addr{} }

type fakeReturn struct{}

func (fakeReturn) ReturnHeadroom() int                     { return 0 }
func (fakeReturn) ReturnPackets(packets [][]byte) [][]byte { return nil }

var _ tun.Return = fakeReturn{}

// A lazy endpoint is born torn down and every device-less query is safe.
func TestLazyEndpoint_bornTornDown(t *testing.T) {
	builds := 0
	defer SetNewDeviceFnForTest(func(options DeviceOptions) (Device, error) {
		builds++
		return newFakeDevice(), nil
	})()
	e := newLazyTransportEndpoint(t, false, "10.0.0.2/32")
	if !e.TornDown() {
		t.Fatal("a lazy endpoint must be born torn down")
	}
	if builds != 0 {
		t.Fatalf("construction must not build a device, built %d", builds)
	}
	if e.ActiveTCPFlows() != 0 || e.TransferTotals() != 0 || e.Lookup(netip.MustParseAddr("1.1.1.1")) != nil {
		t.Fatal("device-less queries must report nothing")
	}
	if err := e.BindUpdate(); err != nil {
		t.Fatal(err)
	}
	if e.RebindIfSessionStale() {
		t.Fatal("no device, no rebind")
	}
	if e.WritePackets([][]byte{{0x45}}) == nil {
		t.Fatal("writing through an unbuilt endpoint must fail, not panic")
	}
	if _, err := e.DialContext(context.Background(), "tcp", M.ParseSocksaddr("10.0.0.9:80")); err == nil {
		t.Fatal("dialing an unbuilt endpoint must fail")
	}
	e.Suspend()
	if err := e.Resume(); err != nil {
		t.Fatal(err)
	}
	e.applyPauseEvent(0)
	if err := e.Close(); err != nil {
		t.Fatalf("closing an unbuilt endpoint: %v", err)
	}
}

// The placeholder device inside the return wrapper is safe to call.
func TestLazyEndpoint_placeholderDeviceSafe(t *testing.T) {
	e := newLazyTransportEndpoint(t, false, "10.0.0.2/32")
	wrapper := e.returnDevice
	if _, err := wrapper.Write([][]byte{{1}}, 0); err == nil {
		t.Fatal("the placeholder must refuse writes")
	}
	if _, err := wrapper.Read([][]byte{{1}}, []int{0}, 0); err == nil {
		t.Fatal("the placeholder must refuse reads")
	}
	if _, ok := <-wrapper.Events(); ok {
		t.Fatal("the placeholder's event channel must be closed")
	}
	if wrapper.File() != nil || wrapper.BatchSize() != 1 || wrapper.Close() != nil || wrapper.Start() == nil {
		t.Fatal("unexpected placeholder behaviour")
	}
	if _, err := wrapper.MTU(); err == nil {
		t.Fatal("the placeholder has no MTU")
	}
	if _, err := wrapper.Name(); err == nil {
		t.Fatal("the placeholder has no name")
	}
	if _, err := wrapper.DialContext(context.Background(), "tcp", M.ParseSocksaddr("10.0.0.9:80")); err == nil {
		t.Fatal("the placeholder must refuse dials")
	}
	if _, err := wrapper.ListenPacket(context.Background(), M.ParseSocksaddr("10.0.0.9:80")); err == nil {
		t.Fatal("the placeholder must refuse listens")
	}
	wrapper.SetDevice(nil)
	if wrapper.Inet4Address().IsValid() || wrapper.Inet6Address().IsValid() {
		t.Fatal("the placeholder has no addresses")
	}
}

// Port addresses come from the configured prefixes, the way each device kind
// would pick them: the stack device keeps the last address per family, the
// system device the first.
func TestLazyEndpoint_portAddressesFromOptions(t *testing.T) {
	addresses := []string{"10.0.0.2/32", "10.0.0.3/32", "fd00::2/128", "fd00::3/128"}
	stack := newLazyTransportEndpoint(t, false, addresses...)
	v4, v6 := stack.PortAddresses()
	if v4 != netip.MustParseAddr("10.0.0.3") || v6 != netip.MustParseAddr("fd00::3") {
		t.Fatalf("stack device: got %v %v", v4, v6)
	}
	system := newLazyTransportEndpoint(t, true, addresses...)
	v4, v6 = system.PortAddresses()
	if v4 != netip.MustParseAddr("10.0.0.2") || v6 != netip.MustParseAddr("fd00::2") {
		t.Fatalf("system device: got %v %v", v4, v6)
	}
}

// sing-tun may attach its return path before the first build; Rebuild carries
// it over to the real device, and the build goes through newDeviceFn.
func TestLazyEndpoint_attachReturnBeforeBuild(t *testing.T) {
	builds := 0
	defer SetNewDeviceFnForTest(func(options DeviceOptions) (Device, error) {
		builds++
		return newFakeDevice(), nil
	})()
	e := newLazyTransportEndpoint(t, false, "10.0.0.2/32")
	returnPath := fakeReturn{}
	if err := e.AttachReturn(returnPath); err != nil {
		t.Fatalf("attach before build: %v", err)
	}
	attached := e.returnDevice.state.Load()
	if err := e.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("Rebuild must build exactly one device, built %d", builds)
	}
	if e.TornDown() || e.suspended.Load() {
		t.Fatal("a built endpoint is neither torn down nor suspended")
	}
	if got := e.returnDevice.state.Load(); got != attached || got == nil {
		t.Fatal("Rebuild must carry the early return path over")
	}
	if err := e.DetachReturn(returnPath); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}
