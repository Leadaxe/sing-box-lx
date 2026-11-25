package impair

import "github.com/sagernet/sing-box/adapter"

// DownStats exposes the receive-direction counters to the integration test,
// which lives in package impair_test (it starts a full instance through
// include, which imports this package).
func DownStats(out adapter.Outbound) Stats {
	return out.(*Outbound).shaper.model.stats(dirDown)
}

// Stopped reports whether the outbound's shaper was closed (its Close ran).
func Stopped(out adapter.Outbound) bool {
	return isClosed(out.(*Outbound).shaper.stopped)
}
