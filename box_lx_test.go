package box

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/adapter"
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
