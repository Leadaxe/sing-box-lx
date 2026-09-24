//go:build with_lxd

package lxd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func sumOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// setFixture lays out a launcher bin dir with sing-box and, when library is
// not empty, libcronet.dll beside it, plus an empty copy dir.
func setFixture(t *testing.T, binary, library string) (source, copyDir string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	copyDir = filepath.Join(root, "sing-box-lxd")
	for _, dir := range []string{bin, copyDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	source = filepath.Join(bin, "sing-box.exe")
	writeFile(t, source, binary)
	if library != "" {
		writeFile(t, filepath.Join(bin, cronetLibraryName), library)
	}
	return source, copyDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readString(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func mustSourceSet(t *testing.T, source string) []setMember {
	t.Helper()
	members, err := sourceSet(source, []string{cronetLibraryName})
	if err != nil {
		t.Fatal(err)
	}
	return members
}

// place runs a placement and commits it with a sidecar for service, the way
// install and copy do.
func place(t *testing.T, out *bytes.Buffer, source, dir, service string) *setPlacement {
	t.Helper()
	members := mustSourceSet(t, source)
	previous, _, _ := readInstallSetMarker(dir)
	var previousNames []string
	for _, file := range previous.Files {
		previousNames = append(previousNames, file.Name)
	}
	placement, err := placeBinarySet(out, dir, members, setPlaceOptions{previous: previousNames})
	if err != nil {
		t.Fatal(err)
	}
	if err = writeInstallSetMarker(dir, installSetMarker{Source: source, Version: "1.14.2-lx.2", Service: service, Files: setFiles(members)}, nil); err != nil {
		t.Fatal(err)
	}
	placement.commit()
	return placement
}

func TestSetSidecarRoundTripAndKeys(t *testing.T) {
	dir := t.TempDir()
	if _, found, err := readInstallSetMarker(dir); found || err != nil {
		t.Fatalf("absent sidecar: found=%v err=%v", found, err)
	}
	marker := installSetMarker{
		Source:      `C:\Users\u\AppData\Local\singbox-launcher\bin\sing-box.exe`,
		Version:     "1.14.2-lx.2",
		InstalledAt: "2026-09-24T12:00:00Z",
		Service:     "sing-box-lxd",
		Files:       []installSetFile{{Name: "sing-box-lxd.exe", SHA256: sumOf("a")}, {Name: cronetLibraryName, SHA256: sumOf("b")}},
		Warnings:    []installWarning{{Code: warningStateDirForeign, Text: "WARN: x"}},
	}
	if err := writeInstallSetMarker(dir, marker, nil); err != nil {
		t.Fatal(err)
	}
	got, found, err := readInstallSetMarker(dir)
	if err != nil || !found || !sameSet(got.Files, marker.Files) || got.Service != marker.Service || !slices.Equal(got.Warnings, marker.Warnings) {
		t.Fatalf("round trip: %+v %v %v", got, found, err)
	}
	// The launcher reads these exact keys (SPEC 103 §2.5); no macOS keys.
	var raw map[string]any
	if err = json.Unmarshal([]byte(readString(t, installMarkerPath(dir))), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"source", "version", "installed_at", "service", "files", "warnings"} {
		if _, present := raw[key]; !present {
			t.Fatalf("sidecar lacks %q: %v", key, raw)
		}
	}
	for _, key := range []string{"sha256", "plist_path", "label"} {
		if _, present := raw[key]; present {
			t.Fatalf("the Windows sidecar must not carry the macOS key %q", key)
		}
	}
	file := raw["files"].([]any)[1].(map[string]any)
	if file["name"] != cronetLibraryName || file["sha256"] != sumOf("b") {
		t.Fatalf("files[] entry: %v", file)
	}
	// No warnings: the field is absent, which the launcher reads as none.
	marker.Warnings = nil
	if err = writeInstallSetMarker(dir, marker, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readString(t, installMarkerPath(dir)), "warnings") {
		t.Fatal("an empty warnings list must be omitted")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".*.tmp-*")); len(leftovers) > 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}
}

func TestSetSourceMembers(t *testing.T) {
	source, _ := setFixture(t, "core", "cronet")
	members := mustSourceSet(t, source)
	if len(members) != 2 || members[0].Name != execCopyName || members[1].Name != cronetLibraryName || members[1].SHA256 != sumOf("cronet") {
		t.Fatalf("members %+v", members)
	}
	alone, _ := setFixture(t, "core", "")
	if members = mustSourceSet(t, alone); len(members) != 1 {
		t.Fatalf("without the library the set is the binary alone: %+v", members)
	}
	if describeSet(setFiles(mustSourceSet(t, source))) != "sha256 "+sumOf("core")+", libcronet.dll "+sumOf("cronet") {
		t.Fatal(describeSet(setFiles(mustSourceSet(t, source))))
	}
}

func TestSetPlacementAndIdempotency(t *testing.T) {
	source, dir := setFixture(t, "core v1", "cronet v1")
	var out bytes.Buffer
	placement := place(t, &out, source, dir, "sing-box-lxd")
	if placement.unchanged || readString(t, filepath.Join(dir, execCopyName)) != "core v1" || readString(t, filepath.Join(dir, cronetLibraryName)) != "cronet v1" {
		t.Fatalf("first placement:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "lxd: copied "+source+" -> "+filepath.Join(dir, execCopyName)+" (sha256 "+sumOf("core v1")) {
		t.Fatalf("missing the copied line:\n%s", out.String())
	}
	// The same set again: nothing is touched.
	out.Reset()
	before, _ := os.Stat(filepath.Join(dir, execCopyName))
	placement = place(t, &out, source, dir, "sing-box-lxd")
	after, _ := os.Stat(filepath.Join(dir, execCopyName))
	want := "lxd: binary set unchanged (sha256 " + sumOf("core v1") + ", libcronet.dll " + sumOf("cronet v1") + "), copy skipped"
	if !placement.unchanged || !strings.Contains(out.String(), want) || !os.SameFile(before, after) {
		t.Fatalf("an unchanged set must be skipped:\n%s", out.String())
	}
	// A core update replaces the binary only; no .old stays behind.
	writeFile(t, source, "core v2")
	out.Reset()
	place(t, &out, source, dir, "sing-box-lxd")
	if readString(t, filepath.Join(dir, execCopyName)) != "core v2" || !strings.Contains(out.String(), "previous copy") {
		t.Fatalf("update:\n%s", out.String())
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{cronetLibraryName, execCopyName, installMarkerName}) {
		t.Fatalf("copy dir holds %v", names)
	}
}

func TestSetDroppedLibraryAndLeftovers(t *testing.T) {
	source, dir := setFixture(t, "core", "cronet")
	var out bytes.Buffer
	place(t, &out, source, dir, "")
	// A previous run left a parked image and a temporary file.
	writeFile(t, filepath.Join(dir, execCopyName+".old"), "old core")
	writeFile(t, filepath.Join(dir, "."+cronetLibraryName+".tmp-0011"), "partial")
	// The next build ships without the library: it leaves the set.
	if err := os.Remove(filepath.Join(filepath.Dir(source), cronetLibraryName)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	place(t, &out, source, dir, "")
	if names := dirNames(t, dir); !slices.Equal(names, []string{execCopyName, installMarkerName}) {
		t.Fatalf("copy dir holds %v:\n%s", names, out.String())
	}
	if !strings.Contains(out.String(), "(no longer part of the set)") {
		t.Fatalf("the dropped library must be announced:\n%s", out.String())
	}
}

func TestSetUnknownFileRefuses(t *testing.T) {
	source, dir := setFixture(t, "core", "")
	stranger := filepath.Join(dir, "version.dll")
	writeFile(t, stranger, "planted")
	var out bytes.Buffer
	_, err := placeBinarySet(&out, dir, mustSourceSet(t, source), setPlaceOptions{})
	if err == nil || err.Error() != stranger+": unknown file in the copy directory; remove it: Remove-Item "+stranger {
		t.Fatalf("an unknown file must refuse, got %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(dir, execCopyName)); !os.IsNotExist(statErr) {
		t.Fatal("nothing may be copied next to an unknown file")
	}
}

func TestSetVerifyFailureAndRollback(t *testing.T) {
	source, dir := setFixture(t, "core v1", "")
	var out bytes.Buffer
	place(t, &out, source, dir, "sing-box-lxd")
	writeFile(t, source, "core v2")
	_, err := placeBinarySet(&out, dir, mustSourceSet(t, source), setPlaceOptions{
		beforeVerify: func(tempPath string) error { return os.WriteFile(tempPath, []byte("corrupted"), 0o600) },
	})
	if err == nil || !strings.Contains(err.Error(), "copy verification failed") {
		t.Fatalf("a corrupted copy must refuse, got %v", err)
	}
	if readString(t, filepath.Join(dir, execCopyName)) != "core v1" {
		t.Fatal("the previous copy must stay")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".*.tmp-*")); len(leftovers) > 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}
	// A swapped placement rolled back puts the previous image back.
	placement, err := placeBinarySet(&out, dir, mustSourceSet(t, source), setPlaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if readString(t, filepath.Join(dir, execCopyName)) != "core v2" || readString(t, filepath.Join(dir, execCopyName+".old")) != "core v1" {
		t.Fatal("before commit the new copy is in place and the old one parked")
	}
	if err = placement.rollback(); err != nil {
		t.Fatal(err)
	}
	if readString(t, filepath.Join(dir, execCopyName)) != "core v1" {
		t.Fatal("rollback must restore the previous image")
	}
	if _, statErr := os.Lstat(filepath.Join(dir, execCopyName+".old")); !os.IsNotExist(statErr) {
		t.Fatal("rollback must not leave the .old behind")
	}
}

func TestSetImageStillInUse(t *testing.T) {
	source, dir := setFixture(t, "core v1", "")
	var out bytes.Buffer
	place(t, &out, source, dir, "")
	writeFile(t, source, "core v2")
	inUse := errors.New("the process cannot access the file")
	placement, err := placeBinarySet(&out, dir, mustSourceSet(t, source), setPlaceOptions{
		remove: func(path string) error {
			if strings.HasSuffix(path, ".old") {
				return inUse
			}
			return os.Remove(path)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	placement.commit()
	old := filepath.Join(dir, execCopyName+".old")
	if !strings.Contains(out.String(), "lxd: "+old+" is still in use, left for the next install") {
		t.Fatalf("a running image must stay with a line:\n%s", out.String())
	}
	// The next run (the image has exited) clears it; it is not an extra file.
	out.Reset()
	place(t, &out, source, dir, "")
	if _, statErr := os.Lstat(old); !os.IsNotExist(statErr) {
		t.Fatalf("the leftover must go on the next run:\n%s", out.String())
	}
}

func TestSetUninstallDecisions(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		arrange    func(t *testing.T, dir string)
		wantGone   bool
		wantOutput string
	}{
		{"everything matches", nil, true, "lxd: removed copy "},
		{"one file differs: nothing goes", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, cronetLibraryName), "patched")
		}, false, "lxd: copy left in place: sha differs from sidecar (libcronet.dll: file "},
		{"another service's copy", func(t *testing.T, dir string) {
			marker, _, _ := readInstallSetMarker(dir)
			marker.Service = "other-service"
			if err := writeInstallSetMarker(dir, marker, nil); err != nil {
				t.Fatal(err)
			}
		}, false, "belongs to other-service, not sing-box-lxd"},
		{"no sidecar", func(t *testing.T, dir string) {
			if err := os.Remove(installMarkerPath(dir)); err != nil {
				t.Fatal(err)
			}
		}, false, "has no sidecar"},
		{"sidecar without its files is stale", func(t *testing.T, dir string) {
			for _, name := range []string{execCopyName, cronetLibraryName} {
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
		}, true, "lxd: removed stale sidecar "},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			source, dir := setFixture(t, "core", "cronet")
			var out bytes.Buffer
			place(t, &out, source, dir, "sing-box-lxd")
			writeFile(t, filepath.Join(dir, execCopyName+".old"), "parked")
			if testCase.arrange != nil {
				testCase.arrange(t, dir)
			}
			before := dirNames(t, dir)
			// Dry run first: the same decision, nothing removed.
			out.Reset()
			if err := removeInstalledSet(&out, dir, "sing-box-lxd", true); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(dirNames(t, dir), before) {
				t.Fatal("a dry run must not remove anything")
			}
			out.Reset()
			if err := removeInstalledSet(&out, dir, "sing-box-lxd", false); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), testCase.wantOutput) {
				t.Fatalf("missing %q in:\n%s", testCase.wantOutput, out.String())
			}
			left := dirNames(t, dir)
			if testCase.wantGone && len(left) != 0 {
				t.Fatalf("everything must go, left %v", left)
			}
			if !testCase.wantGone && !slices.Equal(left, before) {
				t.Fatalf("nothing may go, left %v of %v", left, before)
			}
		})
	}
}

func TestSetKeepCopyDecisions(t *testing.T) {
	source, dir := setFixture(t, "core", "")
	var out bytes.Buffer
	place(t, &out, source, dir, "sing-box-lxd")
	exe := filepath.Join(dir, execCopyName)

	// Not privileged: reported, nothing rewritten.
	if err := unbindInstalledSet(&out, dir, "sing-box-lxd", false, false, nil); err != nil {
		t.Fatal(err)
	}
	if marker, _, _ := readInstallSetMarker(dir); marker.Service != "sing-box-lxd" || !strings.Contains(out.String(), "needs administrator rights") {
		t.Fatalf("an unprivileged keep-copy must not rewrite:\n%s", out.String())
	}
	// Dry run: announced only.
	out.Reset()
	if err := unbindInstalledSet(&out, dir, "sing-box-lxd", true, true, nil); err != nil {
		t.Fatal(err)
	}
	if marker, _, _ := readInstallSetMarker(dir); marker.Service != "sing-box-lxd" || !strings.Contains(out.String(), "would keep the copy") {
		t.Fatalf("a dry run must not rewrite:\n%s", out.String())
	}
	// For real: unbound, the copy stays.
	out.Reset()
	if err := unbindInstalledSet(&out, dir, "sing-box-lxd", false, true, nil); err != nil {
		t.Fatal(err)
	}
	want := "lxd: copy kept for non-service use: " + exe + "; remove with --service=uninstall without --keep-copy"
	if marker, _, _ := readInstallSetMarker(dir); marker.Service != "" || !strings.Contains(out.String(), want) || readString(t, exe) != "core" {
		t.Fatalf("keep-copy must unbind:\n%s", out.String())
	}
	// Again: a no-op with the same line.
	out.Reset()
	before := readString(t, installMarkerPath(dir))
	if err := unbindInstalledSet(&out, dir, "sing-box-lxd", false, true, nil); err != nil {
		t.Fatal(err)
	}
	if readString(t, installMarkerPath(dir)) != before || !strings.Contains(out.String(), want) {
		t.Fatalf("an unbound copy is a no-op:\n%s", out.String())
	}
	// A copy that differs from its sidecar stays bound as it was.
	marker, _, _ := readInstallSetMarker(dir)
	marker.Service = "sing-box-lxd"
	if err := writeInstallSetMarker(dir, marker, nil); err != nil {
		t.Fatal(err)
	}
	writeFile(t, exe, "patched")
	out.Reset()
	if err := unbindInstalledSet(&out, dir, "sing-box-lxd", false, true, nil); err != nil {
		t.Fatal(err)
	}
	if marker, _, _ = readInstallSetMarker(dir); marker.Service != "sing-box-lxd" || !strings.Contains(out.String(), "sha differs from sidecar") {
		t.Fatalf("a mismatched copy must stay bound:\n%s", out.String())
	}
}

func TestSetReason(t *testing.T) {
	const hint = "fix it"
	source, dir := setFixture(t, "core", "cronet")
	var out bytes.Buffer
	place(t, &out, source, dir, "sing-box-lxd")
	caller := setFiles(mustSourceSet(t, source))
	if reason := inspectInstalledSet(dir).setReason(caller, hint); reason != "" {
		t.Fatalf("a consistent set has no reason, got %q", reason)
	}
	// The caller has no libcronet.dll: a different set.
	withoutLibrary := caller[:1]
	if reason := inspectInstalledSet(dir).setReason(withoutLibrary, hint); !strings.Contains(reason, "differs from this one") {
		t.Fatalf("a different presence of libcronet.dll is a mismatch, got %q", reason)
	}
	// A leftover is reported by status but is no mismatch.
	writeFile(t, filepath.Join(dir, execCopyName+".old"), "parked")
	set := inspectInstalledSet(dir)
	if reason := set.setReason(caller, hint); reason != "" || !slices.Equal(set.entries.leftovers, []string{execCopyName + ".old"}) {
		t.Fatalf("a leftover must not change the verdict: %q %v", reason, set.entries.leftovers)
	}
	// An extra file is.
	writeFile(t, filepath.Join(dir, "version.dll"), "planted")
	if reason := inspectInstalledSet(dir).setReason(caller, hint); !strings.Contains(reason, "extra file") {
		t.Fatalf("an extra file is a mismatch, got %q", reason)
	}
	if err := os.Remove(filepath.Join(dir, "version.dll")); err != nil {
		t.Fatal(err)
	}
	// A file that differs from the sidecar, and one that is missing.
	writeFile(t, filepath.Join(dir, cronetLibraryName), "patched")
	if reason := inspectInstalledSet(dir).setReason(caller, hint); !strings.Contains(reason, "differs from its sidecar") {
		t.Fatalf("got %q", reason)
	}
	if err := os.Remove(filepath.Join(dir, cronetLibraryName)); err != nil {
		t.Fatal(err)
	}
	if reason := inspectInstalledSet(dir).setReason(caller, hint); !strings.Contains(reason, "which is missing") {
		t.Fatalf("got %q", reason)
	}
	if err := os.Remove(installMarkerPath(dir)); err != nil {
		t.Fatal(err)
	}
	if reason := inspectInstalledSet(dir).setReason(caller, hint); !strings.Contains(reason, "no sidecar") {
		t.Fatalf("got %q", reason)
	}
}
