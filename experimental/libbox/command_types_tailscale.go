package libbox

import "github.com/sagernet/sing-box/daemon"

type TailscaleStatusUpdate struct {
	endpoints []*TailscaleEndpointStatus
}

func (u *TailscaleStatusUpdate) Endpoints() TailscaleEndpointStatusIterator {
	return newIterator(u.endpoints)
}

type TailscaleEndpointStatusIterator interface {
	Next() *TailscaleEndpointStatus
	HasNext() bool
}

type TailscaleEndpointStatus struct {
	EndpointTag        string
	BackendState       string
	StateText          string
	AuthURL            string
	NetworkName        string
	MagicDNSSuffix     string
	Self               *TailscalePeer
	ExitNode           *TailscalePeer
	KeyAuth            bool
	CanShareFiles      bool
	WaitingFileCount   int32
	ReceivingFileCount int32
	UnreadFileCount    int32
	userGroups         []*TailscaleUserGroup
	// lx:begin tailscale-status (SPEC 115)
	health []string
	// lx:end tailscale-status
}

func (s *TailscaleEndpointStatus) UserGroups() TailscaleUserGroupIterator {
	return newIterator(s.userGroups)
}

// lx:begin tailscale-status (SPEC 115)

// Health returns the backend's health warnings (no DERP connection, exit node
// offline, ...); empty when healthy.
func (s *TailscaleEndpointStatus) Health() StringIterator {
	return newIterator(s.health)
}

// lx:end tailscale-status

type TailscaleUserGroupIterator interface {
	Next() *TailscaleUserGroup
	HasNext() bool
}

type TailscaleUserGroup struct {
	UserID        int64
	LoginName     string
	DisplayName   string
	ProfilePicURL string
	peers         []*TailscalePeer
}

func (g *TailscaleUserGroup) Peers() TailscalePeerIterator {
	return newIterator(g.peers)
}

type TailscalePeerIterator interface {
	Next() *TailscalePeer
	HasNext() bool
}

type TailscalePeer struct {
	StableID        string
	HostName        string
	DNSName         string
	OS              string
	tailscaleIPs    []string
	sshHostKeys     []string
	Online          bool
	ExitNode        bool
	ExitNodeOption  bool
	ShareeNode      bool
	Expired         bool
	Active          bool
	CanReceiveFiles bool
	RxBytes         int64
	TxBytes         int64
	KeyExpiry       int64
	LastSeen        int64
	// lx:begin tailscale-status (SPEC 115)
	// Path: "direct" / "peer_relay" / "derp" / "" (never sent to the peer).
	// Fresh in GetTailscaleStatus; in the stream, the snapshot of the last
	// event. Endpoint is the direct ip:port, PeerRelay the relay ip:port:vni,
	// DERPRegionCode the peer's home DERP; LastHandshake Unix seconds, 0 = none.
	Path           string
	Endpoint       string
	PeerRelay      string
	DERPRegionCode string
	LastHandshake  int64
	// lx:end tailscale-status
}

func (p *TailscalePeer) TailscaleIPs() StringIterator {
	return newIterator(p.tailscaleIPs)
}

func (p *TailscalePeer) SSHHostKeys() StringIterator {
	return newIterator(p.sshHostKeys)
}

type TailscaleStatusHandler interface {
	OnStatusUpdate(status *TailscaleStatusUpdate)
	OnError(message string)
}

type TailscaleStatusSubscription struct {
	streamSession
}

func tailscaleStatusUpdateFromGRPC(update *daemon.TailscaleStatusUpdate) *TailscaleStatusUpdate {
	endpoints := make([]*TailscaleEndpointStatus, len(update.Endpoints))
	for i, endpoint := range update.Endpoints {
		endpoints[i] = tailscaleEndpointStatusFromGRPC(endpoint)
	}
	return &TailscaleStatusUpdate{endpoints: endpoints}
}

func tailscaleEndpointStatusFromGRPC(status *daemon.TailscaleEndpointStatus) *TailscaleEndpointStatus {
	userGroups := make([]*TailscaleUserGroup, len(status.UserGroups))
	for i, group := range status.UserGroups {
		userGroups[i] = tailscaleUserGroupFromGRPC(group)
	}
	result := &TailscaleEndpointStatus{
		EndpointTag:        status.EndpointTag,
		BackendState:       status.BackendState,
		StateText:          status.StateText,
		AuthURL:            status.AuthURL,
		NetworkName:        status.NetworkName,
		MagicDNSSuffix:     status.MagicDNSSuffix,
		KeyAuth:            status.GetKeyAuth(),
		CanShareFiles:      status.CanShareFiles,
		WaitingFileCount:   status.WaitingFileCount,
		ReceivingFileCount: status.ReceivingFileCount,
		UnreadFileCount:    status.UnreadFileCount,
		userGroups:         userGroups,
		health:             status.Health, // lx: SPEC 115
	}
	if status.Self != nil {
		result.Self = tailscalePeerFromGRPC(status.Self)
	}
	if status.ExitNode != nil {
		result.ExitNode = tailscalePeerFromGRPC(status.ExitNode)
	}
	return result
}

func tailscaleUserGroupFromGRPC(group *daemon.TailscaleUserGroup) *TailscaleUserGroup {
	peers := make([]*TailscalePeer, len(group.Peers))
	for i, peer := range group.Peers {
		peers[i] = tailscalePeerFromGRPC(peer)
	}
	return &TailscaleUserGroup{
		UserID:        group.UserID,
		LoginName:     group.LoginName,
		DisplayName:   group.DisplayName,
		ProfilePicURL: group.ProfilePicURL,
		peers:         peers,
	}
}

func tailscalePeerFromGRPC(peer *daemon.TailscalePeer) *TailscalePeer {
	return &TailscalePeer{
		StableID:        peer.StableID,
		HostName:        peer.HostName,
		DNSName:         peer.DnsName,
		OS:              peer.Os,
		tailscaleIPs:    peer.TailscaleIPs,
		sshHostKeys:     peer.SshHostKeys,
		Online:          peer.Online,
		ExitNode:        peer.ExitNode,
		ExitNodeOption:  peer.ExitNodeOption,
		ShareeNode:      peer.ShareeNode,
		Expired:         peer.Expired,
		Active:          peer.Active,
		CanReceiveFiles: peer.CanReceiveFiles,
		RxBytes:         peer.RxBytes,
		TxBytes:         peer.TxBytes,
		KeyExpiry:       peer.KeyExpiry,
		LastSeen:        peer.LastSeen,
		// lx:begin tailscale-status (SPEC 115)
		Path:           tailscalePeerPathFromGRPC(peer.Path),
		Endpoint:       peer.Endpoint,
		PeerRelay:      peer.PeerRelay,
		DERPRegionCode: peer.DerpRegionCode,
		LastHandshake:  peer.LastHandshake,
		// lx:end tailscale-status
	}
}
