package trusttunnel

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
)

func encodeClient(t *testing.T, source M.Socksaddr, destination M.Socksaddr, appName string, payload []byte) []byte {
	t.Helper()
	headroom := ClientFrameHeadroom(appName)
	buffer := buf.NewSize(headroom + len(payload))
	defer buffer.Release()
	buffer.Advance(headroom)
	buffer.Write(payload)
	err := EncodeClientFrame(buffer, source, destination, appName)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Clone(buffer.Bytes())
}

func encodeServer(t *testing.T, source M.Socksaddr, destination M.Socksaddr, payload []byte) []byte {
	t.Helper()
	buffer := buf.NewSize(ServerFrameHeadroom + len(payload))
	defer buffer.Release()
	buffer.Advance(ServerFrameHeadroom)
	buffer.Write(payload)
	err := EncodeServerFrame(buffer, source, destination)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Clone(buffer.Bytes())
}

func TestClientFrameLayoutIPv4(t *testing.T) {
	frame := encodeClient(t, M.ParseSocksaddr("10.0.0.1:1000"), M.ParseSocksaddr("1.2.3.4:53"), "app", []byte{0xaa, 0xbb})
	expected := []byte{
		0, 0, 0, 37 + 3 + 2,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10, 0, 0, 1, 0x03, 0xe8,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4, 0, 53,
		3, 'a', 'p', 'p',
		0xaa, 0xbb,
	}
	if !bytes.Equal(frame, expected) {
		t.Fatalf("frame mismatch:\n got %x\nwant %x", frame, expected)
	}
}

func TestServerFrameLayoutIPv6(t *testing.T) {
	frame := encodeServer(t, M.ParseSocksaddr("[2001:db8::1]:443"), M.ParseSocksaddr("[::1]:5000"), []byte{1})
	if got := binary.BigEndian.Uint32(frame); got != 36+1 {
		t.Fatalf("length %d", got)
	}
	if frame[4] != 0x20 || frame[5] != 0x01 || frame[19] != 1 {
		t.Fatalf("source address not IPv6: %x", frame[4:20])
	}
	if port := binary.BigEndian.Uint16(frame[20:]); port != 443 {
		t.Fatalf("source port %d", port)
	}
	if frame[37] != 1 || binary.BigEndian.Uint16(frame[38:]) != 5000 {
		t.Fatalf("destination mismatch: %x", frame[22:40])
	}
}

func TestClientFrameRoundTrip(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		source      M.Socksaddr
		destination M.Socksaddr
		appName     string
		payloadLen  int
	}{
		{"ipv4", M.ParseSocksaddr("192.168.1.2:4000"), M.ParseSocksaddr("8.8.8.8:53"), "sing-box-lx", 100},
		{"ipv6", M.ParseSocksaddr("[fe80::2]:4000"), M.ParseSocksaddr("[2001:4860:4860::8888]:53"), "", 1},
		{"zero-source", M.Socksaddr{}, M.ParseSocksaddr("1.1.1.1:443"), "x", 0},
		{"max", M.ParseSocksaddr("1.1.1.1:1"), M.ParseSocksaddr("2.2.2.2:2"), string(bytes.Repeat([]byte{'a'}, MaxAppNameSize)), MaxPayloadSize},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			payload := bytes.Repeat([]byte{0x5a}, testCase.payloadLen)
			frame := encodeClient(t, testCase.source, testCase.destination, testCase.appName, payload)
			reader := frameReader{reader: bytes.NewReader(frame)}
			source, destination, decoded, err := reader.ReadClientFrame()
			if err != nil {
				t.Fatal(err)
			}
			defer decoded.Release()
			expectedSource := testCase.source
			if !expectedSource.IsValid() {
				expectedSource = M.ParseSocksaddr("0.0.0.0:0")
			}
			if source != expectedSource || destination != testCase.destination {
				t.Fatalf("addresses: %s -> %s", source, destination)
			}
			if !bytes.Equal(decoded.Bytes(), payload) {
				t.Fatal("payload mismatch")
			}
		})
	}
}

func TestServerFrameRoundTrip(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(encodeServer(t, M.ParseSocksaddr("8.8.8.8:53"), M.ParseSocksaddr("10.0.0.2:999"), []byte("first")))
	stream.Write(encodeServer(t, M.ParseSocksaddr("[2001:db8::5]:53"), M.Socksaddr{}, bytes.Repeat([]byte{1}, MaxPayloadSize)))
	stream.Write(encodeServer(t, M.ParseSocksaddr("1.0.0.1:53"), M.Socksaddr{}, []byte("third")))
	reader := frameReader{reader: &stream}

	buffer := buf.NewSize(64)
	source, destination, err := reader.ReadServerFrame(buffer)
	if err != nil || source != M.ParseSocksaddr("8.8.8.8:53") || destination != M.ParseSocksaddr("10.0.0.2:999") || string(buffer.Bytes()) != "first" {
		t.Fatalf("first frame: %v %s %s %q", err, source, destination, string(buffer.Bytes()))
	}

	// A payload larger than the buffer is truncated and the stream stays in sync.
	buffer.Reset()
	source, _, err = reader.ReadServerFrame(buffer)
	if err != nil || source != M.ParseSocksaddr("[2001:db8::5]:53") || buffer.Len() != 64 {
		t.Fatalf("second frame: %v %s %d", err, source, buffer.Len())
	}
	buffer.Reset()
	_, _, err = reader.ReadServerFrame(buffer)
	if err != nil || string(buffer.Bytes()) != "third" {
		t.Fatalf("third frame: %v %q", err, string(buffer.Bytes()))
	}
	buffer.Release()

	_, _, err = reader.ReadServerFrame(buf.NewSize(16))
	if err != io.EOF {
		t.Fatalf("end of stream: %v", err)
	}
}

func TestFrameBounds(t *testing.T) {
	buffer := buf.NewSize(ClientFrameHeadroom("") + MaxPayloadSize + 1)
	buffer.Advance(ClientFrameHeadroom(""))
	buffer.Extend(MaxPayloadSize + 1)
	if err := EncodeClientFrame(buffer, M.Socksaddr{}, M.ParseSocksaddr("1.1.1.1:1"), ""); err != errFrameTooLarge {
		t.Fatalf("oversized payload: %v", err)
	}
	buffer.Release()
	if err := EncodeClientFrame(buf.NewSize(ClientFrameHeadroom("")), M.Socksaddr{}, M.ParseSocksaddrHostPort("example.com", 53), ""); err == nil {
		t.Fatal("domain destination accepted")
	}

	header := func(length uint32) []byte {
		frame := make([]byte, 4+clientFrameHeaderSize)
		binary.BigEndian.PutUint32(frame, length)
		return frame
	}
	for _, testCase := range []struct {
		name  string
		frame []byte
		err   error
	}{
		{"too-short", header(clientFrameHeaderSize - 1), errFrameTooShort},
		{"too-large", header(maxClientFrameSize + 1), errFrameTooLarge},
		{"huge", header(0xffffffff), errFrameTooLarge},
		{"app-name-past-end", func() []byte {
			frame := header(clientFrameHeaderSize + 2)
			frame[4+2*frameEndpointSize] = 10
			return frame
		}(), errFrameTooShort},
		{"truncated-header", header(clientFrameHeaderSize + 5)[:20], io.ErrUnexpectedEOF},
		{"truncated-payload", header(clientFrameHeaderSize + 5), io.ErrUnexpectedEOF},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reader := frameReader{reader: bytes.NewReader(testCase.frame)}
			_, _, payload, err := reader.ReadClientFrame()
			if err != testCase.err {
				t.Fatalf("got %v, want %v", err, testCase.err)
			}
			if payload != nil {
				t.Fatal("payload returned with an error")
			}
		})
	}

	serverHeader := make([]byte, 4)
	binary.BigEndian.PutUint32(serverHeader, maxServerFrameSize+1)
	reader := frameReader{reader: bytes.NewReader(serverHeader)}
	if _, _, err := reader.ReadServerFrame(buf.NewSize(16)); err != errFrameTooLarge {
		t.Fatalf("oversized server frame: %v", err)
	}
}

// The wire format marks IPv4 by 12 leading zero bytes, so an IPv6 address in
// ::/96 (::1 included) cannot be carried: it decodes as IPv4, on the official
// endpoint as here.
func TestFrameIPv6CompatibleAmbiguity(t *testing.T) {
	frame := encodeServer(t, M.ParseSocksaddr("[::1]:53"), M.ParseSocksaddr("[::ffff:1.2.3.4]:1"), nil)
	reader := frameReader{reader: bytes.NewReader(frame)}
	source, destination, err := reader.ReadServerFrame(buf.NewSize(16))
	if err != nil {
		t.Fatal(err)
	}
	if source != M.ParseSocksaddr("0.0.0.1:53") {
		t.Fatalf("::1 decoded as %s", source)
	}
	if destination != M.ParseSocksaddr("1.2.3.4:1") {
		t.Fatalf("IPv4-mapped address decoded as %s", destination)
	}
}
