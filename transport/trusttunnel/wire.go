// Package trusttunnel implements the TrustTunnel protocol (AdGuard, PROTOCOL.md
// v1.0): TCP and UDP proxying through plain CONNECT requests over HTTP/2 or
// HTTP/3, with Basic proxy authorization.
//
// The package is self-contained: it depends on sing, x/net/http2 and, under the
// with_quic build tag, on quic-go and sing-quic, but on nothing from sing-box.
package trusttunnel

import (
	"encoding/binary"
	"io"
	"net/netip"

	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

// Magic :authority values. They are compared literally, without a port.
const (
	AuthorityUDP   = "_udp2"
	AuthorityCheck = "_check"
	AuthorityICMP  = "_icmp"
)

const (
	headerProxyAuthorization = "Proxy-Authorization"
	headerProxyAuthenticate  = "Proxy-Authenticate"
	headerUserAgent          = "User-Agent"
	headerWarning            = "X-Warning"
	headerDNSError           = "X-Adguard-Vpn-Error"

	proxyAuthenticateValue = "Basic realm=Authorization Required"
)

// UDP frame layout, big-endian:
//
//	client -> server: u32 len | src_ip[16] | src_port u16 | dst_ip[16] | dst_port u16 | u8 L | app_name[L] | payload
//	server -> client: u32 len | src_ip[16] | src_port u16 | dst_ip[16] | dst_port u16 | payload
//
// len does not include itself. IPv4 addresses are 12 zero bytes followed by the
// four address bytes.
const (
	frameLengthSize   = 4
	frameAddressSize  = 16
	frameEndpointSize = frameAddressSize + 2

	clientFrameHeaderSize = 2*frameEndpointSize + 1
	serverFrameHeaderSize = 2 * frameEndpointSize

	// MaxPayloadSize is the largest UDP payload a frame may carry.
	MaxPayloadSize = 65535
	// MaxAppNameSize is the largest application name a client frame may carry.
	MaxAppNameSize = 255

	maxClientFrameSize = clientFrameHeaderSize + MaxAppNameSize + MaxPayloadSize
	maxServerFrameSize = serverFrameHeaderSize + MaxPayloadSize
)

// ClientFrameHeadroom is the room a client frame needs in front of the payload.
func ClientFrameHeadroom(appName string) int {
	return frameLengthSize + clientFrameHeaderSize + len(appName)
}

// ServerFrameHeadroom is the room a server frame needs in front of the payload.
const ServerFrameHeadroom = frameLengthSize + serverFrameHeaderSize

var (
	errFrameTooLarge = E.New("trusttunnel: UDP frame too large")
	errFrameTooShort = E.New("trusttunnel: UDP frame too short")
)

// isFrameError reports a malformed frame: the stream can no longer be parsed.
func isFrameError(err error) bool {
	return err == errFrameTooLarge || err == errFrameTooShort || err == io.ErrUnexpectedEOF
}

func putEndpoint(b []byte, address M.Socksaddr) {
	clear(b[:frameAddressSize])
	addr := address.Addr.Unmap()
	if addr.Is4() {
		ip := addr.As4()
		copy(b[12:frameAddressSize], ip[:])
	} else if addr.Is6() {
		ip := addr.As16()
		copy(b[:frameAddressSize], ip[:])
	}
	binary.BigEndian.PutUint16(b[frameAddressSize:], address.Port)
}

func readEndpoint(b []byte) M.Socksaddr {
	var addr netip.Addr
	if isZero(b[:12]) {
		addr = netip.AddrFrom4([4]byte(b[12:frameAddressSize]))
	} else {
		addr = netip.AddrFrom16([16]byte(b[:frameAddressSize])).Unmap()
	}
	return M.SocksaddrFrom(addr, binary.BigEndian.Uint16(b[frameAddressSize:]))
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// EncodeClientFrame prepends a client -> server frame header to the payload held
// by buffer. The buffer must have ClientFrameHeadroom(appName) bytes of front
// headroom. Both addresses must be IP addresses (an invalid source is sent as
// zero).
func EncodeClientFrame(buffer *buf.Buffer, source M.Socksaddr, destination M.Socksaddr, appName string) error {
	if len(appName) > MaxAppNameSize {
		return E.New("trusttunnel: application name too long")
	}
	if !destination.IsIP() {
		return E.New("trusttunnel: UDP destination is not an IP address: ", destination)
	}
	payloadLen := buffer.Len()
	if payloadLen > MaxPayloadSize {
		return errFrameTooLarge
	}
	header := buffer.ExtendHeader(ClientFrameHeadroom(appName))
	binary.BigEndian.PutUint32(header, uint32(clientFrameHeaderSize+len(appName)+payloadLen))
	putEndpoint(header[frameLengthSize:], source)
	putEndpoint(header[frameLengthSize+frameEndpointSize:], destination)
	header[frameLengthSize+2*frameEndpointSize] = byte(len(appName))
	copy(header[frameLengthSize+clientFrameHeaderSize:], appName)
	return nil
}

// EncodeServerFrame prepends a server -> client frame header to the payload held
// by buffer. The buffer must have ServerFrameHeadroom bytes of front headroom.
func EncodeServerFrame(buffer *buf.Buffer, source M.Socksaddr, destination M.Socksaddr) error {
	payloadLen := buffer.Len()
	if payloadLen > MaxPayloadSize {
		return errFrameTooLarge
	}
	header := buffer.ExtendHeader(ServerFrameHeadroom)
	binary.BigEndian.PutUint32(header, uint32(serverFrameHeaderSize+payloadLen))
	putEndpoint(header[frameLengthSize:], source)
	putEndpoint(header[frameLengthSize+frameEndpointSize:], destination)
	return nil
}

// frameReader decodes frames from a stream. It is not safe for concurrent use;
// its scratch space keeps the decoding allocation-free.
type frameReader struct {
	reader  io.Reader
	scratch [frameLengthSize + clientFrameHeaderSize + MaxAppNameSize]byte
}

// readLength reads the frame length and checks it against the header size and
// the protocol bound.
func (r *frameReader) readLength(headerSize int, maxSize int) (int, error) {
	_, err := io.ReadFull(r.reader, r.scratch[:frameLengthSize])
	if err != nil {
		return 0, err
	}
	length := binary.BigEndian.Uint32(r.scratch[:frameLengthSize])
	if length < uint32(headerSize) {
		return 0, errFrameTooShort
	}
	if length > uint32(maxSize) {
		return 0, errFrameTooLarge
	}
	return int(length), nil
}

// ReadClientFrame reads one client -> server frame. The payload is returned in a
// new buffer owned by the caller; the application name is checked and dropped.
func (r *frameReader) ReadClientFrame() (source M.Socksaddr, destination M.Socksaddr, payload *buf.Buffer, err error) {
	length, err := r.readLength(clientFrameHeaderSize, maxClientFrameSize)
	if err != nil {
		return
	}
	header := r.scratch[:clientFrameHeaderSize]
	_, err = io.ReadFull(r.reader, header)
	if err != nil {
		err = unexpectedEOF(err)
		return
	}
	source = readEndpoint(header)
	destination = readEndpoint(header[frameEndpointSize:])
	appNameLen := int(header[2*frameEndpointSize])
	payloadLen := length - clientFrameHeaderSize - appNameLen
	if payloadLen < 0 {
		err = errFrameTooShort
		return
	}
	if payloadLen > MaxPayloadSize {
		err = errFrameTooLarge
		return
	}
	if appNameLen > 0 {
		_, err = io.ReadFull(r.reader, r.scratch[:appNameLen])
		if err != nil {
			err = unexpectedEOF(err)
			return
		}
	}
	payload = buf.NewSize(payloadLen)
	_, err = payload.ReadFullFrom(r.reader, payloadLen)
	if err != nil {
		err = unexpectedEOF(err)
		payload.Release()
		payload = nil
	}
	return
}

// ReadServerFrame reads one server -> client frame into buffer. A payload that
// does not fit the buffer is truncated and the rest is discarded, as a UDP socket
// does, so the stream stays in sync.
func (r *frameReader) ReadServerFrame(buffer *buf.Buffer) (source M.Socksaddr, destination M.Socksaddr, err error) {
	length, err := r.readLength(serverFrameHeaderSize, maxServerFrameSize)
	if err != nil {
		return
	}
	header := r.scratch[:serverFrameHeaderSize]
	_, err = io.ReadFull(r.reader, header)
	if err != nil {
		err = unexpectedEOF(err)
		return
	}
	source = readEndpoint(header)
	destination = readEndpoint(header[frameEndpointSize:])
	payloadLen := length - serverFrameHeaderSize
	readLen := min(payloadLen, buffer.FreeLen())
	_, err = buffer.ReadFullFrom(r.reader, readLen)
	if err != nil {
		err = unexpectedEOF(err)
		return
	}
	if discard := payloadLen - readLen; discard > 0 {
		err = unexpectedEOF(r.discard(discard))
	}
	return
}

// unexpectedEOF reports the end of the stream inside a frame as a truncated
// frame; only the end of the stream between frames is io.EOF.
func unexpectedEOF(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// ReadServerFrameTo is ReadServerFrame for a plain byte slice.
func (r *frameReader) ReadServerFrameTo(p []byte) (n int, source M.Socksaddr, err error) {
	buffer := buf.With(p)
	source, _, err = r.ReadServerFrame(buffer)
	n = buffer.Len()
	return
}

func (r *frameReader) discard(n int) error {
	for n > 0 {
		chunk := min(n, len(r.scratch))
		_, err := io.ReadFull(r.reader, r.scratch[:chunk])
		if err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}
