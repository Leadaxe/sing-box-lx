//go:build with_lxd && !unix && !windows

package lxd

import (
	"os"

	E "github.com/sagernet/sing/common/exceptions"
)

// CreateInviteFile creates the --invite-out file exclusively.
func CreateInviteFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, E.New(path, ": already exists; the invite is written to a new file only")
		}
		return nil, E.Cause(err, "create invite file ", path)
	}
	return file, nil
}
