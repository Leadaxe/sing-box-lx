// Package connectip is a vendored, lx-adapted subset of connect-ip-go
// (CONNECT-IP / RFC 9484 client side), ported onto the sagernet/quic-go fork.
//
// Upstream: https://github.com/metacubex/connect-ip-go (which in turn forked
// github.com/quic-go/connect-ip-go). We vendor it here (SPEC 021) instead of
// adding an external dependency, because the upstream fork is pinned to
// github.com/metacubex/quic-go and would pull a second, incompatible quic-go
// into the build (sing-box uses github.com/sagernet/quic-go).
//
// Only the client-facing pieces are kept: the proxied Conn (IP packets over
// HTTP Datagrams + Capsule Protocol), route/address capsules, IPv4 checksum
// recomputation and the ICMP "packet too big" helper. The server/proxy side
// (proxy.go, request.go) and the RFC-strict Dial (client.go) are intentionally
// dropped — the dialing (with Cloudflare quirks) lives in transport/masque.
//
// License: MIT (connect-ip-go). See SPEC 021 for the porting notes.
package connectip

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/quic-go/quicvarint"

	"golang.org/x/exp/slices"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// contextIDZero is the QUIC varint encoding of context ID 0, which prefixes
// every proxied IP datagram (RFC 9484 §6).
var contextIDZero = quicvarint.Append(nil, 0)

type CloseError struct {
	Remote bool
}

func (e *CloseError) Error() string        { return net.ErrClosed.Error() }
func (e *CloseError) Is(target error) bool { return target == net.ErrClosed }

type appendable interface{ append([]byte) []byte }

type writeCapsule struct {
	capsule appendable
	result  chan error
}

const (
	ipProtoICMP   = 1
	ipProtoICMPv6 = 58
)

// Http3Stream is the minimal stream surface the proxied Conn needs. Both
// *http3.Stream and *http3.RequestStream satisfy it.
type Http3Stream interface {
	io.ReadWriteCloser
	ReceiveDatagram(context.Context) ([]byte, error)
	SendDatagram([]byte) error
	CancelRead(quic.StreamErrorCode)
}

var (
	_ Http3Stream = &http3.Stream{}
	_ Http3Stream = &http3.RequestStream{}
)

// If a packet is too large to fit into a QUIC datagram, we send an ICMP Packet
// Too Big packet. On IPv6, the minimum MTU of a link is 1280 bytes.
const minMTU = 1280

// Conn is a connection that proxies IP packets over HTTP/3.
type Conn struct {
	str    Http3Stream
	writes chan writeCapsule

	assignedAddressNotify chan struct{}
	availableRoutesNotify chan struct{}

	mu                sync.Mutex
	peerAddresses     []netip.Prefix
	localRoutes       []IPRoute
	assignedAddresses []netip.Prefix
	availableRoutes   []IPRoute

	closeChan chan struct{}
	closeErr  error

	// sendBuf is a reusable scratch for the outgoing datagram (contextID + IP
	// packet). WritePacket is only ever called from the single tx pump, and
	// quic-go copies the slice into its own frame before returning
	// (connection.go SendDatagram), so one reused buffer is safe and avoids a
	// per-packet allocation on the hot path. lx: SPEC 021 B3.
	sendBuf []byte
}

// NewProxiedConn wraps an already-established CONNECT-IP request stream.
func NewProxiedConn(str Http3Stream) *Conn {
	c := &Conn{
		str:                   str,
		writes:                make(chan writeCapsule),
		assignedAddressNotify: make(chan struct{}, 1),
		availableRoutesNotify: make(chan struct{}, 1),
		closeChan:             make(chan struct{}),
	}
	go func() {
		if err := c.readFromStream(); err != nil {
			c.mu.Lock()
			if c.closeErr == nil {
				c.closeErr = &CloseError{Remote: true}
				close(c.closeChan)
			}
			c.mu.Unlock()
		}
	}()
	go func() {
		if err := c.writeToStream(); err != nil {
			c.mu.Lock()
			if c.closeErr == nil {
				c.closeErr = &CloseError{Remote: true}
				close(c.closeChan)
			}
			c.mu.Unlock()
		}
	}()
	return c
}

// AdvertiseRoute informs the peer about available routes. Only the routes from
// the most recent call are active.
func (c *Conn) AdvertiseRoute(ctx context.Context, routes []IPRoute) error {
	for _, route := range routes {
		if route.StartIP.Compare(route.EndIP) == 1 {
			return fmt.Errorf("invalid route advertising start_ip: %s larger than %s", route.StartIP, route.EndIP)
		}
	}
	c.mu.Lock()
	c.localRoutes = slices.Clone(routes)
	c.mu.Unlock()
	return c.sendCapsule(ctx, &routeAdvertisementCapsule{IPAddressRanges: routes})
}

// AssignAddresses assigns address prefixes to the peer. Only the addresses from
// the most recent call are active.
func (c *Conn) AssignAddresses(ctx context.Context, prefixes []netip.Prefix) error {
	c.mu.Lock()
	c.peerAddresses = slices.Clone(prefixes)
	c.mu.Unlock()
	capsule := &addressAssignCapsule{AssignedAddresses: make([]AssignedAddress, 0, len(prefixes))}
	for _, p := range prefixes {
		capsule.AssignedAddresses = append(capsule.AssignedAddresses, AssignedAddress{IPPrefix: p})
	}
	return c.sendCapsule(ctx, capsule)
}

func (c *Conn) sendCapsule(ctx context.Context, capsule appendable) error {
	res := make(chan error, 1)
	select {
	case c.writes <- writeCapsule{capsule: capsule, result: res}:
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-res:
			return err
		}
	case <-c.closeChan:
		return c.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LocalPrefixes returns the prefixes the peer currently assigned. The peer can
// change the assignment at any time, so call this in a loop if you care.
func (c *Conn) LocalPrefixes(ctx context.Context) ([]netip.Prefix, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closeChan:
		return nil, c.closeErr
	case <-c.assignedAddressNotify:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.assignedAddresses, nil
	}
}

// Routes returns the routes the peer currently advertised.
func (c *Conn) Routes(ctx context.Context) ([]IPRoute, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closeChan:
		return nil, c.closeErr
	case <-c.availableRoutesNotify:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.availableRoutes, nil
	}
}

func (c *Conn) readFromStream() error {
	defer c.str.Close()
	// lx: quic-go v0.61 replaced the stateless http3.ParseCapsule(r) with a
	// stateful CapsuleParser: the parser is created once and Next() advances it,
	// returning a CapsuleReader valid only until the following Next(). Our
	// parse*Capsule helpers take a quicvarint.Reader, which CapsuleReader
	// satisfies, so only the loop head changes.
	parser := http3.NewCapsuleParser(c.str)
	for {
		t, cr, err := parser.Next()
		if err != nil {
			return err
		}
		switch t {
		case capsuleTypeAddressAssign:
			capsule, err := parseAddressAssignCapsule(cr)
			if err != nil {
				return err
			}
			prefixes := make([]netip.Prefix, 0, len(capsule.AssignedAddresses))
			for _, assigned := range capsule.AssignedAddresses {
				prefixes = append(prefixes, assigned.IPPrefix)
			}
			c.mu.Lock()
			c.assignedAddresses = prefixes
			c.mu.Unlock()
			select {
			case c.assignedAddressNotify <- struct{}{}:
			default:
			}
		case capsuleTypeAddressRequest:
			if _, err := parseAddressRequestCapsule(cr); err != nil {
				return err
			}
			return errors.New("connect-ip: address request not yet supported")
		case capsuleTypeRouteAdvertisement:
			capsule, err := parseRouteAdvertisementCapsule(cr)
			if err != nil {
				return err
			}
			c.mu.Lock()
			c.availableRoutes = capsule.IPAddressRanges
			c.mu.Unlock()
			select {
			case c.availableRoutesNotify <- struct{}{}:
			default:
			}
		default:
			return fmt.Errorf("unknown capsule type: %d", t)
		}
	}
}

func (c *Conn) writeToStream() error {
	buf := make([]byte, 0, 1024)
	for {
		select {
		case <-c.closeChan:
			return c.closeErr
		case req, ok := <-c.writes:
			if !ok {
				return nil
			}
			buf = req.capsule.append(buf[:0])
			_, err := c.str.Write(buf)
			req.result <- err
			if err != nil {
				return err
			}
		}
	}
}

// ReadPacket reads a single proxied IP packet from the tunnel.
func (c *Conn) ReadPacket() (b []byte, err error) {
start:
	data, err := c.str.ReceiveDatagram(context.Background())
	if err != nil {
		select {
		case <-c.closeChan:
			return nil, c.closeErr
		default:
			return nil, err
		}
	}
	contextID, n, err := quicvarint.Parse(data)
	if err != nil {
		// A single malformed/empty datagram (unparseable context-ID varint) must
		// NOT tear down the tunnel — drop it and continue, like the sibling
		// context-ID!=0 and bad-payload cases. (lx: SPEC 021 A1. Upstream
		// connect-ip-go returns here with a "// TODO: close connection", but in
		// this integration the pump has no restart loop, so a return would
		// blackhole the outbound permanently on one bad packet.)
		goto start
	}
	if contextID != 0 {
		// We only support proxying of IP payloads (context ID 0).
		goto start
	}
	if err := c.handleIncomingProxiedPacket(data[n:]); err != nil {
		goto start
	}
	return data[n:], nil
}

func (c *Conn) handleIncomingProxiedPacket(data []byte) error {
	if len(data) == 0 {
		return errors.New("connect-ip: empty packet")
	}
	var src, dst netip.Addr
	var ipProto uint8
	version := ipVersion(data)
	switch version {
	default:
		return fmt.Errorf("connect-ip: unknown IP versions: %d", version)
	case 4:
		if len(data) < ipv4.HeaderLen {
			return fmt.Errorf("connect-ip: malformed datagram: too short")
		}
		src = netip.AddrFrom4([4]byte(data[12:16]))
		dst = netip.AddrFrom4([4]byte(data[16:20]))
		ipProto = data[9]
	case 6:
		if len(data) < ipv6.HeaderLen {
			return fmt.Errorf("connect-ip: malformed datagram: too short")
		}
		src = netip.AddrFrom16([16]byte(data[8:24]))
		dst = netip.AddrFrom16([16]byte(data[24:40]))
		ipProto = data[6]
	}

	c.mu.Lock()
	assignedAddresses := c.assignedAddresses
	localRoutes := c.localRoutes
	peerAddresses := c.peerAddresses
	c.mu.Unlock()

	if peerAddresses != nil {
		if !slices.ContainsFunc(peerAddresses, func(p netip.Prefix) bool { return p.Contains(src) }) {
			return fmt.Errorf("connect-ip: datagram source address not allowed: %s", src)
		}
	}

	var isAllowedDst bool
	if len(assignedAddresses) > 0 {
		isAllowedDst = slices.ContainsFunc(assignedAddresses, func(p netip.Prefix) bool { return p.Contains(dst) })
	}
	if !isAllowedDst {
		isICMP := (version == 4 && ipProto == ipProtoICMP) || (version == 6 && ipProto == ipProtoICMPv6)
		isAllowedDst = slices.ContainsFunc(localRoutes, func(r IPRoute) bool {
			if r.StartIP.Compare(dst) > 0 || dst.Compare(r.EndIP) > 0 {
				return false
			}
			if isICMP {
				return true
			}
			return r.IPProtocol == 0 || r.IPProtocol == ipProto
		})
	}
	if !isAllowedDst {
		return fmt.Errorf("connect-ip: datagram destination address / protocol not allowed: %s (protocol: %d)", dst, ipProto)
	}
	return nil
}

// WritePacket writes an IP packet to the stream. If the packet is too large it
// returns an ICMP "packet too big" reply the caller should feed back locally.
func (c *Conn) WritePacket(b []byte) (icmp []byte, err error) {
	// Snapshot the original header BEFORE composeDatagram mutates b in place
	// (TTL/hop-limit decrement + checksum rewrite), so a potential ICMP "packet
	// too big" reply quotes the datagram as the app sent it (RFC 1191/792).
	// lx: SPEC 021 A3.
	var origHead []byte
	if len(b) > 0 {
		snapLen := len(b)
		if snapLen > minMTU {
			snapLen = minMTU
		}
		origHead = append(origHead, b[:snapLen]...)
	}

	data, err := c.composeDatagram(b)
	if err != nil {
		return nil, nil
	}
	if data == nil {
		return nil, nil
	}
	if err := c.str.SendDatagram(data); err != nil {
		var errDTL *quic.DatagramTooLargeError
		if errors.As(err, &errDTL) {
			icmpPacket, cerr := composeICMPTooLargePacket(origHead, minMTU)
			if cerr != nil {
				return nil, nil
			}
			return icmpPacket, nil
		}
		select {
		case <-c.closeChan:
			return nil, c.closeErr
		default:
			return nil, err
		}
	}
	return nil, nil
}

func (c *Conn) composeDatagram(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	switch v := ipVersion(b); v {
	default:
		return nil, fmt.Errorf("connect-ip: unknown IP versions: %d", v)
	case 4:
		if len(b) < ipv4.HeaderLen {
			return nil, fmt.Errorf("connect-ip: IPv4 packet too short")
		}
		// IHL*4 is the real header length; recompute the checksum over it so options
		// (IHL>5) are covered (SPEC 022 #6). Reject a header length that overruns the
		// buffer before it can drive an out-of-bounds read.
		ihl := int(b[0]&0x0f) * 4
		if ihl < ipv4.HeaderLen || ihl > len(b) {
			return nil, fmt.Errorf("connect-ip: invalid IPv4 header length: %d", ihl)
		}
		ttl := b[8]
		if ttl <= 1 {
			return nil, fmt.Errorf("connect-ip: datagram TTL too small: %d", ttl)
		}
		b[8]-- // decrement TTL
		binary.BigEndian.PutUint16(b[10:12], calculateIPv4Checksum(b[:ihl]))
	case 6:
		if len(b) < ipv6.HeaderLen {
			return nil, fmt.Errorf("connect-ip: IPv6 packet too short")
		}
		hopLimit := b[7]
		if hopLimit <= 1 {
			return nil, fmt.Errorf("connect-ip: datagram Hop Limit too small: %d", hopLimit)
		}
		b[7]-- // decrement Hop Limit
	}
	// Reuse the scratch buffer: contextID (0) + IP packet. Safe because quic-go
	// copies the slice into its own datagram frame before SendDatagram returns.
	need := len(contextIDZero) + len(b)
	if cap(c.sendBuf) < need {
		c.sendBuf = make([]byte, 0, need)
	}
	data := c.sendBuf[:0]
	data = append(data, contextIDZero...)
	data = append(data, b...)
	c.sendBuf = data
	return data, nil
}

// Close tears down the tunnel.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closeErr == nil {
		c.closeErr = &CloseError{Remote: false}
		close(c.closeChan)
	}
	c.mu.Unlock()
	c.str.CancelRead(quic.StreamErrorCode(http3.ErrCodeNoError))
	return c.str.Close()
}

func ipVersion(b []byte) uint8 { return b[0] >> 4 }
