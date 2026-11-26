package wireguard

import (
	"fmt"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/wireguard-go/conn"
	"github.com/sagernet/wireguard-go/device"
)

// lx: SPEC 158 part 2, SPEC 160 — the lx.wg knobs that reach wireguard-go.
// They are global, read from the box context; an endpoint built outside a box
// (a test, a tool) gets the defaults.

func (e *Endpoint) lxTuning() option.LXWGResolved {
	return service.PtrFromContext[option.LXResolved](e.options.Context).WGOrZero()
}

// tuneStandardBind applies lx.wg.buffers.read_datagram_count to the
// endpoint's own socket. An endpoint behind a detour is not affected: it
// reads one datagram at a time from a connection its outbound keeps.
func (e *Endpoint) tuneStandardBind(bind *conn.StdNetBind) error {
	count := e.lxTuning().ReadDatagramCount
	if count == 0 || count == conn.IdealBatchSize {
		return nil
	}
	err := bind.SetReadDatagramCount(count)
	if err != nil {
		return E.Cause(err, "apply lx.wg.buffers.read_datagram_count")
	}
	return nil
}

// connectStandardBind applies lx.wg.connected_socket. It is called only for
// a bind in single-peer mode, on the same promise: one peer with a fixed
// address. The bind gives the connection up by itself whenever it is in
// doubt, and for good when it meets a second destination. Each connect and
// disconnect, with its reason, goes to the endpoint's debug log.
func (e *Endpoint) connectStandardBind(bind *conn.StdNetBind) {
	if e.lxTuning().ConnectedSocket {
		bind.SetConnectedSocket()
		bind.SetConnectedSocketLog(func(format string, args ...any) {
			e.options.Logger.Debug(fmt.Sprintf(format, args...))
		})
	}
}

// tuneDevice applies lx.wg.buffers.send_by_mtu and receive_copy (1.15 line,
// SPEC 158): send_by_mtu reads a one-packet stack straight into a buffer of
// the packet's own, receive_copy decides whether a received message leaves
// the read slab for a small buffer.
func (e *Endpoint) tuneDevice(wgDevice *device.Device) {
	tuning := e.lxTuning()
	wgDevice.SetTunReadsByMTU(tuning.SendByMTU)
	switch tuning.ReceiveCopy {
	case option.LXWGReceiveCopyAuto:
		wgDevice.SetReceiveCopy(device.ReceiveCopyAuto)
	case option.LXWGReceiveCopySpeed:
		wgDevice.SetReceiveCopy(device.ReceiveCopySpeed)
	default:
		wgDevice.SetReceiveCopy(device.ReceiveCopyMemory)
	}
}
