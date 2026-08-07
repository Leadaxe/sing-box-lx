//go:build with_lx_command && darwin

package lxd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

const launchdLabel = "com.leadaxe.sing-box-lxd"

// serviceScope captures where a launchd job lives. The system scope is a
// LaunchDaemon (root, starts before login, machine-wide) — needed for a
// headless server that owns TUN. The user scope is a LaunchAgent (the logged-in
// user, starts at login, no sudo) — the desktop UX, like ordinary .app helpers.
type serviceScope struct {
	user     bool
	plist    string // absolute plist path
	logPath  string
	bootTgt  string // launchctl domain target: "system" or "gui/<uid>"
	needRoot bool
}

func systemScope() serviceScope {
	return serviceScope{
		user:     false,
		plist:    filepath.Join("/Library/LaunchDaemons", launchdLabel+".plist"),
		logPath:  "/Library/Application Support/sing-box-lxd/lxd.log",
		bootTgt:  "system",
		needRoot: true,
	}
}

func userScope() serviceScope {
	home, _ := os.UserHomeDir()
	return serviceScope{
		user:     true,
		plist:    filepath.Join(home, "Library/LaunchAgents", launchdLabel+".plist"),
		logPath:  filepath.Join(home, "Library/Application Support/sing-box-lxd/lxd.log"),
		bootTgt:  "gui/" + strconv.Itoa(os.Getuid()),
		needRoot: false,
	}
}

// InstallService registers the daemon as a system LaunchDaemon (root).
func InstallService(daemonArgs []string) error {
	return installScope(systemScope(), daemonArgs)
}

// InstallUserService registers the daemon as a per-user LaunchAgent (no sudo).
func InstallUserService(daemonArgs []string) error {
	return installScope(userScope(), daemonArgs)
}

func installScope(scope serviceScope, daemonArgs []string) error {
	if scope.needRoot && os.Getuid() != 0 {
		return E.New("--service=install needs root (run with sudo); for a per-user agent without sudo use --service=install-user")
	}
	executable, err := os.Executable()
	if err != nil {
		return E.Cause(err, "locate own binary")
	}
	// Create the log/support directory so launchd's StandardOutPath is writable
	// from the first start — the operator no longer needs a separate mkdir.
	if err = os.MkdirAll(filepath.Dir(scope.logPath), 0o755); err != nil {
		return E.Cause(err, "create support directory")
	}
	if err = os.MkdirAll(filepath.Dir(scope.plist), 0o755); err != nil {
		return E.Cause(err, "create launchd directory")
	}
	programArgs := append([]string{executable}, daemonArgs...)
	plist := buildPlist(launchdLabel, programArgs, scope.logPath)
	if err = os.WriteFile(scope.plist, []byte(plist), 0o644); err != nil {
		return E.Cause(err, "write plist")
	}
	_ = exec.Command("launchctl", "bootout", scope.bootTgt+"/"+launchdLabel).Run()
	out, err := exec.Command("launchctl", "bootstrap", scope.bootTgt, scope.plist).CombinedOutput()
	if err != nil {
		return E.Cause(E.New(strings.TrimSpace(string(out))), "launchctl bootstrap")
	}
	kind := "LaunchDaemon (system)"
	if scope.user {
		kind = "LaunchAgent (user)"
	}
	fmt.Println("lxd: installed", kind, launchdLabel)
	fmt.Println("lxd: plist", scope.plist)
	fmt.Println("lxd: logs ", scope.logPath)
	return nil
}

// PrintService renders the plist InstallService would write, without touching
// the system — a dry run to inspect before installing.
func PrintService(daemonArgs []string) error {
	scope := systemScope()
	executable, err := os.Executable()
	if err != nil {
		return E.Cause(err, "locate own binary")
	}
	programArgs := append([]string{executable}, daemonArgs...)
	fmt.Print(buildPlist(launchdLabel, programArgs, scope.logPath))
	fmt.Fprintln(os.Stderr, "\n# would install to", scope.plist)
	fmt.Fprintln(os.Stderr, "# then: launchctl bootstrap", scope.bootTgt, scope.plist)
	return nil
}

// UninstallService removes whichever scope is present (tries user first — no
// sudo — then system).
func UninstallService() error {
	removedAny := false
	for _, scope := range []serviceScope{userScope(), systemScope()} {
		if _, statErr := os.Stat(scope.plist); statErr != nil {
			continue
		}
		out, err := exec.Command("launchctl", "bootout", scope.bootTgt+"/"+launchdLabel).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "No such process") {
			return E.Cause(E.New(strings.TrimSpace(string(out))), "launchctl bootout")
		}
		if err := os.Remove(scope.plist); err != nil && !os.IsNotExist(err) {
			return E.Cause(err, "remove plist")
		}
		fmt.Println("lxd: uninstalled", scope.plist)
		removedAny = true
	}
	if !removedAny {
		fmt.Println("lxd: no installed service found")
	}
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
