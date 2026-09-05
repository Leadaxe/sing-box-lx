package sniff

// lx: SPEC 079 — IKE (ISAKMP) sniffer: IKEv2 IKE_SA_INIT and IKEv1
// Main/Aggressive mode, i.e. the first packet of an IPsec negotiation.
//
// ISAKMP header (RFC 7296 §3.1 / RFC 2408 §3.1), 28 bytes:
//
//	SPIi[8] SPIr[8] NextPayload Version ExchangeType Flags MessageID[4] Length[4]
//
// Initiator's first message: SPIi ≠ 0, SPIr = 0, MessageID = 0, Length equal
// to the datagram, and
//
//	IKEv2  Version 0x20, ExchangeType 34 (IKE_SA_INIT), Flags = Initiator (0x08)
//	IKEv1  Version 0x10, ExchangeType 2 (Identity Protection) or 4 (Aggressive), Flags = 0
//
// On UDP 4500 (NAT-T) the header is preceded by a 4-byte zero "non-ESP marker".

import (
	"context"
	"encoding/binary"
	"os"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
)

const (
	ikeHeaderLen = 28

	ikeV1Version = 0x10
	ikeV2Version = 0x20

	ikeV1ExchangeIdentityProtection = 2
	ikeV1ExchangeAggressive         = 4
	ikeV2ExchangeSAInit             = 34

	ikeV2FlagInitiator = 0x08
)

// IKE detects the first packet of an IKEv2 or IKEv1 exchange.
func IKE(_ context.Context, metadata *adapter.InboundContext, packet []byte) error {
	p := packet
	// NAT-T (UDP 4500): non-ESP marker.
	if len(p) >= ikeHeaderLen+4 && ovpnAllZero(p[:4]) {
		p = p[4:]
	}
	if len(p) < ikeHeaderLen {
		return os.ErrInvalid
	}
	if ovpnAllZero(p[0:8]) || !ovpnAllZero(p[8:16]) {
		return os.ErrInvalid // SPIi must be set, SPIr must be zero
	}
	nextPayload, version, exchange, flags := p[16], p[17], p[18], p[19]
	if nextPayload == 0 {
		return os.ErrInvalid
	}
	switch version {
	case ikeV2Version:
		if exchange != ikeV2ExchangeSAInit || flags != ikeV2FlagInitiator {
			return os.ErrInvalid
		}
	case ikeV1Version:
		if (exchange != ikeV1ExchangeIdentityProtection && exchange != ikeV1ExchangeAggressive) || flags != 0 {
			return os.ErrInvalid
		}
	default:
		return os.ErrInvalid
	}
	if binary.BigEndian.Uint32(p[20:]) != 0 {
		return os.ErrInvalid // message id
	}
	if int(binary.BigEndian.Uint32(p[24:])) != len(p) {
		return os.ErrInvalid // length must cover the whole datagram
	}
	metadata.Protocol = C.ProtocolIKE
	return nil
}
