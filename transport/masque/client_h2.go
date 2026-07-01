package masque

// HTTP/2 CONNECT-IP path. Adapted from mihomo's transport/masque/client_h2.go
// (itself from Diniboy1123/connect-ip-go), ported to the stdlib net/http.
//
// Unlike HTTP/3, HTTP/2 has no native datagrams: proxied IP packets are carried
// as HTTP capsule DATAGRAM frames (type 0) inside the bodies of a single
// long-lived CONNECT request/response pair. TTL/checksum handling mirrors the
// HTTP/3 path.

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/sagernet/quic-go/quicvarint"
	E "github.com/sagernet/sing/common/exceptions"
)

const h2DatagramCapsuleType uint64 = 0

const (
	ipv4HeaderLen = 20
	ipv6HeaderLen = 40
)

// ConnectTunnelH2 establishes a CONNECT-IP tunnel over HTTP/2.
func ConnectTunnelH2(ctx context.Context, profile Profile, transport *http.Transport, connectURI string) (io.Closer, IpConn, error) {
	header := http.Header{
		"User-Agent": []string{""},
	}
	if profile.H2ConnectProto != "" {
		header.Set("cf-connect-proto", profile.H2ConnectProto)
		// PQC not supported.
		header.Set("pq-enabled", "false")
	}

	client := &http.Client{Transport: transport}

	ipConn, statusOK, err := dialH2(ctx, client, connectURI, header)
	if err != nil {
		if strings.Contains(err.Error(), "tls: access denied") {
			return nil, nil, E.New("masque: login failed — verify the TLS key/cert is enrolled with Cloudflare Access")
		}
		return nil, nil, E.Cause(err, "masque: dial connect-ip over HTTP/2")
	}
	if !statusOK {
		_ = ipConn.Close()
		return nil, nil, E.New("masque: connect-ip over HTTP/2 rejected")
	}

	return closerFunc(transport.CloseIdleConnections), ipConn, nil
}

type closerFunc func()

func (f closerFunc) Close() error { f(); return nil }

func dialH2(ctx context.Context, client *http.Client, connectURI string, header http.Header) (*h2IpConn, bool, error) {
	u, err := url.Parse(connectURI)
	if err != nil {
		return nil, false, E.Cause(err, "parse connect uri")
	}

	// reqCtx must be independent of ctx: cancelling ctx would otherwise tear
	// down the whole HTTP/2 connection instead of just this stream.
	reqCtx, cancel := context.WithCancel(context.Background())

	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodConnect, u.String(), pr)
	if err != nil {
		cancel()
		_ = pr.Close()
		_ = pw.Close()
		return nil, false, E.Cause(err, "create request")
	}
	req.Host = authorityFromURL(u)
	req.ContentLength = -1
	for k, v := range header {
		req.Header[k] = v
	}

	// Bridge cancellation for the duration of the round trip only.
	stop := context.AfterFunc(ctx, cancel)
	rsp, err := client.Do(req)
	stop()
	if err != nil {
		cancel()
		_ = pr.Close()
		_ = pw.Close()
		return nil, false, E.Cause(err, "send request")
	}
	if rsp.StatusCode < 200 || rsp.StatusCode > 299 {
		cancel()
		_ = pr.Close()
		_ = pw.Close()
		_ = rsp.Body.Close()
		return nil, false, E.New("connect-ip: server responded with ", rsp.StatusCode)
	}

	stream := &h2DatagramStream{
		requestBody:  pw,
		responseBody: rsp.Body,
		cancel:       cancel,
	}
	return &h2IpConn{str: stream, closeChan: make(chan struct{})}, true, nil
}

func authorityFromURL(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	host := u.Hostname()
	if host == "" {
		return u.Host
	}
	return host + ":443"
}

type h2IpConn struct {
	str *h2DatagramStream

	mu        sync.Mutex
	closeChan chan struct{}
	closeErr  error
}

func (c *h2IpConn) ReadPacket() (b []byte, err error) {
start:
	data, err := c.str.ReceiveDatagram()
	if err != nil {
		// h2 mode has no recoverable errors; closing lets the read loop exit.
		defer func() { _ = c.Close() }()
		select {
		case <-c.closeChan:
			return nil, c.closeErr
		default:
			return nil, err
		}
	}
	if err := validateProxiedPacket(data); err != nil {
		goto start
	}
	return data, nil
}

func (c *h2IpConn) WritePacket(b []byte) (icmp []byte, err error) {
	data, err := composeH2Datagram(b)
	if err != nil {
		return nil, nil
	}
	if data == nil {
		return nil, nil
	}
	if err := c.str.SendDatagram(data); err != nil {
		select {
		case <-c.closeChan:
			return nil, c.closeErr
		default:
			return nil, err
		}
	}
	return nil, nil
}

func (c *h2IpConn) Close() error {
	c.mu.Lock()
	if c.closeErr == nil {
		c.closeErr = io.ErrClosedPipe
		close(c.closeChan)
	}
	c.mu.Unlock()
	return c.str.Close()
}

func validateProxiedPacket(data []byte) error {
	if len(data) == 0 {
		return E.New("connect-ip: empty packet")
	}
	switch v := ipVersion(data); v {
	default:
		return E.New("connect-ip: unknown IP version: ", v)
	case 4:
		if len(data) < ipv4HeaderLen {
			return E.New("connect-ip: malformed datagram: too short")
		}
	case 6:
		if len(data) < ipv6HeaderLen {
			return E.New("connect-ip: malformed datagram: too short")
		}
	}
	return nil
}

func composeH2Datagram(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	switch v := ipVersion(b); v {
	default:
		return nil, E.New("connect-ip: unknown IP version: ", v)
	case 4:
		if len(b) < ipv4HeaderLen {
			return nil, E.New("connect-ip: IPv4 packet too short")
		}
		ttl := b[8]
		if ttl <= 1 {
			return nil, E.New("connect-ip: datagram TTL too small")
		}
		b[8]--
		binary.BigEndian.PutUint16(b[10:12], ipv4Checksum(b[:ipv4HeaderLen]))
	case 6:
		if len(b) < ipv6HeaderLen {
			return nil, E.New("connect-ip: IPv6 packet too short")
		}
		hopLimit := b[7]
		if hopLimit <= 1 {
			return nil, E.New("connect-ip: datagram Hop Limit too small")
		}
		b[7]--
	}
	return b, nil
}

func ipVersion(b []byte) uint8 { return b[0] >> 4 }

func ipv4Checksum(header []byte) uint16 {
	var sum uint32
	for i := 0; i < ipv4HeaderLen; i += 2 {
		if i == 10 {
			continue
		}
		sum += uint32(binary.BigEndian.Uint16(header[i : i+2]))
	}
	for (sum >> 16) > 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// h2DatagramStream carries capsule DATAGRAM frames over the CONNECT body pipes.
type h2DatagramStream struct {
	requestBody  *io.PipeWriter
	responseBody io.ReadCloser
	cancel       context.CancelFunc

	readMu  sync.Mutex
	writeMu sync.Mutex
}

func (s *h2DatagramStream) ReceiveDatagram() ([]byte, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()

	reader := quicvarint.NewReader(s.responseBody)
	for {
		capsuleType, err := quicvarint.Read(reader)
		if err != nil {
			return nil, err
		}
		payloadLen, err := quicvarint.Read(reader)
		if err != nil {
			return nil, err
		}
		payload := make([]byte, payloadLen)
		if _, err = io.ReadFull(reader, payload); err != nil {
			return nil, err
		}
		if capsuleType != h2DatagramCapsuleType {
			continue
		}
		return payload, nil
	}
}

func (s *h2DatagramStream) SendDatagram(data []byte) error {
	frame := make([]byte, 0, quicvarint.Len(h2DatagramCapsuleType)+quicvarint.Len(uint64(len(data)))+len(data))
	frame = quicvarint.Append(frame, h2DatagramCapsuleType)
	frame = quicvarint.Append(frame, uint64(len(data)))
	frame = append(frame, data...)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.requestBody.Write(frame); err != nil {
		return E.Cause(err, "send datagram capsule")
	}
	return nil
}

func (s *h2DatagramStream) Close() error {
	_ = s.requestBody.Close()
	err := s.responseBody.Close()
	s.cancel()
	return err
}
