//go:build with_lx_command && darwin

package lxd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

const launchdLabel = "com.leadaxe.sing-box-lxd"

func launchdPlistPath() string {
	return filepath.Join("/Library/LaunchDaemons", launchdLabel+".plist")
}

// InstallService writes a LaunchDaemon plist whose ProgramArguments are the
// current daemon invocation (args, minus the --service flag) and bootstraps it
// into launchd. Requires root. The daemon's own run-state memory handles "as
// it was" across reboots; launchd only keeps the process alive (KeepAlive).
func InstallService(daemonArgs []string) error {
	executable, err := os.Executable()
	if err != nil {
		return E.Cause(err, "locate own binary")
	}
	logPath := "/Library/Application Support/sing-box-lxd/lxd.log"
	programArgs := append([]string{executable}, daemonArgs...)
	plist := buildPlist(launchdLabel, programArgs, logPath)
	if err = os.WriteFile(launchdPlistPath(), []byte(plist), 0o644); err != nil {
		return E.Cause(err, "write plist (need root?)")
	}
	// bootout an old instance if present, then bootstrap fresh.
	_ = exec.Command("launchctl", "bootout", "system/"+launchdLabel).Run()
	out, err := exec.Command("launchctl", "bootstrap", "system", launchdPlistPath()).CombinedOutput()
	if err != nil {
		return E.Cause(E.New(strings.TrimSpace(string(out))), "launchctl bootstrap")
	}
	fmt.Println("lxd: installed LaunchDaemon", launchdLabel)
	fmt.Println("lxd: plist", launchdPlistPath())
	fmt.Println("lxd: logs ", logPath)
	return nil
}

// PrintService renders the LaunchDaemon plist that InstallService would write,
// without touching the system — a dry run to inspect before installing.
func PrintService(daemonArgs []string) error {
	executable, err := os.Executable()
	if err != nil {
		return E.Cause(err, "locate own binary")
	}
	logPath := "/Library/Application Support/sing-box-lxd/lxd.log"
	programArgs := append([]string{executable}, daemonArgs...)
	fmt.Print(buildPlist(launchdLabel, programArgs, logPath))
	fmt.Fprintln(os.Stderr, "\n# would install to", launchdPlistPath())
	fmt.Fprintln(os.Stderr, "# then: sudo launchctl bootstrap system", launchdPlistPath())
	return nil
}

func UninstallService() error {
	out, err := exec.Command("launchctl", "bootout", "system/"+launchdLabel).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such process") {
		return E.Cause(E.New(strings.TrimSpace(string(out))), "launchctl bootout")
	}
	if err := os.Remove(launchdPlistPath()); err != nil && !os.IsNotExist(err) {
		return E.Cause(err, "remove plist")
	}
	fmt.Println("lxd: uninstalled LaunchDaemon", launchdLabel)
	return nil
}

func buildPlist(label string, programArgs []string, logPath string) string {
	var args strings.Builder
	for _, arg := range programArgs {
		args.WriteString("        <string>")
		args.WriteString(plistEscape(arg))
		args.WriteString("</string>\n")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
%s    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, plistEscape(label), args.String(), plistEscape(logPath), plistEscape(logPath))
}

func plistEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
