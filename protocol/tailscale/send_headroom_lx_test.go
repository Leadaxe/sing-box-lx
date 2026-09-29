//go:build with_gvisor

package tailscale

import (
	"testing"

	"github.com/sagernet/tailscale/net/packet"
	"github.com/sagernet/wireguard-go/device"
)

// magicsock rejects any Bind.Send offset other than the Geneve header length
// on the direct UDP path; the device passes MessageEncapsulatingTransportSize.
func TestSendHeadroomMatchesGeneveHeader(t *testing.T) {
	if device.MessageEncapsulatingTransportSize != packet.GeneveFixedHeaderLength {
		t.Fatalf("wireguard-go send headroom %d != Geneve header length %d",
			device.MessageEncapsulatingTransportSize, packet.GeneveFixedHeaderLength)
	}
}
