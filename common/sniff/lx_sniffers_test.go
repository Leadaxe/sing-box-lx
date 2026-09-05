package sniff_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/sniff"
	C "github.com/sagernet/sing-box/constant"

	"github.com/stretchr/testify/require"
)

// ---- vector builders (SPEC 079) --------------------------------------------

func fill(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i*13)
		if b[i] == 0 {
			b[i] = 1
		}
	}
	return b
}

func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

func nowBE() []byte { return be32(uint32(time.Now().Unix())) }

func ovpnPlain(op byte) []byte {
	return append(append([]byte{op}, fill(8, 0x21)...), 0, 0, 0, 0, 0)
}

func ovpnTLSAuth(op byte, hmacLen int) []byte {
	p := append([]byte{op}, fill(8, 0x31)...)
	p = append(p, fill(hmacLen, 0x41)...)
	p = append(p, be32(1)...)
	p = append(p, nowBE()...)
	return append(p, 0, 0, 0, 0, 0)
}

func ovpnTLSCrypt(op byte) []byte {
	p := append([]byte{op}, fill(8, 0x51)...)
	p = append(p, be32(1)...)
	p = append(p, nowBE()...)
	p = append(p, fill(32, 0x61)...)
	return append(p, fill(40, 0x71)...)
}

func ovpnTCP(frame []byte) []byte {
	l := make([]byte, 2)
	binary.BigEndian.PutUint16(l, uint16(len(frame)))
	return append(l, frame...)
}

func ikeHeader(version, exchange, flags, next byte, payloadLen int, natT bool) []byte {
	p := append([]byte{}, fill(8, 0x91)...) // SPIi
	p = append(p, make([]byte, 8)...)       // SPIr
	p = append(p, next, version, exchange, flags)
	p = append(p, be32(0)...)
	p = append(p, be32(uint32(28+payloadLen))...)
	p = append(p, fill(payloadLen, 0xa1)...)
	if natT {
		p = append([]byte{0, 0, 0, 0}, p...)
	}
	return p
}

func ikeV2Init(natT bool) []byte { return ikeHeader(0x20, 34, 0x08, 33, 200, natT) }
func ikeV1Main() []byte          { return ikeHeader(0x10, 2, 0, 1, 120, false) }
func ikeV1Aggr() []byte          { return ikeHeader(0x10, 4, 0, 1, 160, false) }

func tsDisco() []byte {
	p := []byte("TS\xf0\x9f\x92\xac")
	p = append(p, fill(32, 0xb1)...)
	p = append(p, fill(24, 0xc1)...)
	return append(p, fill(40, 0xd1)...)
}

const (
	sipInvite   = "INVITE sip:alice@apteka.ru SIP/2.0\r\nVia: SIP/2.0/UDP 10.0.0.2:5060;branch=z9hG4bK776\r\nMax-Forwards: 70\r\nContent-Length: 0\r\n\r\n"
	sipRegister = "REGISTER sips:registrar.Example.COM:5061;transport=tls SIP/2.0\r\nVia: SIP/2.0/TCP 10.0.0.2\r\n\r\n"
	sipIPHost   = "OPTIONS sip:1234@10.0.0.1:5060 SIP/2.0\r\n\r\n"
	sipV6Host   = "OPTIONS sip:[2001:db8::1] SIP/2.0\r\n\r\n"
	sipTel      = "INVITE tel:+15551234567 SIP/2.0\r\n\r\n"
)

var (
	wgInit      = mustHexBytes("010000006c3b96c0cdcce89a026bbdd1dbc334bab681b882ffd4b0dc4d7d12f673804d78ed74a77fe4d81705b89936b6830b48b4218ecdbdba94f7c4493ace64394e20c92b5fe21d45b186d9f3441cdb8e84982f57289f503d662f2021ac08b07104a7b848b94152ebd6da4d71dc509f9bf92898f67e88cd283a3c2aec2995bb59cb76d300000000000000000000000000000000")
	utpSYN      = mustHexBytes("410277ef0b1fb1f60000000000040000c233000000080000000000000000")
	stunBinding = mustHexBytes("000100002112a442000000000000000000000000")
	dnsQueryA   = mustHexBytes("abcd01000001000000000000" + "01610000010001") // "a" A IN
	httpGet     = []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	sshBanner   = []byte("SSH-2.0-OpenSSH_9.6\r\n")
)

func mustHexBytes(s string) []byte {
	b := make([]byte, len(s)/2)
	for i := 0; i < len(b); i++ {
		var v byte
		for j := 0; j < 2; j++ {
			c := s[i*2+j]
			switch {
			case c >= '0' && c <= '9':
				v = v<<4 | (c - '0')
			case c >= 'a' && c <= 'f':
				v = v<<4 | (c - 'a' + 10)
			}
		}
		b[i] = v
	}
	return b
}

// ---- OpenVPN ---------------------------------------------------------------

func TestSniffOpenVPN(t *testing.T) {
	t.Parallel()
	for name, pkt := range map[string][]byte{
		"plain-v2":         ovpnPlain(0x38),
		"plain-v3":         ovpnPlain(0x50),
		"tls-auth-sha1":    ovpnTLSAuth(0x38, 20),
		"tls-auth-sha256":  ovpnTLSAuth(0x38, 32),
		"tls-auth-sha512":  ovpnTLSAuth(0x38, 64),
		"tls-crypt":        ovpnTLSCrypt(0x38),
		"tls-crypt-v2":     ovpnTLSCrypt(0x50),
		"tls-crypt-v2-big": append(ovpnTLSCrypt(0x50), fill(600, 0x11)...),
	} {
		var m adapter.InboundContext
		require.NoError(t, sniff.OpenVPN(context.TODO(), &m, pkt), name)
		require.Equal(t, C.ProtocolOpenVPN, m.Protocol, name)
	}
}

func TestSniffNotOpenVPN(t *testing.T) {
	t.Parallel()
	stale := ovpnTLSCrypt(0x38)
	copy(stale[13:17], be32(946684800)) // year 2000
	replay2 := ovpnTLSCrypt(0x38)
	copy(replay2[9:13], be32(2))
	plainBadID := ovpnPlain(0x38)
	plainBadID[13] = 7
	for name, pkt := range map[string][]byte{
		"key-id-1":         append([]byte{0x39}, ovpnPlain(0x38)[1:]...),
		"wrong-opcode":     append([]byte{0x40}, ovpnPlain(0x38)[1:]...), // P_CONTROL_HARD_RESET_SERVER_V2
		"plain-bad-pktid":  plainBadID,
		"plain-len-15":     append(ovpnPlain(0x38), 0),
		"tls-crypt-stale":  stale,
		"tls-crypt-replay": replay2,
		"wireguard-init":   wgInit,
		"utp-syn":          utpSYN,
		"short":            {0x38, 1, 2},
	} {
		var m adapter.InboundContext
		require.Error(t, sniff.OpenVPN(context.TODO(), &m, pkt), name)
	}
}

func TestSniffOpenVPNStream(t *testing.T) {
	t.Parallel()
	full := ovpnTCP(ovpnTLSAuth(0x38, 20))
	var m adapter.InboundContext
	require.NoError(t, sniff.OpenVPNStream(context.TODO(), &m, bytes.NewReader(full)))
	require.Equal(t, C.ProtocolOpenVPN, m.Protocol)

	var short adapter.InboundContext
	err := sniff.OpenVPNStream(context.TODO(), &short, bytes.NewReader(full[:10]))
	require.ErrorIs(t, err, sniff.ErrNeedMoreData)

	for name, data := range map[string][]byte{
		"http":          httpGet,
		"tls-hello":     {0x16, 0x03, 0x01, 0x02, 0x00, 0x01},
		"bad-frame":     ovpnTCP(append(ovpnPlain(0x38), 9)),
		"len-too-big":   {0x20, 0x00, 0x38},
		"len-too-small": {0x00, 0x05, 0x38, 1, 2, 3, 4},
	} {
		var mm adapter.InboundContext
		err := sniff.OpenVPNStream(context.TODO(), &mm, bytes.NewReader(data))
		require.Error(t, err, name)
		require.False(t, errors.Is(err, sniff.ErrNeedMoreData), name)
	}
}

// ---- IKE -------------------------------------------------------------------

func TestSniffIKE(t *testing.T) {
	t.Parallel()
	for name, pkt := range map[string][]byte{
		"v2-sa-init":      ikeV2Init(false),
		"v2-sa-init-natt": ikeV2Init(true),
		"v1-main":         ikeV1Main(),
		"v1-aggressive":   ikeV1Aggr(),
	} {
		var m adapter.InboundContext
		require.NoError(t, sniff.IKE(context.TODO(), &m, pkt), name)
		require.Equal(t, C.ProtocolIKE, m.Protocol, name)
	}
}

func TestSniffNotIKE(t *testing.T) {
	t.Parallel()
	spiR := ikeV2Init(false)
	spiR[8] = 1
	badLen := ikeV2Init(false)
	badLen = badLen[:len(badLen)-1]
	response := ikeHeader(0x20, 34, 0x20, 33, 100, false)
	for name, pkt := range map[string][]byte{
		"spir-set":       spiR,
		"length-short":   badLen,
		"ike-auth":       ikeHeader(0x20, 35, 0x08, 46, 100, false),
		"v2-response":    response,
		"v1-quick-mode":  ikeHeader(0x10, 32, 0, 8, 100, false),
		"bad-version":    ikeHeader(0x30, 34, 0x08, 33, 100, false),
		"wireguard-init": wgInit,
		"stun":           stunBinding,
		"short":          fill(20, 1),
	} {
		var m adapter.InboundContext
		require.Error(t, sniff.IKE(context.TODO(), &m, pkt), name)
	}
}

// ---- Tailscale disco -------------------------------------------------------

func TestSniffTailscaleDisco(t *testing.T) {
	t.Parallel()
	var m adapter.InboundContext
	require.NoError(t, sniff.TailscaleDisco(context.TODO(), &m, tsDisco()))
	require.Equal(t, C.ProtocolTailscale, m.Protocol)
	for name, pkt := range map[string][]byte{
		"short":     tsDisco()[:60],
		"bad-magic": append([]byte("TS\xf0\x9f\x92\xad"), fill(100, 1)...),
		"wireguard": wgInit,
	} {
		var mm adapter.InboundContext
		require.Error(t, sniff.TailscaleDisco(context.TODO(), &mm, pkt), name)
	}
}

// ---- SIP -------------------------------------------------------------------

func TestSniffSIP(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		pkt    string
		domain string
	}{
		"invite":   {sipInvite, "apteka.ru"},
		"register": {sipRegister, "registrar.example.com"},
		"ip-host":  {sipIPHost, ""},
		"v6-host":  {sipV6Host, ""},
		"tel":      {sipTel, ""},
	} {
		var m adapter.InboundContext
		require.NoError(t, sniff.SIP(context.TODO(), &m, []byte(tc.pkt)), name)
		require.Equal(t, C.ProtocolSIP, m.Protocol, name)
		require.Equal(t, tc.domain, m.Domain, name)
	}
}

func TestSniffNotSIP(t *testing.T) {
	t.Parallel()
	for name, pkt := range map[string][]byte{
		"status-line":  []byte("SIP/2.0 100 Trying\r\n\r\n"),
		"http":         httpGet,
		"sip-1.0":      []byte("INVITE sip:a@b SIP/1.0\r\n"),
		"no-crlf":      []byte("INVITE sip:a@b SIP/2.0"),
		"bad-scheme":   []byte("INVITE http://a SIP/2.0\r\n"),
		"lower-method": []byte("invite sip:a@b SIP/2.0\r\n"),
		"wireguard":    wgInit,
		"dns":          dnsQueryA,
	} {
		var m adapter.InboundContext
		require.Error(t, sniff.SIP(context.TODO(), &m, pkt), name)
	}
}

func TestSniffSIPStream(t *testing.T) {
	t.Parallel()
	var m adapter.InboundContext
	require.NoError(t, sniff.SIPStream(context.TODO(), &m, bytes.NewReader([]byte(sipInvite))))
	require.Equal(t, C.ProtocolSIP, m.Protocol)
	require.Equal(t, "apteka.ru", m.Domain)

	var short adapter.InboundContext
	require.ErrorIs(t, sniff.SIPStream(context.TODO(), &short, bytes.NewReader([]byte("INVI"))), sniff.ErrNeedMoreData)
	var half adapter.InboundContext
	require.ErrorIs(t, sniff.SIPStream(context.TODO(), &half, bytes.NewReader([]byte("INVITE sip:alice@apteka.ru SI"))), sniff.ErrNeedMoreData)

	var http adapter.InboundContext
	err := sniff.SIPStream(context.TODO(), &http, bytes.NewReader(httpGet))
	require.Error(t, err)
	require.False(t, errors.Is(err, sniff.ErrNeedMoreData), "non-SIP prefix must fail fast")
}

// ---- cross-checks against the default order --------------------------------

// Mirrors route.defaultPacketSniffers (route/route.go) — keep in sync.
var defaultPacketOrder = []sniff.PacketSniffer{
	sniff.DomainNameQuery, sniff.QUICClientHello, sniff.STUNMessage,
	sniff.WireGuard, sniff.OpenVPN, sniff.IKE, sniff.TailscaleDisco, sniff.SIP,
	sniff.UTP, sniff.UDPTracker, sniff.DTLSRecord, sniff.NTP,
}

// Mirrors the default stream list in route.go — keep in sync.
var defaultStreamOrder = []sniff.StreamSniffer{
	sniff.TLSClientHello, sniff.HTTPHost, sniff.StreamDomainNameQuery,
	sniff.BitTorrent, sniff.SSH, sniff.RDP, sniff.SIPStream, sniff.OpenVPNStream,
}

func TestDefaultPacketOrderLX(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		pkt  []byte
		want string
	}{
		"wireguard": {wgInit, C.ProtocolWireGuard},
		"openvpn":   {ovpnTLSCrypt(0x38), C.ProtocolOpenVPN},
		"ike":       {ikeV2Init(true), C.ProtocolIKE},
		"tailscale": {tsDisco(), C.ProtocolTailscale},
		"sip":       {[]byte(sipInvite), C.ProtocolSIP},
		"utp":       {utpSYN, C.ProtocolBitTorrent},
		"stun":      {stunBinding, C.ProtocolSTUN},
		"dns":       {dnsQueryA, C.ProtocolDNS},
	} {
		var m adapter.InboundContext
		require.NoError(t, sniff.PeekPacket(context.TODO(), &m, tc.pkt, defaultPacketOrder...), name)
		require.Equal(t, tc.want, m.Protocol, name)
	}
}

func peekStreamOrdered(data []byte, sniffers ...sniff.StreamSniffer) (adapter.InboundContext, error) {
	var last error
	for _, s := range sniffers {
		var m adapter.InboundContext
		err := s(context.TODO(), &m, bytes.NewReader(data))
		if err == nil {
			return m, nil
		}
		last = err
	}
	return adapter.InboundContext{}, last
}

func TestDefaultStreamOrderLX(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		data []byte
		want string
	}{
		"http":    {httpGet, C.ProtocolHTTP},
		"ssh":     {sshBanner, C.ProtocolSSH},
		"sip":     {[]byte(sipInvite), C.ProtocolSIP},
		"openvpn": {ovpnTCP(ovpnPlain(0x38)), C.ProtocolOpenVPN},
	} {
		m, err := peekStreamOrdered(tc.data, defaultStreamOrder...)
		require.NoError(t, err, name)
		require.Equal(t, tc.want, m.Protocol, name)
	}
	_ = io.EOF
}
