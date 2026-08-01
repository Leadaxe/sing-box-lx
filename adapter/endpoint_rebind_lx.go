package adapter

// lx: SPEC 041 v2 — StaleRebindable is implemented by a WG/AWG endpoint so
// the libbox wake nudge (CommandServer.RebindStaleEndpoints) can ask it to
// heal a provably dead session — rebind the socket and re-initiate — without
// importing protocol/wireguard. The call is best-effort and cheap: a healthy,
// sleeping (SPEC 020), deliberately-stopped or closing endpoint treats it as
// a no-op, and it never wakes a sleeper.
type StaleRebindable interface {
	Tag() string
	RebindStale()
}
