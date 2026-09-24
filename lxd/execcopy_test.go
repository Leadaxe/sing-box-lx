//go:build with_lxd && unix

package lxd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyFixture lays out a source binary and an empty exec dir.
func copyFixture(t *testing.T, content []byte) (source, dir string) {
	t.Helper()
	root := t.TempDir()
	source = filepath.Join(root, "bundle-sing-box")
	if err := os.WriteFile(source, content, 0o755); err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(root, "PrivilegedHelperTools")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return source, dir
}

// assertNoTempFiles: a failed or finished copy must not leave its temporary
// file behind in the root-owned directory.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	if len(leftovers) > 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

func TestCopyInstallsVerifiedCopy(t *testing.T) {
	content := []byte("mach-o bytes with an embedded signature")
	source, dir := copyFixture(t, content)
	var out bytes.Buffer

	result, err := installExecCopy(&out, source, dir, execCopyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, execCopyName)
	if result.Target != target || result.Skipped || result.SHA256 != shaOf(content) {
		t.Fatalf("unexpected result %+v", result)
	}
	copied, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(copied, content) {
		t.Fatalf("copy content differs: %v", err)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("copy mode = %s, want 0755", formatMode(info.Mode()))
	}
	assertNoTempFiles(t, dir)
	if want := "lxd: copied " + source + " -> " + target + " (sha256 " + shaOf(content); !strings.Contains(out.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, out.String())
	}
}

func TestCopyRefusesSymlinkAndNonRegularSource(t *testing.T) {
	source, dir := copyFixture(t, []byte("binary"))
	link := filepath.Join(filepath.Dir(source), "link-to-sing-box")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := installExecCopy(&out, link, dir, execCopyOptions{}); err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Fatalf("a symlink source must be refused, got %v", err)
	}
	if _, err := installExecCopy(&out, filepath.Dir(source), dir, execCopyOptions{}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a directory source must be refused, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, execCopyName)); !os.IsNotExist(err) {
		t.Fatal("a refused source must not produce a copy")
	}
}

func TestCopyShaMismatchDiscardsTemp(t *testing.T) {
	source, dir := copyFixture(t, []byte("new binary"))
	target := filepath.Join(dir, execCopyName)
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err := installExecCopy(&out, source, dir, execCopyOptions{
		beforeVerify: func(tempPath string) error {
			file, openErr := os.OpenFile(tempPath, os.O_WRONLY|os.O_APPEND, 0)
			if openErr != nil {
				return openErr
			}
			_, _ = file.Write([]byte("corruption"))
			return file.Close()
		},
	})
	if err == nil || !strings.Contains(err.Error(), "copy verification failed") {
		t.Fatalf("a corrupted copy must be refused, got %v", err)
	}
	assertNoTempFiles(t, dir)
	if kept, _ := os.ReadFile(target); string(kept) != "old binary" {
		t.Fatalf("the previous copy must stay untouched, got %q", kept)
	}
}

func TestCopyUnchangedSkipped(t *testing.T) {
	content := []byte("same binary")
	source, dir := copyFixture(t, content)
	target := filepath.Join(dir, execCopyName)
	if err := os.WriteFile(target, content, 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(target)
	var out bytes.Buffer

	result, err := installExecCopy(&out, source, dir, execCopyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped {
		t.Fatal("an identical copy must be skipped")
	}
	if want := "lxd: binary unchanged (sha256 " + shaOf(content) + "), copy skipped"; !strings.Contains(out.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, out.String())
	}
	after, _ := os.Stat(target)
	if !os.SameFile(before, after) {
		t.Fatal("a skipped copy must keep the same inode")
	}
}

func TestCopySourceIsTargetSkipped(t *testing.T) {
	_, dir := copyFixture(t, nil)
	target := filepath.Join(dir, execCopyName)
	if err := os.WriteFile(target, []byte("installed copy"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	result, err := installExecCopy(&out, target, dir, execCopyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || !strings.Contains(out.String(), "installing from the installed copy itself") {
		t.Fatalf("install from the copy must skip, got %+v:\n%s", result, out.String())
	}
}

// TestCopyReplacesByRename: the old inode must survive the replacement — a
// running daemon keeps executing it, and macOS kills a process whose signed
// pages are rewritten in place.
func TestCopyReplacesByRename(t *testing.T) {
	source, dir := copyFixture(t, []byte("version two"))
	target := filepath.Join(dir, execCopyName)
	if err := os.WriteFile(target, []byte("version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	running, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()

	var out bytes.Buffer
	if _, err = installExecCopy(&out, source, dir, execCopyOptions{}); err != nil {
		t.Fatal(err)
	}
	if replaced, _ := os.ReadFile(target); string(replaced) != "version two" {
		t.Fatalf("target must hold the new binary, got %q", replaced)
	}
	if old, _ := io.ReadAll(running); string(old) != "version one" {
		t.Fatalf("the old inode must be intact, got %q", old)
	}
	if !strings.Contains(out.String(), "will be replaced") {
		t.Fatalf("replacement must be announced:\n%s", out.String())
	}
}

func TestCopyDryRunWritesNothing(t *testing.T) {
	source, dir := copyFixture(t, []byte("binary"))
	var out bytes.Buffer
	if _, err := installExecCopy(&out, source, dir, execCopyOptions{dryRun: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "lxd: would copy "+source+" -> "+filepath.Join(dir, execCopyName)) {
		t.Fatalf("dry run must print the plan:\n%s", out.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("dry run wrote %d entries", len(entries))
	}
}

func TestSidecarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, found, err := readInstallMarker(dir); err != nil || found {
		t.Fatalf("absent sidecar must read as not found, got %v %v", found, err)
	}
	marker := installMarker{
		Source:      "/Applications/singbox-launcher.app/Contents/MacOS/bin/sing-box",
		SHA256:      shaOf([]byte("x")),
		Version:     "1.14.1-lx.11",
		InstalledAt: "2026-09-24T12:00:00Z",
		PlistPath:   "/Library/LaunchDaemons/com.leadaxe.sing-box-lxd.plist",
		Label:       launchdLabel,
	}
	if err := writeInstallMarker(dir, marker, false); err != nil {
		t.Fatal(err)
	}
	got, found, err := readInstallMarker(dir)
	if err != nil || !found || got != marker {
		t.Fatalf("round trip: got %+v found=%v err=%v", got, found, err)
	}
	info, _ := os.Stat(filepath.Join(dir, installMarkerName))
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("sidecar mode = %s, want 0644 (readable without root)", formatMode(info.Mode()))
	}
	// The launcher reads these exact keys.
	raw, _ := os.ReadFile(filepath.Join(dir, installMarkerName))
	var keys map[string]any
	if err = json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"source", "sha256", "version", "installed_at", "plist_path", "label"} {
		if _, present := keys[key]; !present {
			t.Fatalf("sidecar lacks %q: %s", key, raw)
		}
	}
	assertNoTempFiles(t, dir)
}

func TestSidecarUninstallDecisions(t *testing.T) {
	const plist = "/Library/LaunchDaemons/com.leadaxe.sing-box-lxd.plist"
	content := []byte("installed binary")
	for _, testCase := range []struct {
		name        string
		fileContent []byte // nil = no copy
		marker      *installMarker
		dryRun      bool
		wantRemoved bool
		wantMarker  bool // sidecar still present afterwards
		wantOutput  string
	}{
		{"matching copy is removed", content, &installMarker{SHA256: shaOf(content), PlistPath: plist, Label: launchdLabel}, false, true, false, "lxd: removed copy "},
		{"sha differs stays", []byte("replaced by someone"), &installMarker{SHA256: shaOf(content), PlistPath: plist, Label: launchdLabel}, false, false, true, "lxd: copy left in place: sha differs from sidecar"},
		{"copy-only sidecar is removed without a plist", content, &installMarker{SHA256: shaOf(content), Label: launchdLabel}, false, true, false, "lxd: removed copy "},
		{"foreign plist stays", content, &installMarker{SHA256: shaOf(content), PlistPath: "/Library/LaunchDaemons/other.plist", Label: "other"}, false, false, true, "lxd: copy left in place: sidecar"},
		{"no sidecar stays", content, nil, false, false, false, "has no sidecar"},
		{"stale sidecar is removed", nil, &installMarker{SHA256: shaOf(content), PlistPath: plist, Label: launchdLabel}, false, false, false, "lxd: removed stale sidecar"},
		{"dry run removes nothing", content, &installMarker{SHA256: shaOf(content), PlistPath: plist, Label: launchdLabel}, true, false, true, "lxd: would remove copy "},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "PrivilegedHelperTools")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, execCopyName)
			if testCase.fileContent != nil {
				if err := os.WriteFile(target, testCase.fileContent, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if testCase.marker != nil {
				if err := writeInstallMarker(dir, *testCase.marker, false); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if err := removeInstalledCopy(&out, dir, launchdLabel, plist, testCase.dryRun); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), testCase.wantOutput) {
				t.Fatalf("missing %q in:\n%s", testCase.wantOutput, out.String())
			}
			_, statErr := os.Lstat(target)
			if removed := os.IsNotExist(statErr); testCase.fileContent != nil && removed != testCase.wantRemoved {
				t.Fatalf("copy removed = %v, want %v", removed, testCase.wantRemoved)
			}
			if _, markerErr := os.Lstat(filepath.Join(dir, installMarkerName)); (markerErr == nil) != testCase.wantMarker {
				t.Fatalf("sidecar present = %v, want %v", markerErr == nil, testCase.wantMarker)
			}
			// The exec dir belongs to macOS (or the operator): never removed.
			if _, dirErr := os.Lstat(dir); dirErr != nil {
				t.Fatal("uninstall must not remove the exec dir")
			}
		})
	}
}

// TestSidecarKeepCopyDecisions: --keep-copy unbinds only our own, intact,
// plist-bound copy; an already unbound copy is a no-op; everything else is
// left exactly as it was.
func TestSidecarKeepCopyDecisions(t *testing.T) {
	const plist = "/Library/LaunchDaemons/com.leadaxe.sing-box-lxd.plist"
	content := []byte("installed binary")
	bound := installMarker{SHA256: shaOf(content), InstalledAt: "2026-09-24T00:00:00Z", PlistPath: plist, Label: launchdLabel}
	unbound := bound
	unbound.PlistPath = ""
	for _, testCase := range []struct {
		name        string
		fileContent []byte
		marker      installMarker
		dryRun      bool
		canWrite    bool
		wantPlist   string // sidecar plist_path afterwards
		wantOutput  string
	}{
		{"bound copy becomes copy only", content, bound, false, true, "", "lxd: copy kept for non-service use: "},
		{"already copy only is a no-op", content, unbound, false, true, "", "lxd: copy kept for non-service use: "},
		{"no-op needs no root", content, unbound, false, false, "", "; remove with --service=uninstall without --keep-copy"},
		{"unbinding without root reports", content, bound, false, false, plist, "needs root"},
		{"dry run rewrites nothing", content, bound, true, true, plist, "lxd: would keep the copy for non-service use: "},
		{"sha differs stays bound", []byte("replaced"), bound, false, true, plist, "lxd: copy left in place: sha differs from sidecar"},
		{"foreign plist stays", content, installMarker{SHA256: shaOf(content), PlistPath: "/other.plist", Label: launchdLabel}, false, true, "/other.plist", "belongs to"},
		{"sidecar without copy stays", nil, bound, false, true, plist, "lxd: nothing to keep: the copy "},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "PrivilegedHelperTools")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, execCopyName)
			if testCase.fileContent != nil {
				if err := os.WriteFile(target, testCase.fileContent, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeInstallMarker(dir, testCase.marker, false); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := unbindInstalledCopy(&out, dir, launchdLabel, plist, testCase.dryRun, testCase.canWrite, false); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), testCase.wantOutput) {
				t.Fatalf("missing %q in:\n%s", testCase.wantOutput, out.String())
			}
			marker, found, err := readInstallMarker(dir)
			if err != nil || !found {
				t.Fatalf("the sidecar must stay: found=%v err=%v", found, err)
			}
			if marker.PlistPath != testCase.wantPlist || marker.SHA256 != testCase.marker.SHA256 || marker.InstalledAt != testCase.marker.InstalledAt {
				t.Fatalf("sidecar %+v, want plist_path %q and the rest unchanged", marker, testCase.wantPlist)
			}
			if testCase.fileContent != nil {
				if kept, _ := os.ReadFile(target); !bytes.Equal(kept, testCase.fileContent) {
					t.Fatal("--keep-copy must never touch the copy")
				}
			}
		})
	}
}

// TestCopyLegacyLayoutDirectory: a directory where the flat copy belongs is
// what an early pre-release left behind (<label>/sing-box). Install and copy
// refuse with the remedy; uninstall and --keep-copy leave it alone; nothing
// ever deletes it on its own.
func TestCopyLegacyLayoutDirectory(t *testing.T) {
	source, dir := copyFixture(t, []byte("binary"))
	legacy := filepath.Join(dir, execCopyName)
	if err := os.MkdirAll(filepath.Join(legacy, "sing-box"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for _, dryRun := range []bool{true, false} {
		_, err := installExecCopy(&out, source, dir, execCopyOptions{dryRun: dryRun})
		if want := "target is a directory (legacy layout); remove it: sudo rm -rf " + legacy; err == nil || err.Error() != want {
			t.Fatalf("dryRun=%v: got %v, want %q", dryRun, err, want)
		}
	}
	mustRemain := func() {
		t.Helper()
		if info, err := os.Lstat(filepath.Join(legacy, "sing-box")); err != nil || !info.IsDir() {
			t.Fatal("the legacy directory must stay untouched")
		}
	}
	mustRemain()
	if err := writeInstallMarker(dir, installMarker{SHA256: "x", Label: launchdLabel}, false); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := removeInstalledCopy(&out, dir, launchdLabel, "/p.plist", false); err != nil {
		t.Fatal(err)
	}
	if err := unbindInstalledCopy(&out, dir, launchdLabel, "/p.plist", false, true, false); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "is a directory (legacy layout), left in place; remove it: sudo rm -rf "+legacy) != 2 {
		t.Fatalf("uninstall and --keep-copy must name the legacy directory:\n%s", out.String())
	}
	mustRemain()
}

// TestSidecarGoldenJSON: the macOS sidecar is the launcher's contract (SPEC
// 100 §2.3) and stays byte for byte what it was before the Windows set got
// its own (SPEC 103 §2.5).
func TestSidecarGoldenJSON(t *testing.T) {
	dir := t.TempDir()
	marker := installMarker{
		Source:      "/Applications/singbox-launcher.app/Contents/MacOS/bin/sing-box",
		SHA256:      "0f1e",
		Version:     "1.14.1-lx.12",
		InstalledAt: "2026-09-24T12:00:00Z",
		PlistPath:   "/Library/LaunchDaemons/com.leadaxe.sing-box-lxd.plist",
		Label:       launchdLabel,
	}
	if err := writeInstallMarker(dir, marker, false); err != nil {
		t.Fatal(err)
	}
	const golden = `{
  "source": "/Applications/singbox-launcher.app/Contents/MacOS/bin/sing-box",
  "sha256": "0f1e",
  "version": "1.14.1-lx.12",
  "installed_at": "2026-09-24T12:00:00Z",
  "plist_path": "/Library/LaunchDaemons/com.leadaxe.sing-box-lxd.plist",
  "label": "com.leadaxe.sing-box-lxd"
}
`
	raw, err := os.ReadFile(filepath.Join(dir, "sing-box-lxd.install.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != golden {
		t.Fatalf("the macOS sidecar changed:\n%s\nwant:\n%s", raw, golden)
	}
	if execCopyName != "sing-box-lxd" || installMarkerName != "sing-box-lxd.install.json" {
		t.Fatalf("the macOS names changed: %s %s", execCopyName, installMarkerName)
	}
}
