package wireguard

// lx: SPEC 097 — lazy device build.
//
// With EndpointOptions.LazyDevice the constructor stops short of NewDevice: the
// endpoint is born in the SPEC 020 level-3 shape (tunDevice == nil, so
// TornDown() is true) with its recipe kept in deviceOptions, and the first
// dial builds the device through the ordinary Rebuild path. Everything that may
// be asked before that first build is served without a device: the port
// addresses come from the configured addresses, and the L3 return path can be
// attached to a wrapper around a placeholder device, which Rebuild carries
// over to the real one.

import (
	"context"
	"net"
	"net/netip"
	"os"

	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/wireguard-go/device"
	wgTun "github.com/sagernet/wireguard-go/tun"
)

// newDeviceFn builds a tun device. Every device build after construction goes
// through it (Rebuild), so tests can count builds. Production value: NewDevice.
var newDeviceFn = NewDevice

// SetNewDeviceFnForTest replaces the device constructor used by Rebuild and
// returns a func restoring the previous one. Test-only; not safe to call while
// endpoints are being rebuilt.
func SetNewDeviceFnForTest(fn func(options DeviceOptions) (Device, error)) (restore func()) {
	previous := newDeviceFn
	newDeviceFn = fn
	return func() { newDeviceFn = previous }
}

// newLazyEndpoint finishes NewEndpoint without building the device.
func newLazyEndpoint(options EndpointOptions, peers []peerConfig, ipcConf string, allowedAddress []netip.Prefix, deviceOptions DeviceOptions) *Endpoint {
	inet4Address, inet6Address := devicePortAddresses(deviceOptions)
	e := &Endpoint{
		options:        options,
		peers:          peers,
		ipcConf:        ipcConf,
		allowedAddress: allowedAddress,
		returnDevice:   &returnDeviceWrapper{Device: unbuiltDevice{}},
		deviceOptions:  deviceOptions,
		inet4Address:   inet4Address,
		inet6Address:   inet6Address,
	}
	// Same flag a Teardown leaves behind: a pause/wake event has no device to
	// bring up, and only a dial builds one.
	e.suspended.Store(true)
	return e
}

// devicePortAddresses derives the port addresses from the configured prefixes
// the way the device would: the gVisor stack device keeps the last address of
// each family, the system device the first.
func devicePortAddresses(options DeviceOptions) (inet4Address netip.Addr, inet6Address netip.Addr) {
	for _, prefix := range options.Address {
		addr := prefix.Addr()
		if addr.Is4() {
			if !options.System || !inet4Address.IsValid() {
				inet4Address = addr
			}
		} else if !options.System || !inet6Address.IsValid() {
			inet6Address = addr
		}
	}
	return
}

// closedEvents is the event channel of unbuiltDevice: closed, so a reader
// returns at once instead of blocking forever.
var closedEvents = func() chan wgTun.Event {
	events := make(chan wgTun.Event)
	close(events)
	return events
}()

// unbuiltDevice stands in for the tun device inside the return-path wrapper
// until the first build. No wireguard device is ever created around it (Start
// runs only after Rebuild has replaced the wrapper), so its methods only have
// to be safe, not useful.
type unbuiltDevice struct{}

var _ Device = unbuiltDevice{}

func (unbuiltDevice) File() *os.File { return nil }

func (unbuiltDevice) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	return 0, os.ErrClosed
}

func (unbuiltDevice) Write(bufs [][]byte, offset int) (int, error) {
	return 0, os.ErrClosed
}

func (unbuiltDevice) MTU() (int, error) { return 0, os.ErrClosed }

func (unbuiltDevice) Name() (string, error) { return "", os.ErrClosed }

func (unbuiltDevice) Events() <-chan wgTun.Event { return closedEvents }

func (unbuiltDevice) Close() error { return nil }

func (unbuiltDevice) BatchSize() int { return 1 }

func (unbuiltDevice) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return nil, os.ErrClosed
}

func (unbuiltDevice) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrClosed
}

func (unbuiltDevice) Start() error { return os.ErrClosed }

func (unbuiltDevice) SetDevice(device *device.Device) {}

func (unbuiltDevice) Inet4Address() netip.Addr { return netip.Addr{} }

func (unbuiltDevice) Inet6Address() netip.Addr { return netip.Addr{} }
