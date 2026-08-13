//go:build with_lx_command && !linux

package lxd

// Off Linux none of these tables exist. The providers report "nothing to say"
// rather than an error, and the endpoint still answers with whatever the
// lease and label providers found — the same principle as rssUnsupported in
// SPEC 065: a value's absence is a state, not a platform complaint.
//
// Note the Linux implementations return false too when a source is missing on
// that particular host (no /proc/net/arp, no bridge or ubus binary), so a
// client needs exactly one branch for both cases.

func enrichARP(_ map[string]*clientEntry) bool { return false }

func enrichBridge(_ map[string]*clientEntry) bool { return false }

func enrichWireless(_ map[string]*clientEntry) bool { return false }

// defaultLeaseFiles still names the common locations: leases are plain files
// and a non-Linux host may well have a dhcpd or Kea one. bootpd's path (macOS)
// is included because the parser upstream already handles that format.
func defaultLeaseFiles() []string {
	return []string{
		"/var/lib/dhcp/dhcpd.leases",
		"/var/lib/dhcpd/dhcpd.leases",
		"/var/lib/kea/kea-leases4.csv",
		"/var/lib/kea/kea-leases6.csv",
		"/var/db/dhcpd_leases",
	}
}
