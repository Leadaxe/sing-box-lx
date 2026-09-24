//go:build with_lxd && darwin

package lxd

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// nextTagAfterKey returns the first XML tag following the given <key> entry,
// so assertions survive indentation changes but still verify tag adjacency.
func nextTagAfterKey(t *testing.T, plist, key string) string {
	t.Helper()
	marker := "<key>" + key + "</key>"
	idx := strings.Index(plist, marker)
	if idx < 0 {
		t.Fatalf("plist has no %s", marker)
	}
	rest := strings.TrimLeft(plist[idx+len(marker):], " \t\n")
	end := strings.Index(rest, "\n")
	if end < 0 {
		end = len(rest)
	}
	return strings.TrimSpace(rest[:end])
}

// capture runs fn with stdout redirected to a pipe and returns what it wrote.
func capture(t *testing.T, fn func() error) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	runErr := fn()
	os.Stdout = original
	_ = writer.Close()
	buf := make([]byte, 1<<16)
	n, _ := reader.Read(buf)
	_ = reader.Close()
	if runErr != nil {
		t.Fatalf("dry run returned an error: %v", runErr)
	}
	return string(buf[:n])
}

// TestDryRunInstallTouchesNothing: --dry-run must be safe to run anywhere,
// including without root — it reports the plan and writes nothing. The user
// scope is used because its paths are the ones a test can legitimately check.
func TestDryRunInstallTouchesNothing(t *testing.T) {
	scope := userScope()
	_, plistExistedBefore := os.Stat(scope.plist)

	out := capture(t, func() error {
		return InstallUserService([]string{"lxd", "--state-dir", "/tmp/lxd-dry-run"}, true)
	})

	if !strings.Contains(out, "dry run") || !strings.Contains(out, "nothing was installed") {
		t.Fatalf("dry run must say it changed nothing; got:\n%s", out)
	}
	if !strings.Contains(out, "<key>ProgramArguments</key>") {
		t.Fatalf("dry run must show the plist; got:\n%s", out)
	}
	if !strings.Contains(out, "--state-dir") || !strings.Contains(out, "/tmp/lxd-dry-run") {
		t.Fatalf("dry run plist must carry the daemon args; got:\n%s", out)
	}
	// The plist must be exactly as absent (or as present) as it was before.
	if _, plistExistsNow := os.Stat(scope.plist); (plistExistsNow == nil) != (plistExistedBefore == nil) {
		t.Fatal("dry run must not create or remove the plist")
	}
	if _, err := os.Stat("/tmp/lxd-dry-run"); !os.IsNotExist(err) {
		t.Fatal("dry run must not create the state dir")
	}
}

func TestBuildPlist(t *testing.T) {
	programArgs := []string{"/usr/local/bin/sing-box", "lxd", "--listen", "127.0.0.1:9090"}
	logPath := "/Library/Application Support/sing-box-lxd/lxd.log"
	plist := buildPlist(launchdLabel, programArgs, logPath)

	if nextTagAfterKey(t, plist, "Label") != "<string>"+launchdLabel+"</string>" {
		t.Fatal("Label must be the launchd label")
	}

	// Every program argument must appear as its own <string>, in call order —
	// launchd feeds ProgramArguments to execve positionally.
	lastIdx := -1
	for _, arg := range programArgs {
		idx := strings.Index(plist, "<string>"+arg+"</string>")
		if idx < 0 {
			t.Fatal("missing program argument:", arg)
		}
		if idx <= lastIdx {
			t.Fatal("program argument out of order:", arg)
		}
		lastIdx = idx
	}

	// RunAtLoad + KeepAlive keep the daemon supervised across crashes/reboots.
	if nextTagAfterKey(t, plist, "RunAtLoad") != "<true/>" {
		t.Fatal("RunAtLoad must be true")
	}
	if nextTagAfterKey(t, plist, "KeepAlive") != "<true/>" {
		t.Fatal("KeepAlive must be true")
	}

	// Both stdout and stderr land in the same log file.
	wantLog := "<string>" + logPath + "</string>"
	if nextTagAfterKey(t, plist, "StandardOutPath") != wantLog {
		t.Fatal("StandardOutPath must be the log path")
	}
	if nextTagAfterKey(t, plist, "StandardErrorPath") != wantLog {
		t.Fatal("StandardErrorPath must be the log path")
	}
}

func TestBuildPlistEscapesArguments(t *testing.T) {
	rawArg := `--token=a&b<c>d`
	plist := buildPlist(launchdLabel, []string{"/bin/daemon", rawArg}, "/tmp/lxd.log")
	if !strings.Contains(plist, "<string>--token=a&amp;b&lt;c&gt;d</string>") {
		t.Fatal("special characters in an argument must be XML-escaped")
	}
	if strings.Contains(plist, rawArg) {
		t.Fatal("raw unescaped argument leaked into the plist")
	}
}

func TestPlistEscape(t *testing.T) {
	for _, testCase := range []struct {
		in   string
		want string
	}{
		{"", ""},
		{"plain-arg", "plain-arg"},
		{"&<>", "&amp;&lt;&gt;"},
		{"a&&b<<c>>d", "a&amp;&amp;b&lt;&lt;c&gt;&gt;d"},
	} {
		if got := plistEscape(testCase.in); got != testCase.want {
			t.Fatalf("plistEscape(%q) = %q, want %q", testCase.in, got, testCase.want)
		}
	}
}

func TestDefaultServiceStateDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	for _, user := range []bool{false, true} {
		dir := DefaultServiceStateDir(user)
		// A launchd unit runs with cwd "/" — a relative state dir would land there.
		if !filepath.IsAbs(dir) {
			t.Fatalf("state dir must be absolute (user=%v): %s", user, dir)
		}
		if filepath.Base(dir) != "state" {
			t.Fatalf("state dir must end in /state (user=%v): %s", user, dir)
		}
	}

	if !strings.HasPrefix(DefaultServiceStateDir(true), home+string(filepath.Separator)) {
		t.Fatal("user state dir must live under the home directory:", DefaultServiceStateDir(true))
	}
	if !strings.HasPrefix(DefaultServiceStateDir(false), "/Library/") {
		t.Fatal("system state dir must live under /Library:", DefaultServiceStateDir(false))
	}
}

func TestServiceScopes(t *testing.T) {
	system := systemScope()
	if system.user {
		t.Fatal("system scope must not be flagged as user")
	}
	if !filepath.IsAbs(system.plist) {
		t.Fatal("system plist path must be absolute:", system.plist)
	}
	if system.bootTgt != "system" {
		t.Fatal("system boot target must be \"system\":", system.bootTgt)
	}
	if !system.needRoot {
		t.Fatal("installing a LaunchDaemon must require root")
	}

	user := userScope()
	if !user.user {
		t.Fatal("user scope must be flagged as user")
	}
	if !filepath.IsAbs(user.plist) {
		t.Fatal("user plist path must be absolute:", user.plist)
	}
	if !strings.HasPrefix(user.bootTgt, "gui/") {
		t.Fatal("user boot target must be in the gui domain:", user.bootTgt)
	}
	if user.bootTgt != "gui/"+strconv.Itoa(os.Getuid()) {
		t.Fatal("user boot target must carry the current uid:", user.bootTgt)
	}
	if user.needRoot {
		t.Fatal("installing a LaunchAgent must not require root")
	}
}

// useOwnerLstat swaps the stat behind the invariant walks for one test.
func useOwnerLstat(t *testing.T, lstat func(string) (ownerInfo, error)) {
	t.Helper()
	saved := ownerLstat
	ownerLstat = lstat
	t.Cleanup(func() { ownerLstat = saved })
}

// rootOwnsEverything reports the real file tree as if root owned it with no
// group/other write — the state install produces, without needing root.
func rootOwnsEverything(path string) (ownerInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ownerInfo{}, err
	}
	return ownerInfo{mode: info.Mode() &^ 0o022}, nil
}

// realTempDir resolves t.TempDir: on macOS it sits under /var, a symlink the
// invariant rightly refuses.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBuildPlistRoundTrip(t *testing.T) {
	programArgs := []string{
		"/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/sing-box",
		"lxd", "--state-dir", "/Library/Application Support/sing-box-lxd/state",
		"-c", "/tmp/a&b<c>.json",
	}
	program, arguments, err := parsePlistProgram([]byte(buildPlist(launchdLabel, programArgs, "/tmp/lxd.log")))
	if err != nil {
		t.Fatal(err)
	}
	if program != programArgs[0] || !slices.Equal(arguments, programArgs) {
		t.Fatalf("round trip: program %q args %q", program, arguments)
	}
}

func TestBuildPlistProgramKey(t *testing.T) {
	// launchd prefers Program over ProgramArguments[0]; so does status.
	withProgram := `<?xml version="1.0"?><plist version="1.0"><dict>
<key>EnvironmentVariables</key><dict><key>Program</key><string>/nested/ignored</string></dict>
<key>Program</key><string>/usr/local/libexec/sing-box</string>
<key>ProgramArguments</key><array><string>sing-box</string><integer>1</integer><string>lxd</string></array>
</dict></plist>`
	program, arguments, err := parsePlistProgram([]byte(withProgram))
	if err != nil {
		t.Fatal(err)
	}
	if program != "/usr/local/libexec/sing-box" || !slices.Equal(arguments, []string{"sing-box", "lxd"}) {
		t.Fatalf("program %q args %q", program, arguments)
	}
	if _, _, err = parsePlistProgram([]byte(`<plist><dict><key>Label</key><string>x</string></dict></plist>`)); err == nil {
		t.Fatal("a plist without a program must be an error")
	}
}

func TestServiceLaunchctlPrint(t *testing.T) {
	output := "system/com.leadaxe.sing-box-lxd = {\n" +
		"\tactive count = 1\n" +
		"\tpath = /Library/LaunchDaemons/com.leadaxe.sing-box-lxd.plist\n" +
		"\ttype = LaunchDaemon\n" +
		"\tstate = running\n" +
		"\n" +
		"\tprogram = /Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/sing-box\n" +
		"\targuments = {\n" +
		"\t\t/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/sing-box\n" +
		"\t}\n" +
		"\tendpoints = {\n" +
		"\t\t\"com.example\" = {\n" +
		"\t\t\tstate = active\n" +
		"\t\t\tpid = 1\n" +
		"\t\t}\n" +
		"\t}\n" +
		"\tpid = 86234\n" +
		"}\n"
	state, pid, program := parseLaunchctlPrint(output)
	if state != "running" || pid != "86234" || program != "/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/sing-box" {
		t.Fatalf("state %q pid %q program %q", state, pid, program)
	}
}

// TestDryRunSystemInstallPlansCopy: the system dry run shows the whole copy
// plan and a plist that runs the copy, and creates nothing.
func TestDryRunSystemInstallPlansCopy(t *testing.T) {
	useOwnerLstat(t, rootOwnsEverything)
	execDir := filepath.Join(realTempDir(t), launchdLabel)
	target := filepath.Join(execDir, "sing-box")
	self, err := resolveOwnExecutable()
	if err != nil {
		t.Fatal(err)
	}
	daemonArgs := []string{"lxd", "--state-dir", "/tmp/lxd-dry-run"}
	var out bytes.Buffer
	if err = printPlan(&out, systemScope(), daemonArgs, execDir); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"lxd: dry run — nothing was installed.",
		"lxd: would create " + execDir + " (root:wheel 0755)",
		"lxd: would copy " + self + " -> " + target,
		"chown root:wheel, chmod 0755, verify sha256, rename into place",
		"lxd: would write sidecar " + filepath.Join(execDir, "install.json") + " (root:wheel 0644)",
		"lxd: dry run result: plist ProgramArguments[0] = " + target,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	program, arguments, err := parsePlistProgram([]byte(text[strings.Index(text, "<?xml"):]))
	if err != nil {
		t.Fatal(err)
	}
	// Only ProgramArguments[0] changes; the daemon arguments ride as before.
	if program != target || !slices.Equal(arguments[1:], daemonArgs) {
		t.Fatalf("plist runs %q with %q", program, arguments)
	}
	if _, err = os.Lstat(execDir); !os.IsNotExist(err) {
		t.Fatal("dry run must not create the exec dir")
	}
}

func TestDryRunSystemInstallRefusesUnsafeExecDir(t *testing.T) {
	// Real ownership: a test's temp dir belongs to the user running it.
	execDir := filepath.Join(realTempDir(t), launchdLabel)
	var out bytes.Buffer
	err := printPlan(&out, systemScope(), []string{"lxd"}, execDir)
	if os.Geteuid() == 0 {
		t.Skip("running as root: the temp dir may well be root-owned")
	}
	if err == nil || !strings.Contains(err.Error(), "must be root-owned and not group/world-writable") {
		t.Fatalf("a user-owned exec dir must be refused, got %v", err)
	}
	if !strings.Contains(out.String(), "lxd: dry run result: would refuse:") {
		t.Fatalf("the refusal must be printed:\n%s", out.String())
	}
}

// squash collapses runs of spaces so assertions survive column alignment.
func squash(text string) string {
	return strings.Join(strings.FieldsFunc(text, func(r rune) bool { return r == ' ' }), " ")
}

// testServiceEnv is a machine in a temp dir: scopes, exec dir and a source
// binary of its own; launchctl is a no-op and root owns every file
// (rootOwnsEverything). root=true opens the system scope; chown stays off.
func testServiceEnv(t *testing.T) (serviceEnv, *bytes.Buffer, string) {
	t.Helper()
	useOwnerLstat(t, rootOwnsEverything)
	base := realTempDir(t)
	for _, dir := range []string{"bundle", "PrivilegedHelperTools", "LaunchDaemons", "LaunchAgents"} {
		if err := os.Mkdir(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	out := &bytes.Buffer{}
	env := serviceEnv{
		out:     out,
		source:  filepath.Join(base, "bundle", "sing-box"),
		execDir: filepath.Join(base, "PrivilegedHelperTools", launchdLabel),
		root:    true,
		system: serviceScope{
			plist:    filepath.Join(base, "LaunchDaemons", launchdLabel+".plist"),
			logPath:  filepath.Join(base, "support-system", "lxd.log"),
			bootTgt:  "gui/2147483646",
			needRoot: true,
		},
		user: serviceScope{
			user:    true,
			plist:   filepath.Join(base, "LaunchAgents", launchdLabel+".plist"),
			logPath: filepath.Join(base, "support-user", "lxd.log"),
			bootTgt: "gui/2147483646",
		},
		load:   func(serviceScope) error { return nil },
		unload: func(serviceScope) error { return nil },
	}
	return env, out, base
}

// setSource puts a new core binary into the bundle — a launcher update.
func setSource(t *testing.T, env serviceEnv, content string) string {
	t.Helper()
	if err := os.WriteFile(env.source, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return shaOf([]byte(content))
}

// TestServiceStateTransitions walks the state machine of SPEC 100 §2.7:
// none → copy only → installed → (core update) → none, and copy only → none.
func TestServiceStateTransitions(t *testing.T) {
	env, out, base := testServiceEnv(t)
	callerSHA := setSource(t, env, "core v1")
	copyPath := filepath.Join(env.execDir, "sing-box")
	exists := func(path string) bool { _, err := os.Lstat(path); return err == nil }

	for _, step := range []struct {
		name        string
		action      func() error
		newCore     string // non-empty: the launcher updated its bundle first
		wantVerdict ServiceVerdict
		wantOutput  string
		wantPlist   bool
		wantCopy    bool
	}{
		{"none", nil, "", ServiceNotInstalled, "lxd: verdict: NOT INSTALLED", false, false},
		{"copy: none → copy only", env.copyOnly, "", ServiceCopyOnly, "lxd: copied ", false, true},
		{"copy again: no-op", env.copyOnly, "", ServiceCopyOnly, "lxd: already up to date " + shaOf([]byte("core v1")), false, true},
		{"install: copy only → installed, no second copy", func() error { return env.installSystem([]string{"lxd", "--state-dir", "/x"}) }, "", ServiceOK, "copy skipped", true, true},
		{"core updated, not yet copied", nil, "core v2", ServiceMismatch, "differs from this one", true, true},
		{"copy under an installed service keeps it bound", env.copyOnly, "", ServiceOK, "lxd: copied ", true, true},
		{"uninstall: installed → none", func() error { return env.uninstall(false, false) }, "", ServiceNotInstalled, "lxd: removed copy ", false, false},
		{"copy: none → copy only", env.copyOnly, "", ServiceCopyOnly, "lxd: wrote sidecar", false, true},
		{"uninstall: copy only → none", func() error { return env.uninstall(false, false) }, "", ServiceNotInstalled, "lxd: removed copy ", false, false},
	} {
		out.Reset()
		if step.newCore != "" {
			callerSHA = setSource(t, env, step.newCore)
		}
		if step.action != nil {
			if err := step.action(); err != nil {
				t.Fatalf("%s: %v\n%s", step.name, err, out.String())
			}
		}
		actionOutput := out.String()
		verdict, err := env.status(callerSHA)
		if err != nil {
			t.Fatalf("%s: status: %v", step.name, err)
		}
		if verdict != step.wantVerdict {
			t.Fatalf("%s: verdict %s (exit %d), want %s:\n%s", step.name, verdict, verdict.ExitCode(), step.wantVerdict, out.String())
		}
		if !strings.Contains(squash(out.String()), step.wantOutput) {
			t.Fatalf("%s: missing %q in:\n%s", step.name, step.wantOutput, out.String())
		}
		if exists(env.system.plist) != step.wantPlist || exists(copyPath) != step.wantCopy {
			t.Fatalf("%s: plist=%v copy=%v, want %v/%v\n%s", step.name, exists(env.system.plist), exists(copyPath), step.wantPlist, step.wantCopy, actionOutput)
		}
		if marker, found, _ := readInstallMarker(env.execDir); found {
			wantPlist := ""
			if step.wantPlist {
				wantPlist = env.system.plist
			}
			copySHA, _ := sha256File(copyPath, maxExecutableSize)
			if marker.PlistPath != wantPlist || marker.Label != launchdLabel || marker.SHA256 != copySHA {
				t.Fatalf("%s: sidecar %+v, copy sha256 %s", step.name, marker, copySHA)
			}
		} else if step.wantCopy {
			t.Fatalf("%s: the copy has no sidecar", step.name)
		}
	}
	// Uninstall removed our label directory, never its parent.
	if exists(env.execDir) || !exists(filepath.Join(base, "PrivilegedHelperTools")) {
		t.Fatal("uninstall must remove the label directory and nothing above it")
	}
}

// TestServiceStatusAnomalies: every state outside the machine is reported
// with a reason and exit code 2.
func TestServiceStatusAnomalies(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		arrange     func(t *testing.T, env serviceEnv, base string)
		wantVerdict ServiceVerdict
		wantReason  string
	}{
		{"sidecar without its copy", func(t *testing.T, env serviceEnv, base string) {
			mustDo(t, env.copyOnly())
			mustDo(t, os.Remove(filepath.Join(env.execDir, "sing-box")))
		}, ServiceMismatch, "is missing but its sidecar remains"},
		{"copy altered behind the sidecar", func(t *testing.T, env serviceEnv, base string) {
			mustDo(t, env.installSystem([]string{"lxd"}))
			mustDo(t, os.WriteFile(filepath.Join(env.execDir, "sing-box"), []byte("tampered"), 0o755))
		}, ServiceMismatch, "differs from its sidecar"},
		{"copy bound to a vanished plist", func(t *testing.T, env serviceEnv, base string) {
			mustDo(t, env.installSystem([]string{"lxd"}))
			mustDo(t, os.Remove(env.system.plist))
		}, ServiceMismatch, "which is absent"},
		{"plist runs a root-owned binary that is not a copy", func(t *testing.T, env serviceEnv, base string) {
			other := filepath.Join(base, "PrivilegedHelperTools", "sing-box")
			mustDo(t, os.WriteFile(other, []byte("core v1"), 0o755))
			mustDo(t, os.WriteFile(env.system.plist, []byte(buildPlist(launchdLabel, []string{other, "lxd"}, env.system.logPath)), 0o644))
		}, ServiceMismatch, "no sidecar next to"},
		{"plist runs the user-writable bundle binary", func(t *testing.T, env serviceEnv, base string) {
			bundle := filepath.Join(base, "bundle")
			useOwnerLstat(t, func(path string) (ownerInfo, error) {
				info, err := rootOwnsEverything(path)
				if err == nil && strings.HasPrefix(path, bundle) {
					info.uid = 501
				}
				return info, err
			})
			mustDo(t, os.WriteFile(env.system.plist, []byte(buildPlist(launchdLabel, []string{env.source, "lxd"}, env.system.logPath)), 0o644))
		}, ServiceUnsafe, "owned by uid 501"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env, out, base := testServiceEnv(t)
			callerSHA := setSource(t, env, "core v1")
			testCase.arrange(t, env, base)
			out.Reset()
			verdict, err := env.status(callerSHA)
			if err != nil {
				t.Fatal(err)
			}
			if verdict != testCase.wantVerdict || verdict.ExitCode() != 2 {
				t.Fatalf("verdict %s exit %d, want %s exit 2:\n%s", verdict, verdict.ExitCode(), testCase.wantVerdict, out.String())
			}
			if !strings.Contains(out.String(), "lxd: verdict: "+testCase.wantVerdict.String()+" — ") || !strings.Contains(out.String(), testCase.wantReason) {
				t.Fatalf("verdict line must carry %q:\n%s", testCase.wantReason, out.String())
			}
		})
	}
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestServiceStatusUserAgent(t *testing.T) {
	env, out, _ := testServiceEnv(t)
	callerSHA := setSource(t, env, "core v1")
	mustDo(t, os.WriteFile(env.user.plist, []byte(buildPlist(launchdLabel, []string{env.source, "lxd"}, env.user.logPath)), 0o644))
	verdict, err := env.status(callerSHA)
	if err != nil || verdict != ServiceOK {
		t.Fatalf("verdict %s err %v:\n%s", verdict, err, out.String())
	}
	text := squash(out.String())
	for _, want := range []string{"[user scope, LaunchAgent]", "not required for a per-user agent", "launchd: not loaded"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestDryRunCopyPlansOnlyTheCopy(t *testing.T) {
	useOwnerLstat(t, rootOwnsEverything)
	execDir := filepath.Join(realTempDir(t), launchdLabel)
	var out bytes.Buffer
	env := serviceEnv{out: &out, execDir: execDir}
	mustDo(t, env.planCopy())
	text := out.String()
	for _, want := range []string{"lxd: dry run — nothing was copied.", "lxd: would copy ", "lxd: would write sidecar", "no plist written, launchd untouched"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<plist") {
		t.Fatal("copy must not plan a plist")
	}
	if _, err := os.Lstat(execDir); !os.IsNotExist(err) {
		t.Fatal("dry run must not create the exec dir")
	}
}
