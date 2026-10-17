package tun

import (
	"testing"

	"github.com/sagernet/sing-tun"
)

// InterfaceName is the lx accessor the lxd daemon reads for its lifecycle
// hooks (SPEC 135): the name sits in tunOptions once the interface exists.
func TestInterfaceNameAccessor(t *testing.T) {
	inbound := &Inbound{}
	if inbound.InterfaceName() != "" {
		t.Fatal("no name before the interface exists")
	}
	inbound.tunOptions = tun.Options{Name: "utun9"}
	if inbound.InterfaceName() != "utun9" {
		t.Fatal("accessor must return tunOptions.Name")
	}
}
