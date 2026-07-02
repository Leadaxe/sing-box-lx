package masque

import (
	"bytes"
	"testing"

	"github.com/sagernet/quic-go/quicvarint"
)

// encodeCapsule builds a DATAGRAM capsule (type 0) around a payload.
func encodeCapsule(payload []byte) []byte {
	frame := quicvarint.Append(nil, h2DatagramCapsuleType)
	frame = quicvarint.Append(frame, uint64(len(payload)))
	return append(frame, payload...)
}

// TestH2ReassemblyAcrossDataFrames verifies receiveDatagram reassembles a
// capsule split across multiple DATA-frame chunks, and yields multiple capsules
// packed into a single chunk.
func TestH2ReassemblyAcrossDataFrames(t *testing.T) {
	t.Parallel()

	pkt1 := bytes.Repeat([]byte{0x45, 0xAB}, 40) // 80 bytes, looks like IPv4-ish
	pkt2 := bytes.Repeat([]byte{0x60, 0xCD}, 30) // 60 bytes

	cap1 := encodeCapsule(pkt1)
	cap2 := encodeCapsule(pkt2)

	raw := &h2RawConn{recvCh: make(chan []byte, 8), closed: make(chan struct{})}
	conn := &h2IpConn{str: raw, closeChan: make(chan struct{})}

	// cap1 split across two chunks; cap2 fully inside a third chunk that also
	// carries the tail of nothing (clean boundary).
	stream := append(append([]byte{}, cap1...), cap2...)
	// feed in awkward slices: 5 bytes, then rest of cap1+start of cap2, then rest
	split1 := 5
	split2 := len(cap1) + 3
	raw.recvCh <- stream[:split1]
	raw.recvCh <- stream[split1:split2]
	raw.recvCh <- stream[split2:]
	close(raw.recvCh)

	got1, err := conn.receiveDatagram()
	if err != nil {
		t.Fatalf("first datagram: %v", err)
	}
	if !bytes.Equal(got1, pkt1) {
		t.Fatalf("first payload mismatch: got %d bytes", len(got1))
	}
	got2, err := conn.receiveDatagram()
	if err != nil {
		t.Fatalf("second datagram: %v", err)
	}
	if !bytes.Equal(got2, pkt2) {
		t.Fatalf("second payload mismatch: got %d bytes", len(got2))
	}
}

// TestH2NonZeroCapsuleSkipped ensures non-DATAGRAM capsules are skipped.
func TestH2NonZeroCapsuleSkipped(t *testing.T) {
	t.Parallel()

	// a capsule of type 1 (not DATAGRAM) followed by a real DATAGRAM
	other := quicvarint.Append(nil, 1)
	other = quicvarint.Append(other, 3)
	other = append(other, 0xDE, 0xAD, 0xBE)

	pkt := bytes.Repeat([]byte{0x45, 0x11}, 20)
	stream := append(other, encodeCapsule(pkt)...)

	raw := &h2RawConn{recvCh: make(chan []byte, 4), closed: make(chan struct{})}
	conn := &h2IpConn{str: raw, closeChan: make(chan struct{})}
	raw.recvCh <- stream
	close(raw.recvCh)

	got, err := conn.receiveDatagram()
	if err != nil {
		t.Fatalf("datagram: %v", err)
	}
	if !bytes.Equal(got, pkt) {
		t.Fatal("should skip non-DATAGRAM capsule and return the real one")
	}
}
