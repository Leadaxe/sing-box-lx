//go:build with_lxd && !windows

package lxd

import (
	"fmt"
	"io"
	"os"
)

// The service hooks the command calls on every platform (SPEC 103); outside
// Windows they keep the behaviour the command had before them.

// ServiceDefaultLogFile: no log_file is written into daemon.json here; the
// daemon derives its log path from the state dir (DefaultLogPath).
func ServiceDefaultLogFile() string {
	return ""
}

// PrepareServiceHome: launchd's support directory is created with the plist
// (service_darwin.go); nothing to take over beforehand.
func PrepareServiceHome(out io.Writer) error {
	return nil
}

// CheckUserScope: install-user is served where it exists (a LaunchAgent) or
// printed (linux).
func CheckUserScope() error {
	return nil
}

// ServiceStateDirAccessError: an unreadable service home falls back to the
// dev state dir, as it always has.
func ServiceStateDirAccessError() error {
	return nil
}

// ServiceRestartCommand is the launchd restart the install summary prints.
func ServiceRestartCommand(user bool) string {
	if user {
		return fmt.Sprintf("launchctl kickstart -k gui/%d/%s", os.Getuid(), launchdLabel)
	}
	return "sudo launchctl kickstart -k system/" + launchdLabel
}
