//go:build with_lx_command

package main

import (
	"strings"
	"time"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/spf13/cobra"
)

// lx: SPEC 115 — per-peer path of a Tailscale endpoint, from GetTailscaleStatus.

var commandAPITailscalePeers = &cobra.Command{
	Use:   "peers",
	Short: "List Tailscale peers: path (direct / peer relay / DERP), handshake, transfer",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAPITailscalePeers()
	},
}

func init() {
	commandAPITailscale.AddCommand(commandAPITailscalePeers)
}

func runAPITailscalePeers() error {
	clientConn, client, err := createAPIClient()
	if err != nil {
		return err
	}
	defer clientConn.Close()
	// The endpoint is picked the way the other tailscale subcommands do it
	// (--endpoint, or the only one); the snapshot itself comes from the getter.
	endpoints, err := fetchTailscaleStatus(client)
	if err != nil {
		return err
	}
	listed, err := resolveTailscaleEndpointStatus(endpoints)
	if err != nil {
		return err
	}
	endpoint, err := client.GetTailscaleStatus(globalCtx, &daemon.TailscaleStatusRequest{EndpointTag: listed.GetEndpointTag()})
	if err != nil {
		return E.Cause(err, listed.GetEndpointTag())
	}
	table := tableWriter{
		header:       []string{"PEER", "IP", "ONLINE", "PATH", "VIA", "HANDSHAKE", "RX", "TX"},
		emptyMessage: "no peers",
	}
	now := time.Now()
	for _, group := range endpoint.GetUserGroups() {
		for _, peer := range group.GetPeers() {
			name := peer.GetHostName()
			if peer.GetExitNode() {
				name += " (exit node)"
			}
			var ip string
			if ips := peer.GetTailscaleIPs(); len(ips) > 0 {
				ip = ips[0]
			}
			table.addRow(name, ip, formatBool(peer.GetOnline()), formatTailscalePeerPath(peer.GetPath()), formatTailscalePeerVia(peer),
				formatHandshakeAge(now, peer.GetLastHandshake()), formatTaildropSize(peer.GetRxBytes()), formatTaildropSize(peer.GetTxBytes()))
		}
	}
	table.flush()
	if health := endpoint.GetHealth(); len(health) > 0 {
		var block blockWriter
		block.addLine("Health", strings.Join(health, "; "))
		block.flush()
	}
	return nil
}

func formatBool(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func formatTailscalePeerPath(path daemon.TailscalePeerPath) string {
	switch path {
	case daemon.TailscalePeerPath_TAILSCALE_PEER_PATH_DIRECT:
		return "direct"
	case daemon.TailscalePeerPath_TAILSCALE_PEER_PATH_PEER_RELAY:
		return "peer relay"
	case daemon.TailscalePeerPath_TAILSCALE_PEER_PATH_DERP:
		return "derp"
	default:
		return "-"
	}
}

// formatTailscalePeerVia names what the path goes through: the direct address,
// the relay, or the DERP region. A direct peer still shows its home region.
func formatTailscalePeerVia(peer *daemon.TailscalePeer) string {
	switch peer.GetPath() {
	case daemon.TailscalePeerPath_TAILSCALE_PEER_PATH_DIRECT:
		if peer.GetDerpRegionCode() != "" {
			return peer.GetEndpoint() + " (home " + peer.GetDerpRegionCode() + ")"
		}
		return peer.GetEndpoint()
	case daemon.TailscalePeerPath_TAILSCALE_PEER_PATH_PEER_RELAY:
		return peer.GetPeerRelay()
	default:
		return peer.GetDerpRegionCode()
	}
}
