package constant

const (
	TypeTun                = "tun"
	TypeRedirect           = "redirect"
	TypeTProxy             = "tproxy"
	TypeDirect             = "direct"
	TypeBlock              = "block"
	TypeDNS                = "dns"
	TypeSOCKS              = "socks"
	TypeHTTP               = "http"
	TypeMixed              = "mixed"
	TypeShadowsocks        = "shadowsocks"
	TypeVMess              = "vmess"
	TypeTrojan             = "trojan"
	TypeNaive              = "naive"
	TypeWireGuard          = "wireguard"
	TypeHysteria           = "hysteria"
	TypeTor                = "tor"
	TypeSSH                = "ssh"
	TypeShadowTLS          = "shadowtls"
	TypeAnyTLS             = "anytls"
	TypeShadowsocksR       = "shadowsocksr"
	TypeVLESS              = "vless"
	TypeTUIC               = "tuic"
	TypeHysteria2          = "hysteria2"
	TypeTailscale          = "tailscale"
	TypeCloudflared        = "cloudflared"
	TypeDERP               = "derp"
	TypeResolved           = "resolved"
	TypeSSMAPI             = "ssm-api"
	TypeAPI                = "api"
	TypeCCM                = "ccm"
	TypeOCM                = "ocm"
	TypeOOMKiller          = "oom-killer"
	TypeUSBIPServer        = "usbip-server"
	TypeUSBIPClient        = "usbip-client"
	TypeHysteriaRealm      = "hysteria-realm"
	TypeACME               = "acme"
	TypeCloudflareOriginCA = "cloudflare-origin-ca"
)

const (
	TypeSelector = "selector"
	TypeURLTest  = "urltest"
)

// lx: SPEC 019 — URLTest balancing mode (urltest "mode" option).
const (
	URLTestModeLeastTest       = "least_test"       // default — pick lowest-delay node (legacy urltest behaviour)
	URLTestModeRoundRobin      = "round_robin"      // rotate across live nodes
	URLTestModeLeastConnection = "least_connection" // fewest active conns — phase 2, not yet implemented
)

// lx: SPEC 019 — URLTest sticky binding mechanism (urltest "sticky.mode" option).
const (
	URLTestStickyJumpHash = "jumphash" // stateless consistent hash over live nodes (default)
	URLTestStickyTTLMap   = "ttlmap"   // key->node table with TTL + LRU cap
)

// lx: SPEC 019 — URLTest sticky key components (urltest "sticky.hash" entries).
const (
	URLTestStickyProcess  = "process"
	URLTestStickyDomain   = "domain"
	URLTestStickySourceIP = "source_ip"
	URLTestStickyDestIP   = "dest_ip"
	URLTestStickyDestPort = "dest_port"
)

// lx: SPEC 019 — sticky ttlmap default cap (DefaultURLTestStickyTimeout lives in timeout.go).
const DefaultURLTestStickyCap = 2000

func ProxyDisplayName(proxyType string) string {
	switch proxyType {
	case TypeTun:
		return "TUN"
	case TypeRedirect:
		return "Redirect"
	case TypeTProxy:
		return "TProxy"
	case TypeDirect:
		return "Direct"
	case TypeBlock:
		return "Block"
	case TypeDNS:
		return "DNS"
	case TypeSOCKS:
		return "SOCKS"
	case TypeHTTP:
		return "HTTP"
	case TypeMixed:
		return "Mixed"
	case TypeShadowsocks:
		return "Shadowsocks"
	case TypeVMess:
		return "VMess"
	case TypeTrojan:
		return "Trojan"
	case TypeNaive:
		return "Naive"
	case TypeWireGuard:
		return "WireGuard"
	case TypeHysteria:
		return "Hysteria"
	case TypeTor:
		return "Tor"
	case TypeSSH:
		return "SSH"
	case TypeShadowTLS:
		return "ShadowTLS"
	case TypeShadowsocksR:
		return "ShadowsocksR"
	case TypeVLESS:
		return "VLESS"
	case TypeTUIC:
		return "TUIC"
	case TypeHysteria2:
		return "Hysteria2"
	case TypeAnyTLS:
		return "AnyTLS"
	case TypeTailscale:
		return "Tailscale"
	case TypeCloudflared:
		return "Cloudflared"
	case TypeSelector:
		return "Selector"
	case TypeURLTest:
		return "URLTest"
	default:
		return "Unknown"
	}
}
