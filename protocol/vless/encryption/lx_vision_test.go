package encryption

import (
	"bytes"
	"net"
	"reflect"
	"testing"

	"github.com/sagernet/sing-vmess/vless"
)

// SPEC 105 §4.1: Vision accepts a *CommonConn and reads the input/rawInput
// fields Xray reads.
func TestVisionAcceptsCommonConn_LX(t *testing.T) {
	inner, peer := net.Pipe()
	defer inner.Close()
	defer peer.Close()
	commonConn := NewCommonConn(inner, false)

	if _, err := vless.NewVisionConn(commonConn, commonConn, [16]byte{}, nil); err != nil {
		t.Fatalf("NewVisionConn over *CommonConn: %v", err)
	}

	loaded, netConn, reflectType, reflectPointer := castVisionConn(commonConn)
	if !loaded {
		t.Fatal("castVisionConn did not load *CommonConn")
	}
	if netConn != inner {
		t.Fatalf("direct copy conn = %T, want the conn beneath the encryption layer", netConn)
	}
	input, ok := reflectType.FieldByName("input")
	if !ok || input.Type != reflect.TypeOf(bytes.Reader{}) {
		t.Fatal("CommonConn.input missing or not bytes.Reader")
	}
	rawInput, ok := reflectType.FieldByName("rawInput")
	if !ok || rawInput.Type != reflect.TypeOf(bytes.Buffer{}) {
		t.Fatal("CommonConn.rawInput missing or not bytes.Buffer")
	}
	if reflectPointer+input.Offset != reflect.ValueOf(&commonConn.input).Pointer() {
		t.Fatal("input offset does not point at CommonConn.input")
	}
	if reflectPointer+rawInput.Offset != reflect.ValueOf(&commonConn.rawInput).Pointer() {
		t.Fatal("rawInput offset does not point at CommonConn.rawInput")
	}
}

// SPEC 105 §4.2: the linkname target still exists with the expected shape and
// holds our entry. A sing-vmess bump that renames or reshapes the registry
// fails here instead of silently breaking Vision.
func TestVisionRegistryHoldsCommonConn_LX(t *testing.T) {
	for _, entry := range visionTLSRegistry {
		if reflect.ValueOf(entry).Pointer() == reflect.ValueOf(castVisionConn).Pointer() {
			return
		}
	}
	t.Fatal("castVisionConn is not in sing-vmess vless.tlsRegistry")
}

// A plain conn is still rejected, so the entry does not widen Vision beyond
// TLS-like conns.
func TestVisionRejectsPlainConn_LX(t *testing.T) {
	inner, peer := net.Pipe()
	defer inner.Close()
	defer peer.Close()
	if _, err := vless.NewVisionConn(inner, inner, [16]byte{}, nil); err == nil {
		t.Fatal("NewVisionConn accepted a plain conn")
	}
}
