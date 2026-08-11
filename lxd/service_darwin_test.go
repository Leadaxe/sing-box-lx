//go:build with_lx_command && darwin

package lxd

import (
	"os"
	"path/filepath"
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
