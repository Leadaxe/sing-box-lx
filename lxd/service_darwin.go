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

// ServiceInstallIsAdvisory is false here: on darwin --service really installs
// and starts the job, so the caller materializes daemon.json first and mints a
// pairing invite afterwards. On linux the installer only prints a recipe, so
// the caller must skip both (see service_linux.go).
const ServiceInstallIsAdvisory = false

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

// DefaultServiceStateDir returns the absolute state directory a service should
// use — beside the log, never cwd-relative (a launchd unit runs with cwd "/").
func DefaultServiceStateDir(user bool) string {
	scope := systemScope()
	if user {
		scope = userScope()
	}
	return filepath.Join(filepath.Dir(scope.logPath), "state")
}

// InstallService registers the daemon as a system LaunchDaemon (root).
// dryRun prints the plist and the launchctl commands instead, touching
// nothing — the operator's look before the leap.
func InstallService(daemonArgs []string, dryRun bool) error {
	if dryRun {
		return printPlan(systemScope(), daemonArgs)
	}
	return installScope(systemScope(), daemonArgs)
}

// InstallUserService registers the daemon as a per-user LaunchAgent (no sudo).
func InstallUserService(daemonArgs []string, dryRun bool) error {
	if dryRun {
		return printPlan(userScope(), daemonArgs)
	}
	return installScope(userScope(), daemonArgs)
}

// printPlan renders what installScope would write and run. It reads no state
// and mutates nothing, so it is safe without root.
func printPlan(scope serviceScope, daemonArgs []string) error {
	executable, err := os.Executable()
	if err != nil {
		return E.Cause(err, "locate own binary")
	}
	kind := "LaunchDaemon (system)"
	if scope.user {
		kind = "LaunchAgent (user)"
	}
	fmt.Println("lxd: dry run — nothing was installed.")
	fmt.Println("lxd: would install", kind, launchdLabel)
	fmt.Println("lxd: would write  ", scope.plist)
	fmt.Println("lxd: would create ", filepath.Dir(scope.logPath), "(0700) with state/ and lxd.log inside")
	fmt.Println("lxd: would prepare", filepath.Join(DefaultServiceStateDir(scope.user), daemonConfigFile),
		"(free loopback port from 19091, mTLS, generated secret; an existing file keeps its address and secret)")
	fmt.Println("lxd: would run    launchctl bootstrap", scope.bootTgt, scope.plist)
	fmt.Println()
	fmt.Print(buildPlist(launchdLabel, append([]string{executable}, daemonArgs...), scope.logPath))
	return nil
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
	// 0700: the directory holds the state dir (server key, trusted clients,
	// last-good with credentials) and the log — none of it is for other users.
	if err = os.MkdirAll(filepath.Dir(scope.logPath), 0o700); err != nil {
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

// UninstallService removes whichever scope is present (tries user first — no
// sudo — then system). State (trusted clients, last-good, keys) is KEPT unless
// purge is set, so a reinstall preserves enrollment; a non-interactive hint
// points at --purge rather than prompting (this runs from scripts/services with
// no TTY to answer a Y/N). dryRun reports what would be removed and stops
// short of removing it.
func UninstallService(purge bool, dryRun bool) error {
	removedAny := false
	for _, scope := range []serviceScope{userScope(), systemScope()} {
		if _, statErr := os.Stat(scope.plist); statErr != nil {
			continue
		}
		if dryRun {
			fmt.Println("lxd: dry run — nothing was removed.")
			fmt.Println("lxd: would boot out", scope.bootTgt+"/"+launchdLabel, "and delete", scope.plist)
			supportDir := filepath.Dir(scope.logPath)
			if purge {
				fmt.Println("lxd: would delete state at", supportDir, "(--purge)")
			} else if _, statErr := os.Stat(supportDir); statErr == nil {
				fmt.Println("lxd: would keep state at", supportDir, "(clients, last-good, keys)")
			}
			removedAny = true
			continue
		}
		if scope.needRoot && os.Getuid() != 0 {
			return E.New("a system LaunchDaemon is installed at ", scope.plist, " — uninstalling it needs root (rerun with sudo)")
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

		supportDir := filepath.Dir(scope.logPath)
		if purge {
			if err := os.RemoveAll(supportDir); err != nil {
				return E.Cause(err, "purge state directory")
			}
			fmt.Println("lxd: purged state at", supportDir)
		} else if _, statErr := os.Stat(supportDir); statErr == nil {
			fmt.Println("lxd: kept state at", supportDir, "(clients, last-good, keys)")
			fmt.Println("lxd:   to remove it too, add --purge")
		}
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
