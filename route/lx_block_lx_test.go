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
	"github.com/sagernet/sing/common/json/badoption"
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
	resolved, _, err := option.ResolveLX(&options)
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

// lx: SPEC 098 acceptance 1 — the `lx` block and the deprecated route.lx_idle_*
// aliases give the router the same idle parameters.
func TestRouterIdleFields_lxBlockEqualsAliases(t *testing.T) {
	cases := []struct {
		name          string
		lx, legacy    string
		want          idleFields
		checkTeardown bool
	}{
		{
			name:   "all three, explicit teardown 0",
			lx:     `{"lx":{"wg":{"idle_suspend":"30s","idle_suspend_reachable":"5m","idle_teardown":"0"}}}`,
			legacy: `{"route":{"lx_idle_suspend":"30s","lx_idle_suspend_reachable":"5m","lx_idle_teardown":"0"}}`,
			want:   idleFields{30 * time.Second, 5 * time.Minute, 0, true},
		},
		{
			name:   "teardown inherits the reachable window",
			lx:     `{"lx":{"wg":{"idle_suspend":"30s","idle_suspend_reachable":"5m"}}}`,
			legacy: `{"route":{"lx_idle_suspend":"30s","lx_idle_suspend_reachable":"5m"}}`,
			want:   idleFields{30 * time.Second, 5 * time.Minute, 5 * time.Minute, false},
		},
		{
			name:   "suspend only",
			lx:     `{"lx":{"wg":{"idle_suspend":"1m"}}}`,
			legacy: `{"route":{"lx_idle_suspend":"1m"}}`,
			want:   idleFields{time.Minute, 0, 0, false},
		},
		{
			name:   "nothing set",
			lx:     `{"lx":{}}`,
			legacy: `{"route":{}}`,
			want:   idleFields{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fromLX := idleFieldsOf(routerFromConfig(t, tc.lx))
			fromLegacy := idleFieldsOf(routerFromConfig(t, tc.legacy))
			if fromLX != tc.want {
				t.Fatalf("lx block: got %+v, want %+v", fromLX, tc.want)
			}
			if fromLegacy != fromLX {
				t.Fatalf("aliases must match the lx block: got %+v, want %+v", fromLegacy, fromLX)
			}
		})
	}
}

// The router reads the idle windows only from the context: RouteOptions that
// still carry route.lx_idle_* (a caller that skipped ResolveLX) leave the
// feature off. Tests that build a router must go through the context, or the
// tick never starts and they pass for the wrong reason.
func TestRouterIdleFields_ignoreRouteOptions(t *testing.T) {
	teardown := badoption.Duration(time.Minute)
	r := NewRouter(context.Background(), log.NewNOPFactory(), option.RouteOptions{
		LXIdleSuspend:          badoption.Duration(30 * time.Second),
		LXIdleSuspendReachable: badoption.Duration(5 * time.Minute),
		LXIdleTeardown:         &teardown,
	}, option.DNSOptions{})
	if got := idleFieldsOf(r); got != (idleFields{}) {
		t.Fatalf("route.lx_idle_* must not reach the router directly, got %+v", got)
	}
}

// lx:end idle-suspend
