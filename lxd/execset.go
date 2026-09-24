//go:build with_lxd

package lxd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

// The Windows copy is a SET of files, not one binary (SPEC 103 §2.4): the
// executable plus libcronet.dll when naive's library lies beside the source
// (wintun.dll is embedded in the binary). This file is the platform-neutral
// half of it — the sidecar with files[], the placement by temp + verify +
// rename, the uninstall and --keep-copy decisions and the status facts — so
// its tests run on any CI host; service_windows.go adds the ACLs and the SCM.
// The macOS copy keeps its own one-file sidecar (execcopy.go), untouched.

// cronetLibraryName is the library that travels with the binary set.
const cronetLibraryName = "libcronet.dll"

// setMember is one file of a binary set: its name in the copy dir, the file
// it comes from and that file's hash.
type setMember struct {
	Name   string
	Source string
	SHA256 string
}

// installSetFile is one file of the set in the sidecar.
type installSetFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// installWarning is a warning of the last install/copy the operator did not
// see: the launcher runs install through runas without a console and reads
// the sidecar without privileges (SPEC 103 §2.5).
type installWarning struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// warningStateDirForeign: the data dir belonged to, or was readable by, a
// SID outside the allowlist before this install (SPEC 103 §4.2 p. 6).
const warningStateDirForeign = "state_dir_foreign_before_install"

// installSetMarker is the Windows sidecar (SPEC 103 §2.5). service binds the
// set to an SCM service ("" = a copy without a service); files is the whole
// set with the hash of each copy.
type installSetMarker struct {
	Source      string           `json:"source"`
	Version     string           `json:"version"`
	InstalledAt string           `json:"installed_at"`
	Service     string           `json:"service"`
	Files       []installSetFile `json:"files"`
	Warnings    []installWarning `json:"warnings,omitempty"`
}

// sourceSet lists the set a binary brings: the binary itself under the copy
// name, then each library lying beside it. Every member must be a regular
// file (not a link) within the size limit; each is hashed.
func sourceSet(source string, libraries []string) ([]setMember, error) {
	sum, err := hashSetSource(source)
	if err != nil {
		return nil, err
	}
	members := []setMember{{Name: execCopyName, Source: source, SHA256: sum}}
	for _, library := range libraries {
		path := filepath.Join(filepath.Dir(source), library)
		if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
			continue
		}
		sum, err = hashSetSource(path)
		if err != nil {
			return nil, err
		}
		members = append(members, setMember{Name: library, Source: path, SHA256: sum})
	}
	return members, nil
}

func hashSetSource(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", E.Cause(err, "copy source")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", E.New(path, ": is a symbolic link; the copy source must be a regular file")
	}
	if !info.Mode().IsRegular() {
		return "", E.New(path, ": not a regular file (", info.Mode().Type(), ")")
	}
	if info.Size() > maxExecutableSize {
		return "", E.New(path, ": ", info.Size(), " bytes exceeds the ", maxExecutableSize>>20, " MiB limit")
	}
	return sha256File(path, maxExecutableSize)
}

func setFiles(members []setMember) []installSetFile {
	files := make([]installSetFile, 0, len(members))
	for _, member := range members {
		files = append(files, installSetFile{Name: member.Name, SHA256: member.SHA256})
	}
	return files
}

// sameSet: the same names with the same hashes, in any order.
func sameSet(first, second []installSetFile) bool {
	if len(first) != len(second) {
		return false
	}
	for _, file := range first {
		index := slices.IndexFunc(second, func(other installSetFile) bool { return sameName(other.Name, file.Name) })
		if index < 0 || second[index].SHA256 != file.SHA256 {
			return false
		}
	}
	return true
}

// describeSet renders a set's hashes the way the output lines carry them:
// "sha256 <exe>, libcronet.dll <hex>".
func describeSet(files []installSetFile) string {
	var builder strings.Builder
	for index, file := range files {
		if index > 0 {
			builder.WriteString(", ")
		}
		if sameName(file.Name, execCopyName) {
			builder.WriteString("sha256 " + file.SHA256)
		} else {
			builder.WriteString(file.Name + " " + file.SHA256)
		}
	}
	return builder.String()
}

// setFileHash returns the hash the set gives a file name, "" if absent.
func setFileHash(files []installSetFile, name string) string {
	for _, file := range files {
		if sameName(file.Name, name) {
			return file.SHA256
		}
	}
	return ""
}

// sameName compares file names in the copy dir. The set is a Windows notion
// and NTFS names are case-insensitive.
func sameName(first, second string) bool {
	return strings.EqualFold(first, second)
}

func readInstallSetMarker(dir string) (installSetMarker, bool, error) {
	path := installMarkerPath(dir)
	content, found, err := readOptional(path)
	if err != nil || !found {
		return installSetMarker{}, false, err
	}
	var marker installSetMarker
	if err = json.Unmarshal([]byte(content), &marker); err != nil {
		return installSetMarker{}, false, E.Cause(err, "parse sidecar ", path)
	}
	return marker, true, nil
}

// writeInstallSetMarker replaces the sidecar atomically: temporary file in
// the same directory, flushed, protected (the Windows DACL), renamed.
func writeInstallSetMarker(dir string, marker installSetMarker, protect func(path string) (bool, error)) error {
	if marker.Files == nil {
		marker.Files = []installSetFile{}
	}
	encoded, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+installMarkerName+".tmp-*")
	if err != nil {
		return E.Cause(err, "create sidecar")
	}
	tempPath := temp.Name()
	if _, err = temp.Write(append(encoded, '\n')); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil && protect != nil {
		_, err = protect(tempPath)
	}
	if err == nil {
		err = os.Rename(tempPath, installMarkerPath(dir))
	}
	if err != nil {
		_ = os.Remove(tempPath)
		return E.Cause(err, "write sidecar ", installMarkerPath(dir))
	}
	return nil
}

// copyDirEntries sorts what lies in the copy dir (SPEC 103 §2.4 steps 3-4).
type copyDirEntries struct {
	members   []string // files of the current set
	marker    bool     // the sidecar
	leftovers []string // <member>.old and .<member>.tmp-<hex> of a past run
	dropped   []string // named by the previous sidecar, gone from the set
	unknown   []string // anything else, directories included
}

// classifyCopyDir sorts the entries of dir. current names the set being
// placed (or checked), previous the files of the sidecar found there.
func classifyCopyDir(dir string, current, previous []string) (copyDirEntries, error) {
	var entries copyDirEntries
	listing, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return entries, nil
		}
		return entries, E.Cause(err, "list ", dir)
	}
	known := append(append([]string{execCopyName, cronetLibraryName, installMarkerName}, current...), previous...)
	contains := func(names []string, name string) bool {
		return slices.ContainsFunc(names, func(other string) bool { return sameName(other, name) })
	}
	for _, entry := range listing {
		name := entry.Name()
		switch {
		case entry.IsDir():
			entries.unknown = append(entries.unknown, name)
		case sameName(name, installMarkerName):
			entries.marker = true
		case contains(current, name):
			entries.members = append(entries.members, name)
		case contains(previous, name):
			entries.dropped = append(entries.dropped, name)
		case isLeftover(name, known):
			entries.leftovers = append(entries.leftovers, name)
		default:
			entries.unknown = append(entries.unknown, name)
		}
	}
	return entries, nil
}

// isLeftover: <name>.old kept by a replace whose old image was still running,
// or a temporary file .<name>.tmp-<suffix> of an interrupted run.
func isLeftover(name string, known []string) bool {
	lower := strings.ToLower(name)
	for _, candidate := range known {
		candidate = strings.ToLower(candidate)
		if lower == candidate+".old" || strings.HasPrefix(lower, "."+candidate+".tmp-") {
			return true
		}
	}
	return false
}

func unknownFileError(path string) error {
	return E.New(path, ": unknown file in the copy directory; remove it: Remove-Item ", path)
}

// setPlaceOptions are the platform's hooks into the placement.
type setPlaceOptions struct {
	dryRun bool
	// previous: the files of the sidecar already in the dir.
	previous []string
	// protect gives a written file its DACL (Windows) and reports whether it
	// had to change a kept one; nil = nothing to do (tests, dry run).
	protect func(path string) (bool, error)
	// remove deletes a file; tests make it fail like an image in use.
	remove func(path string) error
	// beforeVerify runs between writing a temporary file and verifying it.
	beforeVerify func(tempPath string) error
}

// setPlacement is a placed set awaiting commit or rollback: the replaced
// files wait as <name>.old, so a failed install can put them back.
type setPlacement struct {
	out       io.Writer
	dir       string
	options   setPlaceOptions
	unchanged bool
	dropped   []string
	swaps     []setSwap
}

type setSwap struct {
	target string
	old    string // "" when the member was new
	temp   string
	placed bool
}

// placeBinarySet puts members into dir (SPEC 103 §2.4 steps 3-6): an unknown
// file refuses before anything changes; leftovers of a past run go; a member
// whose copy already has its hash is kept; every other member is written to
// a temporary file in dir (O_EXCL), flushed, protected, verified against the
// source hash, and renamed into place with the previous file parked as
// <name>.old. Never written in place: a running image cannot be opened for
// writing nor deleted, but it can be renamed. The caller commits (drops the
// .old files and the files the set lost) or rolls back.
func placeBinarySet(out io.Writer, dir string, members []setMember, options setPlaceOptions) (*setPlacement, error) {
	if options.remove == nil {
		options.remove = os.Remove
	}
	placement := &setPlacement{out: out, dir: dir, options: options}
	names := make([]string, 0, len(members))
	for _, member := range members {
		names = append(names, member.Name)
	}
	entries, err := classifyCopyDir(dir, names, options.previous)
	if err != nil {
		return nil, err
	}
	if len(entries.unknown) > 0 {
		return nil, unknownFileError(filepath.Join(dir, entries.unknown[0]))
	}
	placement.dropped = entries.dropped
	for _, leftover := range entries.leftovers {
		placement.removeLeftover(filepath.Join(dir, leftover))
	}
	var changed []setMember
	for _, member := range members {
		target := filepath.Join(dir, member.Name)
		keep, keepErr := placement.keepMember(member, target)
		if keepErr != nil {
			return nil, keepErr
		}
		if !keep {
			changed = append(changed, member)
		}
	}
	if len(changed) == 0 && len(entries.dropped) == 0 {
		placement.unchanged = true
		skipped := "copy skipped"
		if options.dryRun {
			skipped = "copy would be skipped"
		}
		fmt.Fprintf(out, "lxd: binary set unchanged (%s), %s\n", describeSet(setFiles(members)), skipped)
		return placement, nil
	}
	if options.dryRun {
		for _, member := range changed {
			fmt.Fprintf(out, "lxd: would copy %s -> %s (sha256 %s)\n", member.Source, filepath.Join(dir, member.Name), member.SHA256)
		}
		for _, name := range entries.dropped {
			fmt.Fprintln(out, "lxd: would remove", filepath.Join(dir, name), "(no longer part of the set)")
		}
		return placement, nil
	}
	for _, member := range changed {
		swap := setSwap{target: filepath.Join(dir, member.Name)}
		swap.temp, err = placement.writeTemp(member)
		if err != nil {
			placement.discardTemps()
			return nil, err
		}
		placement.swaps = append(placement.swaps, swap)
	}
	for index := range placement.swaps {
		if err = placement.swap(&placement.swaps[index]); err != nil {
			_ = placement.rollback()
			return nil, err
		}
	}
	for index, member := range changed {
		fmt.Fprintf(out, "lxd: copied %s -> %s (sha256 %s, protected DACL)\n", member.Source, placement.swaps[index].target, member.SHA256)
	}
	return placement, nil
}

// keepMember decides whether a member's copy stays: the same file as the
// source (install run from the copy itself) or the same hash. A kept file is
// re-protected when its DACL drifted.
func (placement *setPlacement) keepMember(member setMember, target string) (bool, error) {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return false, unknownFileError(target)
	}
	if !info.Mode().IsRegular() {
		fmt.Fprintf(placement.out, "lxd: %s is not a regular file (%s), it will be replaced\n", target, info.Mode().Type())
		return false, nil
	}
	sourceInfo, statErr := os.Stat(member.Source)
	same := statErr == nil && os.SameFile(sourceInfo, info)
	if !same {
		existing, hashErr := sha256File(target, maxExecutableSize)
		if hashErr != nil {
			fmt.Fprintf(placement.out, "lxd: previous copy %s is unreadable (%v), it will be replaced\n", target, hashErr)
			return false, nil
		}
		if existing != member.SHA256 {
			fmt.Fprintf(placement.out, "lxd: previous copy %s has sha256 %s, it will be replaced\n", target, existing)
			return false, nil
		}
	}
	if placement.options.protect != nil && !placement.options.dryRun {
		changed, protectErr := placement.options.protect(target)
		if protectErr != nil {
			return false, protectErr
		}
		if changed {
			fmt.Fprintln(placement.out, "lxd: replaced DACL on", target)
		}
	}
	return true, nil
}

func (placement *setPlacement) writeTemp(member setMember) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", E.Cause(err, "name temporary copy")
	}
	tempPath := filepath.Join(placement.dir, "."+member.Name+".tmp-"+hex.EncodeToString(suffix[:]))
	temp, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", E.Cause(err, "create temporary copy")
	}
	input, err := os.Open(member.Source)
	if err == nil {
		var written int64
		written, err = io.Copy(temp, io.LimitReader(input, maxExecutableSize+1))
		_ = input.Close()
		if err == nil && written > maxExecutableSize {
			err = E.New(member.Source, ": grew past the ", maxExecutableSize>>20, " MiB limit while copying")
		}
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil && placement.options.protect != nil {
		_, err = placement.options.protect(tempPath)
	}
	if err == nil && placement.options.beforeVerify != nil {
		err = placement.options.beforeVerify(tempPath)
	}
	var copySHA string
	if err == nil {
		copySHA, err = sha256File(tempPath, maxExecutableSize)
	}
	if err == nil && copySHA != member.SHA256 {
		err = E.New("copy verification failed: sha256 of ", tempPath, " is ", copySHA, ", source ", member.Source, " is ", member.SHA256, "; the copy was discarded")
	}
	if err != nil {
		_ = os.Remove(tempPath)
		return "", E.Cause(err, "write temporary copy ", tempPath)
	}
	return tempPath, nil
}

// swap parks the current file as <name>.old and renames the verified
// temporary file into place.
func (placement *setPlacement) swap(swap *setSwap) error {
	if _, err := os.Lstat(swap.target); err == nil {
		old := swap.target + ".old"
		if _, statErr := os.Lstat(old); statErr == nil {
			if err = placement.options.remove(old); err != nil {
				return E.Cause(err, old, " is still in use and blocks the replace; retry once the old image has exited")
			}
		}
		if err = os.Rename(swap.target, old); err != nil {
			return E.Cause(err, "move ", swap.target, " aside")
		}
		swap.old = old
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(swap.temp, swap.target); err != nil {
		return E.Cause(err, "move copy into place")
	}
	swap.placed = true
	syncDir(placement.dir)
	return nil
}

// commit finishes a placement: the parked .old files and the files the set
// lost are deleted — an image still running cannot be, and stays for the next
// install with a line saying so.
func (placement *setPlacement) commit() {
	if placement == nil || placement.options.dryRun {
		return
	}
	for _, swap := range placement.swaps {
		if swap.old != "" {
			placement.removeLeftover(swap.old)
		}
	}
	for _, name := range placement.dropped {
		path := filepath.Join(placement.dir, name)
		if err := placement.options.remove(path); err == nil {
			fmt.Fprintln(placement.out, "lxd: removed", path, "(no longer part of the set)")
			continue
		}
		// A library still loaded cannot be deleted but can be renamed: park
		// it as a leftover the next install removes.
		if err := os.Rename(path, path+".old"); err == nil {
			placement.removeLeftover(path + ".old")
		} else {
			fmt.Fprintf(placement.out, "lxd: %s is still in use, left for the next install\n", path)
		}
	}
}

// rollback puts the previous files back: the new copies go, the parked .old
// files return to their names. Used when the install fails after the swap.
func (placement *setPlacement) rollback() error {
	if placement == nil {
		return nil
	}
	var errs []error
	for index := len(placement.swaps) - 1; index >= 0; index-- {
		swap := placement.swaps[index]
		if swap.placed {
			if err := os.Remove(swap.target); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
				continue
			}
		}
		if swap.old != "" {
			if err := os.Rename(swap.old, swap.target); err != nil {
				errs = append(errs, err)
			}
		}
	}
	placement.discardTemps()
	placement.swaps = nil
	return E.Errors(errs...)
}

func (placement *setPlacement) discardTemps() {
	for _, swap := range placement.swaps {
		if !swap.placed && swap.temp != "" {
			_ = os.Remove(swap.temp)
		}
	}
}

func (placement *setPlacement) removeLeftover(path string) {
	if placement.options.dryRun {
		fmt.Fprintln(placement.out, "lxd: would remove", path)
		return
	}
	if err := placement.options.remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(placement.out, "lxd: %s is still in use, left for the next install\n", path)
	}
}

// installedSet is what a copy dir holds according to its sidecar.
type installedSet struct {
	dir         string
	marker      installSetMarker
	markerFound bool
	markerErr   error
	// present: the sidecar's files that exist, with the hash each has now.
	present []installSetFile
	// missing: files the sidecar names that are gone.
	missing []string
	// mismatch: the first way a present file differs from the sidecar.
	mismatch string
	// entries: the directory listing sorted against the sidecar.
	entries    copyDirEntries
	entriesErr error
}

func inspectInstalledSet(dir string) installedSet {
	set := installedSet{dir: dir}
	set.marker, set.markerFound, set.markerErr = readInstallSetMarker(dir)
	var names []string
	for _, file := range set.marker.Files {
		names = append(names, file.Name)
		path := filepath.Join(dir, file.Name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			set.missing = append(set.missing, file.Name)
			continue
		}
		switch {
		case err != nil:
			set.noteMismatch(err.Error())
		case !info.Mode().IsRegular():
			set.noteMismatch(path + " is not a regular file (" + info.Mode().Type().String() + ")")
		default:
			sum, hashErr := sha256File(path, maxExecutableSize)
			if hashErr != nil {
				set.noteMismatch(hashErr.Error())
				continue
			}
			set.present = append(set.present, installSetFile{Name: file.Name, SHA256: sum})
			if sum != file.SHA256 {
				set.noteMismatch(file.Name + ": file " + sum + ", sidecar " + file.SHA256)
			}
		}
	}
	if !set.markerFound {
		// No sidecar: judge the directory against the canonical names.
		names = []string{execCopyName}
	}
	set.entries, set.entriesErr = classifyCopyDir(dir, names, nil)
	return set
}

func (set *installedSet) noteMismatch(reason string) {
	if set.mismatch == "" {
		set.mismatch = reason
	}
}

// exeExists: the copy's executable is on disk.
func (set installedSet) exeExists() bool {
	_, err := os.Lstat(filepath.Join(set.dir, execCopyName))
	return err == nil
}

// setReason judges a set against the caller's (SPEC 103 §2.9, the MISMATCH
// column): "" when the sidecar, the files and the caller agree and nothing
// else lies in the directory. The service binding is the caller's to judge.
func (set installedSet) setReason(caller []installSetFile, hint string) string {
	switch {
	case set.markerErr != nil:
		return set.markerErr.Error() + "; " + hint
	case !set.markerFound:
		return "no sidecar next to " + filepath.Join(set.dir, execCopyName) + "; " + hint
	case set.mismatch != "":
		return "the copy differs from its sidecar (" + set.mismatch + "); " + hint
	case len(set.missing) > 0:
		return "the sidecar names " + filepath.Join(set.dir, set.missing[0]) + ", which is missing; " + hint
	case set.entriesErr != nil:
		return set.entriesErr.Error() + "; " + hint
	case len(set.entries.unknown) > 0 || len(set.entries.dropped) > 0:
		extra := append(append([]string(nil), set.entries.unknown...), set.entries.dropped...)
		return "extra file " + filepath.Join(set.dir, extra[0]) + " in the copy directory; " + hint
	case !sameSet(set.marker.Files, caller):
		return "the installed set (" + describeSet(set.marker.Files) + ", version " + set.marker.Version + ") differs from this one (" + describeSet(caller) + "); " + hint
	default:
		return ""
	}
}

// removeInstalledSet removes a copy dir's set and its sidecar ONLY when the
// sidecar is this service's (bound to it, or to no service) and every file it
// names that still exists has the hash it records (SPEC 103 §2.8): one
// difference and nothing is removed. A sidecar whose files are all gone is
// removed as stale; leftovers of past runs go with the set. The directory is
// not removed here. The returned error is a failed removal, never a decision
// to keep.
func removeInstalledSet(out io.Writer, dir, service string, dryRun bool) error {
	set := inspectInstalledSet(dir)
	markerPath := installMarkerPath(dir)
	exe := filepath.Join(dir, execCopyName)
	switch {
	case set.markerErr != nil:
		fmt.Fprintf(out, "lxd: copy left in place: %v (%s)\n", set.markerErr, dir)
		return nil
	case !set.markerFound:
		if set.exeExists() {
			fmt.Fprintf(out, "lxd: copy left in place: %s has no sidecar (%s), so it is not a copy this service installed\n", exe, markerPath)
		} else {
			fmt.Fprintln(out, "lxd: no installed copy in", dir)
		}
		return nil
	case set.marker.Service != "" && set.marker.Service != service:
		fmt.Fprintf(out, "lxd: copy left in place: sidecar %s belongs to %s, not %s\n", markerPath, set.marker.Service, service)
		return nil
	case set.mismatch != "":
		fmt.Fprintf(out, "lxd: copy left in place: sha differs from sidecar (%s): %s\n", set.mismatch, dir)
		return nil
	}
	if len(set.present) == 0 {
		if dryRun {
			fmt.Fprintf(out, "lxd: would remove stale sidecar %s: the copy is already gone\n", markerPath)
			return nil
		}
		if err := os.Remove(markerPath); err != nil {
			return E.Cause(err, "remove stale sidecar")
		}
		fmt.Fprintf(out, "lxd: removed stale sidecar %s: the copy is already gone\n", markerPath)
		removeSetLeftovers(out, dir, set)
		return nil
	}
	if dryRun {
		fmt.Fprintf(out, "lxd: would remove copy %s (%s, matches the sidecar) and sidecar %s\n", exe, describeSet(set.present), markerPath)
		return nil
	}
	for _, file := range set.present {
		if err := os.Remove(filepath.Join(dir, file.Name)); err != nil {
			return E.Cause(err, "remove copy")
		}
	}
	if err := os.Remove(markerPath); err != nil {
		return E.Cause(err, "remove sidecar")
	}
	fmt.Fprintf(out, "lxd: removed copy %s (%s, matches the sidecar) and sidecar %s\n", exe, describeSet(set.present), markerPath)
	removeSetLeftovers(out, dir, set)
	return nil
}

func removeSetLeftovers(out io.Writer, dir string, set installedSet) {
	for _, leftover := range set.entries.leftovers {
		path := filepath.Join(dir, leftover)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(out, "lxd: %s is still in use, left in place\n", path)
		}
	}
}

// unbindInstalledSet is the --keep-copy half of uninstall (SPEC 103 §2.8
// p. 3): the set and its sidecar stay, and a sidecar bound to service is
// rewritten with service "" — the copy-only state — under the same checks as
// removeInstalledSet. An unbound copy is a no-op. canWrite=false reports
// instead of rewriting.
func unbindInstalledSet(out io.Writer, dir, service string, dryRun, canWrite bool, protect func(string) (bool, error)) error {
	set := inspectInstalledSet(dir)
	markerPath := installMarkerPath(dir)
	exe := filepath.Join(dir, execCopyName)
	switch {
	case set.markerErr != nil:
		fmt.Fprintf(out, "lxd: copy left in place: %v (%s)\n", set.markerErr, dir)
		return nil
	case !set.markerFound:
		if set.exeExists() {
			fmt.Fprintf(out, "lxd: copy left in place: %s has no sidecar (%s), so it is not a copy this service installed\n", exe, markerPath)
		} else {
			fmt.Fprintln(out, "lxd: no installed copy in", dir)
		}
		return nil
	case set.marker.Service != "" && set.marker.Service != service:
		fmt.Fprintf(out, "lxd: copy left in place: sidecar %s belongs to %s, not %s\n", markerPath, set.marker.Service, service)
		return nil
	case set.mismatch != "":
		fmt.Fprintf(out, "lxd: copy left in place: sha differs from sidecar (%s): %s\n", set.mismatch, dir)
		return nil
	case !set.exeExists():
		fmt.Fprintf(out, "lxd: nothing to keep: the copy %s is gone; its sidecar %s stays for --service=uninstall\n", exe, markerPath)
		return nil
	case set.marker.Service == "":
		fmt.Fprintln(out, keepCopyMessage(exe))
		return nil
	case dryRun:
		fmt.Fprintf(out, "lxd: would keep the copy for non-service use: %s (sidecar unbound from service %s)\n", exe, set.marker.Service)
		return nil
	case !canWrite:
		fmt.Fprintf(out, "lxd: the copy %s is still bound to service %s — unbinding its sidecar needs administrator rights\n", exe, set.marker.Service)
		return nil
	}
	marker := set.marker
	marker.Service = ""
	if err := writeInstallSetMarker(dir, marker, protect); err != nil {
		return err
	}
	fmt.Fprintln(out, keepCopyMessage(exe))
	return nil
}
