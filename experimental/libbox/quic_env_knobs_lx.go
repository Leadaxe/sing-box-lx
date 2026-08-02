package libbox

import "os"

// lx: runtime diagnostic knobs for quic-go's env escape hatches.
//
// quic-go re-reads QUIC_GO_DISABLE_GSO / QUIC_GO_DISABLE_ECN from the
// environment on every new UDP socket it wraps (sys_conn_helper_linux.go
// isGSOEnabled, sys_conn_oob.go), and every QUIC client dial creates a fresh
// socket — so flipping these via Go-side os.Setenv takes effect on the next
// (re)connect of any quic-go consumer (hysteria2, tuic, masque-h3) without a
// process restart. Wired to the LxBox Debug API for on-device A/B of the
// "protected UDP socket silently drops offloaded/marked sends on vendor
// kernels" family of failures (first seen 2026-08-02: every QUIC outbound
// dead on OnePlus/MTK/Android 15 inside the VPN process while TCP and
// wireguard-go UDP live; the same binary unprotected works).
//
// Static like Version(): safe to call at any moment, no Setup or running
// service required.

// SetQuicGSODisabled toggles quic-go's UDP GSO sends (sendmsg+UDP_SEGMENT).
// true — force-disable GSO for sockets created afterwards; false — restore
// the library default (auto-detect by kernel/socket probe).
func SetQuicGSODisabled(disabled bool) {
	setQuicEnvKnob("QUIC_GO_DISABLE_GSO", disabled)
}

// SetQuicECNDisabled toggles quic-go's ECN marking (TOS/TCLASS cmsg on every
// send). Same semantics as SetQuicGSODisabled.
func SetQuicECNDisabled(disabled bool) {
	setQuicEnvKnob("QUIC_GO_DISABLE_ECN", disabled)
}

func setQuicEnvKnob(name string, disabled bool) {
	if disabled {
		os.Setenv(name, "true")
	} else {
		// Unset (not "false") so the library default keeps auto-detecting;
		// an explicit value would also work, but unset matches "untouched".
		os.Unsetenv(name)
	}
}
