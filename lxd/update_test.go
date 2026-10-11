//go:build with_lxd

package lxd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

func TestParseAndCompareLxVersion(t *testing.T) {
	order := []string{"1.14.3-lx.15", "1.14.3-lx.16-rc.1", "1.14.3-lx.16-rc.2", "1.14.3-lx.16", "1.14.3-lx.20", "1.15.0-lx.1", "v2.0.0-lx.1"}
	for i := 1; i < len(order); i++ {
		older, err := parseLxVersion(order[i-1])
		if err != nil {
			t.Fatal(err)
		}
		newer, err := parseLxVersion(order[i])
		if err != nil {
			t.Fatal(err)
		}
		if compareLxVersion(older, newer) != -1 || compareLxVersion(newer, older) != 1 || compareLxVersion(newer, newer) != 0 {
			t.Fatalf("%s must sort before %s", order[i-1], order[i])
		}
	}
	for _, bad := range []string{"unknown", "1.14.3", "v1.14.3-lx", "1.14.3-lx.1-beta.1", ""} {
		if _, err := parseLxVersion(bad); err == nil {
			t.Fatalf("%q must not parse", bad)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	sums := strings.Join([]string{
		strings.Repeat("a", 64) + "  sing-box-1.14.3-lx.16-linux-mips-softfloat.tar.gz",
		strings.Repeat("b", 64) + "  sing-box-1.14.3-lx.16-linux-mipsle-softfloat.tar.gz",
		strings.Repeat("c", 64) + " *sing-box-1.14.3-lx.16-linux-arm64.tar.gz",
		strings.Repeat("d", 64) + "  sing-box-1.14.3-lx.16-windows-amd64.zip",
		"garbage line",
		"",
	}, "\n")
	asset, err := parseChecksums(sums, "linux-mips-softfloat")
	if err != nil || asset.name != "sing-box-1.14.3-lx.16-linux-mips-softfloat.tar.gz" || asset.sha256 != strings.Repeat("a", 64) || asset.version != "1.14.3-lx.16" {
		t.Fatalf("mips: %+v %v", asset, err)
	}
	if asset, err = parseChecksums(sums, "linux-arm64"); err != nil || asset.sha256 != strings.Repeat("c", 64) {
		t.Fatalf("arm64 (with the binary-mode star): %+v %v", asset, err)
	}
	if _, err = parseChecksums(sums, "linux-amd64"); err == nil {
		t.Fatal("a missing platform must be an error")
	}
	if _, err = parseChecksums(sums, "windows-amd64"); err == nil {
		t.Fatal("only tar.gz assets are updates")
	}
}

// makeArchive builds the release .tar.gz shape: a versioned directory with the
// binary and a licence beside it. Returns the archive and its sha256.
func makeArchive(t *testing.T, binary []byte, extraBinary bool) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	write := func(name string, content []byte, mode int64) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	write("sing-box-1.14.3-lx.16-linux-arm64/LICENSE", []byte("GPL"), 0o644)
	write("sing-box-1.14.3-lx.16-linux-arm64/sing-box", binary, 0o755)
	if extraBinary {
		write("other/sing-box", []byte("impostor"), 0o755)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buffer.Bytes())
	return buffer.Bytes(), hex.EncodeToString(sum[:])
}

func TestExtractBinary(t *testing.T) {
	archive, sum := makeArchive(t, []byte("new core"), false)
	dst := filepath.Join(t.TempDir(), "sing-box.new")
	if err := extractBinary(bytes.NewReader(archive), dst, sum); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(dst)
	if err != nil || string(content) != "new core" {
		t.Fatalf("extracted %q %v", content, err)
	}
	if info, _ := os.Stat(dst); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", info.Mode())
	}
	// Wrong sum: the file is gone.
	if err := extractBinary(bytes.NewReader(archive), dst, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "MISMATCH") {
		t.Fatal("mismatch must fail, got", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("a mismatched download must not stay on disk")
	}
	// Two binaries: refused.
	archive, sum = makeArchive(t, []byte("x"), true)
	if err := extractBinary(bytes.NewReader(archive), dst, sum); err == nil {
		t.Fatal("two sing-box entries must be refused")
	}
}

// fakeUpdateHost is the injected world of one runUpdate call.
type fakeUpdateHost struct {
	t        *testing.T
	urls     map[string][]byte
	calls    []string
	running  []bool // successive answers of the "running" probe
	runFails map[string]string
	out      bytes.Buffer
}

func (h *fakeUpdateHost) env(target string) updateEnv {
	return updateEnv{
		goos: "linux", euid: 0, executable: target, version: "1.14.3-lx.15", platform: "linux-arm64", initSystem: "procd",
		out: &h.out,
		fetch: func(url string) (io.ReadCloser, error) {
			content, found := h.urls[url]
			if !found {
				return nil, E.New("404 ", url)
			}
			return io.NopCloser(bytes.NewReader(content)), nil
		},
		run: func(name string, args ...string) (string, error) {
			call := name + " " + strings.Join(args, " ")
			h.calls = append(h.calls, call)
			if msg, fails := h.runFails[call]; fails {
				return msg, E.New("exit 1")
			}
			if strings.HasSuffix(call, " running") {
				if len(h.running) == 0 {
					return "", E.New("not running")
				}
				answer := h.running[0]
				h.running = h.running[1:]
				if !answer {
					return "", E.New("not running")
				}
			}
			return "", nil
		},
		freeBytes: func(string) (uint64, error) { return 1 << 30, nil },
		sleep:     func(time.Duration) {},
	}
}

func newFakeUpdateHost(t *testing.T, binary []byte, version string) (*fakeUpdateHost, string) {
	t.Helper()
	archive, sum := makeArchive(t, binary, false)
	name := "sing-box-" + version + "-linux-arm64.tar.gz"
	host := &fakeUpdateHost{t: t, urls: map[string][]byte{
		"https://github.com/Leadaxe/sing-box-lx/releases/latest/download/SHA256SUMS":             []byte(sum + "  " + name + "\n"),
		"https://github.com/Leadaxe/sing-box-lx/releases/download/v" + version + "/" + name:      archive,
		"https://api.github.com/repos/Leadaxe/sing-box-lx/releases?per_page=1":                   []byte(`[{"tag_name":"v1.14.3-lx.17-rc.1"}]`),
		"https://github.com/Leadaxe/sing-box-lx/releases/download/v1.14.3-lx.17-rc.1/SHA256SUMS": []byte(sum + "  sing-box-1.14.3-lx.17-rc.1-linux-arm64.tar.gz\n"),
	}, runFails: map[string]string{}}
	target := filepath.Join(t.TempDir(), "sing-box")
	if err := os.WriteFile(target, []byte("old core"), 0o755); err != nil {
		t.Fatal(err)
	}
	return host, target
}

func readTarget(t *testing.T, target string) string {
	t.Helper()
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestUpdateSucceeds(t *testing.T) {
	host, target := newFakeUpdateHost(t, []byte("new core"), "1.14.3-lx.16")
	host.running = []bool{false, true, true}
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "last_good.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, err := runUpdate(host.env(target), UpdateOptions{StateDir: stateDir})
	if err != nil || code != UpdateExitOK {
		t.Fatalf("code %d err %v\n%s", code, err, host.out.String())
	}
	if readTarget(t, target) != "new core" {
		t.Fatal("the binary was not replaced")
	}
	for _, leftover := range []string{target + ".new", target + ".old"} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Fatal("leftover", leftover)
		}
	}
	want := []string{
		target + ".new version",
		target + ".new check --disable-color -c " + filepath.Join(stateDir, "last_good.json"),
		"/etc/init.d/sing-box-lxd restart",
		"/etc/init.d/sing-box-lxd running", "/etc/init.d/sing-box-lxd running", "/etc/init.d/sing-box-lxd running",
	}
	if strings.Join(host.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(host.calls, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(host.out.String(), "done: 1.14.3-lx.16 is running") {
		t.Fatal(host.out.String())
	}
}

func TestUpdateRollsBack(t *testing.T) {
	host, target := newFakeUpdateHost(t, []byte("new core"), "1.14.3-lx.16")
	host.running = nil // never comes up
	code, err := runUpdate(host.env(target), UpdateOptions{})
	if code != UpdateExitRolledBack || err == nil {
		t.Fatalf("code %d err %v", code, err)
	}
	if readTarget(t, target) != "old core" {
		t.Fatal("the old binary must be back")
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Fatal(".old must be renamed back, not left")
	}
	restarts := 0
	for _, call := range host.calls {
		if strings.HasSuffix(call, " restart") {
			restarts++
		}
	}
	if restarts != 2 {
		t.Fatalf("restart must run again after the rollback, got %d", restarts)
	}
	if !strings.Contains(host.out.String(), "skipping check") {
		t.Fatal("no state dir: check must be skipped with a line", host.out.String())
	}
}

func TestUpdateRestartFailureKeepsNewBinary(t *testing.T) {
	host, target := newFakeUpdateHost(t, []byte("new core"), "1.14.3-lx.16")
	host.runFails["systemctl restart custom"] = "Unit custom.service not found"
	env := host.env(target)
	env.initSystem = "systemd"
	code, err := runUpdate(env, UpdateOptions{Service: "custom"})
	if err != nil || code != UpdateExitOK || readTarget(t, target) != "new core" {
		t.Fatalf("code %d err %v content %q", code, err, readTarget(t, target))
	}
	if !strings.Contains(host.out.String(), "restart the service yourself") {
		t.Fatal(host.out.String())
	}
	for _, call := range host.calls {
		if strings.Contains(call, "is-active") {
			t.Fatal("no health probe after a failed restart")
		}
	}
}

func TestUpdateNoRestartAndUnknownInit(t *testing.T) {
	for _, variant := range []struct {
		name string
		init string
		opts UpdateOptions
	}{{"no-restart", "procd", UpdateOptions{NoRestart: true}}, {"unknown init", "", UpdateOptions{}}} {
		host, target := newFakeUpdateHost(t, []byte("new core"), "1.14.3-lx.16")
		env := host.env(target)
		env.initSystem = variant.init
		code, err := runUpdate(env, variant.opts)
		if err != nil || code != UpdateExitOK || readTarget(t, target) != "new core" {
			t.Fatalf("%s: code %d err %v", variant.name, code, err)
		}
		for _, call := range host.calls {
			if strings.Contains(call, "restart") || strings.Contains(call, "running") {
				t.Fatalf("%s: init must not be touched, got %s", variant.name, call)
			}
		}
	}
}

func TestUpdateDryRunUpToDateAndNewer(t *testing.T) {
	host, target := newFakeUpdateHost(t, []byte("new core"), "1.14.3-lx.16")
	code, err := runUpdate(host.env(target), UpdateOptions{DryRun: true})
	if err != nil || code != UpdateExitOK || readTarget(t, target) != "old core" || len(host.calls) != 0 {
		t.Fatalf("dry run: code %d err %v calls %v", code, err, host.calls)
	}
	if !strings.Contains(host.out.String(), "would download sing-box-1.14.3-lx.16-linux-arm64.tar.gz") {
		t.Fatal(host.out.String())
	}

	host, target = newFakeUpdateHost(t, []byte("same"), "1.14.3-lx.15")
	if code, err := runUpdate(host.env(target), UpdateOptions{}); err != nil || code != UpdateExitOK || !strings.Contains(host.out.String(), "already up to date") {
		t.Fatalf("up to date: code %d err %v\n%s", code, err, host.out.String())
	}

	host, target = newFakeUpdateHost(t, []byte("older"), "1.14.3-lx.14")
	if code, err := runUpdate(host.env(target), UpdateOptions{}); err == nil || code != UpdateExitFailed || readTarget(t, target) != "old core" {
		t.Fatalf("downgrade: code %d err %v", code, err)
	}
}

func TestUpdatePrereleaseAndGuards(t *testing.T) {
	host, target := newFakeUpdateHost(t, []byte("rc core"), "1.14.3-lx.17-rc.1")
	host.running = []bool{true, true}
	if code, err := runUpdate(host.env(target), UpdateOptions{Prerelease: true}); err != nil || code != UpdateExitOK || readTarget(t, target) != "rc core" {
		t.Fatalf("prerelease: code %d err %v\n%s", code, err, host.out.String())
	}

	host, target = newFakeUpdateHost(t, []byte("x"), "1.14.3-lx.16")
	env := host.env(target)
	env.goos = "darwin"
	if code, err := runUpdate(env, UpdateOptions{}); err == nil || code != UpdateExitFailed {
		t.Fatal("non-linux must refuse")
	}
	env = host.env(target)
	env.euid = 501
	if code, err := runUpdate(env, UpdateOptions{}); err == nil || code != UpdateExitFailed {
		t.Fatal("non-root must refuse")
	}
	env = host.env(target)
	env.freeBytes = func(string) (uint64, error) { return 1, nil }
	if code, err := runUpdate(env, UpdateOptions{}); err == nil || code != UpdateExitFailed || !strings.Contains(err.Error(), "KiB free") {
		t.Fatalf("no space: %d %v", code, err)
	}
	env = host.env(target)
	env.platform = "linux-amd64"
	if code, err := runUpdate(env, UpdateOptions{}); err == nil || code != UpdateExitFailed || !strings.Contains(err.Error(), "no asset") {
		t.Fatalf("missing asset: %d %v", code, err)
	}
	if readTarget(t, target) != "old core" {
		t.Fatal("guards must change nothing")
	}
}

func TestServiceCommands(t *testing.T) {
	if restart, running, ok := serviceCommands("procd", "sing-box-lxd"); !ok || restart[0] != "/etc/init.d/sing-box-lxd" || running[1] != "running" {
		t.Fatalf("procd: %v %v %v", restart, running, ok)
	}
	if restart, _, ok := serviceCommands("systemd", "x"); !ok || strings.Join(restart, " ") != "systemctl restart x" {
		t.Fatalf("systemd: %v", restart)
	}
	if _, _, ok := serviceCommands("launchd", "x"); ok {
		t.Fatal("launchd is not driven here")
	}
}
