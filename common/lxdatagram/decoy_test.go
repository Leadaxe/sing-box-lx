package lxdatagram

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

type writeRecordingConn struct {
	*framedConn
	writes [][]byte
}

func (c *writeRecordingConn) Write(p []byte) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (c *writeRecordingConn) decoyFirst() bool {
	return len(c.writes) > 0 && string(c.writes[0]) == string(sniffDecoy)
}

// carriedDialer stands for a detour to an outbound of carrierType: it records
// the carrier the way the detour dialer does and hands out a framed conn.
type carriedDialer struct {
	carrierType string
	conns       []*writeRecordingConn
}

func (d *carriedDialer) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	if d.carrierType != "" {
		adapter.RecordCarrier(ctx, stubOutbound{d.carrierType})
	}
	conn := &writeRecordingConn{framedConn: newFramedConn()}
	d.conns = append(d.conns, conn)
	return conn, nil
}

func (d *carriedDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

type stubOutbound struct{ outboundType string }

func (s stubOutbound) Type() string           { return s.outboundType }
func (s stubOutbound) Tag() string            { return s.outboundType + "-node" }
func (s stubOutbound) Network() []string      { return nil }
func (s stubOutbound) Dependencies() []string { return nil }
func (s stubOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, os.ErrInvalid
}

func (s stubOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

func policy(mode string, send string, outboundTypes, detourTypes []string) option.LXQUICSniffDecoyResolved {
	resolved := option.LXQUICSniffDecoyResolved{Mode: mode, OutboundTypes: outboundTypes, DetourTypes: detourTypes, Retry: send != option.LXQUICSniffDecoySendAlways}
	if resolved.OutboundTypes == nil {
		resolved.OutboundTypes = []string{"masque"}
	}
	if resolved.DetourTypes == nil {
		resolved.DetourTypes = []string{"vless"}
	}
	return resolved
}

func decoyContext(resolved option.LXQUICSniffDecoyResolved) context.Context {
	return service.ContextWithPtr(context.Background(), &option.LXResolved{QUICSniffDecoy: resolved})
}

var errHandshake = errors.New("remote endpoint has a different public key than pinned")

// attempt runs one Begin → DialContext → Dialed → Done cycle on d through
// dialer and returns the conn and the redo verdict.
func attempt(t *testing.T, d *Decoy, dialer *carriedDialer, err error) (*writeRecordingConn, bool) {
	t.Helper()
	a := d.Begin(context.Background())
	conn, dialErr := dialer.DialContext(a.Context, N.NetworkUDP, M.ParseSocksaddr("1.1.1.1:443"))
	if dialErr != nil {
		t.Fatal(dialErr)
	}
	recording := conn.(*writeRecordingConn)
	a.Dialed(recording, recording)
	return recording, a.Done(err)
}

// The policy by path: a kernel socket is the direct path, a carried conn
// matches by the relay type, an unrecorded carrier never matches.
func TestPolicyApplies(t *testing.T) {
	kernel := Path{Kernel: true}
	vless := Path{Carrier: "vless"}
	socks := Path{Carrier: "socks"}
	unknown := Path{}
	cases := []struct {
		mode                          string
		kernel, vless, socks, unknown bool
	}{
		{option.LXQUICSniffDecoyOff, false, false, false, false},
		{"", false, false, false, false},
		{option.LXQUICSniffDecoyDirect, true, false, false, false},
		{option.LXQUICSniffDecoyDetour, false, true, false, false},
		{option.LXQUICSniffDecoyAll, true, true, false, false},
	}
	for _, tc := range cases {
		p := policy(tc.mode, "", nil, []string{"vless"})
		for _, path := range []struct {
			path Path
			want bool
		}{{kernel, tc.kernel}, {vless, tc.vless}, {socks, tc.socks}, {unknown, tc.unknown}} {
			if got := PolicyApplies(p, path.path); got != path.want {
				t.Fatalf("%q on %+v: got %v, want %v", tc.mode, path.path, got, path.want)
			}
		}
	}
	kernelConn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9})
	if err != nil {
		t.Fatal(err)
	}
	defer kernelConn.Close()
	if got := ClassifyPath(kernelConn, &adapter.CarrierRecord{Type: "vless"}); !got.Kernel || got.Carrier != "" {
		t.Fatalf("a kernel socket is the direct path whatever was recorded: %+v", got)
	}
	if got := ClassifyPath(newFramedConn(), &adapter.CarrierRecord{Type: "vless"}); got.Kernel || got.Carrier != "vless" {
		t.Fatalf("a carried conn takes the recorded carrier: %+v", got)
	}
}

// A consumer outside outbound_type, or mode off, gets no controller.
func TestNewSniffDecoyByOutboundType(t *testing.T) {
	ctx := decoyContext(policy(option.LXQUICSniffDecoyDetour, "", []string{"masque", "tuic"}, nil))
	if NewSniffDecoy(ctx, "masque", nil) == nil || NewSniffDecoy(ctx, "tuic", nil) == nil {
		t.Fatal("listed types must get a controller")
	}
	if NewSniffDecoy(ctx, "hysteria2", nil) != nil || NewSniffDecoy(ctx, "", nil) != nil {
		t.Fatal("unlisted types must not")
	}
	if NewSniffDecoy(decoyContext(policy(option.LXQUICSniffDecoyOff, "", nil, nil)), "masque", nil) != nil || NewSniffDecoy(context.Background(), "masque", nil) != nil {
		t.Fatal("mode off and no lx block must not")
	}
	var disabled *Decoy
	a := disabled.Begin(context.Background())
	conn := &writeRecordingConn{framedConn: newFramedConn()}
	if a.Dialed(conn, conn) || a.Done(errHandshake) || len(conn.writes) != 0 {
		t.Fatal("a nil controller never writes and never asks for a redo")
	}
}

// The retry rule per path: clean → armed on a fast failure (with a redo right
// away) → decoy once the byte worked → back to clean when the byte fails.
func TestRetryMemory(t *testing.T) {
	d := NewSniffDecoy(decoyContext(policy(option.LXQUICSniffDecoyDetour, "", nil, nil)), "masque", nil)
	dialer := &carriedDialer{carrierType: "vless"}
	path := Path{Carrier: "vless"}

	conn, redo := attempt(t, d, dialer, nil)
	if conn.decoyFirst() || redo || d.State(path) != "clean" {
		t.Fatalf("a clean success stays clean: decoy=%v redo=%v state=%s", conn.decoyFirst(), redo, d.State(path))
	}
	conn, redo = attempt(t, d, dialer, errHandshake)
	if conn.decoyFirst() || !redo || d.State(path) != "armed" {
		t.Fatalf("a clean fast failure arms and asks for a redo: decoy=%v redo=%v state=%s", conn.decoyFirst(), redo, d.State(path))
	}
	conn, redo = attempt(t, d, dialer, nil)
	if !conn.decoyFirst() || redo || d.State(path) != "decoy" {
		t.Fatalf("the armed attempt carries the byte and pins it on success: decoy=%v redo=%v state=%s", conn.decoyFirst(), redo, d.State(path))
	}
	conn, redo = attempt(t, d, dialer, nil)
	if !conn.decoyFirst() || redo {
		t.Fatal("pinned: every dial carries the byte")
	}
	conn, redo = attempt(t, d, dialer, errHandshake)
	if !conn.decoyFirst() || redo || d.State(path) != "clean" {
		t.Fatalf("a failure with the byte returns to clean, no redo: decoy=%v redo=%v state=%s", conn.decoyFirst(), redo, d.State(path))
	}
	conn, _ = attempt(t, d, dialer, nil)
	if conn.decoyFirst() {
		t.Fatal("back to clean: the next dial is clean")
	}

	// armed, then the byte fails too: clean again, no second redo.
	attempt(t, d, dialer, errHandshake)
	conn, redo = attempt(t, d, dialer, errHandshake)
	if !conn.decoyFirst() || redo || d.State(path) != "clean" {
		t.Fatalf("armed + failure: decoy=%v redo=%v state=%s", conn.decoyFirst(), redo, d.State(path))
	}
}

// A timeout or a dead caller context arms the memory but asks for no redo
// now: the byte goes on the next dial.
func TestRetryNoRedoOnTimeout(t *testing.T) {
	d := NewSniffDecoy(decoyContext(policy(option.LXQUICSniffDecoyDetour, "", nil, nil)), "masque", nil)
	dialer := &carriedDialer{carrierType: "vless"}
	path := Path{Carrier: "vless"}
	for _, err := range []error{context.DeadlineExceeded, &net.OpError{Err: timeoutError{}}} {
		if _, redo := attempt(t, d, dialer, err); redo || d.State(path) != "armed" {
			t.Fatalf("%v: redo=%v state=%s", err, redo, d.State(path))
		}
		d.memory.report(path.key(), true, false) // back to clean
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := d.Begin(ctx)
	conn, _ := dialer.DialContext(a.Context, N.NetworkUDP, M.ParseSocksaddr("1.1.1.1:443"))
	a.Dialed(conn, conn)
	if a.Done(errHandshake) {
		t.Fatal("a cancelled caller gets no redo")
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// The path is part of the memory key: the direct path and each relay type
// have their own state; a path the policy does not cover is never touched.
func TestRetryMemoryPerPath(t *testing.T) {
	d := NewSniffDecoy(decoyContext(policy(option.LXQUICSniffDecoyAll, "", nil, []string{"vless"})), "masque", nil)
	vless := &carriedDialer{carrierType: "vless"}
	socks := &carriedDialer{carrierType: "socks"}
	attempt(t, d, vless, errHandshake)
	if d.State(Path{Carrier: "vless"}) != "armed" || d.State(Path{Kernel: true}) != "clean" {
		t.Fatal("states are per path")
	}
	conn, redo := attempt(t, d, socks, errHandshake)
	if conn.decoyFirst() || redo || d.State(Path{Carrier: "socks"}) != "clean" {
		t.Fatal("a relay type outside detour_type is never touched")
	}
}

// send: always — every applicable dial carries the byte, no memory, no redo.
func TestSendAlways(t *testing.T) {
	d := NewSniffDecoy(decoyContext(policy(option.LXQUICSniffDecoyDetour, option.LXQUICSniffDecoySendAlways, nil, nil)), "masque", nil)
	dialer := &carriedDialer{carrierType: "vless"}
	for i := 0; i < 3; i++ {
		conn, redo := attempt(t, d, dialer, errHandshake)
		if !conn.decoyFirst() || redo {
			t.Fatalf("attempt %d: decoy=%v redo=%v", i, conn.decoyFirst(), redo)
		}
	}
	if conn, _ := attempt(t, d, &carriedDialer{carrierType: "socks"}, nil); conn.decoyFirst() {
		t.Fatal("detour_type still applies")
	}
}

// The library seam (hysteria2, tuic): the dialer cannot see the handshake, so
// it judges the previous conn at the next dial — closed within the window is
// a failed attempt, still open or long-lived is a success.
func TestDialerJudgesPreviousConn(t *testing.T) {
	ctx := decoyContext(policy(option.LXQUICSniffDecoyDetour, "", []string{"hysteria2"}, nil))
	carried := &carriedDialer{carrierType: "vless"}
	d := Dialer(ctx, "hysteria2", carried, nil)
	dial := func() net.Conn {
		conn, err := d.DialContext(context.Background(), N.NetworkUDP, M.ParseSocksaddr("1.1.1.1:443"))
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	first := dial()
	if carried.conns[0].decoyFirst() {
		t.Fatal("the first dial is clean")
	}
	first.Close() // young
	second := dial()
	if !carried.conns[1].decoyFirst() {
		t.Fatal("a conn closed young arms the next dial")
	}
	// Pretend the second conn lived long: move its dial time into the past.
	d.(*truncDialer).last.dialedAt = time.Now().Add(-youngConnWindow - time.Second)
	second.Close()
	dial()
	if !carried.conns[2].decoyFirst() {
		t.Fatal("a long-lived conn with the byte pins it")
	}
	fourth := dial() // the third conn is still open: a success
	if !carried.conns[3].decoyFirst() {
		t.Fatal("an open conn counts as a success")
	}
	fourth.Close() // young
	dial()
	if carried.conns[4].decoyFirst() {
		t.Fatal("a conn with the byte closed young returns to clean")
	}
	tcp, err := d.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("1.1.1.1:443"))
	if err != nil {
		t.Fatal(err)
	}
	if _, isAttempt := tcp.(*attemptConn); isAttempt {
		t.Fatal("tcp dials pass through")
	}
	off := Dialer(context.Background(), "hysteria2", &carriedDialer{carrierType: "vless"}, nil)
	if _, isDialer := off.(*truncDialer); !isDialer || off.(*truncDialer).decoy != nil {
		t.Fatal("no lx block: the dialer only truncates")
	}
}

// The decoy must be something a QUIC sniffer rejects outright (not a long
// header, so Xray's sniffer drops out and sing-box's records an error) and a
// QUIC server discards unparsed (fixed bit clear, too short for a stateless
// reset).
func TestSniffDecoyIsNotAQUICPacket(t *testing.T) {
	if sniffDecoy[0]&0x80 != 0 {
		t.Fatal("decoy must not be a long-header packet")
	}
	if sniffDecoy[0]&0x40 != 0 {
		t.Fatal("decoy must have the QUIC fixed bit clear")
	}
	if len(sniffDecoy) >= 21 {
		t.Fatal("decoy must stay below the smallest packet that can draw a stateless reset")
	}
}

var (
	_ net.Conn         = (*writeRecordingConn)(nil)
	_ adapter.Outbound = stubOutbound{}
)
