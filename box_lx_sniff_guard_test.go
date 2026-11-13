package box

import (
	"encoding/pem"
	"testing"

	"github.com/sagernet/sing-box/common/lxsniffguard"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/cryptobyte"
)

func sniffGuardTestECHConfig(t *testing.T, publicName string) []string {
	t.Helper()
	builder := cryptobyte.NewBuilder(nil)
	builder.AddUint16LengthPrefixed(func(list *cryptobyte.Builder) {
		list.AddUint16(0xfe0d)
		list.AddUint16LengthPrefixed(func(contents *cryptobyte.Builder) {
			contents.AddUint8(1)
			contents.AddUint16(0x0020)
			contents.AddUint16LengthPrefixed(func(key *cryptobyte.Builder) { key.AddBytes(make([]byte, 32)) })
			contents.AddUint16LengthPrefixed(func(suites *cryptobyte.Builder) { suites.AddUint16(1); suites.AddUint16(1) })
			contents.AddUint8(0)
			contents.AddUint8LengthPrefixed(func(name *cryptobyte.Builder) { name.AddBytes([]byte(publicName)) })
			contents.AddUint16(0)
		})
	})
	configList, err := builder.Bytes()
	require.NoError(t, err)
	return []string{string(pem.EncodeToMemory(&pem.Block{Type: "ECH CONFIGS", Bytes: configList}))}
}

// lx: SPEC 060 — node facts against the SNI table of FEATURE 019.
func TestSniffGuardNodeInfo(t *testing.T) {
	server := func(host string) option.ServerOptions { return option.ServerOptions{Server: host, ServerPort: 443} }
	tlsOn := func(serverName string) option.OutboundTLSOptionsContainer {
		return option.OutboundTLSOptionsContainer{TLS: &option.OutboundTLSOptions{Enabled: true, ServerName: serverName}}
	}
	reality := tlsOn("www.microsoft.com")
	reality.TLS.Reality = &option.OutboundRealityOptions{Enabled: true}
	disableSNI := tlsOn("cover.example")
	disableSNI.TLS.DisableSNI = true
	appleEngine := tlsOn("")
	appleEngine.TLS.Engine = "apple"
	echOwn := tlsOn("inner.example")
	echOwn.TLS.ECH = &option.OutboundECHOptions{Enabled: true, Config: sniffGuardTestECHConfig(t, "node.example")}
	echCover := tlsOn("node.example")
	echCover.TLS.ECH = &option.OutboundECHOptions{Enabled: true, Config: sniffGuardTestECHConfig(t, "public.cdn.example")}
	echDNS := tlsOn("cover.example")
	echDNS.TLS.ECH = &option.OutboundECHOptions{Enabled: true}

	for _, testCase := range []struct {
		name     string
		typ      string
		options  any
		covered  bool
		mismatch bool
	}{
		{"domain, no sni", C.TypeVLESS, &option.VLESSOutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: tlsOn("")}, true, false},
		{"domain, same sni", C.TypeTrojan, &option.TrojanOutboundOptions{ServerOptions: server("Node.Example."), OutboundTLSOptionsContainer: tlsOn("node.example")}, true, false},
		{"domain, other sni", C.TypeVMess, &option.VMessOutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: tlsOn("cover.example")}, true, true},
		{"subdomain is another name", C.TypeVLESS, &option.VLESSOutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: tlsOn("www.node.example")}, true, true},
		{"ip, sni", C.TypeVLESS, &option.VLESSOutboundOptions{ServerOptions: server("203.0.113.1"), OutboundTLSOptionsContainer: tlsOn("node.example")}, true, true},
		{"ip, no sni", C.TypeAnyTLS, &option.AnyTLSOutboundOptions{ServerOptions: server("203.0.113.1"), OutboundTLSOptionsContainer: tlsOn("")}, true, false},
		{"disable_sni", C.TypeVLESS, &option.VLESSOutboundOptions{ServerOptions: server("203.0.113.1"), OutboundTLSOptionsContainer: disableSNI}, true, false},
		{"reality", C.TypeVLESS, &option.VLESSOutboundOptions{ServerOptions: server("203.0.113.1"), OutboundTLSOptionsContainer: reality}, true, true},
		{"ech public name is the node", C.TypeVLESS, &option.VLESSOutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: echOwn}, true, false},
		{"ech public name is a cover", C.TypeVLESS, &option.VLESSOutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: echCover}, true, true},
		{"ech over dns falls back to server_name", C.TypeVLESS, &option.VLESSOutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: echDNS}, true, true},
		{"vless without tls", C.TypeVLESS, &option.VLESSOutboundOptions{ServerOptions: server("node.example")}, false, false},
		{"http without tls", C.TypeHTTP, &option.HTTPOutboundOptions{ServerOptions: server("node.example")}, false, false},
		{"http with tls", C.TypeHTTP, &option.HTTPOutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: tlsOn("cover.example")}, true, true},
		{"system engine", C.TypeShadowTLS, &option.ShadowTLSOutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: appleEngine}, false, false},
		{"shadowtls cover", C.TypeShadowTLS, &option.ShadowTLSOutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: tlsOn("www.apple.com")}, true, true},
		{"trusttunnel", C.TypeTrustTunnel, &option.TrustTunnelOutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: tlsOn("node.example")}, true, false},
		{"tuic cover", C.TypeTUIC, &option.TUICOutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: tlsOn("cover.example")}, true, true},
		{"hysteria2 cover", C.TypeHysteria2, &option.Hysteria2OutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: tlsOn("cover.example")}, true, true},
		{"hysteria2 salamander", C.TypeHysteria2, &option.Hysteria2OutboundOptions{ServerOptions: server("node.example"), OutboundTLSOptionsContainer: tlsOn("cover.example"), Obfs: &option.Hysteria2Obfs{Type: C.Hysteria2ObfsTypeSalamander}}, true, false},
		{"masque warp", C.TypeMASQUE, &option.MASQUEOutboundOptions{ServerOptions: server("162.159.198.1"), OutboundTLSOptionsContainer: tlsOn("162.159.198.1")}, true, true},
		{"masque standard, own name", C.TypeMASQUE, &option.MASQUEOutboundOptions{ServerOptions: server("node.example"), Profile: "standard"}, true, false},
		{"masque standard, legacy sni", C.TypeMASQUE, &option.MASQUEOutboundOptions{ServerOptions: server("node.example"), Profile: "standard", SNI: "cover.example"}, true, true},
		{"awg quic masquerade", C.TypeWireGuard, &option.WireGuardEndpointOptions{AmneziaWGOptions: option.AmneziaWGOptions{Ip: "quic", Id: "node.example"}}, true, true},
		{"awg static i1", C.TypeWireGuard, &option.WireGuardEndpointOptions{AmneziaWGOptions: option.AmneziaWGOptions{I1: "<b 0xc0000000>"}}, true, true},
		{"awg dns masquerade", C.TypeWireGuard, &option.WireGuardEndpointOptions{AmneziaWGOptions: option.AmneziaWGOptions{Ip: "dns"}}, false, false},
		{"plain wireguard", C.TypeWireGuard, &option.WireGuardEndpointOptions{}, false, false},
		{"naive", C.TypeNaive, &option.NaiveOutboundOptions{ServerOptions: server("node.example")}, false, false},
		{"socks", C.TypeSOCKS, &option.SOCKSOutboundOptions{ServerOptions: server("node.example")}, false, false},
	} {
		info, covered := sniffGuardNodeInfo(testCase.typ, testCase.options)
		require.Equal(t, testCase.covered, covered, testCase.name)
		if covered {
			require.Equal(t, lxsniffguard.NodeInfo{Type: testCase.typ, SNIMismatch: testCase.mismatch}, info, testCase.name)
		}
	}
}

func TestSniffGuardCarrierInfo(t *testing.T) {
	server := option.ServerOptions{Server: "relay.example", ServerPort: 8443}
	require.Equal(t, lxsniffguard.CarrierInfo{Type: "vless", Fingerprint: "vless|relay.example|8443", Encrypted: true},
		sniffGuardCarrierInfo("vless", "relay", &option.VLESSOutboundOptions{ServerOptions: server}))
	require.Equal(t, lxsniffguard.CarrierInfo{Type: "socks", Fingerprint: "socks|relay.example|8443"},
		sniffGuardCarrierInfo("socks", "relay", &option.SOCKSOutboundOptions{ServerOptions: server}))
	require.Equal(t, lxsniffguard.CarrierInfo{Type: "http", Fingerprint: "http|relay.example|8443"},
		sniffGuardCarrierInfo("http", "relay", &option.HTTPOutboundOptions{ServerOptions: server}))
	require.Equal(t, lxsniffguard.CarrierInfo{Type: "http", Fingerprint: "http|relay.example|8443", Encrypted: true},
		sniffGuardCarrierInfo("http", "relay", &option.HTTPOutboundOptions{ServerOptions: server, OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: &option.OutboundTLSOptions{Enabled: true}}}))
	require.Equal(t, lxsniffguard.CarrierInfo{Type: "wireguard", Fingerprint: "wireguard|198.51.100.7|51820", Encrypted: true},
		sniffGuardCarrierInfo("wireguard", "wg", &option.WireGuardEndpointOptions{Peers: []option.WireGuardPeer{{Address: "198.51.100.7", Port: 51820}}}))
	require.Equal(t, lxsniffguard.CarrierInfo{Type: "ssh", Fingerprint: "ssh|relay.example|8443", Encrypted: true},
		sniffGuardCarrierInfo("ssh", "relay", &option.SSHOutboundOptions{ServerOptions: server}))
	require.Equal(t, lxsniffguard.CarrierInfo{Type: "chain", Fingerprint: "chain|c", Encrypted: true},
		sniffGuardCarrierInfo("chain", "c", nil))
}
