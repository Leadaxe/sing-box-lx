//go:build with_lxd && unix

package lxd

import (
	"os"
	"syscall"

	E "github.com/sagernet/sing/common/exceptions"
)

// CreateInviteFile creates the --invite-out file before the mint (SPEC 103
// §2.13): exclusively and without following a link at the last component
// (O_CREAT|O_EXCL|O_NOFOLLOW), 0600 — the invite grants trust. An existing
// file is a refusal.
func CreateInviteFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, E.New(path, ": already exists; the invite is written to a new file only")
		}
		return nil, E.Cause(err, "create invite file ", path)
	}
	return file, nil
}
