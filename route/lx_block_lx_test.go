// lx:begin idle-suspend
package route

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

// routerFromConfig builds a router the way box.New does since SPEC 098: decode,
// option.ResolveLX, the resolved block into the context, then NewRouter.
func routerFromConfig(t *testing.T, content string) *Router {
	t.Helper()
	var options option.Options
	if err := json.UnmarshalContext(context.Background(), []byte(content), &options); err != nil {
		t.Fatalf("decode %s: %v", content, err)
	}
	resolved, err := option.ResolveLX(&options)
	if err != nil {
		t.Fatalf("resolve %s: %v", content, err)
	}
	ctx := service.ContextWithPtr(service.ContextWithDefaultRegistry(context.Background()), resolved)
	return NewRouter(ctx, log.NewNOPFactory(), common.PtrValueOrDefault(options.Route), option.DNSOptions{})
}

type idleFields struct {
	suspend, reachable, teardown time.Duration
	teardownSet                  bool
}

func idleFieldsOf(r *Router) idleFields {
	return idleFields{r.idleSuspend, r.idleSuspendReachable, r.idleTeardown, r.idleTeardownSet}
}

// lx: SPEC 098 — the `lx.wg` block gives the router its idle parameters.
func TestRouterIdleFields_lxBlock(t *testing.T) {
	cases := []struct {
		name string
		lx   string
		want idleFields
	}{
		{"all three, explicit teardown 0", `{"lx":{"wg":{"idle_suspend":"30s","idle_suspend_reachable":"5m","idle_teardown":"0"}}}`, idleFields{30 * time.Second, 5 * time.Minute, 0, true}},
		{"teardown inherits the reachable window", `{"lx":{"wg":{"idle_suspend":"30s","idle_suspend_reachable":"5m"}}}`, idleFields{30 * time.Second, 5 * time.Minute, 5 * time.Minute, false}},
		{"suspend only", `{"lx":{"wg":{"idle_suspend":"1m"}}}`, idleFields{time.Minute, 0, 0, false}},
		{"nothing set", `{"lx":{}}`, idleFields{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := idleFieldsOf(routerFromConfig(t, tc.lx)); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The removed route.lx_idle_* aliases are unknown keys now: a config that
// still carries one fails to decode instead of being silently ignored.
func TestRouteIdleAliasesRejected(t *testing.T) {
	for _, key := range []string{"lx_idle_suspend", "lx_idle_suspend_reachable", "lx_idle_teardown"} {
		var options option.Options
		err := json.UnmarshalContext(context.Background(), []byte(`{"route":{"`+key+`":"30s"}}`), &options)
		if err == nil {
			t.Fatalf("route.%s must be rejected as an unknown key", key)
		}
	}
}

// lx:end idle-suspend
