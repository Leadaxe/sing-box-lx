//go:build with_lxd && windows

package lxd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/windows"
)

// CreateInviteFile creates the --invite-out file before the mint (SPEC 103
// §2.13): CREATE_NEW, no link followed at the last component, and then the
// final path of the handle must be the path asked for — a junction in a
// parent would have carried an elevated write into someone else's
// directory. On a mismatch the file is deleted through the handle and the
// command refuses.
func CreateInviteFile(path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, E.Cause(err, "resolve invite file ", path)
	}
	name, err := windows.UTF16PtrFromString(absolute)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name,
		windows.GENERIC_WRITE|windows.DELETE,
		windows.FILE_SHARE_READ,
		nil,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, E.New(path, ": already exists; the invite is written to a new file only")
		}
		return nil, E.Cause(err, "create invite file ", path)
	}
	final, err := finalPathOfHandle(handle)
	// The final path is in long form; a requested 8.3 short name
	// (C:\Users\RUNNER~1) is the same path, a junction is not — and
	// GetLongPathName expands names without resolving reparse points.
	if err == nil && !samePathFold(final, absolute) && !samePathFold(final, longPathName(absolute)) {
		err = E.New(path, ": resolves to ", final, " (a junction or link on the path); refusing to write the invite there")
	}
	if err != nil {
		deleteByHandle(handle)
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), absolute), nil
}

// finalPathOfHandle is GetFinalPathNameByHandle in its DOS form without the
// \\?\ prefix.
func finalPathOfHandle(handle windows.Handle) (string, error) {
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	for {
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", E.Cause(err, "resolve the final path")
		}
		if int(length) < len(buffer) {
			return stripLongPathPrefix(windows.UTF16ToString(buffer[:length])), nil
		}
		buffer = make([]uint16, length+1)
	}
}

// longPathName expands 8.3 short components; on failure the path as given.
func longPathName(path string) string {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	length, err := windows.GetLongPathName(name, &buffer[0], uint32(len(buffer)))
	if err != nil || length == 0 || int(length) >= len(buffer) {
		return path
	}
	return windows.UTF16ToString(buffer[:length])
}

// stripLongPathPrefix turns \\?\C:\x into C:\x and \\?\UNC\h\s into \\h\s.
func stripLongPathPrefix(path string) string {
	switch {
	case strings.HasPrefix(path, `\\?\UNC\`):
		return `\\` + path[len(`\\?\UNC\`):]
	case strings.HasPrefix(path, `\\?\`):
		return path[len(`\\?\`):]
	}
	return path
}

// samePathFold compares two Windows paths the way the file system does:
// cleaned, case-insensitive.
func samePathFold(first, second string) bool {
	return strings.EqualFold(filepath.Clean(first), filepath.Clean(second))
}

// deleteByHandle marks an open file for deletion through its handle — never
// by path, which could name something else by now.
func deleteByHandle(handle windows.Handle) {
	disposition := byte(1)
	_ = windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &disposition, 1)
}
