//go:build with_lx_command

package main

import (
	"time"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/emptypb"
)

// lx: SPEC 114 — per-peer status of WG/AWG endpoints, from GetWireGuardStatus.

var commandAPIPeers = &cobra.Command{
	Use:   "peers [endpoint tag]",
	Short: "List WireGuard peers: last handshake, endpoint, transfer",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var tag string
		if len(args) > 0 {
			tag = args[0]
		}
		return runAPIPeers(tag)
	},
}

func init() {
	commandAPIRoot.AddCommand(commandAPIPeers)
}

func runAPIPeers(tag string) error {
	clientConn, client, err := createAPIClient()
	if err != nil {
		return err
	}
	defer clientConn.Close()
	var tags []string
	if tag != "" {
		tags = []string{tag}
	} else {
		// Without a tag: every WG/AWG endpoint, found by the state GetOutbounds
		// reports for them and for nothing else.
		outbounds, err := client.GetOutbounds(globalCtx, &emptypb.Empty{})
		if err != nil {
			return err
		}
		for _, item := range outbounds.GetOutbounds() {
			if item.GetEndpointState() != "" {
				tags = append(tags, item.GetTag())
			}
		}
	}
	table := tableWriter{
		header:       []string{"ENDPOINT", "STATE", "PEER", "ADDRESS", "HANDSHAKE", "RX", "TX"},
		emptyMessage: "no peers",
	}
	now := time.Now()
	for _, endpointTag := range tags {
		endpoint, err := client.GetWireGuardStatus(globalCtx, &daemon.WireGuardStatusRequest{Tag: endpointTag})
		if err != nil {
			return E.Cause(err, endpointTag)
		}
		if len(endpoint.GetPeers()) == 0 {
			// No device (never built, torn down, disabled): the state says why.
			table.addRow(endpoint.GetEndpointTag(), endpoint.GetEndpointState(), "-", "", "", "", "")
			continue
		}
		for _, peer := range endpoint.GetPeers() {
			table.addRow(endpoint.GetEndpointTag(), endpoint.GetEndpointState(), abbreviatePeerKey(peer.GetPublicKey()), peer.GetEndpoint(),
				formatHandshakeAge(now, peer.GetLastHandshakeUnix()), formatTaildropSize(peer.GetRxBytes()), formatTaildropSize(peer.GetTxBytes()))
		}
	}
	table.flush()
	return nil
}

// abbreviatePeerKey shortens a base64 key the way wireguard-go names a peer in
// its log lines (peer(xxxx…yyyy)), so a table row can be matched to the log.
func abbreviatePeerKey(key string) string {
	if len(key) < 43 {
		return key
	}
	return key[0:4] + "…" + key[39:43]
}

func formatHandshakeAge(now time.Time, unix int64) string {
	if unix == 0 {
		return "never"
	}
	age := now.Sub(time.Unix(unix, 0)).Truncate(time.Second)
	if age < 0 {
		age = 0
	}
	return F.ToString(age, " ago")
}
