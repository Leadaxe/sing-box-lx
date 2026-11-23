package wireguard

// lx: issue #75 — snapshots of the swappable endpoint fields.
//
// Teardown, Rebuild and Close replace tunDevice, device, allowedIPs and the
// return-path wrapper at run time (SPEC 020 level 3, SPEC 097), while the
// dial, routing and bind-update paths read them with no lock in common with
// those writers: a dial that passed the protocol layer's gate holds nothing,
// updateBind holds bindAccess, the teardown holds resumeMu. A reader that
// checks a field for nil and then reads it again can dereference nil in
// between, and an AttachReturn that lands on the wrapper Rebuild has just
// copied from is lost for good (sing-tun attaches once per port).
//
// deviceAccess guards the load and the store of those fields, and nothing
// else. It is never held across a call on a device: a dial can take as long as
// its timeout, and Close must not wait for it.
//
// Readers take one snapshot through the load helpers and work on that; the
// writers go through the store helpers, closeTunDevice and installTunDevice.

import (
	"context"
	"net"
	"net/netip"

	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/wireguard-go/device"
)

// dialTunDevice is the tail of DialContext: one read of tunDevice, then the
// dial outside the lock. SPEC 020 level 3 — a torn-down endpoint has no tun
// device; the protocol layer rebuilds before dialing, so finding none here
// means a caller bypassed it or a Close won the race.
func (e *Endpoint) dialTunDevice(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	tunDevice := e.loadTunDevice()
	if tunDevice == nil {
		return nil, E.New("WireGuard endpoint is torn down")
	}
	return tunDevice.DialContext(ctx, network, destination)
}

// listenTunDevice is the tail of ListenPacket, see dialTunDevice.
func (e *Endpoint) listenTunDevice(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	tunDevice := e.loadTunDevice()
	if tunDevice == nil {
		return nil, E.New("WireGuard endpoint is torn down")
	}
	return tunDevice.ListenPacket(ctx, destination)
}

func (e *Endpoint) loadAllowedIPs() *device.AllowedIPs {
	e.deviceAccess.RLock()
	defer e.deviceAccess.RUnlock()
	return e.allowedIPs
}

func (e *Endpoint) loadReturnDevice() *returnDeviceWrapper {
	e.deviceAccess.RLock()
	defer e.deviceAccess.RUnlock()
	return e.returnDevice
}

// loadPortAddresses returns the cached port addresses. WritePackets uses them
// for its unreachable replies instead of asking tunDevice, which a Teardown or
// a second Close may have released by then.
func (e *Endpoint) loadPortAddresses() (netip.Addr, netip.Addr) {
	e.deviceAccess.RLock()
	defer e.deviceAccess.RUnlock()
	return e.inet4Address, e.inet6Address
}

func (e *Endpoint) loadTunDevice() Device {
	e.deviceAccess.RLock()
	defer e.deviceAccess.RUnlock()
	return e.tunDevice
}

func (e *Endpoint) loadDevice() *device.Device {
	e.deviceAccess.RLock()
	defer e.deviceAccess.RUnlock()
	return e.device
}

func (e *Endpoint) storeDevice(wgDevice *device.Device) {
	e.deviceAccess.Lock()
	e.device = wgDevice
	e.deviceAccess.Unlock()
}

func (e *Endpoint) storeAllowedIPs(allowedIPs *device.AllowedIPs) {
	e.deviceAccess.Lock()
	e.allowedIPs = allowedIPs
	e.deviceAccess.Unlock()
}

// installTunDevice publishes a freshly built tun device together with its
// return-path wrapper and port addresses. The attached L3 return path is
// carried over to the new wrapper: sing-tun attached it once (AttachReturn)
// and will not re-attach after a rebuild it knows nothing about — dropping it
// would silently break the L3 downlink. The copy and the swap happen under the
// write side, which AttachReturn and DetachReturn exclude with holdReturnDevice.
func (e *Endpoint) installTunDevice(tunDevice Device) {
	returnDevice := &returnDeviceWrapper{Device: tunDevice}
	inet4Address, inet6Address := tunDevice.Inet4Address(), tunDevice.Inet6Address()
	e.deviceAccess.Lock()
	defer e.deviceAccess.Unlock()
	if previous := e.returnDevice.state.Load(); previous != nil {
		returnDevice.state.Store(previous)
	}
	e.tunDevice = tunDevice
	e.returnDevice = returnDevice
	e.inet4Address = inet4Address
	e.inet6Address = inet6Address
}

// holdReturnDevice keeps the return-path wrapper in place until the returned
// func runs. For AttachReturn and DetachReturn, which only load and CAS the
// wrapper's state:
//
//	defer e.holdReturnDevice()()
func (e *Endpoint) holdReturnDevice() (release func()) {
	e.deviceAccess.RLock()
	return e.deviceAccess.RUnlock
}
