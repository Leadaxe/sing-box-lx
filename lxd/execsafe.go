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
	case ServiceMismatch:
		return 2
	default:
		return 3
	}
}

// ExitCode is what `--service=status` exits with: 0 OK, 2 reinstall needed
// (MISMATCH, UNSAFE), 3 not installed, 4 copy only. 1 stays with errors,
// which the command reports on its own.
func (v ServiceVerdict) ExitCode() int {
	switch v {
	case ServiceOK:
		return 0
	case ServiceNotInstalled:
		return 3
	case ServiceCopyOnly:
		return 4
	default:
		return 2
	}
}

// selfCheckEnv is what the start-up self-check reads from the process; the
// test fills it from a table.
type selfCheckEnv struct {
	euid       int
	ppid       int
	xpcService string // launchd sets XPC_SERVICE_NAME to the job's label
	executable func() (string, error)
	lstat      func(string) (ownerInfo, error)
}

// CheckServiceExecutable is the start-up self-check of a core running as
// root (SPEC 100 §2.8): its own binary, symlinks resolved, must pass the
// root-owned invariant. Only the launchd job of this label — parent pid 1
// AND XPC_SERVICE_NAME equal to the label — refuses to start on a
// violation; a root run anywhere else (sudo from a terminal, nohup, a
// launcher elevating the core) only warns, as does allowUnsafe. Without
// root there is nothing to escalate to and nothing is checked.
func CheckServiceExecutable(allowUnsafe bool) error {
	warning, err := evaluateSelfCheck(selfCheckEnv{
		euid:       os.Geteuid(),
		ppid:       os.Getppid(),
		xpcService: os.Getenv("XPC_SERVICE_NAME"),
		executable: resolveOwnExecutable,
		lstat:      ownerLstat,
	}, allowUnsafe)
	if warning != "" {
		log.Warn(warning)
	}
	return err
}

func evaluateSelfCheck(env selfCheckEnv, allowUnsafe bool) (warning string, err error) {
	if env.euid != 0 {
		return "", nil
	}
	executable, violation := env.executable()
	if violation == nil {
		violation = checkRootOwnedChain(executable, env.lstat)
	}
	if violation == nil {
		if info, statErr := env.lstat(executable); statErr == nil && !info.mode.IsRegular() {
			violation = E.New(executable, ": not a regular file")
		}
	}
	if violation == nil {
		return "", nil
	}
	subject := describeOwner(executable, env.lstat)
	const remedy = "run `sing-box lxd --service=install` to reinstall from a root-owned copy"
	switch {
	case allowUnsafe:
		return "lxd: --allow-unsafe-exec: running as root from " + subject + ": " + violation.Error() + " — starting anyway; anyone who can replace that file runs code as root", nil
	case env.ppid == 1 && env.xpcService == launchdLabel:
		return "", E.New("lxd: refusing to run as a root service from ", subject, ": ", violation, "; ", remedy)
	default:
		return "lxd: running as root from " + subject + ": " + violation.Error() +
			" — not the launchd service (ppid " + strconv.Itoa(env.ppid) + ", XPC_SERVICE_NAME " + strconv.Quote(env.xpcService) +
			"), starting anyway; the service would refuse this binary: " + remedy + " (or --service=copy)", nil
	}
}

// describeOwner renders "<path> (uid N, mode NNNN)" for a refusal or warning.
func describeOwner(path string, lstat func(string) (ownerInfo, error)) string {
	if path == "" {
		return "an unresolvable executable"
	}
	info, err := lstat(path)
	if err != nil {
		return path + " (" + err.Error() + ")"
	}
	return fmt.Sprintf("%s (uid %d, mode %s)", path, info.uid, formatMode(info.mode))
}
