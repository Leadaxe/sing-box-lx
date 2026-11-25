// lx:begin SPEC 019 / SPEC 138 round_robin: the reference of the sticky layout for tests

package group

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// pick is pickKeyed for a TCP connection, the node only.
func (b *balancer) pick(ctx context.Context, destination M.Socksaddr, fallback adapter.Outbound, resolve func(tag string) adapter.Outbound) adapter.Outbound {
	node, _ := b.pickKeyed(ctx, N.NetworkTCP, destination, fallback, resolve)
	return node
}

// stickyKey builds the binding key from the configured components in order, joined
// by NUL: the layout the slot hash (stickyHashOf) has to keep.
func (b *balancer) stickyKey(ctx context.Context, destination M.Socksaddr) string {
	metadata := adapter.ContextFrom(ctx)
	var buf []byte
	for i, component := range b.stickyHash {
		if i > 0 {
			buf = append(buf, 0)
		}
		buf = append(buf, stickyComponent(component, metadata, destination)...)
	}
	return string(buf)
}

// hashKey is FNV-1a, 64 bit, of key: the slot of a key is hashKey(stickyKey) % pool.
func hashKey(key string) uint64 {
	return fnvString(fnvOffset64, key)
}

// lx:end SPEC 019 / SPEC 138
