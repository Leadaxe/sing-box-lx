//go:build with_lxd && (linux || darwin)

package lxd

import "net"

// decorateInterfaces fills the "passport" half of each interface — name, MAC,
// addresses, MTU, up/down — from net.Interfaces(), which is portable, and
// joins it onto the counters the platform reader produced.
//
// Every interface is reported, including `lo` and down ones (owner decision):
// "wan went down" is exactly what an operator wants to see, and filtering out
// noise like ifb0 is the UI's job — it knows what it is displaying.
func decorateInterfaces(counters []rawInterface) []rawInterface {
	system, err := netInterfaces()
	if err != nil {
		return counters
	}
	byName := make(map[string]net.Interface, len(system))
	for _, item := range system {
		byName[item.Name] = item
	}

	decorated := make([]rawInterface, 0, len(counters))
	seen := make(map[string]bool, len(counters))
	for _, entry := range counters {
		seen[entry.Name] = true
		if system, ok := byName[entry.Name]; ok {
			applyInterfaceMeta(&entry, system)
		}
		decorated = append(decorated, entry)
	}
	// An interface the kernel lists but the counter source does not (rare, but
	// possible mid-registration) still belongs in the answer with zeroes.
	for _, item := range system {
		if seen[item.Name] {
			continue
		}
		entry := rawInterface{Name: item.Name}
		applyInterfaceMeta(&entry, item)
		decorated = append(decorated, entry)
	}
	return decorated
}

func applyInterfaceMeta(entry *rawInterface, system net.Interface) {
	entry.Up = system.Flags&net.FlagUp != 0
	entry.MTU = system.MTU
	// Empty for a tunnel device: a valid state, not a failure.
	entry.Mac = system.HardwareAddr.String()
	addresses, err := system.Addrs()
	if err != nil {
		return
	}
	for _, address := range addresses {
		entry.Addresses = append(entry.Addresses, address.String())
	}
}
