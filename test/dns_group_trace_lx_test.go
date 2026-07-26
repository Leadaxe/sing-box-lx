// lx: SPEC 035 — live trace coverage: a race through a real box against real
// (loopback) DNS servers. The racer's emitted event is a snapshot at answer
// time: the slow straggler must NOT be in it, while the state RPC picture
// (ranking) completes in the background.
package main

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dnstrack"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing/service"

	"github.com/sagernet/sing-box/log"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// nopPlatformWriter switches box.New into observable mode (needObservable),
// which is what registers the dnstrack manager — exactly how a mobile client
// runs. The messages themselves are irrelevant here.
type nopPlatformWriter struct{}

func (w nopPlatformWriter) WriteMessage(level log.Level, message string) {}

// startTestDNSServer runs a loopback UDP DNS server answering every A query
// with 1.2.3.4 after the given delay. Returns its port.
func startTestDNSServer(t *testing.T, delay time.Duration) uint16 {
	t.Helper()
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &mDNS.Server{
		PacketConn: packetConn,
		Handler: mDNS.HandlerFunc(func(w mDNS.ResponseWriter, request *mDNS.Msg) {
			if delay > 0 {
				time.Sleep(delay)
			}
			response := new(mDNS.Msg)
			response.SetReply(request)
			response.Answer = append(response.Answer, &mDNS.A{
				Hdr: mDNS.RR_Header{Name: request.Question[0].Name, Rrtype: mDNS.TypeA, Class: mDNS.ClassINET, Ttl: 60},
				A:   net.IPv4(1, 2, 3, 4),
			})
			_ = w.WriteMsg(response)
		}),
	}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })
	return uint16(netip.MustParseAddrPort(packetConn.LocalAddr().String()).Port())
}

func TestDNSGroupRaceTraceLive_LX(t *testing.T) {
	fastPort := startTestDNSServer(t, 0)
	slowPort := startTestDNSServer(t, 250*time.Millisecond)

	// Fresh context (NOT globalCtx): the dnstrack manager is registered into
	// the service registry of the ctx handed to box.New, and this test needs
	// its own instance's manager.
	ctx := include.Context(context.Background())
	options := parseOptionsLX(t, `{
		"log": {"level": "warning"},
		"dns": {
			"servers": [
				{"type": "udp", "tag": "fast", "server": "127.0.0.1", "server_port": `+strconv.Itoa(int(fastPort))+`},
				{"type": "udp", "tag": "slow", "server": "127.0.0.1", "server_port": `+strconv.Itoa(int(slowPort))+`},
				{"type": "group", "tag": "grp", "servers": ["fast", "slow"], "mode": "race", "interval": "1h"}
			],
			"final": "grp"
		},
		"outbounds": [{"type": "direct"}]
	}`)
	boxCtx, cancel := context.WithCancel(ctx)
	instance, err := box.New(box.Options{Context: boxCtx, Options: options, PlatformLogWriter: nopPlatformWriter{}})
	require.NoError(t, err)
	require.NoError(t, instance.Start())
	defer func() {
		// Let the slow straggler finish before teardown: its detached probe
		// outlives the racer on purpose (that is what the test verifies).
		time.Sleep(400 * time.Millisecond)
		instance.Close()
		cancel()
	}()

	manager := service.PtrFromContext[dnstrack.Manager](boxCtx)
	require.NotNil(t, manager, "dnstrack manager must be registered by box.New")
	subscription, subscriptionDone, err := manager.SubscribeEvents()
	require.NoError(t, err)
	defer manager.UnSubscribeEvents(subscription)

	dnsRouter := service.FromContext[adapter.DNSRouter](boxCtx)
	require.NotNil(t, dnsRouter)

	query := new(mDNS.Msg)
	query.SetQuestion("trace-live.example.", mDNS.TypeA)
	response, err := dnsRouter.Exchange(boxCtx, query, adapter.DNSQueryOptions{})
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)

	var event dnstrack.QueryEvent
	select {
	case event = <-subscription:
	case <-subscriptionDone:
		t.Fatal("subscription closed before the event arrived")
	case <-time.After(3 * time.Second):
		t.Fatal("no DNS query event within 3s")
	}

	require.Equal(t, "trace-live.example", event.Domain)
	require.True(t, event.Racer, "the first query through a race group must be the racer")
	require.Equal(t, "fast", event.DNSServer, "attribution must name the answering member")
	require.Equal(t, []string{"grp"}, event.GroupPath)
	for _, attempt := range event.Attempts {
		require.NotEqual(t, "slow", attempt.Server,
			"the straggler resolved after the answer and must not be in the emitted snapshot")
	}
	require.NotEmpty(t, event.Attempts)
	require.Equal(t, "fast", event.Attempts[0].Server)
	require.Equal(t, "answered", event.Attempts[0].Outcome)
}
