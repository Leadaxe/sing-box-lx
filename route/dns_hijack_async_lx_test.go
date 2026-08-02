// lx:begin dns-hijack-async
// SPEC 046. HijackDNSPacket is invoked synchronously from the stack packet
// loop; these tests pin the contract that a hung DNS exchange (dead detour
// dial) neither blocks the caller nor spawns unbounded goroutines.
package route

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"

	mDNS "github.com/miekg/dns"
	"golang.org/x/sync/semaphore"
)

// blockingDNSRouter is a minimal adapter.DNSRouter whose ExchangeAsync parks
// forever, simulating the submit path stuck in ConnPool.acquireShared behind
// a dead-detour dial. Only ExchangeAsync is implemented; the rest come from
// the embedded nil interface and must never be called.
type blockingDNSRouter struct {
	adapter.DNSRouter
	calls   atomic.Int32
	release chan struct{}
}

func (r *blockingDNSRouter) ExchangeAsync(ctx context.Context, message *mDNS.Msg, options adapter.DNSQueryOptions, callback func(response *mDNS.Msg, err error)) {
	r.calls.Add(1)
	<-r.release
	callback(nil, context.Canceled)
}

func hijackTestRouter(dns adapter.DNSRouter, limit int64) *Router {
	return &Router{
		logger:       log.NewNOPFactory().NewLogger("test"),
		dns:          dns,
		dnsHijackSem: semaphore.NewWeighted(limit),
	}
}

func hijackTestQuery(t *testing.T, domain string) []byte {
	t.Helper()
	var message mDNS.Msg
	message.SetQuestion(mDNS.Fqdn(domain), mDNS.TypeA)
	payload, err := message.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// A hung exchange must not block HijackDNSPacket: before SPEC 046 the packet
// loop froze here for the full DNS timeout per unique query.
func TestHijackDNSPacket_hungExchangeDoesNotBlockCaller(t *testing.T) {
	dns := &blockingDNSRouter{release: make(chan struct{})}
	defer close(dns.release)
	r := hijackTestRouter(dns, 4)

	returned := make(chan struct{})
	go func() {
		r.HijackDNSPacket(context.Background(), hijackTestQuery(t, "example.com"), nil, adapter.InboundContext{})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("HijackDNSPacket blocked on a hung exchange")
	}
}

// Over the semaphore limit queries are dropped without calling into the DNS
// router and without blocking — the loop never waits for a free slot.
func TestHijackDNSPacket_overLimitDropsWithoutBlocking(t *testing.T) {
	dns := &blockingDNSRouter{release: make(chan struct{})}
	defer close(dns.release)
	r := hijackTestRouter(dns, 2)

	r.HijackDNSPacket(context.Background(), hijackTestQuery(t, "a.example.com"), nil, adapter.InboundContext{})
	r.HijackDNSPacket(context.Background(), hijackTestQuery(t, "b.example.com"), nil, adapter.InboundContext{})
	// Both goroutines must be parked inside ExchangeAsync before the third
	// call, otherwise it could race for a free slot.
	deadline := time.Now().Add(time.Second)
	for dns.calls.Load() != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("expected 2 in-flight exchanges, got %d", dns.calls.Load())
		}
		time.Sleep(time.Millisecond)
	}

	returned := make(chan struct{})
	go func() {
		r.HijackDNSPacket(context.Background(), hijackTestQuery(t, "c.example.com"), nil, adapter.InboundContext{})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("over-limit HijackDNSPacket blocked instead of dropping")
	}
	if got := dns.calls.Load(); got != 2 {
		t.Fatalf("over-limit query must be dropped, but ExchangeAsync was called %d times", got)
	}
}

// lx:end dns-hijack-async
