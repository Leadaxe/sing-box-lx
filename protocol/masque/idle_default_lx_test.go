package masque

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/service"
)

func idleDuration(d time.Duration) *badoption.Duration {
	value := badoption.Duration(d)
	return &value
}

// lx: SPEC 021 B1, SPEC 098 — the suspend window is the node's idle_timeout
// when set (an explicit "0" or a negative value keeps the tunnel up whatever
// the global default), otherwise lx.masque.idle_timeout, otherwise off. 0 means
// the idle watcher never starts and the tunnel stays up until Close.
func TestIdleWindowPriority(t *testing.T) {
	cases := []struct {
		name   string
		node   *badoption.Duration
		global time.Duration
		want   time.Duration
	}{
		{"absent, no global", nil, 0, 0},
		{"absent, global", nil, 5 * time.Minute, 5 * time.Minute},
		{"node zero, no global", idleDuration(0), 0, 0},
		{"node zero beats global", idleDuration(0), 5 * time.Minute, 0},
		{"node negative beats global", idleDuration(-time.Second), 5 * time.Minute, 0},
		{"node positive, no global", idleDuration(2 * time.Minute), 0, 2 * time.Minute},
		{"node positive beats global", idleDuration(2 * time.Minute), 5 * time.Minute, 2 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := idleWindow(tc.node, tc.global); got != tc.want {
				t.Fatalf("idleWindow = %v, want %v", got, tc.want)
			}
		})
	}
}

// NewOutbound reads the global default from the box context: a node without
// its own key inherits lx.masque.idle_timeout, a node with an explicit "0"
// does not, and without a resolved block in the context the window is off.
func TestNewOutboundIdleTimeoutFromContext(t *testing.T) {
	withGlobal := service.ContextWithPtr(service.ContextWithDefaultRegistry(context.Background()),
		&option.LXResolved{MASQUE: option.LXMASQUEResolved{IdleTimeout: 5 * time.Minute}})
	cases := []struct {
		name string
		ctx  context.Context
		node *badoption.Duration
		want time.Duration
	}{
		{"no block", context.Background(), nil, 0},
		{"global only", withGlobal, nil, 5 * time.Minute},
		{"node zero", withGlobal, idleDuration(0), 0},
		{"node wins", withGlobal, idleDuration(time.Minute), time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options := standardOptions("h3", "https://example.com/.well-known/masque/ip/*/*/")
			options.IdleTimeout = tc.node
			outbound, err := NewOutbound(tc.ctx, nil, log.NewNOPFactory().Logger(), "masque-idle", options)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			defer outbound.(*Outbound).Close()
			if got := outbound.(*Outbound).idleTimeout; got != tc.want {
				t.Fatalf("idleTimeout = %v, want %v", got, tc.want)
			}
		})
	}
}
