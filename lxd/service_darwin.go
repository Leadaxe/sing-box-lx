//go:build with_lxd && darwin

package lxd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
)

// defaultExecDir holds the system service's root-owned copy of the binary
// (SPEC 100): Apple's place for privileged helpers, one directory per label,
// the binary keeping its own name inside.
const defaultExecDir = "/Library/PrivilegedHelperTools/" + launchdLabel

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

func (scope serviceScope) name() string {
	if scope.user {
		return "user scope, LaunchAgent"
	}
	return "system scope, LaunchDaemon"
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

// serviceEnv is what the service actions take from the machine — the binary
// being installed, root or not, the scopes, launchctl — so the state machine
// none → copy only → installed and back can be driven by a test without root
// or launchd (SPEC 100 §2.7).
type serviceEnv struct {
	out     io.Writer
	source  string // the running binary, symlinks resolved
	execDir string
	root    bool // the system scope may be touched
	chown   bool // hand what install creates to root:wheel
	system  serviceScope
	user    serviceScope
	// load (re)loads a job from its plist; unload removes it.
	load   func(scope serviceScope) error
	unload func(scope serviceScope) error
}

func newServiceEnv(execDir string) (serviceEnv, error) {
	source, err := resolveOwnExecutable()
	if err != nil {
		return serviceEnv{}, err
	}
	if execDir == "" {
		execDir = defaultExecDir
	}
	return serviceEnv{
		out:     os.Stdout,
		source:  source,
		execDir: execDir,
		root:    os.Getuid() == 0,
		chown:   os.Getuid() == 0,
		system:  systemScope(),
		user:    userScope(),
		load:    launchctlLoad,
		unload:  launchctlUnload,
	}, nil
}

// launchctlSettle bounds how long a (re)load waits for launchd: first for
// the old job to disappear after bootout, then for bootstrap to stop
// answering "already in progress".
const launchctlSettle = 10 * time.Second

// launchctlLoad replaces the job: bootout, wait until launchd has really
// removed it, bootstrap. `launchctl bootout` returns as soon as the request
// is accepted; the job with a live core takes seconds to exit, and a
// bootstrap issued meanwhile fails with "Operation already in progress" (37)
// or "Input/output error" (5) — the service is then not loaded at all.
func launchctlLoad(scope serviceScope) error {
	target := scope.bootTgt + "/" + launchdLabel
	_ = exec.Command("launchctl", "bootout", target).Run()
	gone := func() bool { return exec.Command("launchctl", "print", target).Run() != nil }
	if !waitUntilGone(os.Stdout, gone, launchctlSettle, 250*time.Millisecond) {
		fmt.Fprintln(os.Stdout, "lxd: the old service is still loaded after", launchctlSettle, "— trying bootstrap anyway")
	}
	deadline := time.Now().Add(launchctlSettle)
	for {
		output, err := exec.Command("launchctl", "bootstrap", scope.bootTgt, scope.plist).CombinedOutput()
		if err == nil {
			return nil
		}
		text := strings.TrimSpace(string(output))
		if text == "" {
			text = err.Error()
		}
		if !bootstrapRetryable(text) || time.Now().After(deadline) {
			return E.Cause(E.New(text), "launchctl bootstrap")
		}
		fmt.Fprintln(os.Stdout, "lxd: launchctl bootstrap:", text, "— retrying while the old service unloads")
		time.Sleep(500 * time.Millisecond)
	}
}

// bootstrapRetryable: the bootstrap errors launchd gives while the previous
// job of the same label is still going away. Anything else (a bad plist, a
// missing program, permissions) is final.
func bootstrapRetryable(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "already in progress") ||
		strings.Contains(lower, "input/output error") ||
		strings.Contains(lower, "operation now in progress")
}

// waitUntilGone polls gone() every interval until it is true or timeout
// passes, telling the operator each second that it is waiting. Returns
// whether the job went away.
func waitUntilGone(out io.Writer, gone func() bool, timeout, interval time.Duration) bool {
	started := time.Now()
	lastNotice := 0
	for {
		if gone() {
			return true
		}
		waited := time.Since(started)
		if waited >= timeout {
			return false
		}
		if seconds := int(waited / time.Second); seconds > lastNotice {
			lastNotice = seconds
			fmt.Fprintf(out, "lxd: waiting for the old service to unload (%ds)\n", seconds)
		}
		time.Sleep(interval)
	}
}

func launchctlUnload(scope serviceScope) error {
	output, err := exec.Command("launchctl", "bootout", scope.bootTgt+"/"+launchdLabel).CombinedOutput()
	if err != nil && !strings.Contains(string(output), "No such process") {
		return E.Cause(E.New(strings.TrimSpace(string(output))), "launchctl bootout")
	}
	return nil
}

// InstallService registers the daemon as a system LaunchDaemon (root) that
// executes a root-owned copy of this binary in execDir (empty = the
// canonical /Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd). dryRun
// prints the plan instead, touching nothing — the operator's look before the
// leap.
func InstallService(daemonArgs []string, execDir string, dryRun bool) error {
	env, err := newServiceEnv(execDir)
	if err != nil {
		return err
	}
	if dryRun {
		return printPlan(env.out, env.system, daemonArgs, env.execDir)
	}
	if !env.root {
		return E.New("--service=install needs root (run with sudo); for a per-user agent without sudo use --service=install-user")
	}
	return env.installSystem(daemonArgs)
}

// InstallUserService registers the daemon as a per-user LaunchAgent (no sudo).
// The agent runs the binary in place: it executes as the same user who owns
// the file, so there is no privilege to escalate to.
func InstallUserService(daemonArgs []string, dryRun bool) error {
	if dryRun {
		return printPlan(os.Stdout, userScope(), daemonArgs, "")
	}
	return installUser(os.Stdout, userScope(), daemonArgs)
}

// InstallServiceCopy makes ONLY the root-owned copy and its sidecar — the
// same exec dir, checks and algorithm as install — and writes no plist and
// touches no launchd: the launcher's classic TUN mode runs the copy as root
// by itself (SPEC 100 §2.4). A later install binds the same copy to a plist
// without copying again.
func InstallServiceCopy(execDir string, dryRun bool) error {
	env, err := newServiceEnv(execDir)
	if err != nil {
		return err
	}
	if dryRun {
		return env.planCopy()
	}
	if !env.root {
		return E.New("--service=copy needs root (run with sudo): the copy must belong to root")
	}
	return env.copyOnly()
}

// UninstallService removes whichever scope is present (tries user first — no
// sudo — then system). State (trusted clients, last-good, keys) is KEPT unless
// purge is set, so a reinstall preserves enrollment; a non-interactive hint
// points at --purge rather than prompting (this runs from scripts/services with
// no TTY to answer a Y/N). dryRun reports what would be removed and stops
// short of removing it.
//
// The root-owned copy goes only on a sidecar match (SPEC 100 §2.6); the
// plist's ProgramArguments[0] itself is never deleted blindly. keepCopy
// removes the service but leaves the copy and its sidecar, unbound from the
// plist — the copy-only state.
func UninstallService(purge bool, keepCopy bool, execDir string, dryRun bool) error {
	env, err := newServiceEnv(execDir)
	if err != nil {
		return err
	}
	return env.uninstall(purge, keepCopy, dryRun)
}

// ServiceStatus reports the installed service (SPEC 100 §2.6) and changes
// nothing; it needs no root. The verdict's ExitCode is what
// `--service=status` exits with.
func ServiceStatus(execDir string) (ServiceVerdict, error) {
	env, err := newServiceEnv(execDir)
	if err != nil {
		return ServiceNotInstalled, err
	}
	callerSHA, err := sha256File(env.source, maxExecutableSize)
	if err != nil {
		return ServiceNotInstalled, E.Cause(err, "hash the calling binary")
	}
	return env.status(callerSHA)
}

// printPlan renders what an install would do. It mutates nothing, so it is
// safe without root; an exec dir that fails the root-owned check is refused
// here exactly as install would refuse it.
func printPlan(out io.Writer, scope serviceScope, daemonArgs []string, execDir string) error {
	program, err := os.Executable()
	if err != nil {
		return E.Cause(err, "locate own binary")
	}
	fmt.Fprintln(out, "lxd: dry run — nothing was installed.")
	fmt.Fprintln(out, "lxd: would install", scope.name(), launchdLabel)
	result := "(the agent runs the binary in place)"
	if !scope.user {
		copyResult, planErr := planExecCopy(out, execDir)
		if planErr != nil {
			return planErr
		}
		program = copyResult.Target
		result = "(root-owned copy, would be copied)"
		if copyResult.Skipped {
			result = "(root-owned copy, already up to date)"
		}
	}
	supportDir := filepath.Dir(scope.logPath)
	owner := ""
	if !scope.user {
		owner = ", root:wheel"
	}
	fmt.Fprintln(out, "lxd: would write  ", scope.plist)
	fmt.Fprintf(out, "lxd: would create  %s (0700%s) with state/ and lxd.log inside\n", supportDir, owner)
	fmt.Fprintln(out, "lxd: would prepare", filepath.Join(DefaultServiceStateDir(scope.user), daemonConfigFile),
		"(free loopback port from 19091, mTLS, generated secret; an existing file keeps its address and secret)")
	fmt.Fprintln(out, "lxd: would run    launchctl bootstrap", scope.bootTgt, scope.plist)
	fmt.Fprintln(out, "lxd: dry run result: plist ProgramArguments[0] =", program, result)
	fmt.Fprintln(out)
	fmt.Fprint(out, buildPlist(launchdLabel, append([]string{program}, daemonArgs...), scope.logPath))
	return nil
}

// planExecCopy is the dry-run half shared by install and copy: the exec dir
// walk and the copy decision, printed; a refusal is printed and returned.
func planExecCopy(out io.Writer, execDir string) (execCopyResult, error) {
	source, err := resolveOwnExecutable()
	if err != nil {
		return execCopyResult{}, err
	}
	if err = ensureRootOwnedDir(out, execDir, dirPlan, false); err != nil {
		fmt.Fprintln(out, "lxd: dry run result: would refuse:", err)
		return execCopyResult{}, err
	}
	result, err := installExecCopy(out, source, execDir, execCopyOptions{dryRun: true})
	if err != nil {
		fmt.Fprintln(out, "lxd: dry run result: would refuse:", err)
		return result, err
	}
	fmt.Fprintln(out, "lxd: would write sidecar", installMarkerPath(execDir), "(root:wheel 0644)")
	return result, nil
}

func (env serviceEnv) planCopy() error {
	fmt.Fprintln(env.out, "lxd: dry run — nothing was copied.")
	result, err := planExecCopy(env.out, env.execDir)
	if err != nil {
		return err
	}
	state := "would be copied"
	if result.Skipped {
		state = "already up to date"
	}
	fmt.Fprintln(env.out, "lxd: dry run result: root-owned copy", result.Target, "("+state+"); no plist written, launchd untouched")
	return nil
}

// placeCopy lays down the root-owned copy (SPEC 100 §2.3): the exec dir is
// checked (and created) from "/", the binary goes in by a verified rename,
// then the sidecar by the same temp+rename. plistPath binds the copy to a
// service ("" for a copy only; a copy refreshed under an installed service
// keeps its binding). An up-to-date copy with a sidecar already saying the
// same is a no-op.
func (env serviceEnv) placeCopy(plistPath string) (execCopyResult, error) {
	if err := ensureRootOwnedDir(env.out, env.execDir, dirCreate, env.chown); err != nil {
		return execCopyResult{}, err
	}
	result, err := installExecCopy(env.out, env.source, env.execDir, execCopyOptions{chown: env.chown})
	if err != nil {
		return result, err
	}
	previous, found, _ := readInstallMarker(env.execDir)
	marker := installMarker{
		Source:      env.source,
		SHA256:      result.SHA256,
		Version:     C.Version,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
		PlistPath:   plistPath,
		Label:       launchdLabel,
	}
	if plistPath == "" && found && previous.PlistPath != "" {
		if _, statErr := os.Stat(previous.PlistPath); statErr == nil {
			marker.PlistPath = previous.PlistPath
		}
	}
	if found && result.Skipped && previous.SHA256 == result.SHA256 && previous.Label == launchdLabel && previous.PlistPath == marker.PlistPath {
		fmt.Fprintln(env.out, "lxd: already up to date", result.SHA256)
		return result, nil
	}
	// Reinstalling from the copy itself: keep naming where it came from.
	if found && previous.Source != "" && sameFile(env.source, result.Target) {
		marker.Source = previous.Source
	}
	if err = writeInstallMarker(env.execDir, marker, env.chown); err != nil {
		return result, err
	}
	fmt.Fprintln(env.out, "lxd: wrote sidecar", installMarkerPath(env.execDir), "(root:wheel 0644)")
	return result, nil
}

func sameFile(first, second string) bool {
	firstInfo, err := os.Stat(first)
	if err != nil {
		return false
	}
	secondInfo, err := os.Stat(second)
	return err == nil && os.SameFile(firstInfo, secondInfo)
}

func (env serviceEnv) copyOnly() error {
	result, err := env.placeCopy("")
	if err != nil {
		return err
	}
	fmt.Fprintln(env.out, "lxd: root-owned copy ready:", result.Target, "(no plist written, launchd untouched)")
	// The system side of the status report, for the record; the command's
	// own outcome is the copy, which succeeded.
	verdict, reason, err := env.reportSystemSide(result.SHA256)
	if err != nil {
		return err
	}
	fmt.Fprintln(env.out, "lxd: verdict:", verdictLine(verdict, reason))
	return nil
}

func (env serviceEnv) installSystem(daemonArgs []string) error {
	result, err := env.placeCopy(env.system.plist)
	if err != nil {
		return err
	}
	if err = writeServicePlist(env.out, env.system, append([]string{result.Target}, daemonArgs...), env.chown); err != nil {
		return err
	}
	if err = env.load(env.system); err != nil {
		return err
	}
	fmt.Fprintln(env.out, "lxd: installed", env.system.name(), launchdLabel)
	fmt.Fprintln(env.out, "lxd: plist", env.system.plist)
	fmt.Fprintln(env.out, "lxd: logs ", env.system.logPath)
	// The same report `--service=status` prints, for the scope just written.
	verdict, reason, _, err := reportScope(env.out, env.system, result.SHA256)
	if err != nil {
		return err
	}
	fmt.Fprintln(env.out, "lxd: verdict:", verdictLine(verdict, reason))
	if verdict != ServiceOK {
		return E.New("service installed, but its status is ", verdict, ": ", reason)
	}
	return nil
}

func installUser(out io.Writer, scope serviceScope, daemonArgs []string) error {
	program, err := os.Executable()
	if err != nil {
		return E.Cause(err, "locate own binary")
	}
	if err = writeServicePlist(out, scope, append([]string{program}, daemonArgs...), false); err != nil {
		return err
	}
	if err = launchctlLoad(scope); err != nil {
		return err
	}
	fmt.Fprintln(out, "lxd: installed", scope.name(), launchdLabel)
	fmt.Fprintln(out, "lxd: plist", scope.plist)
	fmt.Fprintln(out, "lxd: logs ", scope.logPath)
	callerSHA, err := sha256File(program, maxExecutableSize)
	if err != nil {
		return E.Cause(err, "hash the calling binary")
	}
	verdict, reason, _, err := reportScope(out, scope, callerSHA)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "lxd: verdict:", verdictLine(verdict, reason))
	return nil
}

// writeServicePlist creates the support directory and writes the plist.
// The support directory is created first so launchd's StandardOutPath is
// writable from the first start. 0700: it holds the state dir (server key,
// trusted clients, last-good with credentials) and the log — none of it is
// for other users.
func writeServicePlist(out io.Writer, scope serviceScope, programArgs []string, chownSupport bool) error {
	supportDir := filepath.Dir(scope.logPath)
	if err := os.MkdirAll(supportDir, 0o700); err != nil {
		return E.Cause(err, "create support directory")
	}
	if chownSupport {
		if err := chownSupportDir(out, supportDir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(scope.plist), 0o755); err != nil {
		return E.Cause(err, "create launchd directory")
	}
	if err := os.WriteFile(scope.plist, []byte(buildPlist(launchdLabel, programArgs, scope.logPath)), 0o644); err != nil {
		return E.Cause(err, "write plist")
	}
	fmt.Fprintln(out, "lxd: wrote plist", scope.plist, "with ProgramArguments[0] =", programArgs[0])
	return nil
}

// chownSupportDir makes the support directory's owner explicit. BSD semantics
// hand a new directory its parent's group, and /Library/Application Support is
// root:admin; the directory holds the server key and the client registry.
func chownSupportDir(out io.Writer, supportDir string) error {
	for _, dir := range []string{supportDir, filepath.Join(supportDir, "state")} {
		if _, err := os.Lstat(dir); err != nil {
			continue
		}
		if err := os.Chown(dir, 0, 0); err != nil {
			return E.Cause(err, "chown root:wheel ", dir)
		}
		fmt.Fprintln(out, "lxd: support directory", dir, "owned by root:wheel")
	}
	return nil
}

func (env serviceEnv) uninstall(purge bool, keepCopy bool, dryRun bool) error {
	out := env.out
	removedAny := false
	var copyDirs []string
	for _, scope := range []serviceScope{env.user, env.system} {
		if _, statErr := os.Stat(scope.plist); statErr != nil {
			continue
		}
		if !scope.user {
			// Read before the plist is gone: its program's directory is the
			// first place the copy may live (a custom --exec-dir install).
			if program, _, err := readPlistProgram(scope.plist); err != nil {
				fmt.Fprintln(out, "lxd: cannot read", scope.plist+":", err, "— its program is left alone")
			} else if filepath.Base(program) == execCopyName {
				copyDirs = append(copyDirs, filepath.Dir(program))
			}
		}
		if dryRun {
			fmt.Fprintln(out, "lxd: dry run — nothing was removed.")
			fmt.Fprintln(out, "lxd: would boot out", scope.bootTgt+"/"+launchdLabel, "and delete", scope.plist)
			supportDir := filepath.Dir(scope.logPath)
			if purge {
				fmt.Fprintln(out, "lxd: would delete state at", supportDir, "(--purge)")
			} else if _, statErr := os.Stat(supportDir); statErr == nil {
				fmt.Fprintln(out, "lxd: would keep state at", supportDir, "(clients, last-good, keys)")
			}
			removedAny = true
			continue
		}
		if scope.needRoot && !env.root {
			return E.New("a system LaunchDaemon is installed at ", scope.plist, " — uninstalling it needs root (rerun with sudo)")
		}
		if err := env.unload(scope); err != nil {
			return err
		}
		if err := os.Remove(scope.plist); err != nil && !os.IsNotExist(err) {
			return E.Cause(err, "remove plist")
		}
		fmt.Fprintln(out, "lxd: uninstalled", scope.plist)
		removedAny = true

		supportDir := filepath.Dir(scope.logPath)
		if purge {
			if err := os.RemoveAll(supportDir); err != nil {
				return E.Cause(err, "purge state directory")
			}
			fmt.Fprintln(out, "lxd: purged state at", supportDir)
		} else if _, statErr := os.Stat(supportDir); statErr == nil {
			fmt.Fprintln(out, "lxd: kept state at", supportDir, "(clients, last-good, keys)")
			fmt.Fprintln(out, "lxd:   to remove it too, add --purge")
		}
	}

	// The copy: the plist program's directory and the exec dir, each only on
	// a sidecar match. A copy only (no plist) goes by the same rule; with
	// keepCopy it stays and its sidecar is unbound from the plist instead.
	for _, dir := range appendUniqueDir(copyDirs, env.execDir) {
		if keepCopy {
			if err := unbindInstalledCopy(out, dir, launchdLabel, env.system.plist, dryRun, env.root, env.chown); err != nil {
				return err
			}
			continue
		}
		if !dryRun && !env.root {
			// A user-scope uninstall without sudo: say what stays, touch nothing.
			if _, found, _ := readInstallMarker(dir); found {
				fmt.Fprintln(out, "lxd: a root-owned copy remains in", dir, "— removing it needs root (rerun with sudo)")
			}
			continue
		}
		if err := removeInstalledCopy(out, dir, launchdLabel, env.system.plist, dryRun); err != nil {
			return err
		}
	}
	if !removedAny {
		fmt.Fprintln(out, "lxd: no installed service found")
	}
	return nil
}

func appendUniqueDir(dirs []string, dir string) []string {
	for _, existing := range dirs {
		if filepath.Clean(existing) == filepath.Clean(dir) {
			return dirs
		}
	}
	return append(dirs, dir)
}

// status prints the full report and returns the overall verdict: the most
// severe of the present blocks — the system side (the LaunchDaemon, or a
// copy only when there is no LaunchDaemon) and the user agent.
func (env serviceEnv) status(callerSHA string) (ServiceVerdict, error) {
	fmt.Fprintln(env.out, "lxd: status of", launchdLabel)
	reportLine(env.out, "", "caller", env.source)
	reportLine(env.out, "", "caller sha256", callerSHA)
	overall, overallReason := ServiceNotInstalled, "no plist in either scope and no copy in "+env.execDir
	consider := func(verdict ServiceVerdict, reason string) {
		if overall == ServiceNotInstalled || verdict.severity() > overall.severity() {
			overall, overallReason = verdict, reason
		}
	}
	systemVerdict, systemReason, systemPresent, err := env.reportSystemSideStatus(callerSHA)
	if err != nil {
		return ServiceNotInstalled, err
	}
	if systemPresent {
		consider(systemVerdict, systemReason)
	}
	userVerdict, userReason, userPresent, err := reportScope(env.out, env.user, callerSHA)
	if err != nil {
		return ServiceNotInstalled, err
	}
	if userPresent {
		consider(userVerdict, userReason)
	}
	fmt.Fprintln(env.out, "lxd: verdict:", verdictLine(overall, overallReason))
	return overall, nil
}

func (env serviceEnv) reportSystemSideStatus(callerSHA string) (ServiceVerdict, string, bool, error) {
	verdict, reason, present, err := reportScope(env.out, env.system, callerSHA)
	if err != nil || present {
		return verdict, reason, present, err
	}
	return env.reportCopyOnly(callerSHA)
}

// reportSystemSide is the status of whatever runs the copy: the LaunchDaemon
// when its plist exists, else the copy on its own.
func (env serviceEnv) reportSystemSide(callerSHA string) (ServiceVerdict, string, error) {
	verdict, reason, _, err := env.reportSystemSideStatus(callerSHA)
	return verdict, reason, err
}

func verdictLine(verdict ServiceVerdict, reason string) string {
	if verdict == ServiceOK || verdict == ServiceCopyOnly || reason == "" {
		return verdict.String()
	}
	return verdict.String() + " — " + reason
}

// reportLine prints one aligned "key: value" line of the status report.
func reportLine(out io.Writer, indent, key string, values ...any) {
	fmt.Fprintf(out, "lxd: %s%-*s %s", indent, 18-len(indent), key+":", fmt.Sprintln(values...))
}

// programFacts is what the report learned about one program file.
type programFacts struct {
	exists      bool
	sha         string // "" when absent or unreadable
	chainErr    error  // the root-owned invariant; nil = passes
	marker      installMarker
	markerFound bool
	markerErr   error
}

// reportProgram prints the program file, its ownership, hash and sidecar.
func reportProgram(out io.Writer, program, callerSHA string, rootRequired bool) programFacts {
	var facts programFacts
	info, statErr := ownerLstat(program)
	facts.exists = statErr == nil
	if statErr != nil {
		reportLine(out, "  ", "program file", statErr)
	} else {
		reportLine(out, "  ", "program file", fmt.Sprintf("exists, %s, uid %d gid %d, mode %s", describeType(info.mode), info.uid, info.gid, formatMode(info.mode)))
	}
	facts.chainErr = checkRootOwnedChain(program, ownerLstat)
	if facts.chainErr == nil && !info.mode.IsRegular() {
		facts.chainErr = E.New(program, ": not a regular file")
	}
	switch {
	case !rootRequired && facts.chainErr == nil:
		reportLine(out, "  ", "root-owned", "yes (not required for a per-user agent)")
	case !rootRequired:
		reportLine(out, "  ", "root-owned", "no (not required for a per-user agent)")
	case facts.chainErr == nil:
		reportLine(out, "  ", "root-owned", "yes — every component from / is root-owned, not group/world-writable")
	case facts.exists:
		reportLine(out, "  ", "root-owned", "NO —", facts.chainErr)
	}
	// Hashed through the path as launchd resolves it: a per-user agent may
	// legitimately point at a symlink.
	if facts.exists {
		sum, err := sha256File(program, maxExecutableSize)
		switch {
		case err != nil:
			reportLine(out, "  ", "program sha256", "unreadable:", err)
		case sum == callerSHA:
			facts.sha = sum
			reportLine(out, "  ", "program sha256", sum, "(same as the caller)")
		default:
			facts.sha = sum
			reportLine(out, "  ", "program sha256", sum, "(differs from the caller)")
		}
	}
	dir := filepath.Dir(program)
	facts.marker, facts.markerFound, facts.markerErr = readInstallMarker(dir)
	switch {
	case facts.markerErr != nil:
		reportLine(out, "  ", "sidecar", facts.markerErr)
	case !facts.markerFound:
		reportLine(out, "  ", "sidecar", "none at", installMarkerPath(dir))
	default:
		reportLine(out, "  ", "sidecar", installMarkerPath(dir))
		reportLine(out, "    ", "source", facts.marker.Source)
		reportLine(out, "    ", "sha256", facts.marker.SHA256)
		reportLine(out, "    ", "version", facts.marker.Version)
		reportLine(out, "    ", "installed_at", facts.marker.InstalledAt)
		reportLine(out, "    ", "plist_path", facts.marker.PlistPath)
		reportLine(out, "    ", "label", facts.marker.Label)
	}
	return facts
}

// reportScope prints one scope's block of the status report and judges it
// (SPEC 100 §2.6). present=false means the scope has no plist.
func reportScope(out io.Writer, scope serviceScope, callerSHA string) (verdict ServiceVerdict, reason string, present bool, err error) {
	fmt.Fprintf(out, "lxd: [%s]\n", scope.name())
	if _, statErr := os.Stat(scope.plist); statErr != nil {
		if os.IsNotExist(statErr) {
			reportLine(out, "  ", "plist", scope.plist, "(absent)")
			return ServiceOK, "", false, nil
		}
		return ServiceNotInstalled, "", true, statErr
	}
	reportLine(out, "  ", "plist", scope.plist, "(present)")
	program, _, err := readPlistProgram(scope.plist)
	if err != nil {
		return ServiceNotInstalled, "", true, E.Cause(err, "read ", scope.plist)
	}
	reportLine(out, "  ", "program", program)
	facts := reportProgram(out, program, callerSHA, !scope.user)
	state, running := probeLaunchd(scope)
	reportLine(out, "  ", "launchd", state)
	if scope.user {
		verdict, reason = judgeUserAgent(program, facts, callerSHA)
	} else {
		verdict, reason = judgeDaemon(scope, program, facts, callerSHA)
	}
	// Consistent on disk is not the same as up: a bootstrap that failed (or
	// a bootout without one) leaves a correct plist and no daemon.
	if verdict == ServiceOK && !running {
		verdict, reason = ServiceNotRunning, notRunningReason(scope, state)
	}
	reportLine(out, "  ", "verdict", verdictLine(verdict, reason))
	return verdict, reason, true, nil
}

// reportCopyOnly reports the exec dir when no LaunchDaemon exists: a copy
// made by `--service=copy`, or remains of one. present=false when there is
// neither a copy nor a sidecar.
func (env serviceEnv) reportCopyOnly(callerSHA string) (ServiceVerdict, string, bool, error) {
	target := filepath.Join(env.execDir, execCopyName)
	_, found, markerErr := readInstallMarker(env.execDir)
	if _, statErr := os.Lstat(target); os.IsNotExist(statErr) && !found && markerErr == nil {
		reportLine(env.out, "", "copy", target, "(absent)")
		return ServiceNotInstalled, "", false, nil
	}
	fmt.Fprintln(env.out, "lxd: [copy only, no service]")
	reportLine(env.out, "  ", "copy", target)
	facts := reportProgram(env.out, target, callerSHA, true)
	verdict, reason := judgeCopyOnly(target, facts, callerSHA)
	reportLine(env.out, "  ", "verdict", verdictLine(verdict, reason))
	return verdict, reason, true, nil
}

const (
	reinstallHint = "reinstall: sudo sing-box lxd --service=install"
	recopyHint    = "refresh: sudo sing-box lxd --service=copy (or --service=uninstall to remove it)"
)

// judgeDaemon: the LaunchDaemon is OK only when it runs OUR copy — root-owned
// all the way from /, with a sidecar bound to this plist whose sha256 the file
// still has — and that copy is the caller's binary.
func judgeDaemon(scope serviceScope, program string, facts programFacts, callerSHA string) (ServiceVerdict, string) {
	switch {
	case !facts.exists:
		return ServiceMismatch, "the plist runs " + program + ", which does not exist; " + reinstallHint
	case facts.chainErr != nil:
		return ServiceUnsafe, "the LaunchDaemon executes a binary that is not a root-owned copy (" + facts.chainErr.Error() + "); " + reinstallHint
	case facts.markerErr != nil:
		return ServiceMismatch, facts.markerErr.Error() + "; " + reinstallHint
	case !facts.markerFound:
		return ServiceMismatch, "the plist does not run an installed copy: no sidecar next to " + program + "; " + reinstallHint
	case facts.marker.Label != launchdLabel || facts.marker.PlistPath != scope.plist:
		return ServiceMismatch, "the sidecar next to " + program + " is bound to " + facts.marker.Label + " (" + facts.marker.PlistPath + "), not to " + scope.plist + "; " + reinstallHint
	case facts.sha == "":
		return ServiceMismatch, "the installed program " + program + " cannot be read; " + reinstallHint
	case facts.sha != facts.marker.SHA256:
		return ServiceMismatch, "the copy (sha256 " + facts.sha + ") differs from its sidecar (sha256 " + facts.marker.SHA256 + "); " + reinstallHint
	case facts.sha != callerSHA:
		return ServiceMismatch, "the installed binary (sha256 " + facts.sha + ", version " + facts.marker.Version + ") differs from this one (sha256 " + callerSHA + "); " + reinstallHint
	default:
		return ServiceOK, ""
	}
}

// judgeUserAgent: a LaunchAgent runs the user's own binary in place; only
// identity with the caller matters.
func judgeUserAgent(program string, facts programFacts, callerSHA string) (ServiceVerdict, string) {
	const hint = "reinstall: sing-box lxd --service=install-user"
	switch {
	case !facts.exists:
		return ServiceMismatch, "the plist runs " + program + ", which does not exist; " + hint
	case facts.sha == "":
		return ServiceMismatch, "the program " + program + " cannot be read; " + hint
	case facts.sha != callerSHA:
		return ServiceMismatch, "the agent's binary (sha256 " + facts.sha + ") differs from this one (sha256 " + callerSHA + "); " + hint
	default:
		return ServiceOK, ""
	}
}

// judgeCopyOnly: a copy with no LaunchDaemon is COPY ONLY when it is
// root-owned, carries an unbound sidecar of this label whose sha256 it still
// has, and is the caller's binary. A sidecar bound to a plist that is gone is
// a half-removed service.
func judgeCopyOnly(target string, facts programFacts, callerSHA string) (ServiceVerdict, string) {
	switch {
	case !facts.exists:
		return ServiceMismatch, "the copy " + target + " is missing but its sidecar remains; " + recopyHint
	case facts.chainErr != nil:
		return ServiceUnsafe, "the copy is not root-owned (" + facts.chainErr.Error() + "); " + recopyHint
	case facts.markerErr != nil:
		return ServiceMismatch, facts.markerErr.Error() + "; " + recopyHint
	case !facts.markerFound:
		return ServiceMismatch, "no sidecar next to the copy " + target + "; " + recopyHint
	case facts.marker.Label != launchdLabel:
		return ServiceMismatch, "the sidecar next to " + target + " belongs to " + facts.marker.Label + "; " + recopyHint
	case facts.marker.PlistPath != "":
		return ServiceMismatch, "the sidecar is bound to " + facts.marker.PlistPath + ", which is absent; " + reinstallHint + ", or " + recopyHint
	case facts.sha == "":
		return ServiceMismatch, "the copy " + target + " cannot be read; " + recopyHint
	case facts.sha != facts.marker.SHA256:
		return ServiceMismatch, "the copy (sha256 " + facts.sha + ") differs from its sidecar (sha256 " + facts.marker.SHA256 + "); " + recopyHint
	case facts.sha != callerSHA:
		return ServiceMismatch, "the copy (sha256 " + facts.sha + ", version " + facts.marker.Version + ") differs from this binary (sha256 " + callerSHA + "); " + recopyHint
	default:
		return ServiceCopyOnly, ""
	}
}

func describeType(mode os.FileMode) string {
	switch {
	case mode.IsRegular():
		return "regular file"
	case mode.IsDir():
		return "directory"
	case mode&os.ModeSymlink != 0:
		return "symbolic link"
	default:
		return "special file"
	}
}

// probeLaunchd is the launchd query behind the status report; tests point
// it at a table.
var probeLaunchd = launchdState

// notRunningReason tells the operator how to bring a consistent, unloaded
// service up. A reload through install is the same bootout/bootstrap pair.
func notRunningReason(scope serviceScope, state string) string {
	sudo := ""
	if scope.needRoot {
		sudo = "sudo "
	}
	return "the service is installed and consistent but not running in launchd (" + state + "); load it: " +
		sudo + "launchctl bootstrap " + scope.bootTgt + " " + scope.plist + " (or " + sudo + "sing-box lxd --service=install to reload)"
}

// launchdState summarizes `launchctl print <domain>/<label>` for the report
// and says whether the job is running. The text format is Apple's and has no
// contract; only "state = running" is read for the verdict, the rest is for
// the operator.
func launchdState(scope serviceScope) (summary string, running bool) {
	output, err := exec.Command("launchctl", "print", scope.bootTgt+"/"+launchdLabel).CombinedOutput()
	if err != nil {
		firstLine, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
		if firstLine == "" {
			firstLine = err.Error()
		}
		return "not loaded (" + firstLine + ")", false
	}
	state, pid, program := parseLaunchctlPrint(string(output))
	summary = state
	if summary == "" {
		summary = "loaded, state unknown"
	}
	if pid != "" {
		summary += ", pid " + pid
	}
	if program != "" {
		summary += ", program " + program
	}
	return summary, state == "running"
}

// parseLaunchctlPrint reads the job's own top-level fields (one tab deep);
// nested blocks — endpoints, environment — are deeper and ignored.
func parseLaunchctlPrint(output string) (state, pid, program string) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "\t\t") {
			continue
		}
		key, value, found := strings.Cut(strings.TrimSpace(line), " = ")
		if !found {
			continue
		}
		switch key {
		case "state":
			if state == "" {
				state = value
			}
		case "pid":
			if pid == "" {
				pid = value
			}
		case "program":
			if program == "" {
				program = value
			}
		}
	}
	return state, pid, program
}

// readPlistProgram returns the program a plist makes launchd execute —
// the Program key when set, else ProgramArguments[0] — and the arguments.
// Binary plists are converted by plutil first.
func readPlistProgram(path string) (string, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	if bytes.HasPrefix(data, []byte("bplist")) {
		if data, err = exec.Command("plutil", "-convert", "xml1", "-o", "-", path).Output(); err != nil {
			return "", nil, E.Cause(err, "plutil -convert xml1")
		}
	}
	return parsePlistProgram(data)
}

func parsePlistProgram(data []byte) (string, []string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var (
		depth     int
		lastKey   string
		program   string
		arguments []string
	)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, E.Cause(err, "parse plist")
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch {
			case element.Name.Local == "dict":
				depth++
			case element.Name.Local == "key":
				var key string
				if err = decoder.DecodeElement(&key, &element); err != nil {
					return "", nil, E.Cause(err, "parse plist")
				}
				if depth == 1 {
					lastKey = key
				}
			case depth == 1 && lastKey == "Program" && element.Name.Local == "string":
				if err = decoder.DecodeElement(&program, &element); err != nil {
					return "", nil, E.Cause(err, "parse plist")
				}
				lastKey = ""
			case depth == 1 && lastKey == "ProgramArguments" && element.Name.Local == "array":
				if arguments, err = decodeStringArray(decoder); err != nil {
					return "", nil, err
				}
				lastKey = ""
			}
		case xml.EndElement:
			if element.Name.Local == "dict" {
				depth--
			}
		}
	}
	if program == "" && len(arguments) > 0 {
		program = arguments[0]
	}
	if program == "" {
		return "", nil, E.New("plist has neither Program nor ProgramArguments")
	}
	return program, arguments, nil
}

func decodeStringArray(decoder *xml.Decoder) ([]string, error) {
	var values []string
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, E.Cause(err, "parse plist array")
		}
		switch element := token.(type) {
		case xml.StartElement:
			if element.Name.Local != "string" {
				if err = decoder.Skip(); err != nil {
					return nil, E.Cause(err, "parse plist array")
				}
				continue
			}
			var value string
			if err = decoder.DecodeElement(&value, &element); err != nil {
				return nil, E.Cause(err, "parse plist array")
			}
			values = append(values, value)
		case xml.EndElement:
			if element.Name.Local == "array" {
				return values, nil
			}
		}
	}
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
