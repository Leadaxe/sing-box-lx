//go:build with_lx_command

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/protocol/group"
)

// lx: SPEC 138 — GetPool carries the self-heal state of a round_robin group.

type pooledGroup struct {
	listedOutbound
	slots []group.PoolSlot
	heal  group.PoolHeal
}

func (g *pooledGroup) Pool() []group.PoolSlot   { return g.slots }
func (g *pooledGroup) PoolHeal() group.PoolHeal { return g.heal }

// poolOnly is a pool provider of an older shape: no group-wide state.
type poolOnly struct {
	listedOutbound
	slots []group.PoolSlot
}

func (g *poolOnly) Pool() []group.PoolSlot { return g.slots }

func TestGetPool_selfHealState_LX(t *testing.T) {
	service := &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance: &Instance{
			ctx:                   context.Background(),
			urlTestHistoryStorage: urltest.NewHistoryStorage(),
			outboundManager: &listingOutboundManager{outbounds: []adapter.Outbound{
				&pooledGroup{
					listedOutbound: listedOutbound{tag: "rr"},
					slots: []group.PoolSlot{
						{Slot: 0, Tag: "a", Delay: 120, Quarantined: true, Conns: 40, Fails: 9},
						{Slot: 1, Tag: "b", Delay: 0, Conns: 3},
					},
					heal: group.PoolHeal{ShiftedKeys: 4, MarkedKeys: 1, Frozen: true, FrozenFor: 2100 * time.Millisecond},
				},
				&pooledGroup{
					listedOutbound: listedOutbound{tag: "net-run"},
					slots:          []group.PoolSlot{{Slot: 0, Tag: "a", Delay: 5}},
					heal:           group.PoolHeal{Frozen: true},
				},
				&poolOnly{listedOutbound: listedOutbound{tag: "old"}, slots: []group.PoolSlot{{Slot: 0, Tag: "x", Delay: 7}}},
				&listedOutbound{tag: "proxy"},
			}},
		},
	}
	list, err := service.GetPool(context.Background(), &GetPoolRequest{GroupTag: "rr"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Slots) != 2 {
		t.Fatalf("slots: %+v", list.Slots)
	}
	if a := list.Slots[0]; a.Tag != "a" || a.Delay != 120 || !a.Quarantined || a.Connections != 40 || a.ConfirmedFailures != 9 {
		t.Fatalf("slot a: %+v", a)
	}
	if b := list.Slots[1]; b.Tag != "b" || b.Delay != 0 || b.Quarantined || b.Connections != 3 || b.ConfirmedFailures != 0 {
		t.Fatalf("slot b: %+v", b)
	}
	if list.ShiftedKeys != 4 || list.MarkedKeys != 1 || !list.Frozen || list.FrozenSeconds != 3 {
		t.Fatalf("group state (2.1 s rounds up to 3): %+v", list)
	}

	list, err = service.GetPool(context.Background(), &GetPoolRequest{GroupTag: "net-run"})
	if err != nil {
		t.Fatal(err)
	}
	if !list.Frozen || list.FrozenSeconds != 0 {
		t.Fatalf("frozen for the network run alone: %+v", list)
	}

	list, err = service.GetPool(context.Background(), &GetPoolRequest{GroupTag: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Slots) != 1 || list.ShiftedKeys != 0 || list.MarkedKeys != 0 || list.Frozen || list.FrozenSeconds != 0 {
		t.Fatalf("no group-wide state: %+v", list)
	}

	for _, tag := range []string{"proxy", "missing"} {
		list, err = service.GetPool(context.Background(), &GetPoolRequest{GroupTag: tag})
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Slots) != 0 || list.ShiftedKeys != 0 || list.Frozen {
			t.Fatalf("%s has no pool: %+v", tag, list)
		}
	}
}
