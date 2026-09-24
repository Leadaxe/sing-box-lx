//go:build with_lxd

package lxd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
)

// launchdLabel names the service everywhere it is referenced: the launchd
// job, the plist file, and the directory holding the root-owned copy of the
// binary (SPEC 100). It lives in a platform-neutral file because the daemon's
// self-check compares it with launchd's XPC_SERVICE_NAME.
const launchdLabel = "com.leadaxe.sing-box-lxd"

// ownerInfo is the part of a stat result the root-owned invariant reads.
type ownerInfo struct {
	uid  uint32
	gid  uint32
	mode os.FileMode
}

// ownerLstat is the stat behind every invariant walk. Tests point it at a
// table so the install paths can be exercised without root.
var ownerLstat = lstatOwner

// rootOwnedViolation is the root-owned invariant for ONE path component
// (SPEC 100 §2.2): not a symlink, owned by uid 0, no write bit for group or
// other. Setuid/setgid/sticky do not matter. It never touches the disk — the
// table test feeds it values, so it runs on any CI host without root.
func rootOwnedViolation(path string, uid uint32, mode os.FileMode) error {
	if mode&os.ModeSymlink != 0 {
		return E.New(path, ": is a symbolic link, must be a real file or directory owned by root")
	}
	if uid != 0 || mode.Perm()&0o022 != 0 {
		return E.New(path, ": owned by uid ", uid, ", mode ", formatMode(mode), ", must be root-owned and not group/world-writable")
	}
	return nil
}

// formatMode renders permission bits the way chmod takes them: four octal
// digits with setuid/setgid/sticky included (/private/tmp reads 1777).
func formatMode(mode os.FileMode) string {
	bits := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		bits |= 0o1000
	}
	return fmt.Sprintf("%04o", bits)
}

// pathChain lists every component of an absolute path from "/" down to the
// path itself: /a/b → [/ /a /a/b].
func pathChain(path string) ([]string, error) {
	if !filepath.IsAbs(path) {
		return nil, E.New(path, ": not an absolute path")
	}
	clean := filepath.Clean(path)
	chain := []string{"/"}
	if clean == "/" {
		return chain, nil
	}
	current := ""
	for _, part := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		current += "/" + part
		chain = append(chain, current)
	}
	return chain, nil
}

// checkRootOwnedChain applies the invariant to every component from "/" to
// path inclusive, without following symlinks: one writable directory anywhere
// above the file lets its owner swap whatever lies below it.
func checkRootOwnedChain(path string, lstat func(string) (ownerInfo, error)) error {
	chain, err := pathChain(path)
	if err != nil {
		return err
	}
	for _, component := range chain {
		info, statErr := lstat(component)
		if statErr != nil {
			return statErr
		}
		if err = rootOwnedViolation(component, info.uid, info.mode); err != nil {
			return err
		}
	}
	return nil
}

// dirAction selects what ensureRootOwnedDir does about a missing component.
type dirAction int

const (
	dirCheck  dirAction = iota // a missing component is an error
	dirPlan                    // print what would be created (dry run)
	dirCreate                  // create it root:wheel 0755
)

// ensureRootOwnedDir walks dir from "/" (SPEC 100 §2.3 step 2): existing
// components must be directories passing the invariant; missing ones are
// created root:wheel 0755, announced, or refused, per action. Nothing is
// created before every existing component has passed. chown is off only
// where no root is at hand (tests).
func ensureRootOwnedDir(out io.Writer, dir string, action dirAction, chown bool) error {
	chain, err := pathChain(dir)
	if err != nil {
		return err
	}
	var missing []string
	for _, component := range chain {
		if len(missing) > 0 {
			missing = append(missing, component)
			continue
		}
		info, statErr := ownerLstat(component)
		if statErr != nil {
			if os.IsNotExist(statErr) && action != dirCheck {
				missing = append(missing, component)
				continue
			}
			return statErr
		}
		if err = rootOwnedViolation(component, info.uid, info.mode); err != nil {
			return err
		}
		if !info.mode.IsDir() {
			return E.New(component, ": not a directory")
		}
	}
	for _, component := range missing {
		if action == dirPlan {
			fmt.Fprintln(out, "lxd: would create", component, "(root:wheel 0755)")
			continue
		}
		if err = os.Mkdir(component, 0o755); err != nil {
			return E.Cause(err, "create ", component)
		}
		// Explicit owner and mode: BSD semantics hand a new directory its
		// parent's group, and the umask may have trimmed the mode.
		if chown {
			if err = os.Chown(component, 0, 0); err != nil {
				return E.Cause(err, "chown root:wheel ", component)
			}
		}
		if err = os.Chmod(component, 0o755); err != nil {
			return E.Cause(err, "chmod 0755 ", component)
		}
		fmt.Fprintln(out, "lxd: created", component, "(root:wheel 0755)")
	}
	if action == dirCreate && len(missing) > 0 {
		// The final state is what launchd will execute from — verify it.
		if err = checkRootOwnedChain(dir, ownerLstat); err != nil {
			return err
		}
	}
	if action == dirPlan && len(missing) > 0 {
		fmt.Fprintln(out, "lxd: exec dir", dir, "would be created; its existing parents pass the root-owned check")
		return nil
	}
	fmt.Fprintln(out, "lxd: exec dir", dir, "passes the root-owned check (every component from / is root-owned, not group/world-writable)")
	return nil
}

// ServiceVerdict is the outcome of `--service=status` (SPEC 100 §2.6),
// ordered by severity up to ServiceUnsafe.
type ServiceVerdict int

const (
	// ServiceOK: the service runs a root-owned copy identical to the caller.
	ServiceOK ServiceVerdict = iota
	// ServiceMismatch: the installed binary differs from the caller.
	ServiceMismatch
	// ServiceUnsafe: the system plist points at a binary that is not a
	// root-owned copy.
	ServiceUnsafe
	// ServiceNotInstalled: no plist in either scope and no copy.
	ServiceNotInstalled
	// ServiceCopyOnly: a root-owned copy identical to the caller, made by
	// `--service=copy`, with no service plist (SPEC 100 §2.4).
	ServiceCopyOnly
	// ServiceNotRunning: the plist and the copy are consistent, but launchd
	// has no running job for the label — the service is installed on disk
	// and not loaded (a failed bootstrap, a bootout without bootstrap).
	ServiceNotRunning
)

func (v ServiceVerdict) String() string {
	switch v {
	case ServiceOK:
		return "OK"
	case ServiceMismatch:
		return "MISMATCH"
	case ServiceUnsafe:
		return "UNSAFE"
	case ServiceNotInstalled:
		return "NOT INSTALLED"
	case ServiceCopyOnly:
		return "COPY ONLY"
	case ServiceNotRunning:
		return "NOT RUNNING"
	default:
		return "UNKNOWN"
	}
}

// severity orders the verdicts of present blocks; the report's overall
// verdict is the most severe one.
func (v ServiceVerdict) severity() int {
	switch v {
	case ServiceOK:
		return 0
	case ServiceCopyOnly:
		return 1
	case ServiceNotRunning:
		return 2
	case ServiceMismatch:
		return 3
	default:
		return 4
	}
}

// ExitCode is what `--service=status` exits with: 0 OK, 2 reinstall needed
// (MISMATCH, UNSAFE), 3 not installed, 4 copy only, 5 installed but not
// running in launchd. 1 stays with errors, which the command reports on
// its own.
func (v ServiceVerdict) ExitCode() int {
	switch v {
	case ServiceOK:
		return 0
	case ServiceNotInstalled:
		return 3
	case ServiceCopyOnly:
		return 4
	case ServiceNotRunning:
		return 5
	default:
		return 2
	}
}

// verdictLine renders the last line of a status report: the verdict, and
// for MISMATCH/UNSAFE/NOT RUNNING the reason with its remedy after " — ".
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

// selfCheckEnv is what the start-up self-check reads from the process; the
// tests fill it from a table. The platform builds it (execsafe_unix.go,
// execsafe_windows.go); the decision below is shared (SPEC 103 §2.11).
type selfCheckEnv struct {
	// privileged: the process holds what the service holds — euid 0 on
	// unix, an elevated token on Windows. Nothing is checked without it:
	// there is nothing to escalate to.
	privileged bool
	// serviceContext: this process is the service itself — the launchd job
	// of this label, or `lxd` under the Windows SCM. A violation refuses the
	// start here and only warns anywhere else.
	serviceContext bool
	// context describes a privileged run outside the service for the
	// warning, e.g. `ppid 4242, XPC_SERVICE_NAME ""`.
	context    string
	executable func() (string, error)
	// check applies the platform's invariant to the executable; subject
	// renders the executable with its owner for the messages.
	check   func(executable string) error
	subject func(executable string) string
	words   selfCheckWords
}

// selfCheckWords is the platform's vocabulary in the self-check messages.
type selfCheckWords struct {
	refuseAs   string // "a root service"
	runningAs  string // "running as root"
	risk       string // "anyone who can replace that file runs code as root"
	notService string // "not the launchd service"
	remedy     string
	okKind     string // "root-owned", in the INFO line of a passed check
	service    string // "launchd service com.leadaxe.sing-box-lxd"
}

// unixSelfCheckWords is SPEC 100 §2.8, word for word.
var unixSelfCheckWords = selfCheckWords{
	refuseAs:   "a root service",
	runningAs:  "running as root",
	risk:       "anyone who can replace that file runs code as root",
	notService: "not the launchd service",
	remedy:     "run `sing-box lxd --service=install` to reinstall from a root-owned copy",
	okKind:     "root-owned",
	service:    "launchd service " + launchdLabel,
}

// unixSelfCheckEnv builds the unix environment from the process facts
// (SPEC 100 §2.8): root is euid 0, the service is the launchd job of this
// label — parent pid 1 AND XPC_SERVICE_NAME equal to the label. Pure, so the
// table test drives it on any host.
func unixSelfCheckEnv(euid, ppid int, xpcService string, executable func() (string, error), lstat func(string) (ownerInfo, error)) selfCheckEnv {
	return selfCheckEnv{
		privileged:     euid == 0,
		serviceContext: ppid == 1 && xpcService == launchdLabel,
		context:        "ppid " + strconv.Itoa(ppid) + ", XPC_SERVICE_NAME " + strconv.Quote(xpcService),
		executable:     executable,
		check: func(path string) error {
			if err := checkRootOwnedChain(path, lstat); err != nil {
				return err
			}
			if info, err := lstat(path); err == nil && !info.mode.IsRegular() {
				return E.New(path, ": not a regular file")
			}
			return nil
		},
		subject: func(path string) string { return describeOwner(path, lstat) },
		words:   unixSelfCheckWords,
	}
}

// CheckServiceExecutable is the start-up self-check of a privileged core
// (SPEC 100 §2.8, SPEC 103 §2.11): its own binary, symlinks resolved, must
// pass the platform invariant — root-owned on unix, the protected path on
// Windows. Only the service itself refuses to start on a violation; a
// privileged run anywhere else (sudo from a terminal, nohup, a launcher
// elevating the core) only warns, as does allowUnsafe. Without privileges
// there is nothing to escalate to and nothing is checked. daemon says the
// caller is the `lxd` command, not `run`: on Windows only `lxd` under the SCM
// is the service; unix tells the service apart by launchd's marks alone.
// A passed check in the service context logs one INFO line.
func CheckServiceExecutable(allowUnsafe bool, daemon bool) error {
	info, warning, err := evaluateSelfCheck(platformSelfCheckEnv(daemon), allowUnsafe)
	if info != "" {
		log.Info(info)
	}
	if warning != "" {
		log.Warn(warning)
	}
	return err
}

func evaluateSelfCheck(env selfCheckEnv, allowUnsafe bool) (info string, warning string, err error) {
	if !env.privileged {
		return "", "", nil
	}
	executable, violation := env.executable()
	if violation == nil {
		violation = env.check(executable)
	}
	if violation == nil {
		if env.serviceContext {
			return "lxd: self-check ok: " + env.words.okKind + " " + executable + ", " + env.words.service, "", nil
		}
		return "", "", nil
	}
	subject := "an unresolvable executable"
	if executable != "" {
		subject = env.subject(executable)
	}
	words := env.words
	switch {
	case allowUnsafe:
		return "", "lxd: --allow-unsafe-exec: " + words.runningAs + " from " + subject + ": " + violation.Error() + " — starting anyway; " + words.risk, nil
	case env.serviceContext:
		return "", "", E.New("lxd: refusing to run as ", words.refuseAs, " from ", subject, ": ", violation, "; ", words.remedy)
	default:
		return "", "lxd: " + words.runningAs + " from " + subject + ": " + violation.Error() +
			" — " + words.notService + " (" + env.context +
			"), starting anyway; the service would refuse this binary: " + words.remedy + " (or --service=copy)", nil
	}
}

// describeOwner renders "<path> (uid N, mode NNNN)" for a refusal or warning.
func describeOwner(path string, lstat func(string) (ownerInfo, error)) string {
	info, err := lstat(path)
	if err != nil {
		return path + " (" + err.Error() + ")"
	}
	return fmt.Sprintf("%s (uid %d, mode %s)", path, info.uid, formatMode(info.mode))
}
