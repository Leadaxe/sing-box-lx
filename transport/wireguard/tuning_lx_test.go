package wireguard

import (
	"context"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

func tuningEndpoint(resolved *option.LXResolved) *Endpoint {
	ctx := context.Background()
	if resolved != nil {
		ctx = service.ContextWithPtr(ctx, resolved)
	}
	return &Endpoint{options: EndpointOptions{Context: ctx}}
}
