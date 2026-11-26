package urltest

import (
	"time"

	"github.com/sagernet/sing-box/adapter"
)

// DeleteURLTestHistoryNotAfterLx deletes the history of tag unless it was stored
// after since, in one step under the storage lock. It reports whether the tag has
// no history now. lx: SPEC 138 — a self-heal verdict "dead" must not erase an
// answer a scheduled check stored after the self-heal probe started.
func (s *HistoryStorage) DeleteURLTestHistoryNotAfterLx(tag string, since time.Time) bool {
	s.access.Lock()
	defer s.access.Unlock()
	history := s.delayHistory[tag]
	if history == nil {
		return true
	}
	if history.Time.After(since) {
		return false
	}
	delete(s.delayHistory, tag)
	s.notifyUpdated()
	return true
}

// RestoreURLTestHistoryLx stores history for tag only when tag has none now, in
// one step under the storage lock. It reports whether it stored. lx: SPEC 019 —
// a round_robin run whose batch got no answer gives back the history its failed
// probes erased, unless something wrote it meanwhile.
func (s *HistoryStorage) RestoreURLTestHistoryLx(tag string, history *adapter.URLTestHistory) bool {
	s.access.Lock()
	defer s.access.Unlock()
	if s.delayHistory[tag] != nil {
		return false
	}
	s.delayHistory[tag] = history
	s.notifyUpdated()
	return true
}
