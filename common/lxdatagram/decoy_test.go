package lxdatagram

import (
	"context"
	"net"
	"testing"

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

func dialBoth(t *testing.T, ctx context.Context) (tcp, udp *writeRecordingConn) {
	t.Helper()
	destination := M.ParseSocksaddr("1.1.1.1:443")
	tcp = &writeRecordingConn{framedConn: newFramedConn()}
	if _, err := Dialer(ctx, &recordingDialer{conn: tcp}, nil).DialContext(context.Background(), N.NetworkTCP, destination); err != nil {
		t.Fatal(err)
	}
	udp = &writeRecordingConn{framedConn: newFramedConn()}
	conn, err := Dialer(ctx, &recordingDialer{conn: udp}, nil).DialContext(context.Background(), N.NetworkUDP, destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Write([]byte{0xc0, 0, 0, 0, 1}); err != nil {
		t.Fatal(err)
	}
	return tcp, udp
}

func decoyContext(mode string) context.Context {
	return service.ContextWithPtr(context.Background(), &option.LXResolved{QUICSniffDecoy: mode})
}

// framedConn stands for a detour conn (not a kernel socket).
func TestDialerSendsSniffDecoyFirstOnUDPOnly(t *testing.T) {
	for _, mode := range []string{option.LXQUICSniffDecoyAll, option.LXQUICSniffDecoyDetour} {
		tcp, udp := dialBoth(t, decoyContext(mode))
		if len(tcp.writes) != 0 {
			t.Fatalf("%s: tcp dial must not carry a decoy: %x", mode, tcp.writes)
		}
		if len(udp.writes) != 2 || string(udp.writes[0]) != string(sniffDecoy) {
			t.Fatalf("%s: udp dial must send the decoy ahead of the first write: %x", mode, udp.writes)
		}
	}
}

func TestSniffDecoyOffByDefault(t *testing.T) {
	for _, ctx := range []context.Context{context.Background(), decoyContext(option.LXQUICSniffDecoyOff), decoyContext("")} {
		if _, udp := dialBoth(t, ctx); len(udp.writes) != 1 {
			t.Fatalf("no decoy expected: %x", udp.writes)
		}
	}
}

// "direct" and "detour" split by what lies under the QUIC stack: a kernel
// socket is the direct path, anything else is carried by another outbound.
func TestSniffDecoyAppliesByPath(t *testing.T) {
	kernel, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9})
	if err != nil {
		t.Fatal(err)
	}
	defer kernel.Close()
	detour := newFramedConn()
	cases := []struct {
		mode           string
		kernel, detour bool
	}{
		{option.LXQUICSniffDecoyOff, false, false},
		{"", false, false},
		{option.LXQUICSniffDecoyDirect, true, false},
		{option.LXQUICSniffDecoyDetour, false, true},
		{option.LXQUICSniffDecoyAll, true, true},
	}
	for _, tc := range cases {
		if got := SniffDecoyApplies(tc.mode, kernel); got != tc.kernel {
			t.Fatalf("%q on a kernel socket: got %v, want %v", tc.mode, got, tc.kernel)
		}
		if got := SniffDecoyApplies(tc.mode, detour); got != tc.detour {
			t.Fatalf("%q on a detour conn: got %v, want %v", tc.mode, got, tc.detour)
		}
	}
	if _, udp := dialBoth(t, decoyContext(option.LXQUICSniffDecoyDirect)); len(udp.writes) != 1 {
		t.Fatalf(`"direct" must not send the decoy on a detour conn: %x`, udp.writes)
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

var _ net.Conn = (*writeRecordingConn)(nil)
