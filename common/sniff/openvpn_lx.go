package sniff

// lx: SPEC 079 — OpenVPN sniffer (UDP packet + TCP stream).
//
// The first packet a client sends is P_CONTROL_HARD_RESET_CLIENT_V2 (opcode 7)
// or _V3 (opcode 10, tls-crypt-v2), key_id 0, so byte 0 is 0x38 or 0x50,
// followed by an 8-byte session id. What comes next depends on the channel
// protection, and each form is checked exactly:
//
//	plain       op sid ackN=0 pktid=0                         → len 14
//	tls-auth    op sid HMAC[H] replay=1 time ackN=0 pktid=0   → len 22+H, H ∈ known digests
//	tls-crypt   op sid replay=1 time HMAC[32] encrypted…      → len ≥ 49
//
// "replay=1": the tls-auth/tls-crypt packet-id counter starts at 1 on the
// first packet. "time" is net-time (unix seconds) and is required to be
// plausible. TCP framing puts a 2-byte big-endian length in front.

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"os"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	ovpnOpcodeHardResetClientV2 = 7
	ovpnOpcodeHardResetClientV3 = 10

	ovpnSessionIDLen = 8
	ovpnPlainLen     = 1 + ovpnSessionIDLen + 1 + 4 // 14
	ovpnTLSCryptMin  = 1 + ovpnSessionIDLen + 4 + 4 + 32
	ovpnTCPMaxLen    = 4096
)

// HMAC lengths of the digests OpenVPN accepts for --auth with tls-auth.
var ovpnHMACLens = []int{16, 20, 28, 32, 48, 64}

func ovpnTimePlausible(t uint32) bool {
	const y2010 = 1262304000
	return t >= y2010 && int64(t) <= time.Now().Unix()+86400
}

func ovpnAllZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// ovpnCheck validates one OpenVPN datagram (or one TCP frame body).
func ovpnCheck(p []byte) bool {
	if len(p) < ovpnPlainLen {
		return false
	}
	if p[0]&0x07 != 0 {
		return false
	}
	switch p[0] >> 3 {
	case ovpnOpcodeHardResetClientV2, ovpnOpcodeHardResetClientV3:
	default:
		return false
	}
	// plain: no HMAC, ack array empty, packet id 0.
	if len(p) == ovpnPlainLen {
		return p[9] == 0 && ovpnAllZero(p[10:14])
	}
	// tls-auth: HMAC then replay id 1, time, empty ack array, packet id 0.
	for _, h := range ovpnHMACLens {
		if len(p) == 22+h &&
			binary.BigEndian.Uint32(p[9+h:]) == 1 &&
			ovpnTimePlausible(binary.BigEndian.Uint32(p[13+h:])) &&
			p[17+h] == 0 && ovpnAllZero(p[18+h:22+h]) {
			return true
		}
	}
	// tls-crypt / tls-crypt-v2: replay id 1, time, 32-byte HMAC, ciphertext.
	if len(p) >= ovpnTLSCryptMin &&
		binary.BigEndian.Uint32(p[9:]) == 1 &&
		ovpnTimePlausible(binary.BigEndian.Uint32(p[13:])) {
		return true
	}
	return false
}

// OpenVPN detects an OpenVPN client hard-reset datagram.
func OpenVPN(_ context.Context, metadata *adapter.InboundContext, packet []byte) error {
	if !ovpnCheck(packet) {
		return os.ErrInvalid
	}
	metadata.Protocol = C.ProtocolOpenVPN
	return nil
}

// OpenVPNStream detects OpenVPN over TCP: a 2-byte big-endian length followed
// by the same hard-reset frame.
func OpenVPNStream(_ context.Context, metadata *adapter.InboundContext, reader io.Reader) error {
	bReader := bufio.NewReaderSize(reader, ovpnTCPMaxLen+2)
	head, err := bReader.Peek(3)
	if err != nil {
		if len(head) >= 1 && head[0] != 0 {
			return os.ErrInvalid // length prefix > 255: not a hard-reset frame
		}
		return E.Cause1(ErrNeedMoreData, err)
	}
	frameLen := int(binary.BigEndian.Uint16(head[:2]))
	if frameLen < ovpnPlainLen || frameLen > ovpnTCPMaxLen {
		return os.ErrInvalid
	}
	if op := head[2]; op != ovpnOpcodeHardResetClientV2<<3 && op != ovpnOpcodeHardResetClientV3<<3 {
		return os.ErrInvalid
	}
	frame, err := bReader.Peek(2 + frameLen)
	if err != nil {
		return E.Cause1(ErrNeedMoreData, err)
	}
	if !ovpnCheck(frame[2:]) {
		return os.ErrInvalid
	}
	metadata.Protocol = C.ProtocolOpenVPN
	return nil
}
