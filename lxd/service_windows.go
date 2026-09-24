//go:build with_lxd && windows

package lxd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// The Windows service (SPEC 103): the SCM executes a protected copy of the
// binary set in <ProgramFiles>\sing-box-lxd, the state and the logs live in
// <ProgramData>\sing-box-lxd — the SPEC 100 model with launchd replaced by
// the SCM, uid 0 and the mode by the owner and the DACL.

// ServiceInstallIsAdvisory is false: --service really installs and starts
// the service, so the caller prepares daemon.json and mints an invite.
const ServiceInstallIsAdvisory = false

const (
	reinstallHint = "reinstall: sing-box lxd --service=install (as administrator)"
	recopyHint    = "refresh: sing-box lxd --service=copy (as administrator), or --service=uninstall to remove it"
)

// knownFolder resolves a known folder, falling back to its environment
// variable and then to the stock path.
func knownFolder(folder *windows.KNOWNFOLDERID, variable, fallback string) string {
	if path, err := windows.KnownFolderPath(folder, 0); err == nil && path != "" {
		return path
	}
	if path := os.Getenv(variable); path != "" {
		return path
	}
	return fallback
}

// defaultCopyDir is <ProgramFiles>\sing-box-lxd: ours, created by install.
func defaultCopyDir() string {
	return filepath.Join(knownFolder(windows.FOLDERID_ProgramFiles, "ProgramFiles", `C:\Program Files`), execCopyBase)
}

// serviceDataRoot is <ProgramData>\sing-box-lxd with state\ and logs\.
func serviceDataRoot() string {
	return filepath.Join(knownFolder(windows.FOLDERID_ProgramData, "ProgramData", `C:\ProgramData`), execCopyBase)
}

// DefaultServiceStateDir is <ProgramData>\sing-box-lxd\state. There is no
// per-user service on Windows; user is ignored.
func DefaultServiceStateDir(user bool) string {
	return filepath.Join(serviceDataRoot(), "state")
}

// ServiceDefaultLogFile is the log_file install writes into daemon.json:
// <ProgramData>\sing-box-lxd\logs\lxd.log, beside the launcher's classic.log
// (DefaultLogPath would put it next to state\, outside logs\).
func ServiceDefaultLogFile() string {
	return filepath.Join(serviceDataRoot(), "logs", "lxd.log")
}

// ServiceRestartCommand: sc.exe stop + start does not wait for STOPPED and
// start fails with 1056 meanwhile; Restart-Service waits.
func ServiceRestartCommand(user bool) string {
	return "Restart-Service " + windowsServiceName + " (PowerShell as administrator)"
}

// CheckUserScope refuses install-user before anything is written.
func CheckUserScope() error {
	return E.New("--service=install-user is not supported on Windows; use --service=install")
}

// ServiceStateDirAccessError explains an unprivileged `client` command on a
// host with an installed service: daemon.json lies under a DACL that admits
// only SYSTEM and Administrators (SPEC 103 §2.15).
func ServiceStateDirAccessError() error {
	path := filepath.Join(DefaultServiceStateDir(false), daemonConfigFile)
	if _, err := os.Stat(path); err != nil && os.IsPermission(err) {
		return E.New("cannot read ", path, ": access denied — run as administrator")
	}
	return nil
}

// windowsFS is the file-security side of the service behind an interface:
// the tests run the state machine on real files in a temporary directory
// with the ACLs faked.
type windowsFS interface {
	// checkAncestors: the volume and every component from its root to the
	// copy dir's parent pass the ancestor rule (§2.2).
	checkAncestors(dir string) error
	// checkProtected: the copy dir or a file of the set passes the copy rule.
	checkProtected(path string, directory bool) error
	ensureCopyDir(out io.Writer, dir string) error
	protectFile(path string) (bool, error)
	prepareDataDir(out io.Writer, root string) ([]installWarning, error)
	owner(path string) string
	dataDirSummary(root string) string
}

type systemFS struct{}

func (systemFS) checkAncestors(dir string) error { return checkProtectedAncestors(dir) }
func (systemFS) checkProtected(path string, directory bool) error {
	return checkProtectedPath(path, directory)
}
func (systemFS) ensureCopyDir(out io.Writer, dir string) error { return ensureCopyDir(out, dir) }
func (systemFS) protectFile(path string) (bool, error)         { return protectCopyNode(nil, path, false) }
func (systemFS) prepareDataDir(out io.Writer, root string) ([]installWarning, error) {
	return prepareDataDir(out, root)
}
func (systemFS) owner(path string) string {
	facts, err := readSecurityFacts(path)
	if err != nil {
		return "unreadable: " + err.Error()
	}
	return principalName(facts.Owner)
}
func (systemFS) dataDirSummary(root string) string { return dataDirSummary(root) }

// windowsServiceEnv is what the service actions take from the machine.
type windowsServiceEnv struct {
	out     io.Writer
	source  string // the running binary, symlinks resolved
	execDir string
	// defaultExecDir: execDir is <ProgramFiles>\sing-box-lxd, which is ours
	// and removed by uninstall once empty; an --exec-dir stays.
	defaultExecDir bool
	dataRoot       string
	privileged     bool
	scm            scmAPI
	fs             windowsFS
}

func newWindowsServiceEnv(execDir string) (*windowsServiceEnv, error) {
	source, err := resolveOwnExecutable()
	if err != nil {
		return nil, err
	}
	defaultDir := execDir == ""
	if defaultDir {
		execDir = defaultCopyDir()
	}
	return &windowsServiceEnv{
		out:            os.Stdout,
		source:         source,
		execDir:        execDir,
		defaultExecDir: defaultDir,
		dataRoot:       serviceDataRoot(),
		privileged:     ServiceActionPrivileged(),
		scm:            systemSCM{},
		fs:             systemFS{},
	}, nil
}

// The data dir is prepared once per process: install does it before
// daemon.json (PrepareServiceHome from the command), and its warnings go
// into the sidecar the same run writes.
var (
	homePrepared bool
	homeWarnings []installWarning
)

// PrepareServiceHome brings the data dir to the norm before daemon.json is
// written into it (SPEC 103 §2.3).
func PrepareServiceHome(out io.Writer) error {
	if !ServiceActionPrivileged() {
		return ServiceInstallPrivilegeError()
	}
	return prepareHome(out, systemFS{}, serviceDataRoot())
}

func prepareHome(out io.Writer, fs windowsFS, root string) error {
	warnings, err := fs.prepareDataDir(out, root)
	if err != nil {
		return err
	}
	homePrepared, homeWarnings = true, warnings
	return nil
}

func InstallService(daemonArgs []string, execDir string, dryRun bool) error {
	env, err := newWindowsServiceEnv(execDir)
	if err != nil {
		return err
	}
	if dryRun {
		return env.planInstall(daemonArgs)
	}
	return env.install(daemonArgs)
}

func InstallUserService(daemonArgs []string, dryRun bool) error {
	return CheckUserScope()
}

func InstallServiceCopy(execDir string, dryRun bool) error {
	env, err := newWindowsServiceEnv(execDir)
	if err != nil {
		return err
	}
	if dryRun {
		return env.planCopy()
	}
	return env.copyOnly()
}

func UninstallService(purge bool, keepCopy bool, execDir string, dryRun bool) error {
	env, err := newWindowsServiceEnv(execDir)
	if err != nil {
		return err
	}
	return env.uninstall(purge, keepCopy, dryRun)
}

// ServiceStatus reports the service, the copy, the sidecar and the data dir
// (SPEC 103 §2.9); it needs no elevation and changes nothing.
func ServiceStatus(execDir string) (ServiceVerdict, error) {
	env, err := newWindowsServiceEnv(execDir)
	if err != nil {
		return ServiceNotInstalled, err
	}
	return env.status()
}

func (env *windowsServiceEnv) canonicalCopy() string {
	return filepath.Join(env.execDir, execCopyName)
}

func (env *windowsServiceEnv) callerSet() ([]setMember, error) {
	members, err := sourceSet(env.source, []string{cronetLibraryName})
	if err != nil {
		return nil, E.Cause(err, "hash the calling binary set")
	}
	return members, nil
}

// sidecarBackup keeps the sidecar's bytes so a failed install can put the
// previous one back.
type sidecarBackup struct {
	path    string
	content []byte
	found   bool
}

func backupSidecar(dir string) sidecarBackup {
	backup := sidecarBackup{path: installMarkerPath(dir)}
	content, err := os.ReadFile(backup.path)
	if err == nil {
		backup.content, backup.found = content, true
	}
	return backup
}

func (backup sidecarBackup) restore(protect func(string) (bool, error)) {
	if !backup.found {
		_ = os.Remove(backup.path)
		return
	}
	temp := backup.path + ".restore"
	if err := os.WriteFile(temp, backup.content, 0o600); err != nil {
		return
	}
	if protect != nil {
		_, _ = protect(temp)
	}
	_ = os.Rename(temp, backup.path)
}

// placeSet lays the binary set into the copy dir (SPEC 103 §2.4): the
// ancestors are checked first (a violation refuses before any change), the
// copy dir is created or normalized, then the files go in by placeBinarySet.
func (env *windowsServiceEnv) placeSet() (*setPlacement, []setMember, installSetMarker, bool, error) {
	if err := env.fs.checkAncestors(env.execDir); err != nil {
		return nil, nil, installSetMarker{}, false, err
	}
	fmt.Fprintln(env.out, "lxd: exec dir", env.execDir, "is below protected ancestors (every component from the volume root: an administrative owner, not replaceable by other accounts)")
	if err := env.fs.ensureCopyDir(env.out, env.execDir); err != nil {
		return nil, nil, installSetMarker{}, false, err
	}
	members, err := env.callerSet()
	if err != nil {
		return nil, nil, installSetMarker{}, false, err
	}
	previous, found, markerErr := readInstallSetMarker(env.execDir)
	if markerErr != nil {
		fmt.Fprintf(env.out, "lxd: previous sidecar is unreadable (%v), it will be rewritten\n", markerErr)
	}
	var previousNames []string
	for _, file := range previous.Files {
		previousNames = append(previousNames, file.Name)
	}
	placement, err := placeBinarySet(env.out, env.execDir, members, setPlaceOptions{previous: previousNames, protect: env.fs.protectFile})
	if err != nil {
		return nil, nil, installSetMarker{}, false, err
	}
	return placement, members, previous, found, nil
}

// writeSetMarker writes the sidecar after the set (SPEC 103 §2.5). service
// binds the set; a copy (service "") keeps a binding to a service that
// exists. Unchanged files, binding and warnings leave it alone.
func (env *windowsServiceEnv) writeSetMarker(placement *setPlacement, members []setMember, service string, previous installSetMarker, found bool) error {
	marker := installSetMarker{
		Source:      env.source,
		Version:     C.Version,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
		Service:     service,
		Files:       setFiles(members),
		Warnings:    homeWarnings,
	}
	if service == "" && found && previous.Service == windowsServiceName {
		if info, err := env.scm.query(windowsServiceName); err == nil && info.exists {
			marker.Service = previous.Service
		}
	}
	if found && placement.unchanged && sameSet(previous.Files, marker.Files) && previous.Service == marker.Service && sameWarnings(previous.Warnings, marker.Warnings) {
		fmt.Fprintln(env.out, "lxd: already up to date", setFileHash(marker.Files, execCopyName))
		return nil
	}
	// Installing from the copy itself: keep naming where it came from.
	if found && previous.Source != "" && samePathFold(env.source, env.canonicalCopy()) {
		marker.Source = previous.Source
	}
	if err := writeInstallSetMarker(env.execDir, marker, env.fs.protectFile); err != nil {
		return err
	}
	fmt.Fprintln(env.out, "lxd: wrote sidecar", installMarkerPath(env.execDir), "(protected DACL)")
	return nil
}

func sameWarnings(first, second []installWarning) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

// install (SPEC 103 §2.6): data dir (already prepared by the command, which
// wrote daemon.json after it) → stop a running service → the set → the
// service configuration → start → status. When the set or the configuration
// fails after the stop, the previous image goes back and the service starts
// on it before the error is returned: a failed update must not leave the host
// without its VPN.
func (env *windowsServiceEnv) install(daemonArgs []string) error {
	if !env.privileged {
		return ServiceInstallPrivilegeError()
	}
	if !homePrepared {
		if err := prepareHome(env.out, env.fs, env.dataRoot); err != nil {
			return err
		}
	}
	info, err := env.scm.query(windowsServiceName)
	if err != nil {
		return err
	}
	stopped := false
	if info.exists && info.state != svc.Stopped {
		if err = env.scm.stop(env.out, windowsServiceName); err != nil {
			return E.Cause(err, "the service did not stop; nothing was changed")
		}
		stopped = true
	}
	backup := backupSidecar(env.execDir)
	var placement *setPlacement
	fail := func(cause error) error {
		if placement != nil {
			if rollbackErr := placement.rollback(); rollbackErr != nil {
				fmt.Fprintln(env.out, "lxd: putting the previous image back failed:", rollbackErr)
			}
			backup.restore(env.fs.protectFile)
		}
		if stopped {
			if startErr := env.scm.start(env.out, windowsServiceName); startErr != nil {
				fmt.Fprintln(env.out, "lxd: install failed, and the previous service image did not start:", startErr)
			} else {
				fmt.Fprintln(env.out, "lxd: install failed, previous service image restarted")
			}
		}
		return cause
	}
	placement, members, previous, found, err := env.placeSet()
	if err != nil {
		return fail(err)
	}
	if err = env.writeSetMarker(placement, members, windowsServiceName, previous, found); err != nil {
		return fail(err)
	}
	binaryPathName := windows.ComposeCommandLine(append([]string{env.canonicalCopy()}, daemonArgs...))
	if err = env.scm.configure(windowsServiceName, binaryPathName); err != nil {
		return fail(err)
	}
	placement.commit()
	fmt.Fprintln(env.out, "lxd: service", windowsServiceName, "configured:", binaryPathName)
	if err = env.scm.start(env.out, windowsServiceName); err != nil {
		return E.Cause(err, "start service ", windowsServiceName)
	}
	fmt.Fprintln(env.out, "lxd: installed windows service", windowsServiceName)
	fmt.Fprintln(env.out, "lxd: logs ", filepath.Join(env.dataRoot, "logs"))
	verdict, reason, err := env.report(setFiles(members))
	if err != nil {
		return err
	}
	fmt.Fprintln(env.out, "lxd: verdict:", verdictLine(verdict, reason))
	if verdict != ServiceOK {
		return E.New("service installed, but its status is ", verdict, ": ", reason)
	}
	return nil
}

// copyOnly (SPEC 103 §2.7): the data dir and the set, the SCM untouched. A
// running service keeps executing the previous image until restarted.
func (env *windowsServiceEnv) copyOnly() error {
	if !env.privileged {
		return E.New("--service=copy needs an elevated token (run as administrator)")
	}
	if err := prepareHome(env.out, env.fs, env.dataRoot); err != nil {
		return err
	}
	placement, members, previous, found, err := env.placeSet()
	if err != nil {
		return err
	}
	if err = env.writeSetMarker(placement, members, "", previous, found); err != nil {
		_ = placement.rollback()
		return err
	}
	placement.commit()
	fmt.Fprintln(env.out, "lxd: protected copy ready:", env.canonicalCopy(), "(no service configured, the SCM untouched)")
	if info, queryErr := env.scm.query(windowsServiceName); queryErr == nil && info.exists && info.state == svc.Running && !placement.unchanged {
		fmt.Fprintln(env.out, "lxd: the service still runs the previous image until restarted")
	}
	verdict, reason, err := env.report(setFiles(members))
	if err != nil {
		return err
	}
	fmt.Fprintln(env.out, "lxd: verdict:", verdictLine(verdict, reason))
	return nil
}

func (env *windowsServiceEnv) planInstall(daemonArgs []string) error {
	fmt.Fprintln(env.out, "lxd: dry run — nothing was installed.")
	fmt.Fprintln(env.out, "lxd: would bring", env.dataRoot, "to owner Administrators with a protected DACL (state\\, logs\\ inside)")
	if err := env.planSet(); err != nil {
		return err
	}
	binaryPathName := windows.ComposeCommandLine(append([]string{env.canonicalCopy()}, daemonArgs...))
	fmt.Fprintln(env.out, "lxd: would stop service", windowsServiceName, "if it runs, then create or update it: LocalSystem, automatic start after Tcpip, restart on failure")
	fmt.Fprintln(env.out, "lxd: would set BinaryPathName =", binaryPathName)
	fmt.Fprintln(env.out, "lxd: would start service", windowsServiceName)
	return nil
}

func (env *windowsServiceEnv) planCopy() error {
	fmt.Fprintln(env.out, "lxd: dry run — nothing was copied.")
	fmt.Fprintln(env.out, "lxd: would bring", env.dataRoot, "to owner Administrators with a protected DACL")
	if err := env.planSet(); err != nil {
		return err
	}
	fmt.Fprintln(env.out, "lxd: dry run result: protected copy", env.canonicalCopy(), "; no service configured, the SCM untouched")
	return nil
}

func (env *windowsServiceEnv) planSet() error {
	if err := env.fs.checkAncestors(env.execDir); err != nil {
		fmt.Fprintln(env.out, "lxd: dry run result: would refuse:", err)
		return err
	}
	members, err := env.callerSet()
	if err != nil {
		return err
	}
	previous, _, _ := readInstallSetMarker(env.execDir)
	var previousNames []string
	for _, file := range previous.Files {
		previousNames = append(previousNames, file.Name)
	}
	if _, err = placeBinarySet(env.out, env.execDir, members, setPlaceOptions{dryRun: true, previous: previousNames}); err != nil {
		fmt.Fprintln(env.out, "lxd: dry run result: would refuse:", err)
		return err
	}
	fmt.Fprintln(env.out, "lxd: would write sidecar", installMarkerPath(env.execDir), "(protected DACL)")
	return nil
}

// uninstall (SPEC 103 §2.8): the service goes; the set goes only on a
// sidecar match, from the directory of the service's argv[0] and from the
// copy dir; the default copy dir is removed once empty.
func (env *windowsServiceEnv) uninstall(purge bool, keepCopy bool, dryRun bool) error {
	out := env.out
	if !dryRun && !env.privileged {
		return E.New("--service=uninstall needs an elevated token (run as administrator)")
	}
	info, err := env.scm.query(windowsServiceName)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintln(out, "lxd: dry run — nothing was removed.")
	}
	var copyDirs []string
	if info.exists {
		if arguments, parseErr := windows.DecomposeCommandLine(info.binaryPath); parseErr == nil && len(arguments) > 0 &&
			sameName(filepath.Base(arguments[0]), execCopyName) {
			copyDirs = append(copyDirs, filepath.Dir(arguments[0]))
		}
		if dryRun {
			fmt.Fprintln(out, "lxd: would stop and delete service", windowsServiceName)
		} else {
			if err = env.scm.remove(out, windowsServiceName); err != nil {
				return err
			}
			fmt.Fprintln(out, "lxd: uninstalled service", windowsServiceName)
		}
	} else {
		fmt.Fprintln(out, "lxd: no installed service found")
	}
	for _, dir := range appendUniqueDirFold(copyDirs, env.execDir) {
		if keepCopy {
			if err = unbindInstalledSet(out, dir, windowsServiceName, dryRun, env.privileged, env.fs.protectFile); err != nil {
				return err
			}
			continue
		}
		if err = removeInstalledSet(out, dir, windowsServiceName, dryRun); err != nil {
			return err
		}
		if !dryRun && env.defaultExecDir && samePathFold(dir, env.execDir) {
			if removeErr := os.Remove(dir); removeErr == nil {
				fmt.Fprintln(out, "lxd: removed", dir)
			}
		}
	}
	switch {
	case purge && dryRun:
		fmt.Fprintln(out, "lxd: would delete state and logs at", env.dataRoot, "(--purge)")
	case purge:
		if err = os.RemoveAll(env.dataRoot); err != nil {
			return E.Cause(err, "purge ", env.dataRoot)
		}
		fmt.Fprintln(out, "lxd: purged state and logs at", env.dataRoot)
	default:
		if _, statErr := os.Stat(env.dataRoot); statErr == nil {
			fmt.Fprintln(out, "lxd: kept state at", env.dataRoot, "(clients, last-good, keys)")
			fmt.Fprintln(out, "lxd:   to remove it too, add --purge")
		}
	}
	return nil
}

func appendUniqueDirFold(dirs []string, dir string) []string {
	for _, existing := range dirs {
		if samePathFold(existing, dir) {
			return dirs
		}
	}
	return append(dirs, dir)
}

func (env *windowsServiceEnv) status() (ServiceVerdict, error) {
	members, err := env.callerSet()
	if err != nil {
		return ServiceNotInstalled, err
	}
	verdict, reason, err := env.report(setFiles(members))
	if err != nil {
		return ServiceNotInstalled, err
	}
	fmt.Fprintln(env.out, "lxd: verdict:", verdictLine(verdict, reason))
	return verdict, nil
}

// copyFacts is what the report learned about the copy dir.
type copyFacts struct {
	set          installedSet
	present      bool  // a sidecar, a copy or a broken sidecar is there
	invariantErr error // the protected-path invariant; nil = passes
}

// report prints the status blocks and judges them (SPEC 103 §2.9).
func (env *windowsServiceEnv) report(caller []installSetFile) (ServiceVerdict, string, error) {
	out := env.out
	fmt.Fprintln(out, "lxd: status of windows service", windowsServiceName)
	reportLine(out, "", "caller", env.source)
	reportLine(out, "", "caller set", describeSet(caller))
	info, err := env.scm.query(windowsServiceName)
	if err != nil {
		return ServiceNotInstalled, "", err
	}
	fmt.Fprintln(out, "lxd: [service]")
	if !info.exists {
		reportLine(out, "  ", "service", windowsServiceName, "(absent)")
	} else {
		reportLine(out, "  ", "service", windowsServiceName, "(present)")
		if info.configErr != nil {
			reportLine(out, "  ", "configuration", "unreadable:", info.configErr)
		} else {
			reportLine(out, "  ", "BinaryPathName", info.binaryPath)
			if arguments, parseErr := windows.DecomposeCommandLine(info.binaryPath); parseErr == nil && len(arguments) > 0 {
				reportLine(out, "  ", "argv[0]", arguments[0])
			}
			reportLine(out, "  ", "start type", describeStartType(info.startType))
			reportLine(out, "  ", "account", info.account)
		}
		if info.statusErr != nil {
			reportLine(out, "  ", "state", "unreadable:", info.statusErr)
		} else {
			reportLine(out, "  ", "state", describeState(info.state), "pid", info.pid)
		}
		switch {
		case info.daclErr != nil:
			reportLine(out, "  ", "service DACL", "unreadable:", info.daclErr)
		case serviceACLViolation(info.dacl, info.nullDACL) != nil:
			reportLine(out, "  ", "service DACL", "NO —", serviceACLViolation(info.dacl, info.nullDACL))
		default:
			reportLine(out, "  ", "service DACL", "protected (no other account may reconfigure or take it over)")
		}
	}
	facts := env.reportCopy(caller)
	fmt.Fprintln(out, "lxd: [data dir]")
	reportLine(out, "  ", "data dir", env.fs.dataDirSummary(env.dataRoot))
	verdict, reason := env.judge(info, facts, caller)
	return verdict, reason, nil
}

func (env *windowsServiceEnv) reportCopy(caller []installSetFile) copyFacts {
	out := env.out
	fmt.Fprintln(out, "lxd: [copy]")
	reportLine(out, "  ", "copy dir", env.execDir)
	facts := copyFacts{set: inspectInstalledSet(env.execDir)}
	set := facts.set
	facts.present = set.markerFound || set.markerErr != nil || set.exeExists()
	if _, err := os.Lstat(env.execDir); os.IsNotExist(err) {
		reportLine(out, "  ", "copy", "(absent)")
		return facts
	}
	facts.invariantErr = env.fs.checkAncestors(env.execDir)
	if facts.invariantErr == nil {
		facts.invariantErr = env.fs.checkProtected(env.execDir, true)
	}
	names := []string{execCopyName}
	if set.markerFound {
		names = nil
		for _, file := range set.marker.Files {
			names = append(names, file.Name)
		}
	}
	for _, name := range names {
		path := filepath.Join(env.execDir, name)
		if _, err := os.Lstat(path); err != nil {
			reportLine(out, "  ", name, "absent")
			continue
		}
		if facts.invariantErr == nil {
			facts.invariantErr = env.fs.checkProtected(path, false)
		}
		sum, err := sha256File(path, maxExecutableSize)
		state := "unreadable: " + fmt.Sprint(err)
		if err == nil {
			state = "sha256 " + sum
			switch expected := setFileHash(caller, name); {
			case expected == sum:
				state += " (same as the caller's)"
			case expected == "":
				state += " (the caller has no such file)"
			default:
				state += " (differs from the caller's)"
			}
		}
		reportLine(out, "  ", name, "owner", env.fs.owner(path)+",", state)
	}
	if facts.invariantErr == nil {
		reportLine(out, "  ", "protected", "yes — administrative owners from the volume root, the copy writable by nobody else")
	} else {
		reportLine(out, "  ", "protected", "NO —", facts.invariantErr)
	}
	for _, name := range append(append([]string(nil), set.entries.unknown...), set.entries.dropped...) {
		reportLine(out, "  ", "extra file", filepath.Join(env.execDir, name))
	}
	for _, name := range set.entries.leftovers {
		reportLine(out, "  ", "leftover", filepath.Join(env.execDir, name), "(removed by the next install)")
	}
	switch {
	case set.markerErr != nil:
		reportLine(out, "  ", "sidecar", set.markerErr)
	case !set.markerFound:
		reportLine(out, "  ", "sidecar", "none at", installMarkerPath(env.execDir))
	default:
		reportLine(out, "  ", "sidecar", installMarkerPath(env.execDir))
		reportLine(out, "    ", "source", set.marker.Source)
		reportLine(out, "    ", "version", set.marker.Version)
		reportLine(out, "    ", "installed_at", set.marker.InstalledAt)
		reportLine(out, "    ", "service", set.marker.Service)
		reportLine(out, "    ", "files", describeSet(set.marker.Files))
		for _, warning := range set.marker.Warnings {
			reportLine(out, "    ", "warning", warning.Code+":", warning.Text)
		}
	}
	return facts
}

// judge (SPEC 103 §2.9). Unlike macOS, a service whose argv[0] is not the
// canonical copy is UNSAFE, not MISMATCH (agreed with the launcher).
func (env *windowsServiceEnv) judge(info scmServiceInfo, facts copyFacts, caller []installSetFile) (ServiceVerdict, string) {
	set := facts.set
	if !info.exists {
		switch {
		case !facts.present:
			return ServiceNotInstalled, "no service and no copy in " + env.execDir
		case facts.invariantErr != nil:
			return ServiceUnsafe, "the copy is not protected (" + facts.invariantErr.Error() + "); " + recopyHint
		case set.markerFound && set.marker.Service != "":
			return ServiceMismatch, "the sidecar is bound to service " + set.marker.Service + ", which does not exist; " + reinstallHint + ", or " + recopyHint
		}
		if reason := set.setReason(caller, recopyHint); reason != "" {
			return ServiceMismatch, reason
		}
		return ServiceCopyOnly, ""
	}
	canonical := env.canonicalCopy()
	if info.configErr != nil {
		return ServiceUnsafe, "the service configuration cannot be read (" + info.configErr.Error() + "); " + reinstallHint
	}
	arguments, err := windows.DecomposeCommandLine(info.binaryPath)
	if err != nil || len(arguments) == 0 {
		return ServiceUnsafe, "the service command line cannot be parsed (" + info.binaryPath + "); " + reinstallHint
	}
	if unquotedPathWithSpace(info.binaryPath) {
		return ServiceUnsafe, "the service command line is not quoted and its path contains a space (" + info.binaryPath + "); " + reinstallHint
	}
	if !samePathFold(arguments[0], canonical) {
		return ServiceUnsafe, "the service runs " + arguments[0] + ", not the protected copy " + canonical + "; " + reinstallHint
	}
	if info.daclErr != nil {
		return ServiceUnsafe, "the service DACL cannot be read (" + info.daclErr.Error() + "); " + reinstallHint
	}
	if err = serviceACLViolation(info.dacl, info.nullDACL); err != nil {
		return ServiceUnsafe, err.Error() + "; " + reinstallHint
	}
	if facts.invariantErr != nil {
		return ServiceUnsafe, "the service executes a copy that is not protected (" + facts.invariantErr.Error() + "); " + reinstallHint
	}
	if set.markerFound && set.marker.Service != windowsServiceName {
		bound := set.marker.Service
		if bound == "" {
			bound = "no service"
		}
		return ServiceMismatch, "the sidecar next to " + canonical + " is bound to " + bound + ", not to service " + windowsServiceName + "; " + reinstallHint
	}
	if reason := set.setReason(caller, reinstallHint); reason != "" {
		return ServiceMismatch, reason
	}
	if info.statusErr != nil || info.state != svc.Running {
		state := describeState(info.state)
		if info.statusErr != nil {
			state = info.statusErr.Error()
		}
		return ServiceNotRunning, "the service is installed and consistent but not running (" + state + "); start it: sc.exe start " +
			windowsServiceName + " (as administrator), or sing-box lxd --service=install to reinstall"
	}
	return ServiceOK, ""
}

// unquotedPathWithSpace: an unquoted command line whose executable path has
// a space lets the SCM try C:\Program.exe first.
func unquotedPathWithSpace(commandLine string) bool {
	commandLine = strings.TrimSpace(commandLine)
	if strings.HasPrefix(commandLine, `"`) {
		return false
	}
	executable := commandLine
	if index := strings.Index(strings.ToLower(commandLine), ".exe"); index >= 0 {
		executable = commandLine[:index]
	}
	return strings.ContainsAny(executable, " \t")
}

func describeStartType(startType uint32) string {
	switch startType {
	case windows.SERVICE_AUTO_START:
		return "automatic"
	case windows.SERVICE_DEMAND_START:
		return "manual"
	case windows.SERVICE_DISABLED:
		return "disabled"
	default:
		return fmt.Sprint(startType)
	}
}

func describeState(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "STOPPED"
	case svc.StartPending:
		return "START_PENDING"
	case svc.StopPending:
		return "STOP_PENDING"
	case svc.Running:
		return "RUNNING"
	case svc.ContinuePending:
		return "CONTINUE_PENDING"
	case svc.PausePending:
		return "PAUSE_PENDING"
	case svc.Paused:
		return "PAUSED"
	default:
		return fmt.Sprint(uint32(state))
	}
}
