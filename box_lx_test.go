package box

import (
	"context"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

// lx: SPEC 097 — every box gets a fresh build-budget slot, even on a service
// registry shared across boxes (daemon reloads), so no budget outlives its box.
func TestApplyLXOptions_freshBudgetSlotPerBox(t *testing.T) {
	ctx := service.ContextWithDefaultRegistry(context.Background())
	var options option.Options
	first, err := applyLXOptions(ctx, &options, log.NewNOPFactory().Logger())
	if err != nil {
		t.Fatal(err)
	}
	firstSlot := service.PtrFromContext[adapter.LXBuildBudgetSlot](first)
	if firstSlot == nil {
		t.Fatal("applyLXOptions must register a budget slot")
	}
	second, err := applyLXOptions(ctx, &options, log.NewNOPFactory().Logger())
	if err != nil {
		t.Fatal(err)
	}
	if secondSlot := service.PtrFromContext[adapter.LXBuildBudgetSlot](second); secondSlot == nil || secondSlot == firstSlot {
		t.Fatal("a second box on the same registry must get a fresh slot")
	}
}

// lx: SPEC 151 — a detour_type no registry of the build knows fails the
// start; a known outbound or endpoint type passes; mode off skips the check.
func TestApplyLXOptions_sniffDecoyDetourTypes(t *testing.T) {
	ctx := service.ContextWithDefaultRegistry(context.Background())
	outbounds := outbound.NewRegistry()
	outbound.Register[option.DirectOutboundOptions](outbounds, "direct", nil)
	outbound.Register[option.SOCKSOutboundOptions](outbounds, "socks", nil)
	outbound.Register[option.VLESSOutboundOptions](outbounds, "vless", nil)
	endpoints := endpoint.NewRegistry()
	endpoint.Register[option.WireGuardEndpointOptions](endpoints, "wireguard", nil)
	ctx = service.ContextWith[adapter.OutboundRegistry](ctx, outbounds)
	ctx = service.ContextWith[adapter.EndpointRegistry](ctx, endpoints)
	decoy := func(mode string, detourTypes ...string) option.Options {
		return option.Options{LX: &option.LXOptions{QUICSniffDecoy: &option.LXQUICSniffDecoyOptions{Mode: mode, DetourType: detourTypes}}}
	}
	for _, options := range []option.Options{decoy("detour"), decoy("detour", "direct", "socks", "wireguard"), decoy("off", "nope")} {
		if _, err := applyLXOptions(ctx, &options, log.NewNOPFactory().Logger()); err != nil {
			t.Fatalf("%+v: %v", options.LX.QUICSniffDecoy, err)
		}
	}
	options := decoy("detour", "vless", "nope")
	if _, err := applyLXOptions(ctx, &options, log.NewNOPFactory().Logger()); err == nil || !strings.Contains(err.Error(), `unknown outbound type "nope"`) {
		t.Fatalf("got %v", err)
	}
}
