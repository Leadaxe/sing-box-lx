//go:build with_lxd

package lxd

import (
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	E "github.com/sagernet/sing/common/exceptions"
)

// maxClientNameLength bounds an operator's client label, in characters.
const maxClientNameLength = 64

// NormalizeClientName is the norm of a client name on mint (SPEC 103 §2.13):
// trimmed of surrounding spaces, then empty (no name, as before) or 1 to 64
// printable characters. The daemon enforces it on /admin/client-code; the CLI
// checks --invite-name and --name with it before touching anything.
func NormalizeClientName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if !utf8.ValidString(name) {
		return "", E.New("client name: not valid UTF-8")
	}
	if length := utf8.RuneCountInString(name); length > maxClientNameLength {
		return "", E.New("client name: ", length, " characters, at most ", maxClientNameLength, " allowed")
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return "", E.New("client name: contains the non-printable character ", strconv.QuoteRune(r))
		}
	}
	return name, nil
}

// CheckInviteFileAbsent is install's first step with --invite-out: an
// existing file refuses the whole command before anything changes.
func CheckInviteFileAbsent(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return E.New(path, ": already exists; the invite is written to a new file only")
	} else if !os.IsNotExist(err) {
		return E.Cause(err, "check invite file ", path)
	}
	return nil
}

// WriteInvite puts the invite into a file made by CreateInviteFile: the
// invite line and a newline, flushed, the file closed.
func WriteInvite(file *os.File, invite string) error {
	_, err := io.WriteString(file, invite+"\n")
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return E.Cause(err, "write invite file ", file.Name())
	}
	return nil
}

// DiscardInviteFile removes an invite file whose mint failed: the launcher
// judges by the exit code, and an empty file must not look like an invite.
func DiscardInviteFile(file *os.File) {
	_ = file.Close()
	_ = os.Remove(file.Name())
}
