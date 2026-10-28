package route

import (
	"io"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	N "github.com/sagernet/sing/common/network"
)

// resolveNode is a node that carries both networks.
type resolveNode struct {
	adapter.Outbound
	tag string
}

func (o *resolveNode) Tag() string       { return o.tag }
func (o *resolveNode) Network() []string { return []string{N.NetworkTCP, N.NetworkUDP} }

// resolveGroup is a group that points at selected.
type resolveGroup struct {
	resolveNode
	selected adapter.Outbound
}

func (g *resolveGroup) All() []string                     { return []string{g.selected.Tag()} }
func (g *resolveGroup) Selected(string) adapter.Outbound  { return g.selected }
func (g *resolveGroup) AttachConnection(io.Closer) func() { return func() {} }

// resolvePerConnectionGroup also picks per connection, as the urltest group does.
type resolvePerConnectionGroup struct {
	resolveGroup
}

func (g *resolvePerConnectionGroup) SelectsPerConnectionLx() {}

func TestResolveOutboundLxStopsAtPerConnectionGroup(t *testing.T) {
	node := &resolveNode{tag: "node"}
	urltest := &resolvePerConnectionGroup{resolveGroup{resolveNode{tag: "auto"}, node}}
	selector := &resolveGroup{resolveNode{tag: "proxy"}, urltest}

	chain, err := resolveOutboundLx(selector, N.NetworkTCP)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 || chain[0] != selector || chain[1] != urltest {
		t.Fatalf("chain = %v, want [proxy auto]: the router must hand the connection to the urltest group", tags(chain))
	}

	plain := &resolveGroup{resolveNode{tag: "proxy"}, node}
	chain, err = resolveOutboundLx(plain, N.NetworkTCP)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 || chain[1] != node {
		t.Fatalf("chain = %v, want [proxy node]: a selector is still resolved to its node", tags(chain))
	}
}

func tags(chain []adapter.Outbound) []string {
	result := make([]string, 0, len(chain))
	for _, outbound := range chain {
		result = append(result, outbound.Tag())
	}
	return result
}
