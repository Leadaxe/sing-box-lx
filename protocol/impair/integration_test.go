//go:build with_lx_impair

package impair_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/impair"
	"github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
)

// Integration: real sing-box instances with impair outbounds over loopback,
// real sockets, the real clock.

func startBox(t *testing.T, config string) *box.Box {
	t.Helper()
	ctx := include.Context(context.Background())
	options, err := json.UnmarshalExtendedContext[option.Options](ctx, []byte(config))
	if err != nil {
		t.Fatal(err)
	}
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	if err = instance.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { instance.Close() })
	return instance
}

func outboundOf(t *testing.T, instance *box.Box, tag string) adapter.Outbound {
	t.Helper()
	out, loaded := instance.Outbound().Outbound(tag)
	if !loaded {
		t.Fatal("no outbound ", tag)
	}
	return out
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// ---- servers ----

type arrivals struct {
	mu    sync.Mutex
	times []time.Time
	sizes []int
}

func (a *arrivals) add(n int) {
	a.mu.Lock()
	a.times = append(a.times, time.Now())
	a.sizes = append(a.sizes, n)
	a.mu.Unlock()
}

// rate is the bit rate between from and to after the first arrival.
func (a *arrivals) rate(from, to time.Duration) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.times) == 0 {
		return 0
	}
	start := a.times[0]
	bytes := 0
	for i, at := range a.times {
		if d := at.Sub(start); d >= from && d < to {
			bytes += a.sizes[i]
		}
	}
	return float64(bytes) * 8 / (to - from).Seconds()
}

func (a *arrivals) total() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	sum := 0
	for _, n := range a.sizes {
		sum += n
	}
	return sum
}

// maxGap is the longest pause between two arrivals.
func (a *arrivals) maxGap() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	var gap time.Duration
	for i := 1; i < len(a.times); i++ {
		gap = max(gap, a.times[i].Sub(a.times[i-1]))
	}
	return gap
}

func udpSink(t *testing.T) (*net.UDPConn, *arrivals) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	got := &arrivals{}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			got.add(n)
		}
	}()
	return conn, got
}

// udpEcho echoes datagrams; "blast" makes it send 1000-byte datagrams at
// 16 Mbit/s for 3 s back to the sender instead.
func udpEcho(t *testing.T) *net.UDPConn {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if string(buf[:n]) == "blast" {
				go blast(func(p []byte) error { _, err := conn.WriteTo(p, addr); return err }, 16e6, 3*time.Second, 1000)
				continue
			}
			conn.WriteTo(buf[:n], addr)
		}
	}()
	return conn
}

// blast sends size-byte units at rate bit/s for d, in 5 ms batches.
func blast(write func([]byte) error, rate float64, d time.Duration, size int) {
	payload := make([]byte, size)
	perTick := int(rate / 8 / float64(size) * 0.005)
	start := time.Now()
	for time.Since(start) < d {
		for range perTick {
			if write(payload) != nil {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func tcpServer(t *testing.T, handle func(net.Conn)) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				handle(conn)
			}()
		}
	}()
	return l.Addr().String()
}

func tcpSink(t *testing.T) (string, *arrivals) {
	got := &arrivals{}
	addr := tcpServer(t, func(conn net.Conn) {
		buf := make([]byte, 65536)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				got.add(n)
			}
			if err != nil {
				return
			}
		}
	})
	return addr, got
}

func tcpEcho(t *testing.T) string {
	return tcpServer(t, func(conn net.Conn) { io.Copy(conn, conn) })
}

// tcpSource writes as fast as the connection takes for 3 s.
func tcpSource(t *testing.T) string {
	return tcpServer(t, func(conn net.Conn) {
		buf := make([]byte, 32<<10)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := conn.Write(buf); err != nil {
				return
			}
		}
	})
}

func dest(addr string) M.Socksaddr { return M.ParseSocksaddr(addr) }

func checkRate(t *testing.T, what string, got, want float64) {
	t.Helper()
	t.Logf("%s: %.2f Mbit/s (want %.2f)", what, got/1e6, want/1e6)
	if got < want*0.95 || got > want*1.05 {
		t.Errorf("%s: %.2f Mbit/s, want %.2f ±5%%", what, got/1e6, want/1e6)
	}
}

// ---- tests ----

const impairOnly = `{"log": {"disabled": true}, "outbounds": [%s, {"type": "direct", "tag": "direct"}]}`

func boxWith(t *testing.T, outbounds ...string) *box.Box {
	return startBox(t, strings.Replace(impairOnly, "%s", strings.Join(outbounds, ","), 1))
}

func TestIntegrationUDPRate(t *testing.T) {
	if testing.Short() {
		t.Skip("real time")
	}
	instance := boxWith(t, `{"type": "impair", "tag": "shape", "link_params": {"rate": "8mbit", "queue": "50ms"}}`)
	out := outboundOf(t, instance, "shape")
	sink, got := udpSink(t)
	pc, err := out.ListenPacket(context.Background(), dest(sink.LocalAddr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	to := sink.LocalAddr()
	blast(func(p []byte) error { _, err := pc.WriteTo(p, to); return err }, 16e6, 3*time.Second, 1000)
	checkRate(t, "udp up", got.rate(500*time.Millisecond, 2500*time.Millisecond), 8e6)

	// down: the echo server blasts at 16 Mbit/s, the impaired reader sees 8
	echo := udpEcho(t)
	pc2, err := out.ListenPacket(context.Background(), dest(echo.LocalAddr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer pc2.Close()
	time.Sleep(200 * time.Millisecond) // the up queue of the first blast drains
	if _, err = pc2.WriteTo([]byte("blast"), echo.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	down := &arrivals{}
	buf := make([]byte, 2048)
	pc2.SetReadDeadline(time.Now().Add(4 * time.Second))
	for {
		n, _, err := pc2.ReadFrom(buf)
		if err != nil {
			break
		}
		down.add(n)
	}
	checkRate(t, "udp down", down.rate(500*time.Millisecond, 2500*time.Millisecond), 8e6)
	stats := impair.DownStats(out)
	if stats.DropQueue == 0 {
		t.Error("down: no queue drops at twice the rate")
	}
}

func TestIntegrationUDPDelayLossOutage(t *testing.T) {
	if testing.Short() {
		t.Skip("real time")
	}
	instance := boxWith(t,
		`{"type": "impair", "tag": "slow", "link_params": {"delay": "30ms"}}`,
		`{"type": "impair", "tag": "lossy", "link_params": {"up": {"loss": "10%"}}, "seed": 7}`,
		`{"type": "impair", "tag": "flaky", "start": "first_use", "schedule": [{"at": "1s", "link_params": {"outage": true}}, {"at": "2s", "link_params": {"outage": false}}]}`)
	echo := udpEcho(t)
	// delay: up and down 30 ms each
	pc, err := outboundOf(t, instance, "slow").ListenPacket(context.Background(), dest(echo.LocalAddr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	var rtts []time.Duration
	buf := make([]byte, 64)
	for range 20 {
		start := time.Now()
		pc.WriteTo([]byte("ping"), echo.LocalAddr())
		pc.SetReadDeadline(time.Now().Add(time.Second))
		if _, _, err = pc.ReadFrom(buf); err != nil {
			t.Fatal(err)
		}
		rtts = append(rtts, time.Since(start))
	}
	sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
	t.Logf("rtt median %v", rtts[10])
	if rtts[10] < 60*time.Millisecond || rtts[10] > 70*time.Millisecond {
		t.Errorf("rtt median %v, want 60 ms + a little", rtts[10])
	}

	// loss: 10 % of the up datagrams
	sink, got := udpSink(t)
	pc2, err := outboundOf(t, instance, "lossy").ListenPacket(context.Background(), dest(sink.LocalAddr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer pc2.Close()
	const sent = 5000
	for i := range sent {
		pc2.WriteTo([]byte("x"), sink.LocalAddr())
		if i%100 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	time.Sleep(200 * time.Millisecond)
	share := 1 - float64(got.total())/sent
	t.Logf("loss share %.3f", share)
	if share < 0.08 || share > 0.12 {
		t.Errorf("loss share %.3f, want 0.10", share)
	}

	// outage from 1 s to 2 s after the start: a gap of about a second
	sink2, got2 := udpSink(t)
	pc3, err := outboundOf(t, instance, "flaky").ListenPacket(context.Background(), dest(sink2.LocalAddr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer pc3.Close()
	for range 300 {
		pc3.WriteTo([]byte("x"), sink2.LocalAddr())
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	gap := got2.maxGap()
	t.Logf("udp outage gap %v, %d of 300 arrived", gap, got2.total())
	if gap < 800*time.Millisecond || gap > 1200*time.Millisecond || got2.total() > 230 || got2.total() < 150 {
		t.Errorf("outage: gap %v, %d of 300 arrived", gap, got2.total())
	}
}

func TestIntegrationTCP(t *testing.T) {
	if testing.Short() {
		t.Skip("real time")
	}
	instance := boxWith(t,
		`{"type": "impair", "tag": "shape", "link_params": {"rate": "8mbit", "queue": "50ms"}}`,
		`{"type": "impair", "tag": "slow", "link_params": {"delay": "30ms"}}`)
	out := outboundOf(t, instance, "shape")
	// up
	sinkAddr, got := tcpSink(t)
	conn, err := out.DialContext(context.Background(), "tcp", dest(sinkAddr))
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 32<<10)
	start := time.Now()
	for time.Since(start) < 3*time.Second {
		if _, err = conn.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close()
	checkRate(t, "tcp up", got.rate(500*time.Millisecond, 2500*time.Millisecond), 8e6)
	// down
	conn, err = out.DialContext(context.Background(), "tcp", dest(tcpSource(t)))
	if err != nil {
		t.Fatal(err)
	}
	down := &arrivals{}
	buf := make([]byte, 64<<10)
	conn.SetReadDeadline(time.Now().Add(4 * time.Second))
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			down.add(n)
		}
		if err != nil {
			break
		}
	}
	conn.Close()
	checkRate(t, "tcp down", down.rate(500*time.Millisecond, 2500*time.Millisecond), 8e6)
	// delay: an echo round trip is two one-way delays
	conn, err = outboundOf(t, instance, "slow").DialContext(context.Background(), "tcp", dest(tcpEcho(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var rtts []time.Duration
	for range 10 {
		start := time.Now()
		conn.Write([]byte("ping"))
		if _, err = io.ReadFull(conn, buf[:4]); err != nil {
			t.Fatal(err)
		}
		rtts = append(rtts, time.Since(start))
	}
	sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
	t.Logf("tcp rtt median %v", rtts[5])
	if rtts[5] < 60*time.Millisecond || rtts[5] > 70*time.Millisecond {
		t.Errorf("tcp rtt median %v", rtts[5])
	}
}

func TestIntegrationTCPOutageAndReset(t *testing.T) {
	if testing.Short() {
		t.Skip("real time")
	}
	instance := boxWith(t,
		`{"type": "impair", "tag": "flaky", "start": "first_use", "schedule": [{"at": "1s", "link_params": {"outage": true}}, {"at": "2s", "link_params": {"outage": false}}]}`,
		`{"type": "impair", "tag": "carrier", "start": "first_use", "schedule": [{"at": "1s", "link_params": {"outage": true, "reset": true}}, {"at": "1500ms", "link_params": {"outage": false}}]}`)
	sinkAddr, got := tcpSink(t)
	conn, err := outboundOf(t, instance, "flaky").DialContext(context.Background(), "tcp", dest(sinkAddr))
	if err != nil {
		t.Fatal(err)
	}
	sent := 0
	for range 300 {
		n, err := conn.Write([]byte("0123456789"))
		if err != nil {
			t.Fatal(err)
		}
		sent += n
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	conn.Close()
	gap := got.maxGap()
	t.Logf("tcp outage gap %v, %d of %d bytes", gap, got.total(), sent)
	if gap < 700*time.Millisecond || gap > 1200*time.Millisecond || got.total() != sent {
		t.Errorf("tcp outage: gap %v, %d of %d bytes", gap, got.total(), sent)
	}

	// reset: the connection breaks at 1 s, a dial fails until 1.5 s
	carrier := outboundOf(t, instance, "carrier")
	conn, err = carrier.DialContext(context.Background(), "tcp", dest(tcpEcho(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = conn.Read(make([]byte, 10))
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("read during reset: %v", err)
	}
	if _, err = carrier.DialContext(context.Background(), "tcp", dest(tcpEcho(t))); !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("dial during reset: %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	conn2, err := carrier.DialContext(context.Background(), "tcp", dest(tcpEcho(t)))
	if err != nil {
		t.Fatalf("dial after the outage: %v", err)
	}
	conn2.Close()
}

// The two positions in a chain, with a loopback socks proxy (the mixed
// inbound of the same instance routing to direct):
//
//	B: socks outbound "via" with detour "shape" (impair dials the proxy)
//	A: impair "over" with detour "proxy" (impair wraps the proxy)
func TestIntegrationChains(t *testing.T) {
	if testing.Short() {
		t.Skip("real time")
	}
	port := freePort(t)
	instance := startBox(t, `{
		"log": {"disabled": true},
		"inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": `+strconv.Itoa(port)+`}],
		"outbounds": [
			{"type": "impair", "tag": "shape", "link_params": {"rate": "8mbit", "delay": "20ms"}},
			{"type": "socks", "tag": "via", "server": "127.0.0.1", "server_port": `+strconv.Itoa(port)+`, "detour": "shape"},
			{"type": "socks", "tag": "proxy", "server": "127.0.0.1", "server_port": `+strconv.Itoa(port)+`},
			{"type": "impair", "tag": "over", "detour": "proxy", "link_params": {"rate": "8mbit", "delay": "20ms"}},
			{"type": "impair", "tag": "over2", "detour": "over"},
			{"type": "direct", "tag": "direct"}
		],
		"route": {"rules": [{"inbound": ["mixed-in"], "outbound": "direct"}], "final": "direct"}
	}`)
	echoTCP := tcpEcho(t)
	echoUDP := udpEcho(t)
	for _, tag := range []string{"via", "over", "over2"} {
		out := outboundOf(t, instance, tag)
		// stream: echo round trip = 2 × 20 ms
		conn, err := out.DialContext(context.Background(), "tcp", dest(echoTCP))
		if err != nil {
			t.Fatal(tag, err)
		}
		buf := make([]byte, 2048)
		start := time.Now()
		conn.Write([]byte("ping"))
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err = io.ReadFull(conn, buf[:4]); err != nil {
			t.Fatal(tag, err)
		}
		rtt := time.Since(start)
		conn.Close()
		// packet: through the proxy's UDP relay
		pc, err := out.ListenPacket(context.Background(), dest(echoUDP.LocalAddr().String()))
		if err != nil {
			t.Fatal(tag, err)
		}
		var udpRTT time.Duration
		for range 3 {
			start = time.Now()
			pc.WriteTo([]byte("ping"), echoUDP.LocalAddr())
			pc.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, _, err := pc.ReadFrom(buf)
			if err != nil || string(buf[:n]) != "ping" {
				t.Fatal(tag, " udp: ", err)
			}
			udpRTT = time.Since(start)
		}
		pc.Close()
		t.Logf("%s: tcp rtt %v, udp rtt %v", tag, rtt, udpRTT)
		if rtt < 40*time.Millisecond || udpRTT < 40*time.Millisecond {
			t.Errorf("%s: not delayed (tcp %v, udp %v)", tag, rtt, udpRTT)
		}
	}
}

// The concurrency of many connections over one outbound: one bottleneck.
func TestIntegrationSharedBottleneck(t *testing.T) {
	if testing.Short() {
		t.Skip("real time")
	}
	instance := boxWith(t, `{"type": "impair", "tag": "shape", "link_params": {"rate": "8mbit"}}`)
	out := outboundOf(t, instance, "shape")
	sinkAddr, got := tcpSink(t)
	var wg sync.WaitGroup
	var failed atomic.Bool
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := out.DialContext(context.Background(), "tcp", dest(sinkAddr))
			if err != nil {
				failed.Store(true)
				return
			}
			defer conn.Close()
			payload := make([]byte, 16<<10)
			start := time.Now()
			for time.Since(start) < 3*time.Second {
				if _, err := conn.Write(payload); err != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	if failed.Load() {
		t.Fatal("dial failed")
	}
	checkRate(t, "4 tcp flows together", got.rate(500*time.Millisecond, 2500*time.Millisecond), 8e6)
}

// Close returns at once and delivers what was written before it in the
// background, as a TCP close sends the send buffer: nothing written is lost
// in the delay line.
func TestIntegrationTCPCloseFlushes(t *testing.T) {
	if testing.Short() {
		t.Skip("real time")
	}
	instance := boxWith(t, `{"type": "impair", "tag": "slow", "link_params": {"rate": "8mbit", "queue": "1s", "delay": "300ms"}}`)
	sinkAddr, got := tcpSink(t)
	conn, err := outboundOf(t, instance, "slow").DialContext(context.Background(), "tcp", dest(sinkAddr))
	if err != nil {
		t.Fatal(err)
	}
	const size = 200 << 10
	if _, err = conn.Write(make([]byte, size)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	conn.Close()
	if took := time.Since(start); took > 50*time.Millisecond {
		t.Errorf("close took %v, want it at once", took)
	}
	if _, err = conn.Write([]byte("x")); err == nil {
		t.Error("write after close succeeded")
	}
	deadline := time.Now().Add(2 * time.Second)
	for got.total() < size && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if total := got.total(); total != size {
		t.Errorf("sink got %d bytes, want %d", total, size)
	}
}

// The runtime control methods the StartedService commands call: JSON in the
// config's schema in, the status JSON out, the same checks as the config.
func TestIntegrationControl(t *testing.T) {
	instance := boxWith(t, `{"type": "impair", "tag": "slow", "link_params": {"rate": "8mbit"}, "seed": 1}`)
	control := outboundOf(t, instance, "slow").(interface {
		ImpairSet(entry []byte) error
		ImpairSchedule(entries []byte, start string, repeat string) error
		ImpairStatus() ([]byte, error)
	})
	status := func() impair.Status {
		t.Helper()
		content, err := control.ImpairStatus()
		if err != nil {
			t.Fatal(err)
		}
		var status impair.Status
		if err = json.Unmarshal(content, &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	for _, bad := range []string{`{"at": "1s", "link_params": {"outage": true}}`, `{"link_params": {"rate": "10mbps"}}`, `{"bogus": 1}`, `{}`, `{"link_params": {"reset": true}}`, `{"rate": "1mbit"}`} {
		if err := control.ImpairSet([]byte(bad)); err == nil {
			t.Errorf("set %s accepted", bad)
		}
	}
	if err := control.ImpairSet([]byte(`{"link_params": {"down": {"outage": true}}}`)); err != nil {
		t.Fatal(err)
	}
	if st := status(); st.Tag != "slow" || st.LinkParams.Up.Outage || !st.LinkParams.Down.Outage {
		t.Fatalf("after set: %+v", st)
	}
	for _, bad := range []string{`[{"link_params": {"outage": true}}]`, `[{"at": "2s", "link_params": {"outage": true}}, {"at": "1s", "link_params": {"outage": false}}]`, `{}`} {
		if err := control.ImpairSchedule([]byte(bad), "now", ""); err == nil {
			t.Errorf("schedule %s accepted", bad)
		}
	}
	if err := control.ImpairSchedule([]byte(`[]`), "later", ""); err == nil {
		t.Error(`start "later" accepted`)
	}
	if st := status(); !st.LinkParams.Down.Outage {
		t.Fatal("a rejected schedule changed the link")
	}
	if err := control.ImpairSchedule([]byte(`[{"at": "50ms", "link_params": {"rate": "2mbit"}}]`), "now", ""); err != nil {
		t.Fatal(err)
	}
	if st := status(); st.LinkParams.Down.Outage || st.LinkParams.Up.Rate != "8mbit" || st.Schedule.Entries != 1 {
		t.Fatalf("after schedule: %+v", st)
	}
	time.Sleep(150 * time.Millisecond)
	if st := status(); st.LinkParams.Up.Rate != "2mbit" || st.Schedule.Applied != 1 {
		t.Fatalf("schedule did not run on its own: %+v", st)
	}
}

// Close is registered in the scope: stopping the box stops the outbound
// (event loop, pumps, connections), as a core restart inside one process
// needs.
func TestIntegrationBoxCloseStopsImpair(t *testing.T) {
	instance := boxWith(t, `{"type": "impair", "tag": "slow", "link_params": {"stall": {"every": "1s", "duration": "100ms"}}}`)
	out := outboundOf(t, instance, "slow")
	if impair.Stopped(out) {
		t.Fatal("stopped before close")
	}
	instance.Close()
	if !impair.Stopped(out) {
		t.Fatal("box close did not close the impair outbound")
	}
}

// A dial held by a silent outage fails at the connect timeout, as a
// handshake without an answer, instead of waiting for the outage to end.
func TestIntegrationSilentOutageDialTimeout(t *testing.T) {
	instance := boxWith(t, `{"type": "impair", "tag": "slow", "connect_timeout": "300ms"}`)
	out := outboundOf(t, instance, "slow")
	control := out.(interface{ ImpairSet(entry []byte) error })
	sinkAddr, _ := tcpSink(t)
	if err := control.ImpairSet([]byte(`{"link_params": {"outage": true}}`)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := out.DialContext(context.Background(), "tcp", dest(sinkAddr))
	took := time.Since(start)
	if !errors.Is(err, os.ErrDeadlineExceeded) || took < 250*time.Millisecond || took > time.Second {
		t.Fatalf("dial in a silent outage: %v after %v, want a timeout after 300ms", err, took)
	}
	if err = control.ImpairSet([]byte(`{"link_params": {"outage": false}}`)); err != nil {
		t.Fatal(err)
	}
	conn, err := out.DialContext(context.Background(), "tcp", dest(sinkAddr))
	if err != nil {
		t.Fatal("dial after the outage: ", err)
	}
	conn.Close()
}

// The v2 schema through the real config path (box.New): lx.impair_profiles,
// a profile with tuning, false in schedule entries (the decoder re-marshals
// outbounds and drops empty arrays and objects, so false is the only way to
// turn a thing off), and a
// sublink matched by port over loopback as a second stage.
func TestIntegrationV2Config(t *testing.T) {
	fast, slow := tcpEcho(t), tcpEcho(t)
	_, slowPort, _ := net.SplitHostPort(slow)
	config := `{"log": {"disabled": true},
	  "lx": {"impair_profiles": {
	    "home":  {"link_params": {"delay": "10ms"}},
	    "metro": {"link_params": {"profile": "lte-walk", "loss": "0%", "stall": false}}
	  }},
	  "outbounds": [
	    {"type": "impair", "tag": "line", "seed": 1,
	     "link_params": {"profile": "home"},
	     "sublinks": [{"match": {"port": [` + slowPort + `]}, "link_params": {"delay": "100ms"}}],
	     "schedule": [
	       {"at": "1h", "link_params": {"profile": "metro", "vary": false}},
	       {"at": "2h", "sublinks": false}
	     ]},
	    {"type": "direct", "tag": "direct"}
	  ]}`
	instance := startBox(t, config)
	out := outboundOf(t, instance, "line")
	status := func() impair.Status {
		t.Helper()
		content, err := out.(interface{ ImpairStatus() ([]byte, error) }).ImpairStatus()
		if err != nil {
			t.Fatal(err)
		}
		var status impair.Status
		if err = json.Unmarshal(content, &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	st := status()
	if st.LinkParams.Profile != "home" || st.LinkParams.Up.Delay != "10ms" || len(st.Sublinks) != 1 || st.Schedule.Entries != 2 {
		t.Fatalf("loaded: %+v", st)
	}
	rtt := func(addr string) time.Duration {
		t.Helper()
		conn, err := out.DialContext(context.Background(), "tcp", dest(addr))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		buf := make([]byte, 4)
		var best time.Duration
		for range 5 {
			start := time.Now()
			conn.Write([]byte("ping"))
			if _, err = io.ReadFull(conn, buf); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); best == 0 || d < best {
				best = d
			}
		}
		return best
	}
	fastRTT, slowRTT := rtt(fast), rtt(slow)
	t.Logf("rtt line only %v, line + sublink %v", fastRTT, slowRTT)
	if fastRTT < 20*time.Millisecond || fastRTT > 40*time.Millisecond || slowRTT < 220*time.Millisecond || slowRTT > 260*time.Millisecond {
		t.Errorf("rtt line only %v (want ≈20ms), line + sublink %v (want ≈220ms)", fastRTT, slowRTT)
	}
	if st = status(); st.Sublinks[0].Matched != 1 {
		t.Errorf("sublink matched %d connections, want 1", st.Sublinks[0].Matched)
	}
}

// Keys of the line outside link_params are an error: the schema has one
// place for them.
func TestIntegrationV2RejectsFlatKeys(t *testing.T) {
	ctx := include.Context(context.Background())
	_, err := json.UnmarshalExtendedContext[option.Options](ctx, []byte(`{"outbounds": [{"type": "impair", "tag": "x", "rate": "1mbit"}]}`))
	if err == nil || !strings.Contains(err.Error(), "rate") {
		t.Fatalf("flat rate accepted: %v", err)
	}
}

// false survives the config decoder in every place it turns a thing off:
// an entry that only turns vary or stall off would be dropped as empty
// otherwise, and the load would fail with "changes nothing".
func TestIntegrationFalseSurvivesDecoder(t *testing.T) {
	boxWith(t, `{"type": "impair", "tag": "x",
	  "link_params": {"vary": {"min": "1mbit", "max": "2mbit", "every": "1s"}, "stall": {"every": "1m", "duration": "1s"}},
	  "schedule": [
	    {"at": "1h", "link_params": {"vary": false}},
	    {"at": "2h", "link_params": {"stall": false}},
	    {"at": "3h", "sublinks": false}
	  ]}`)
}
