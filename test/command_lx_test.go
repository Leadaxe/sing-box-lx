//go:build with_lx_command

package main

import (
	"context"
	"testing"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/include"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

// baseContext mirrors experimental/libbox.baseContext: NewStartedService expects a
// context already carrying the box service registries (newInstance does
// service.ExtendContext + MustRegister on it).
func baseContext() context.Context {
	return box.Context(context.Background(),
		include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(),
		include.DNSTransportRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())
}

// SPEC 015: GetGroups / GetOutbounds pull-getters + the len<2 group-drop fix.
// Driven through the public daemon API (NewStartedService + StartOrReloadService),
// the same path LxBox uses, so this is an end-to-end check, not a white-box poke.

type noopPlatformHandler struct{}

func (noopPlatformHandler) WriteDebugMessage(string)        {}
func (noopPlatformHandler) ConnectSSHAgent() (int32, error) { return 0, nil }

// A selector with a SINGLE node. Pre-fix readGroups() dropped groups with < 2 items,
// so this group vanished from the startup broadcast and any snapshot (main screen
// nodes=0). SPEC 015 §3.5.
const singleNodeGroupConfig = `{
  "log": {"level": "error"},
  "outbounds": [
    {"type": "direct", "tag": "node-a"},
    {"type": "selector", "tag": "my-selector", "outbounds": ["node-a"]}
  ]
}`

func startedServiceForTest(t *testing.T) *daemon.StartedService {
	ctx, cancel := context.WithCancel(baseContext())
	t.Cleanup(cancel)
	svc := daemon.NewStartedService(daemon.ServiceOptions{
		Context: ctx,
		Handler: noopPlatformHandler{},
	})
	require.NoError(t, svc.StartOrReloadService(singleNodeGroupConfig, nil))
	t.Cleanup(func() { _ = svc.CloseService(); svc.Close() })
	return svc
}

func TestGetGroupsKeepsSingleNodeGroup(t *testing.T) {
	svc := startedServiceForTest(t)

	groups, err := svc.GetGroups(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	var found bool
	for _, g := range groups.Group {
		if g.Tag == "my-selector" {
			found = true
			require.Len(t, g.Items, 1, "selector should expose its single node")
			require.Equal(t, "node-a", g.Items[0].Tag)
		}
	}
	require.True(t, found, "single-node group 'my-selector' was dropped (len<2 regression)")
}

func TestGetOutboundsReturnsFlatList(t *testing.T) {
	svc := startedServiceForTest(t)

	list, err := svc.GetOutbounds(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	tags := map[string]bool{}
	for _, ob := range list.Outbounds {
		tags[ob.Tag] = true
	}
	require.True(t, tags["node-a"], "flat list must contain the leaf node")
	require.True(t, tags["my-selector"], "flat list must contain the selector")
}

func TestGetGroupsRejectsWhenNotStarted(t *testing.T) {
	ctx, cancel := context.WithCancel(baseContext())
	t.Cleanup(cancel)
	svc := daemon.NewStartedService(daemon.ServiceOptions{Context: ctx, Handler: noopPlatformHandler{}})
	t.Cleanup(svc.Close)
	// Never started → must error, not return an empty snapshot.
	_, err := svc.GetGroups(context.Background(), &emptypb.Empty{})
	require.Error(t, err)
}
