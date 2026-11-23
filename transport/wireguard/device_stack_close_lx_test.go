//go:build with_gvisor

package wireguard

// lx: issue #75 — stackDevice.Close with TCP connects entering the stack while
// it closes. No wireguard device and no network: SYNs go to an outbound queue
// nobody drains.

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

func newCloseTestStackDevice(t *testing.T) *stackDevice {
	t.Helper()
	tunDevice, err := newStackDevice(DeviceOptions{
		Context: context.Background(),
		Logger:  logger.NOP(),
		MTU:     1408,
		Address: []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tunDevice
}

// Connects already pending when Close starts are aborted by the stack itself.
func TestStackDeviceClose_pendingConnects(t *testing.T) {
	tunDevice := newCloseTestStackDevice(t)
	destination := M.ParseSocksaddr("10.9.9.9:443")
	for range 50 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = tunDevice.DialContext(ctx, "tcp", destination)
		}()
	}
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	_ = tunDevice.Close()
	if took := time.Since(start); took > time.Second {
		t.Fatalf("Close waited %v for connects that were pending before it", took)
	}
}

// A connect that enters the stack while Close runs is past the stack's abort
// pass; Close must not wait for that connect's own timeout.
func TestStackDeviceClose_connectsDuringClose(t *testing.T) {
	destination := M.ParseSocksaddr("10.9.9.9:443")
	for round := range 10 {
		tunDevice := newCloseTestStackDevice(t)
		var (
			stop  = make(chan struct{})
			group sync.WaitGroup
		)
		for range 2 {
			group.Add(1)
			go func() {
				defer group.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						_, _ = tunDevice.DialContext(ctx, "tcp", destination)
					}()
					time.Sleep(100 * time.Microsecond)
				}
			}()
		}
		time.Sleep(5 * time.Millisecond)
		time.AfterFunc(20*time.Millisecond, func() { close(stop) })
		start := time.Now()
		_ = tunDevice.Close()
		took := time.Since(start)
		group.Wait()
		if took > time.Second {
			t.Fatalf("round %d: Close waited %v for a connect started while it was closing", round, took)
		}
	}
}

// A dial into a closed device fails at once.
func TestStackDeviceDial_afterClose(t *testing.T) {
	tunDevice := newCloseTestStackDevice(t)
	_ = tunDevice.Close()
	start := time.Now()
	_, err := tunDevice.DialContext(context.Background(), "tcp", M.ParseSocksaddr("10.9.9.9:443"))
	if err == nil {
		t.Fatal("dial into a closed device must fail")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("dial into a closed device took %v", took)
	}
}
