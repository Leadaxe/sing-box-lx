//go:build with_lxd

package lxd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shaOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// copyFixture lays out a source binary and an empty exec dir.
func copyFixture(t *testing.T, content []byte) (source, dir string) {
	t.Helper()
	root := t.TempDir()
	source = filepath.Join(root, "bundle-sing-box")
	if err := os.WriteFile(source, content, 0o755); err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(root, launchdLabel)
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
	target := filepath.Join(dir, "sing-box")
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
	if _, err := os.Lstat(filepath.Join(dir, "sing-box")); !os.IsNotExist(err) {
		t.Fatal("a refused source must not produce a copy")
	}
}

func TestCopyShaMismatchDiscardsTemp(t *testing.T) {
	source, dir := copyFixture(t, []byte("new binary"))
	target := filepath.Join(dir, "sing-box")
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
	target := filepath.Join(dir, "sing-box")
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
	target := filepath.Join(dir, "sing-box")
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
	target := filepath.Join(dir, "sing-box")
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
	if !strings.Contains(out.String(), "lxd: would copy "+source+" -> "+filepath.Join(dir, "sing-box")) {
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
	info, _ := os.Stat(filepath.Join(dir, "install.json"))
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("sidecar mode = %s, want 0644 (readable without root)", formatMode(info.Mode()))
	}
	// The launcher reads these exact keys.
	raw, _ := os.ReadFile(filepath.Join(dir, "install.json"))
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
		{"no sidecar stays", content, nil, false, false, false, "lxd: copy left in place: no sidecar"},
		{"stale sidecar is removed", nil, &installMarker{SHA256: shaOf(content), PlistPath: plist, Label: launchdLabel}, false, false, false, "lxd: removed stale sidecar"},
		{"dry run removes nothing", content, &installMarker{SHA256: shaOf(content), PlistPath: plist, Label: launchdLabel}, true, false, true, "lxd: would remove copy "},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), launchdLabel)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "sing-box")
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
			if _, markerErr := os.Lstat(filepath.Join(dir, "install.json")); (markerErr == nil) != testCase.wantMarker {
				t.Fatalf("sidecar present = %v, want %v", markerErr == nil, testCase.wantMarker)
			}
			if testCase.wantRemoved {
				if _, dirErr := os.Lstat(dir); !os.IsNotExist(dirErr) {
					t.Fatal("the emptied label directory must be removed")
				}
			}
		})
	}
}
