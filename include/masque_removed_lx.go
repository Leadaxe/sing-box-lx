package include

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/schema"
	E "github.com/sagernet/sing/common/exceptions"
)

// removedMASQUEOptions swallows whatever fields the 1.14 masque outbound had, so
// the config decodes far enough to reach the message below instead of failing
// on the first unknown key.
type removedMASQUEOptions struct{}

func (o *removedMASQUEOptions) UnmarshalJSON([]byte) error {
	return nil
}

// DescribeSchema keeps `sing-box schema` working: the removed type is an open
// object there.
func (o removedMASQUEOptions) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return schema.LooseObject(), nil
}

// lx: SPEC 155 §5.4 — the fork's own MASQUE outbound (type `masque`, 1.14 line)
// was replaced by upstream's masque-client endpoint. A config that still carries
// it fails with a pointer instead of "unknown outbound type".
func registerStubForRemovedMASQUEOutbound(registry *outbound.Registry) {
	outbound.Register[removedMASQUEOptions](registry, "masque", func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options removedMASQUEOptions) (adapter.Outbound, error) {
		return nil, E.New("the masque outbound of the 1.14 line was removed in 1.15; use the masque-client endpoint (WARP profile: SPEC 110)")
	})
}
