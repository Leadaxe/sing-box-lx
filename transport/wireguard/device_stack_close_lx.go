//go:build with_gvisor

package wireguard

// lx: issue #75 — a TCP connect dies with the device.
//
// stackDevice.Close closes the stack, aborts the endpoints the stack knows at
// that moment and then waits for all of them. A connect that enters the stack
// after that pass is not aborted, so the wait lasts until that connect's own
// deadline (up to C.TCPTimeout, see connect_deadline_lx.go). Binding the
// connect to w.done makes DialTCPWithBind close its endpoint as soon as the
// device starts closing: the stack has nothing left to wait for and the
// teardown still runs to the end.
//
// Upstream 1.15 rewrites stackDevice.Close without stack.Wait(); this file and
// its call site in DialContext go away with that merge.

import (
	"context"
	"net"

	"github.com/sagernet/gvisor/pkg/tcpip"
	"github.com/sagernet/gvisor/pkg/tcpip/adapters/gonet"
)

func (w *stackDevice) dialTCPLx(ctx context.Context, localAddr, remoteAddr tcpip.FullAddress, network tcpip.NetworkProtocolNumber) (*gonet.TCPConn, error) {
	select {
	case <-w.done:
		return nil, net.ErrClosed
	default:
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-w.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return DialTCPWithBind(ctx, w.stack, localAddr, remoteAddr, network)
}
