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
	TypeMASQUE             = "masque"
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
	URLTestModeLeastTest  = "least_test"  // default — pick lowest-delay node (legacy urltest behaviour)
	URLTestModeRoundRobin = "round_robin" // rotate over a fixed-size pool of live nodes
)

// lx: SPEC 019 — balancer.sticky_hash key components.
const (
	URLTestStickyProcess  = "process"
	URLTestStickyDomain   = "domain"
	URLTestStickySourceIP = "source_ip"
	URLTestStickyDestIP   = "dest_ip"
	URLTestStickyDestPort = "dest_port"
	// URLTestStickyNone explicitly disables stickiness: sticky_hash: ["none"]. A bare [] cannot
	// be used because the config decoder (badjson.UnmarshallExcludedContext) re-marshals the
	// struct and collapses an empty array to nil, which is indistinguishable from "omitted" —
	// so an explicit sentinel is required. Omitted → default [process, domain].
	URLTestStickyNone = "none"
)

// lx: SPEC 019 — default rotation pool size when balancer.pool is unset.
const DefaultURLTestPool = 3

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
	case TypeMASQUE:
		return "MASQUE"
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
