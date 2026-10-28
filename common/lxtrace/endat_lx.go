//go:build with_lx_command

package lxtrace

import "time"

// EndAt is End with an explicit end time, for a step whose completion the
// caller learns after the fact: a WireGuard handshake is observed through the
// peer's last-handshake time once the dial that triggered it returns, so the
// stage ends when the handshake completed, not when it was noticed. A time
// before the stage start gives a zero duration. The second close is ignored.
func (s *Stage) EndAt(end time.Time, err error) {
	if s == nil {
		return
	}
	if err == nil {
		s.finishAt(OK, "", end, nil)
		return
	}
	s.finishAt(Failed, err.Error(), end, nil)
}
