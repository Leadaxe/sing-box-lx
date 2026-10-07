package daemon

import "github.com/sagernet/sing-box/adapter"

// lx: SPEC 115 — adapter path verdict → proto enum. Tagless: the stream's
// tailscalePeerToProto in started_service.go uses it in every build.
func tailscalePeerPathToProto(path string) TailscalePeerPath {
	switch path {
	case adapter.TailscalePeerPathDirect:
		return TailscalePeerPath_TAILSCALE_PEER_PATH_DIRECT
	case adapter.TailscalePeerPathPeerRelay:
		return TailscalePeerPath_TAILSCALE_PEER_PATH_PEER_RELAY
	case adapter.TailscalePeerPathDERP:
		return TailscalePeerPath_TAILSCALE_PEER_PATH_DERP
	default:
		return TailscalePeerPath_TAILSCALE_PEER_PATH_NONE
	}
}
