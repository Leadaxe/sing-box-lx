package wireguard

import (
	"context"
	"runtime"
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/wireguard-go/conn"
)

func tuningEndpoint(resolved *option.LXResolved) *Endpoint {
	ctx := context.Background()
	if resolved != nil {
		ctx = service.ContextWithPtr(ctx, resolved)
	}
	return &Endpoint{options: EndpointOptions{Context: ctx}}
}

// lx: SPEC 158 part 2 — lx.wg.buffers.read_datagram_count reaches the bind,
// and anything that stands for "not set" leaves the bind alone.
func TestTuneStandardBindReadCount(t *testing.T) {
	unset := conn.NewStdNetBind(nil).BatchSize()
	for name, resolved := range map[string]*option.LXResolved{
		"no lx block":     nil,
		"zero value":      {},
		"the default 128": {WG: option.LXWGResolved{ReadDatagramCount: conn.IdealBatchSize}},
	} {
		bind := conn.NewStdNetBind(nil).(*conn.StdNetBind)
		if err := tuningEndpoint(resolved).tuneStandardBind(bind); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := bind.BatchSize(); got != unset {
			t.Fatalf("%s: batch size %d, want the bind's own %d", name, got, unset)
		}
	}
	bind := conn.NewStdNetBind(nil).(*conn.StdNetBind)
	endpoint := tuningEndpoint(&option.LXResolved{WG: option.LXWGResolved{ReadDatagramCount: 8}})
	if err := endpoint.tuneStandardBind(bind); err != nil {
		t.Fatal(err)
	}
	want := unset
	if runtime.GOOS == "linux" || runtime.GOOS == "android" {
		want = 8
	}
	if got := bind.BatchSize(); got != want {
		t.Fatalf("batch size %d, want %d", got, want)
	}
}
