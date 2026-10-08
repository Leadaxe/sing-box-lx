package wireguard

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
)

// testScope is a throwaway lifecycle scope for tests that drive Start stages
// by hand; the tests close the endpoint themselves.
func testScope() *adapter.Scope {
	return adapter.NewScope(context.Background(), log.NewNOPFactory().Logger())
}
