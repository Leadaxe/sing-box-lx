//go:build with_awg

// lx: end-to-end regression for the AmneziaWG-over-detour ClientBind fix.
//
// When an AmneziaWG endpoint runs through a detour, the WireGuard bind is our
// ClientBind (not conn.StdNetBind — that path is taken only by the DefaultDialer
// / no-detour case, see endpoint.go). Upstream ClientBind unconditionally
// stripped bytes 1-3 of every datagram (clear(b[1:4]) on receive, copy on
// Send) — those are the Cloudflare WARP "reserved" bytes. AmneziaWG 2.0 ranged
// magic headers (h1-h4) instead put a full uint32 into bytes 0-3
// (send.go RoutineEncryption: LittleEndian.PutUint32(header[0:4], magic)).
// Zeroing bytes 1-3 collapses that magic to val <= 255, which falls outside the
// configured range, so DeterminePacketTypeAndPadding returns MessageUnknownType
// and the packet is dropped — the AWG tunnel never comes up at all.
//
// This test wires two REAL wireguard-go Devices together through ClientBind on
// both ends (loopback UDP, no StdNetBind), configures ranged h1-h4 + s4 + junk
// on both, then pushes an inner IP packet through and asserts it is delivered to
// the peer's TUN within the deadline. With the pre-fix unconditional clear the
// handshake magic (h1/h2) is destroyed and delivery never happens; with the fix
// the reserved bytes are left alone for a plain-AWG (reserved == [0,0,0])
// endpoint and the packet arrives.
package wireguard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service/pause"
	"github.com/sagernet/wireguard-go/device"
	"github.com/sagernet/wireguard-go/tun"

	"golang.org/x/crypto/curve25519"
)

// ---------------------------------------------------------------------------
// loopback UDP dialer: replaces N.Dialer so ClientBind.connect() gets a real
// UDP socket bound to 127.0.0.1, connected to the peer's loopback listener.
// This is the "detour" stand-in — the point is only that the bind is ClientBind
// and NOT conn.StdNetBind.
// ---------------------------------------------------------------------------

// loopbackDialer binds ClientBind's listen socket to a FIXED loopback port so
// the two binds can address each other by a known port. We use the non-connect
// ClientBind path (isConnect == false), which routes outbound via
// PacketConn.WriteTo to the wireguard-configured endpoint addr:port — matching
// each bind's fixed listen port. (The connect path dials from an ephemeral
// source port, so neither side would ever bind the reserved ports.)
type loopbackDialer struct {
	listenPort int
}

func (loopbackDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	var d net.Dialer
	d.LocalAddr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}
	return d.DialContext(ctx, "udp", destination.String())
}

func (d loopbackDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	var lc net.ListenConfig
	return lc.ListenPacket(ctx, "udp", fmt.Sprintf("127.0.0.1:%d", d.listenPort))
}

var _ N.Dialer = loopbackDialer{}

// ---------------------------------------------------------------------------
// channelTUN: a minimal tun.Device. Read() blocks handing out inbound IP
// packets (packets we inject to be encrypted and sent to the peer); Write()
// captures decrypted inner packets the Device delivers — that is the delivery
// signal the test waits on. A leading `offset` region is reserved exactly like
// a real TUN, which the Device uses for its own headers.
// ---------------------------------------------------------------------------

type channelTUN struct {
	name     string
	mtu      int
	inbound  chan []byte // packets to hand to the Device via Read (to encrypt+send)
	outbound chan []byte // packets the Device delivered via Write (decrypted inner)
	events   chan tun.Event
	closed   chan struct{}
}

func newChannelTUN(name string, mtu int) *channelTUN {
	t := &channelTUN{
		name:     name,
		mtu:      mtu,
		inbound:  make(chan []byte, 16),
		outbound: make(chan []byte, 16),
		events:   make(chan tun.Event, 4),
		closed:   make(chan struct{}),
	}
	t.events <- tun.EventUp
	return t
}

func (t *channelTUN) File() *os.File { return nil }

func (t *channelTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case <-t.closed:
		return 0, os.ErrClosed
	case pkt := <-t.inbound:
		n := copy(bufs[0][offset:], pkt)
		sizes[0] = n
		return 1, nil
	}
}

func (t *channelTUN) Write(bufs [][]byte, offset int) (int, error) {
	for _, b := range bufs {
		if len(b) <= offset {
			continue
		}
		pkt := make([]byte, len(b)-offset)
		copy(pkt, b[offset:])
		select {
		case t.outbound <- pkt:
		case <-t.closed:
			return 0, os.ErrClosed
		default:
		}
	}
	return len(bufs), nil
}

func (t *channelTUN) MTU() (int, error)        { return t.mtu, nil }
func (t *channelTUN) Name() (string, error)    { return t.name, nil }
func (t *channelTUN) Events() <-chan tun.Event { return t.events }
func (t *channelTUN) BatchSize() int           { return 1 }

func (t *channelTUN) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
		close(t.events)
	}
	return nil
}

var _ tun.Device = (*channelTUN)(nil)

// ---------------------------------------------------------------------------
// key material
// ---------------------------------------------------------------------------

type wgKeypair struct {
	private [32]byte
	public  [32]byte
}

func genKeypair(t *testing.T) wgKeypair {
	t.Helper()
	var kp wgKeypair
	if _, err := rand.Read(kp.private[:]); err != nil {
		t.Fatalf("read random: %v", err)
	}
	// curve25519 clamping (as WireGuard does for private keys).
	kp.private[0] &= 248
	kp.private[31] &= 127
	kp.private[31] |= 64
	pub, err := curve25519.X25519(kp.private[:], curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive public key: %v", err)
	}
	copy(kp.public[:], pub)
	return kp
}

// awgObfLines are the AmneziaWG 2.0 obfuscation knobs, identical on both ends so
// the handshake magic ranges line up. h1-h4 are ranged (AWG 2.0) so the magic
// occupies the full uint32 in bytes 0-3 — exactly the bytes the buggy
// ClientBind used to zero.
const awgObfLines = "" +
	"\njc=4" +
	"\njmin=8" +
	"\njmax=80" +
	"\ns4=12" +
	"\nh1=1888111000-1888111100" +
	"\nh2=1888122000-1888122100" +
	"\nh3=1888133000-1888133100" +
	"\nh4=1888222333-1888222444"

// buildDevice constructs a real wireguard-go Device driven by ClientBind (the
// detour path). It listens on a loopback UDP port and dials the peer's port.
func buildDevice(t *testing.T, ctx context.Context, name string, tunDev tun.Device, self wgKeypair, peerPub [32]byte, listenPort int, peerAddr netip.AddrPort) (*device.Device, *ClientBind) {
	t.Helper()

	// ClientBind (the detour bind), NOT conn.StdNetBind. Non-connect mode so the
	// bind listens on a fixed loopback port and both ends can reach each other.
	// reserved stays [0,0,0]: this is plain AmneziaWG, NOT WARP — exactly the case
	// the pre-fix unconditional clear(b[1:4]) corrupted.
	bind := NewClientBind(ctx, logger.NOP(), loopbackDialer{listenPort: listenPort}, false, peerAddr, [3]uint8{})

	dev := device.NewDevice(ctx, tunDev, bind, &device.Logger{
		Verbosef: func(string, ...any) {},
		Errorf:   func(format string, args ...any) { t.Logf("["+name+"] "+format, args...) },
	}, 0)

	ipc := "private_key=" + hex.EncodeToString(self.private[:]) +
		fmt.Sprintf("\nlisten_port=%d", listenPort) +
		awgObfLines +
		"\npublic_key=" + hex.EncodeToString(peerPub[:]) +
		"\nendpoint=" + peerAddr.String() +
		"\npersistent_keepalive_interval=1" +
		"\nallowed_ip=0.0.0.0/0"

	if err := dev.IpcSet(ipc); err != nil {
		t.Fatalf("[%s] IpcSet: %v", name, err)
	}
	if err := dev.Up(); err != nil {
		t.Fatalf("[%s] device up: %v", name, err)
	}
	return dev, bind
}

// TestAwgDetourClientBindDelivers is the red/green e2e. It stands up two AWG
// Devices linked through ClientBind (loopback UDP), pushes an inner IP packet,
// and asserts delivery within 20s. See the file header for why the pre-fix
// unconditional clear(b[1:4]) makes this impossible.
func TestAwgDetourClientBindDelivers(t *testing.T) {
	// A pause manager must be in the context: ClientBind.receive/Send and the
	// Device both pull it via service.FromContext and call WaitActive().
	ctx := pause.ContextWithDefaultManager(context.Background())

	// Two loopback UDP listeners just to reserve ports; ClientBind opens its own
	// sockets via the dialer, so we only need the port numbers to be free and
	// wire each side to the other's port.
	portA := reserveLoopbackUDPPort(t)
	portB := reserveLoopbackUDPPort(t)
	addrA := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(portA))
	addrB := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(portB))

	kpA := genKeypair(t)
	kpB := genKeypair(t)

	const mtu = 1420
	tunA := newChannelTUN("wgA", mtu)
	tunB := newChannelTUN("wgB", mtu)

	// A: 10.0.0.1, B: 10.0.0.2 (allowed_ip 0.0.0.0/0 on both, so routing is trivial).
	devA, _ := buildDevice(t, ctx, "A", tunA, kpA, kpB.public, portA, addrB)
	devB, _ := buildDevice(t, ctx, "B", tunB, kpB, kpA.public, portB, addrA)
	defer devA.Close()
	defer devB.Close()
	defer tunA.Close()
	defer tunB.Close()

	// Craft an inner IPv4/UDP packet from 10.0.0.1 -> 10.0.0.2 carrying a marker.
	marker := []byte("LX-AWG-DETOUR-E2E")
	pkt := buildIPv4UDP(
		netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2"),
		4711, 4712, marker,
	)

	// Feed A's TUN so the Device encrypts it and sends it (over ClientBind) to B.
	// Resend periodically: the first datagrams race the handshake, and until the
	// handshake completes there is no keypair to encrypt transport data.
	sendDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sendDone:
				return
			default:
			}
			buf := make([]byte, len(pkt))
			copy(buf, pkt)
			select {
			case tunA.inbound <- buf:
			case <-sendDone:
				return
			}
			select {
			case <-ticker.C:
			case <-sendDone:
				return
			}
		}
	}()
	defer close(sendDone)

	deadline := time.After(20 * time.Second)
	for {
		select {
		case got := <-tunB.outbound:
			if containsMarker(got, marker) {
				return // GREEN: inner packet delivered end-to-end through ClientBind
			}
			// Ignore non-marker traffic (keepalives never reach TUN, but be safe).
		case <-deadline:
			t.Fatal("timeout: inner AWG packet was not delivered to peer TUN within 20s " +
				"(pre-fix ClientBind zeroes bytes 1-3, collapsing the ranged h1-h4 magic " +
				"below its range so the handshake/transport packets are dropped)")
		}
	}
}

// reserveLoopbackUDPPort grabs a free UDP port on loopback and releases it, so
// ClientBind's own listen socket can claim it. There is a tiny race window, but
// on loopback in a test it is not a practical problem.
func reserveLoopbackUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	_ = c.Close()
	return port
}

// buildIPv4UDP assembles a minimal IPv4 + UDP datagram (with checksums) carrying
// payload, so a real WireGuard Device routes and delivers it as a valid IP packet.
func buildIPv4UDP(src, dst netip.Addr, srcPort, dstPort uint16, payload []byte) []byte {
	udpLen := 8 + len(payload)
	totalLen := 20 + udpLen
	b := make([]byte, totalLen)

	// IPv4 header
	b[0] = 0x45 // version 4, IHL 5
	b[1] = 0x00
	putU16(b[2:], uint16(totalLen))
	putU16(b[4:], 0)  // id
	putU16(b[6:], 0)  // flags/frag
	b[8] = 64         // TTL
	b[9] = 17         // protocol UDP
	putU16(b[10:], 0) // checksum (fill below)
	copy(b[12:16], src.AsSlice())
	copy(b[16:20], dst.AsSlice())
	putU16(b[10:], ipChecksum(b[:20]))

	// UDP header
	putU16(b[20:], srcPort)
	putU16(b[22:], dstPort)
	putU16(b[24:], uint16(udpLen))
	putU16(b[26:], 0) // checksum optional for IPv4, leave 0
	copy(b[28:], payload)
	return b
}

func putU16(b []byte, v uint16) {
	b[0] = byte(v >> 8)
	b[1] = byte(v)
}

func ipChecksum(h []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(h); i += 2 {
		sum += uint32(h[i])<<8 | uint32(h[i+1])
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func containsMarker(pkt, marker []byte) bool {
	if len(pkt) < len(marker) {
		return false
	}
	for i := 0; i+len(marker) <= len(pkt); i++ {
		if string(pkt[i:i+len(marker)]) == string(marker) {
			return true
		}
	}
	return false
}
