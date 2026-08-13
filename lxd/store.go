//go:build with_lxd

package lxd

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

// store is the daemon's on-disk state: the last config that reached STARTED
// (last_good.json), the currently staged candidate (candidate.json) and the
// pending marker that flags an apply interrupted by a process death. All
// writes are atomic (tmp + fsync + rename) so a crash never leaves a torn
// file, and boot never loads a candidate — only last-good.
type store struct {
	dir string
}

func newStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, E.Cause(err, "create state directory")
	}
	return &store{dir: dir}, nil
}

func (s *store) lastGoodPath() string  { return filepath.Join(s.dir, "last_good.json") }
func (s *store) candidatePath() string { return filepath.Join(s.dir, "candidate.json") }
func (s *store) pendingPath() string   { return filepath.Join(s.dir, "pending") }
func (s *store) runStatePath() string  { return filepath.Join(s.dir, "was_running") }

func (s *store) writeAtomic(path string, content []byte) error {
	temp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err = temp.Write(content); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tempName)
		return err
	}
	if err = os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return err
	}
	return nil
}

// readOptional returns (content, true, nil) when the file exists, ("", false,
// nil) when it is absent, and ("", false, err) on a real read error — callers
// must not silently treat a permission/IO failure as "no file".
func readOptional(path string) (string, bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(content), true, nil
}

func (s *store) LoadLastGood() (string, bool, error) {
	return readOptional(s.lastGoodPath())
}

func (s *store) SaveLastGood(content string) error {
	return s.writeAtomic(s.lastGoodPath(), []byte(content))
}

// SetWasRunning records whether the core should be up, so a daemon restart
// (crash, reboot) can restore the last operator-intended state instead of a
// fixed default — the boxdd "was running" model. "stopped" is written, not
// expressed by deleting the file: an absent file means fresh state where no
// intent was ever recorded, and bootstrap must be able to tell the two apart
// (a fresh install with an explicit seed boots; an operator's stop sticks).
func (s *store) SetWasRunning(running bool) error {
	if running {
		return s.writeAtomic(s.runStatePath(), []byte("1"))
	}
	return s.writeAtomic(s.runStatePath(), []byte("0"))
}

// WasRunning returns the recorded run intent. recorded=false means fresh
// state: no intent was ever written (or the state dir predates run-state).
func (s *store) WasRunning() (running bool, recorded bool) {
	content, exists, _ := readOptional(s.runStatePath())
	if !exists {
		return false, false
	}
	return strings.TrimSpace(content) == "1", true
}

func (s *store) clientsPath() string { return filepath.Join(s.dir, "clients.json") }

func (s *store) LoadClients() ([]trustedClient, error) {
	content, exists, err := readOptional(s.clientsPath())
	if err != nil {
		return nil, E.Cause(err, "read clients")
	}
	if !exists {
		return nil, nil
	}
	var clients []trustedClient
	if err = json.Unmarshal([]byte(content), &clients); err != nil {
		return nil, E.Cause(err, "decode clients")
	}
	return clients, nil
}

func (s *store) SaveClients(clients []trustedClient) error {
	encoded, err := json.MarshalIndent(clients, "", "  ")
	if err != nil {
		return err
	}
	return s.writeAtomic(s.clientsPath(), encoded)
}

func (s *store) StageCandidate(content string) (string, error) {
	if err := s.writeAtomic(s.candidatePath(), []byte(content)); err != nil {
		return "", err
	}
	return s.candidatePath(), nil
}

func (s *store) SetPending(sha string) error {
	return s.writeAtomic(s.pendingPath(), []byte(sha))
}

func (s *store) ClearPending() {
	os.Remove(s.pendingPath())
}

func (s *store) PendingSHA() (string, bool) {
	content, exists, _ := readOptional(s.pendingPath())
	return content, exists
}
