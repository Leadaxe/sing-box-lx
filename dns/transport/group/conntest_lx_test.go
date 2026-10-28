//go:build with_lx_command

package group

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

// lx: SPEC 139 §5.13 — a parallel group answered by one member cancels the
// request's ctx, cutting the others short. A DoT member caught in its TLS
// handshake lost the race: its `tls` stage is SKIPPED with lost_race, not
// FAILED (field run on LxBox v2.25.18-rc.3: two FAILED tls under a group step
// that ended OK).
func TestParallelLoserHandshakeLostRace_LX(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	helloSent := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// The ClientHello is in: the member is inside its handshake. Never answer.
		_, _ = conn.Read(make([]byte, 1))
		close(helloSent)
		_, _ = io.Copy(io.Discard, conn)
	}()
	tlsConfig, err := tls.NewClient(context.Background(), testLogger(), "dot.example", option.OutboundTLSOptions{Enabled: true, ServerName: "dot.example"})
	require.NoError(t, err)
	dot := transport.NewTLSRaw(testLogger(), dns.NewTransportAdapter(C.DNSTypeTLS, "dot", nil), N.SystemDialer, M.SocksaddrFromNet(listener.Addr()), tlsConfig)
	defer dot.Reset()

	group := newTestGroup(t, ModeParallel, time.Minute, 0, held("fast", helloSent))
	group.members = append(group.members, &member{tag: "dot", transport: dot})

	client := dns.NewClient(dns.ClientOptions{Context: context.Background(), Logger: testLogger()})
	trace := lxtrace.New(lxtrace.Options{})
	ctx, parent := lxtrace.Begin(lxtrace.With(context.Background(), trace), "app_dns", "example.org")
	_, err = client.Exchange(ctx, group, testQuery(), adapter.DNSQueryOptions{}, nil)
	require.NoError(t, err)
	parent.End(nil)

	// The loser's handshake ends after the answer, on the cancelled ctx.
	var handshake lxtrace.StageSnapshot
	require.Eventually(t, func() bool {
		for _, child := range parent.Children() {
			if child.Kind != "dns" {
				continue
			}
			for _, grandchild := range trace.Stage(child.ID).Children() {
				if grandchild.Kind == "tls" && grandchild.Result != lxtrace.Running {
					handshake = grandchild
					return true
				}
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)
	summary := trace.Finish(nil)
	require.Equal(t, lxtrace.Skipped, handshake.Result, "attrs = %v, error = %q", handshake.Attrs, handshake.Error)
	var lostRace bool
	for _, attr := range handshake.Attrs {
		lostRace = lostRace || attr.Key == "lost_race"
	}
	require.True(t, lostRace, "attrs = %v", handshake.Attrs)
	require.Zero(t, summary.RootCause)
}
