//go:build with_lxd

package lxd

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	E "github.com/sagernet/sing/common/exceptions"
)

const (
	// execCopyName keeps the binary's own name inside the exec dir: the fork
	// ships as `sing-box`; the directory carries the service label.
	execCopyName = "sing-box"
	// installMarkerName is the sidecar beside the copy (SPEC 100 §2.3).
	installMarkerName = "install.json"
	// maxExecutableSize bounds what install copies and hashes; the release
	// binary is ~70 MB.
	maxExecutableSize = 512 << 20
)

// resolveOwnExecutable returns the running binary with symlinks resolved —
// the file actually mapped, which is what the invariant and the hashes must
// describe.
func resolveOwnExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", E.Cause(err, "locate own binary")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", E.Cause(err, "resolve own binary ", executable)
	}
	return resolved, nil
}

// sha256File hashes a file of at most limit bytes.
func sha256File(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil {
		return "", E.Cause(err, "read ", path)
	}
	if read > limit {
		return "", E.New(path, ": larger than the ", limit>>20, " MiB limit")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type execCopyOptions struct {
	// chown hands the copy to root:wheel; install runs as root, tests do not.
	chown bool
	// dryRun validates and reports, writing nothing.
	dryRun bool
	// beforeVerify runs between writing the temporary file and verifying
	// it — the corrupted-copy test hooks in here. Nil in production.
	beforeVerify func(tempPath string) error
}

type execCopyResult struct {
	Source string
	Target string
	// SHA256 is the source's hash, which is the copy's once installed.
	SHA256 string
	// Skipped: the target already was this binary (same file or same hash).
	Skipped bool
}

// installExecCopy puts source at <dir>/sing-box (SPEC 100 §2.3 step 3). The
// source must be a regular file, not a symlink. An identical target (same
// file, or same sha256) is kept. Otherwise the bytes go to a temporary file
// in dir (O_EXCL, 0600), are fsynced, handed to root:wheel 0755, verified
// against the source hash and renamed over the target. Never written in
// place: a running daemon keeps the old inode, and macOS kills a process
// whose signed pages change under it. Only bytes are copied — no xattrs,
// hence no quarantine — and nothing is re-signed: the ad-hoc signature lives
// inside the Mach-O and a re-sign would change the hash.
func installExecCopy(out io.Writer, source, dir string, options execCopyOptions) (execCopyResult, error) {
	target := filepath.Join(dir, execCopyName)
	result := execCopyResult{Source: source, Target: target}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return result, E.Cause(err, "copy source")
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		return result, E.New(source, ": is a symbolic link; the copy source must be a regular file")
	}
	if !sourceInfo.Mode().IsRegular() {
		return result, E.New(source, ": not a regular file (", sourceInfo.Mode().Type(), ")")
	}
	if sourceInfo.Size() > maxExecutableSize {
		return result, E.New(source, ": ", sourceInfo.Size(), " bytes exceeds the ", maxExecutableSize>>20, " MiB limit for an executable")
	}
	result.SHA256, err = sha256File(source, maxExecutableSize)
	if err != nil {
		return result, err
	}
	fmt.Fprintf(out, "lxd: source %s (%d bytes, sha256 %s)\n", source, sourceInfo.Size(), result.SHA256)

	skipped := "copy skipped"
	if options.dryRun {
		skipped = "copy would be skipped"
	}
	targetInfo, err := os.Lstat(target)
	switch {
	case err == nil:
		if targetInfo.IsDir() {
			return result, E.New(target, ": is a directory; refusing to replace it")
		}
		if os.SameFile(sourceInfo, targetInfo) {
			result.Skipped = true
			fmt.Fprintf(out, "lxd: installing from the installed copy itself (%s), %s\n", target, skipped)
			return result, normalizeExecCopy(out, target, options)
		}
		if targetInfo.Mode().IsRegular() {
			existing, hashErr := sha256File(target, maxExecutableSize)
			if hashErr == nil && existing == result.SHA256 {
				result.Skipped = true
				fmt.Fprintf(out, "lxd: binary unchanged (sha256 %s), %s\n", result.SHA256, skipped)
				return result, normalizeExecCopy(out, target, options)
			}
			if hashErr == nil {
				fmt.Fprintf(out, "lxd: previous copy %s has sha256 %s, it will be replaced\n", target, existing)
			} else {
				fmt.Fprintf(out, "lxd: previous copy %s is unreadable (%v), it will be replaced\n", target, hashErr)
			}
		} else {
			fmt.Fprintf(out, "lxd: %s is not a regular file (%s), it will be replaced\n", target, targetInfo.Mode().Type())
		}
	case !os.IsNotExist(err):
		return result, err
	}
	if options.dryRun {
		fmt.Fprintf(out, "lxd: would copy %s -> %s\n", source, target)
		fmt.Fprintln(out, "lxd: would write a temporary file in", dir+", fsync, chown root:wheel, chmod 0755, verify sha256, rename into place")
		return result, nil
	}
	return result, writeExecCopy(out, source, dir, target, result.SHA256, options)
}

func writeExecCopy(out io.Writer, source, dir, target, sourceSHA string, options execCopyOptions) error {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return E.Cause(err, "name temporary copy")
	}
	tempPath := filepath.Join(dir, "."+execCopyName+".tmp-"+hex.EncodeToString(suffix[:]))
	temp, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return E.Cause(err, "create temporary copy")
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	input, err := os.Open(source)
	if err == nil {
		var written int64
		written, err = io.Copy(temp, io.LimitReader(input, maxExecutableSize+1))
		_ = input.Close()
		if err == nil && written > maxExecutableSize {
			err = E.New(source, ": grew past the ", maxExecutableSize>>20, " MiB limit while copying")
		}
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return E.Cause(err, "write temporary copy ", tempPath)
	}
	if options.chown {
		if err = os.Chown(tempPath, 0, 0); err != nil {
			return E.Cause(err, "chown root:wheel ", tempPath)
		}
	}
	if err = os.Chmod(tempPath, 0o755); err != nil {
		return E.Cause(err, "chmod 0755 ", tempPath)
	}
	if options.beforeVerify != nil {
		if err = options.beforeVerify(tempPath); err != nil {
			return err
		}
	}
	copySHA, err := sha256File(tempPath, maxExecutableSize)
	if err != nil {
		return E.Cause(err, "verify temporary copy")
	}
	if copySHA != sourceSHA {
		return E.New("copy verification failed: sha256 of ", tempPath, " is ", copySHA, ", source ", source, " is ", sourceSHA, "; the copy was discarded")
	}
	if err = os.Rename(tempPath, target); err != nil {
		return E.Cause(err, "move copy into place")
	}
	committed = true
	syncDir(dir)
	ownership := "root:wheel 0755"
	if !options.chown {
		ownership = "mode 0755, owner unchanged: not root"
	}
	fmt.Fprintf(out, "lxd: copied %s -> %s (sha256 %s, %s)\n", source, target, sourceSHA, ownership)
	return nil
}

// normalizeExecCopy brings a kept copy back to root:wheel 0755. Metadata only:
// chown/chmod do not touch the signed pages a running daemon has mapped.
func normalizeExecCopy(out io.Writer, target string, options execCopyOptions) error {
	if !options.chown || options.dryRun {
		return nil
	}
	info, err := lstatOwner(target)
	if err != nil {
		return err
	}
	if info.uid == 0 && info.gid == 0 && info.mode.Perm() == 0o755 {
		return nil
	}
	if err = os.Chown(target, 0, 0); err != nil {
		return E.Cause(err, "chown root:wheel ", target)
	}
	if err = os.Chmod(target, 0o755); err != nil {
		return E.Cause(err, "chmod 0755 ", target)
	}
	fmt.Fprintf(out, "lxd: set %s to root:wheel 0755 (was uid %d gid %d, mode %s)\n", target, info.uid, info.gid, formatMode(info.mode))
	return nil
}

// syncDir makes a rename durable. Best effort: not every platform fsyncs
// directories, and the rename itself has already succeeded.
func syncDir(dir string) {
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
}

// installMarker is the sidecar beside the copy (SPEC 100 §2.3 step 4). It is
// readable without root: uninstall learns from it which copy is its own, and
// the launcher learns what is installed. plist_path and label bind the copy
// to one service, so neither uninstall nor status has to guess.
type installMarker struct {
	Source      string `json:"source"`
	SHA256      string `json:"sha256"`
	Version     string `json:"version"`
	InstalledAt string `json:"installed_at"`
	PlistPath   string `json:"plist_path"`
	Label       string `json:"label"`
}

func installMarkerPath(dir string) string { return filepath.Join(dir, installMarkerName) }

// readInstallMarker returns (marker, true, nil) when the sidecar exists,
// (zero, false, nil) when it does not, and an error when it is unreadable or
// not valid JSON.
func readInstallMarker(dir string) (installMarker, bool, error) {
	path := installMarkerPath(dir)
	content, found, err := readOptional(path)
	if err != nil || !found {
		return installMarker{}, false, err
	}
	var marker installMarker
	if err = json.Unmarshal([]byte(content), &marker); err != nil {
		return installMarker{}, false, E.Cause(err, "parse sidecar ", path)
	}
	return marker, true, nil
}

// writeInstallMarker replaces the sidecar atomically: temporary file in the
// same directory, fsync, root:wheel (when chown), 0644, rename.
func writeInstallMarker(dir string, marker installMarker, chown bool) error {
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
	if err == nil && chown {
		err = os.Chown(tempPath, 0, 0)
	}
	if err == nil {
		err = os.Chmod(tempPath, 0o644)
	}
	if err == nil {
		err = os.Rename(tempPath, installMarkerPath(dir))
	}
	if err != nil {
		_ = os.Remove(tempPath)
		return E.Cause(err, "write sidecar ", installMarkerPath(dir))
	}
	syncDir(dir)
	return nil
}

// removeInstalledCopy removes <dir>/sing-box and its sidecar ONLY when the
// sidecar names this service (label and plist) and the file's sha256 equals
// the sidecar's (SPEC 100 §2.4); anything else stays, with the reason
// printed. A sidecar whose copy is already gone is removed as stale; the
// directory goes too once empty, if it is the label-named one install makes.
// The returned error is a failed removal, never a decision to keep.
func removeInstalledCopy(out io.Writer, dir, label, plistPath string, dryRun bool) error {
	target := filepath.Join(dir, execCopyName)
	markerPath := installMarkerPath(dir)
	marker, found, err := readInstallMarker(dir)
	if err != nil {
		fmt.Fprintf(out, "lxd: copy left in place: %v (%s)\n", err, target)
		return nil
	}
	targetInfo, targetErr := os.Lstat(target)
	if targetErr != nil && !os.IsNotExist(targetErr) {
		fmt.Fprintf(out, "lxd: copy left in place: %v\n", targetErr)
		return nil
	}
	targetExists := targetErr == nil
	if !found {
		if targetExists {
			fmt.Fprintf(out, "lxd: copy left in place: no sidecar at %s (%s)\n", markerPath, target)
		} else {
			fmt.Fprintln(out, "lxd: no installed copy at", target)
		}
		return nil
	}
	if marker.Label != label || marker.PlistPath != plistPath {
		fmt.Fprintf(out, "lxd: copy left in place: sidecar %s belongs to %s (%s), not %s\n", markerPath, marker.Label, marker.PlistPath, plistPath)
		return nil
	}
	if !targetExists {
		if dryRun {
			fmt.Fprintf(out, "lxd: would remove stale sidecar %s: the copy is already gone\n", markerPath)
		} else {
			if err = os.Remove(markerPath); err != nil {
				return E.Cause(err, "remove stale sidecar")
			}
			fmt.Fprintf(out, "lxd: removed stale sidecar %s: the copy is already gone\n", markerPath)
		}
		removeEmptyLabelDir(out, dir, label, dryRun)
		return nil
	}
	if !targetInfo.Mode().IsRegular() {
		fmt.Fprintf(out, "lxd: copy left in place: %s is not a regular file (%s)\n", target, targetInfo.Mode().Type())
		return nil
	}
	sum, err := sha256File(target, maxExecutableSize)
	if err != nil {
		fmt.Fprintf(out, "lxd: copy left in place: %v\n", err)
		return nil
	}
	if sum != marker.SHA256 {
		fmt.Fprintf(out, "lxd: copy left in place: sha differs from sidecar (file %s, sidecar %s): %s\n", sum, marker.SHA256, target)
		return nil
	}
	if dryRun {
		fmt.Fprintf(out, "lxd: would remove copy %s (sha256 %s, matches the sidecar) and sidecar %s\n", target, sum, markerPath)
		removeEmptyLabelDir(out, dir, label, dryRun)
		return nil
	}
	if err = os.Remove(target); err != nil {
		return E.Cause(err, "remove copy")
	}
	if err = os.Remove(markerPath); err != nil {
		return E.Cause(err, "remove sidecar")
	}
	fmt.Fprintf(out, "lxd: removed copy %s (sha256 %s, matches the sidecar) and sidecar %s\n", target, sum, markerPath)
	removeEmptyLabelDir(out, dir, label, dryRun)
	return nil
}

// removeEmptyLabelDir drops the label-named exec dir install created, once
// nothing else lives in it. An operator's own --exec-dir is never removed.
func removeEmptyLabelDir(out io.Writer, dir, label string, dryRun bool) {
	if filepath.Base(dir) != label {
		return
	}
	if dryRun {
		fmt.Fprintln(out, "lxd: would remove", dir, "once empty")
		return
	}
	if err := os.Remove(dir); err == nil {
		fmt.Fprintln(out, "lxd: removed", dir)
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(out, "lxd: kept %s: %v\n", dir, err)
	}
}
