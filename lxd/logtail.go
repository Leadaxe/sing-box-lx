//go:build with_lxd

package lxd

import (
	"os"
	"strings"
)

// The daemon's own log is the one channel gRPC cannot carry: SubscribeLog
// streams what the CORE logged through the instance's PlatformLogWriter, while
// `log.Info("lxd: …")`, bootstrap failures and runtime panics go to the
// process's stdout/stderr — i.e. into lxd.log via the rotator's dup2.
//
// That is precisely the worst-case channel: when the core never came up there
// is no instance to log through, SubscribeLog is empty, and the reason sits in
// a file on a remote host. GET /admin/info only advertises log_path, which a
// launcher cannot read. This serves the bytes (SPEC 065).
//
// A snapshot rather than a live stream: the gRPC log stream already covers
// "follow along", and chunked streaming would need its own timeout discipline
// against the server's IdleTimeout.

const (
	defaultLogTailLines = 200
	maxLogTailLines     = 5000
	// maxLogTailBytes caps how much of the file is read into memory per
	// generation. Lines are bounded in practice, but the log is attacker-
	// adjacent (it records remote input), so the read is bounded too.
	maxLogTailBytes = 8 << 20
)

// tailLog returns the last `lines` lines of the log, reading the rotated
// generation when the current file alone is shorter. Rotation shifts content
// into lxd.log.1 mid-request, so a tail taken right after a rotation would
// otherwise return almost nothing.
func tailLog(path string, lines int) (content string, found bool, err error) {
	current, exists, err := readTailBytes(path)
	if err != nil {
		return "", false, err
	}
	collected := splitLines(current)
	found = exists
	if len(collected) < lines {
		previous, prevExists, prevErr := readTailBytes(path + ".1")
		if prevErr != nil {
			return "", false, prevErr
		}
		if prevExists {
			found = true
			collected = append(splitLines(previous), collected...)
		}
	}
	if !found {
		return "", false, nil
	}
	if len(collected) > lines {
		collected = collected[len(collected)-lines:]
	}
	return strings.Join(collected, "\n"), true, nil
}

// readTailBytes reads at most maxLogTailBytes from the END of the file: the
// tail is what matters, and a log that grew to the rotation ceiling should not
// be pulled into memory whole.
func readTailBytes(path string) (content []byte, found bool, err error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	size := info.Size()
	offset := int64(0)
	if size > maxLogTailBytes {
		offset = size - maxLogTailBytes
		size = maxLogTailBytes
	}
	buffer := make([]byte, size)
	read, err := file.ReadAt(buffer, offset)
	// A short read at EOF is normal when the file shrank between Stat and
	// ReadAt (rotation); keep what we got rather than failing the request.
	if err != nil && read == 0 {
		return nil, false, err
	}
	return buffer[:read], true, nil
}

func splitLines(content []byte) []string {
	trimmed := strings.TrimRight(string(content), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func clampTailLines(requested int) int {
	if requested <= 0 {
		return defaultLogTailLines
	}
	if requested > maxLogTailLines {
		return maxLogTailLines
	}
	return requested
}
