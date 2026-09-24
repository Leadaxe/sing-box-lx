//go:build with_lxd

package lxd

import (
	"bytes"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// fakeTree is a stat table: path → owner info; an absent path is ENOENT.
type fakeTree map[string]ownerInfo

func (tree fakeTree) lstat(path string) (ownerInfo, error) {
	if info, found := tree[path]; found {
		return info, nil
	}
	return ownerInfo{}, &fs.PathError{Op: "lstat", Path: path, Err: fs.ErrNotExist}
}

func rootDir(perm os.FileMode) ownerInfo  { return ownerInfo{mode: os.ModeDir | perm} }
func rootFile(perm os.FileMode) ownerInfo { return ownerInfo{mode: perm} }

// useFakeTree swaps the package stat for the table for one test.
func useFakeTree(t *testing.T, tree fakeTree) {
	t.Helper()
	saved := ownerLstat
	ownerLstat = tree.lstat
	t.Cleanup(func() { ownerLstat = saved })
}

func TestInvariantPredicate(t *testing.T) {
	for _, testCase := range []struct {
		name string
		uid  uint32
		mode os.FileMode
		want string // "" = passes; otherwise the exact error text
	}{
		{"root dir 0755", 0, os.ModeDir | 0o755, ""},
		{"root file 0755", 0, 0o755, ""},
		{"root file 0555", 0, 0o555, ""},
		{"sticky PrivilegedHelperTools 1755", 0, os.ModeDir | os.ModeSticky | 0o755, ""},
		{"setuid root file 4755", 0, os.ModeSetuid | 0o755, ""},
		{"user-owned file", 501, 0o755, "/p: owned by uid 501, mode 0755, must be root-owned and not group/world-writable"},
		{"group-writable /Applications", 0, os.ModeDir | 0o775, "/p: owned by uid 0, mode 0775, must be root-owned and not group/world-writable"},
		{"world-writable", 0, 0o757, "/p: owned by uid 0, mode 0757, must be root-owned and not group/world-writable"},
		{"sticky world-writable /private/tmp", 0, os.ModeDir | os.ModeSticky | 0o777, "/p: owned by uid 0, mode 1777, must be root-owned and not group/world-writable"},
		{"symlink", 0, os.ModeSymlink | 0o755, "/p: is a symbolic link, must be a real file or directory owned by root"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := rootOwnedViolation("/p", testCase.uid, testCase.mode)
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("must pass, got %v", err)
				}
				return
			}
			if err == nil || err.Error() != testCase.want {
				t.Fatalf("got %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestInvariantChain(t *testing.T) {
	good := fakeTree{
		"/":                              rootDir(0o755),
		"/Library":                       rootDir(0o755),
		"/Library/PrivilegedHelperTools": {mode: os.ModeDir | os.ModeSticky | 0o755},
		"/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd":          rootDir(0o755),
		"/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/sing-box": rootFile(0o755),
		"/Applications":                       {uid: 0, gid: 80, mode: os.ModeDir | 0o775},
		"/Applications/launcher.app":          {uid: 501, mode: os.ModeDir | 0o755},
		"/Applications/launcher.app/sing-box": {uid: 501, mode: 0o755},
		"/var":                                rootFile(os.ModeSymlink | 0o755),
		"/opt":                                rootDir(0o755),
		"/opt/user-dir":                       {uid: 501, mode: os.ModeDir | 0o755},
		"/opt/user-dir/root-file":             rootFile(0o755),
	}
	for _, testCase := range []struct {
		name     string
		path     string
		wantPart string // "" = passes
	}{
		{"canonical copy", "/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/sing-box", ""},
		{"root itself", "/", ""},
		{"group-writable parent is caught first", "/Applications/launcher.app/sing-box", "/Applications: owned by uid 0, mode 0775"},
		{"root file under a user dir", "/opt/user-dir/root-file", "/opt/user-dir: owned by uid 501"},
		{"symlink component", "/var/root/sing-box", "/var: is a symbolic link"},
		{"missing component", "/Library/PrivilegedHelperTools/other/sing-box", "file does not exist"},
		{"relative path", "Library/sing-box", "not an absolute path"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := checkRootOwnedChain(testCase.path, good.lstat)
			if testCase.wantPart == "" {
				if err != nil {
					t.Fatalf("must pass, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.wantPart) {
				t.Fatalf("got %v, want it to contain %q", err, testCase.wantPart)
			}
		})
	}
}

func TestInvariantEnsureDir(t *testing.T) {
	tree := fakeTree{
		"/":                              rootDir(0o755),
		"/Library":                       rootDir(0o755),
		"/Library/PrivilegedHelperTools": {mode: os.ModeDir | os.ModeSticky | 0o755},
		"/Users":                         rootDir(0o755),
		"/Users/me":                      {uid: 501, mode: os.ModeDir | 0o755},
		"/Library/file":                  rootFile(0o644),
	}
	useFakeTree(t, tree)

	// Dry run over a missing leaf: announced, nothing else.
	var out bytes.Buffer
	if err := ensureRootOwnedDir(&out, "/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd", dirPlan, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "lxd: would create /Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd (root:wheel 0755)") {
		t.Fatalf("plan must announce the directory, got:\n%s", out.String())
	}

	// Existing and good: passes in every mode.
	out.Reset()
	if err := ensureRootOwnedDir(&out, "/Library/PrivilegedHelperTools", dirCheck, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "passes the root-owned check") {
		t.Fatalf("a passing check must say so, got:\n%s", out.String())
	}

	// A user-owned parent refuses BEFORE anything is created or planned.
	out.Reset()
	err := ensureRootOwnedDir(&out, "/Users/me/lxd-bin", dirPlan, false)
	if err == nil || !strings.Contains(err.Error(), "/Users/me: owned by uid 501, mode 0755, must be root-owned") {
		t.Fatalf("user-owned parent must be refused, got %v", err)
	}
	if strings.Contains(out.String(), "would create") {
		t.Fatalf("nothing may be planned under a refused parent, got:\n%s", out.String())
	}

	// dirCheck does not accept a missing directory.
	if err = ensureRootOwnedDir(&out, "/Library/PrivilegedHelperTools/absent", dirCheck, false); err == nil {
		t.Fatal("a missing directory must fail the check")
	}
	// A file where a directory is expected.
	if err = ensureRootOwnedDir(&out, "/Library/file", dirPlan, false); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("a file must not pass as the exec dir, got %v", err)
	}
}

func TestInvariantSelfCheck(t *testing.T) {
	tree := fakeTree{
		"/":                              rootDir(0o755),
		"/Library":                       rootDir(0o755),
		"/Library/PrivilegedHelperTools": {mode: os.ModeDir | os.ModeSticky | 0o755},
		"/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd":          rootDir(0o755),
		"/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/sing-box": rootFile(0o755),
		"/Applications":                       {gid: 80, mode: os.ModeDir | 0o775},
		"/Applications/launcher.app":          {uid: 501, mode: os.ModeDir | 0o755},
		"/Applications/launcher.app/sing-box": {uid: 501, mode: 0o755},
	}
	const (
		safe   = "/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/sing-box"
		unsafe = "/Applications/launcher.app/sing-box"
	)
	for _, testCase := range []struct {
		name        string
		euid, ppid  int
		xpc         string
		executable  string
		allowUnsafe bool
		wantErr     string // "" = no refusal
		wantWarn    string // "" = silent
	}{
		{"not root: nothing to check", 501, 1, launchdLabel, unsafe, false, "", ""},
		{"service from the root-owned copy", 0, 1, launchdLabel, safe, false, "", ""},
		{"service from the bundle is refused", 0, 1, launchdLabel, unsafe, false,
			"lxd: refusing to run as a root service from /Applications/launcher.app/sing-box (uid 501, mode 0755): /Applications: owned by uid 0, mode 0775, must be root-owned and not group/world-writable; run `sing-box lxd --service=install` to reinstall from a root-owned copy", ""},
		{"service with --allow-unsafe-exec warns", 0, 1, launchdLabel, unsafe, true, "", "lxd: --allow-unsafe-exec: running as root from /Applications/launcher.app/sing-box (uid 501, mode 0755)"},
		{"ppid 1 without the launchd label (nohup) warns", 0, 1, "", unsafe, false, "", "not the launchd service (ppid 1, XPC_SERVICE_NAME \"\")"},
		{"another launchd job warns", 0, 1, "com.example.other", unsafe, false, "", "XPC_SERVICE_NAME \"com.example.other\""},
		{"sudo from a terminal warns", 0, 4242, "", unsafe, false, "", "not the launchd service (ppid 4242"},
		{"root outside a service from the copy is silent", 0, 4242, "", safe, false, "", ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			warning, err := evaluateSelfCheck(selfCheckEnv{
				euid:       testCase.euid,
				ppid:       testCase.ppid,
				xpcService: testCase.xpc,
				executable: func() (string, error) { return testCase.executable, nil },
				lstat:      tree.lstat,
			}, testCase.allowUnsafe)
			if testCase.wantErr == "" && err != nil {
				t.Fatalf("must not refuse, got %v", err)
			}
			if testCase.wantErr != "" && (err == nil || err.Error() != testCase.wantErr) {
				t.Fatalf("refusal %v, want %q", err, testCase.wantErr)
			}
			if testCase.wantWarn == "" && warning != "" {
				t.Fatalf("must be silent, warned %q", warning)
			}
			if !strings.Contains(warning, testCase.wantWarn) {
				t.Fatalf("warning %q, want it to contain %q", warning, testCase.wantWarn)
			}
		})
	}
}
