// lx:begin SPEC 140 a failed dial of least_test erases the history of the leaf

package group

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// least_test whose member is a selector over a leaf: a failed dial through it
// erases the history of the leaf, as upstream 1.14.4 does through the routed
// chain (its last element), and the group no longer picks the dead leaf.
func TestDialHistory_nestedMemberErasesLeaf(t *testing.T) {
	leaf := &penaltyDialNode{tag: "leaf", err: E.New("dial failed")}
	other := &penaltyDialNode{tag: "other", addr: "server"}
	sel := &penaltyGroupNode{penaltyDialNode: penaltyDialNode{tag: "sel", err: E.New("dial failed")}, selected: leaf}
	g := newPenaltyTestGroup(t, []*penaltyDialNode{other}, map[string]uint16{"other": 50})
	g.outbounds = []adapter.Outbound{sel, other}
	g.outbound.(*fakeManager).byTag["sel"] = sel
	g.outbound.(*fakeManager).byTag["leaf"] = leaf
	g.history.StoreURLTestHistory("leaf", &adapter.URLTestHistory{Time: time.Now(), Delay: 10})
	g.selectedOutboundTCP.Store(sel)
	s := &URLTest{outbound: g.outbound, logger: log.NewNOPFactory().NewLogger("test"), group: g}
	if _, err := s.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443)); err == nil {
		t.Fatal("want the dial error")
	}
	if g.history.LoadURLTestHistory("leaf") != nil {
		t.Fatal("the history of the leaf survives the failed dial")
	}
	if selected, _ := g.Select(N.NetworkTCP); selected == sel {
		t.Fatal("the group still picks the member of the dead leaf")
	}
}

// lx:end SPEC 140
