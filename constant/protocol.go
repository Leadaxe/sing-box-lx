package constant

const (
	ProtocolTLS        = "tls"
	ProtocolHTTP       = "http"
	ProtocolQUIC       = "quic"
	ProtocolDNS        = "dns"
	ProtocolSTUN       = "stun"
	ProtocolBitTorrent = "bittorrent"
	ProtocolDTLS       = "dtls"
	ProtocolSSH        = "ssh"
	ProtocolRDP        = "rdp"
	ProtocolNTP        = "ntp"
	// lx:begin sniff-lx
	ProtocolWireGuard = "wireguard" // SPEC 078
	ProtocolOpenVPN   = "openvpn"   // SPEC 079
	ProtocolIKE       = "ike"       // SPEC 079 — IKEv2 / IKEv1 (ISAKMP)
	ProtocolTailscale = "tailscale" // SPEC 079 — disco
	ProtocolSIP       = "sip"       // SPEC 079
	// lx:end sniff-lx
)

const (
	ClientChromium = "chromium"
	ClientSafari   = "safari"
	ClientFirefox  = "firefox"
	ClientQUICGo   = "quic-go"
	ClientUnknown  = "unknown"
)
