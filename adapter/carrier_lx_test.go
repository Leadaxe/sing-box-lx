package adapter

import (
	"context"
	"io"
	"net"
	"os"
	"testing"

	M "github.com/sagernet/sing/common/metadata"
)

type carrierStub struct {
	outboundType string
	group        bool
}

func (s carrierStub) Type() string           { return s.outboundType }
func (s carrierStub) Tag() string            { return s.outboundType + "-tag" }
func (s carrierStub) Network() []string      { return nil }
func (s carrierStub) Dependencies() []string { return nil }
func (s carrierStub) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, os.ErrInvalid
}

func (s carrierStub) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

type carrierGroupStub struct{ carrierStub }

func (carrierGroupStub) All() []string                              { return nil }
func (carrierGroupStub) Selected(string) Outbound                   { return nil }
func (carrierGroupStub) AttachConnection(io.Closer) (detach func()) { return func() {} }

// lx: SPEC 151 — the first non-group outbound recorded wins; groups are
// skipped so nested groups resolve down to the member; a context without a
// record and a nil outbound are no-ops.
func TestCarrierRecord(t *testing.T) {
	ctx, record := ContextWithCarrierRecord(context.Background())
	RecordCarrier(ctx, carrierGroupStub{carrierStub{outboundType: "selector"}})
	if record.Type != "" {
		t.Fatalf("a group must not be recorded: %+v", record)
	}
	RecordCarrier(ctx, carrierStub{outboundType: "vless"})
	RecordCarrier(ctx, carrierStub{outboundType: "socks"})
	if record.Type != "vless" || record.Tag != "vless-tag" {
		t.Fatalf("the first non-group write wins: %+v", record)
	}
	RecordCarrier(context.Background(), carrierStub{outboundType: "vless"})
	RecordCarrier(ctx, nil)
	if ResolveChainLeafRecords(t) != "trojan" {
		t.Fatal("ResolveChainLeaf must record the picked member")
	}
}

// ResolveChainLeafRecords returns what ResolveChainLeaf records for a picked
// member outside any chain.
func ResolveChainLeafRecords(t *testing.T) string {
	t.Helper()
	ctx, record := ContextWithCarrierRecord(context.Background())
	leaf, err := ResolveChainLeaf(ctx, carrierStub{outboundType: "trojan"})
	if err != nil || leaf.Type() != "trojan" {
		t.Fatalf("leaf %v err %v", leaf, err)
	}
	return record.Type
}

var (
	_ Outbound      = carrierStub{}
	_ OutboundGroup = carrierGroupStub{}
)
