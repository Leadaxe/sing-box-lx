//go:build with_lxd

package lxd

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

// resourceStore is the daemon's on-disk store for the files a config REFERS to
// but does not embed: compiled rule-sets (`.srs`, `type: local`), geo databases.
// It is the second admin payload beside the config itself (SPEC 063): the
// operator pushes a resource by name, and points `path` in the config at
// <dir>/<name>. Names are stable, so a config written once keeps working across
// re-uploads of the same name; the sha256 returned with every entry is the
// version handle the client diffs against before deciding to re-upload.
//
// Writes are atomic (tmp + fsync + rename in the same directory) so a crash
// never leaves a torn resource, mirroring store.writeAtomic.
type resourceStore struct {
	dir string
}

// maxResourceNameLen bounds a name so a pathological upload cannot create an
// unusable filename; well above any real rule-set / geo-base name.
const maxResourceNameLen = 255

// badResourceName marks a rejected name: the client's fault (HTTP 400), never
// disk. isBadResourceName lets handlers map it without string matching.
type badResourceName struct{ inner error }

func (e badResourceName) Error() string { return e.inner.Error() }
func (e badResourceName) Unwrap() error { return e.inner }

func isBadResourceName(err error) bool {
	var bad badResourceName
	return errors.As(err, &bad)
}

func badName(message string) error { return badResourceName{E.New(message)} }

func newResourceStore(dir string) (*resourceStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, E.Cause(err, "create resources directory")
	}
	return &resourceStore{dir: dir}, nil
}

// sanitizeResourceName enforces that name is a safe base filename: it must be
// exactly its own base (no separators, no `.`/`..`, no leading dot, non-empty,
// bounded). Any traversal attempt is rejected here so a resource can never be
// written outside dir — the store never touches the filesystem for a bad name.
func sanitizeResourceName(name string) (string, error) {
	if name == "" {
		return "", badName("empty resource name")
	}
	if len(name) > maxResourceNameLen {
		return "", badName("resource name too long")
	}
	// Reject any path structure outright rather than trying to normalize it:
	// separators, parent refs, absolute paths, and hidden/`.`/`..` names.
	if strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, 0) {
		return "", badName("resource name must not contain path separators")
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return "", badName("resource name must not start with a dot")
	}
	// Belt and braces: after all checks the name must still be its own base.
	if filepath.Base(name) != name {
		return "", badName("invalid resource name")
	}
	return name, nil
}

func (rs *resourceStore) pathOf(name string) string {
	return filepath.Join(rs.dir, name)
}

// writeAtomic writes content to path via a temp file in the same directory,
// fsync'd and renamed — a rename within one directory is atomic, so a reader
// (or a crash) never sees a partial resource.
func (rs *resourceStore) writeAtomic(path string, content []byte) error {
	temp, err := os.CreateTemp(rs.dir, ".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err = temp.Write(content); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tempName)
		return err
	}
	if err = os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return err
	}
	return nil
}

// Put stores content under name, overwriting any prior version atomically.
// Returns the sha256 and byte size of what was stored.
func (rs *resourceStore) Put(name string, content []byte) (sha string, size int, err error) {
	safe, err := sanitizeResourceName(name)
	if err != nil {
		return "", 0, err
	}
	if err = rs.writeAtomic(rs.pathOf(safe), content); err != nil {
		return "", 0, err
	}
	return contentSHA(string(content)), len(content), nil
}

// resourceMeta is the metadata view of one resource — everything the client
// needs to diff versions and reference the file from a config, without the
// bytes.
type resourceMeta struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
	Path   string `json:"path"`
}

// Stat returns metadata for one name; found=false when it is absent (never an
// error). The temp files writeAtomic leaves mid-write start with "." and are
// excluded from listing, so a partial upload is never observable as a resource.
func (rs *resourceStore) Stat(name string) (meta resourceMeta, found bool, err error) {
	safe, err := sanitizeResourceName(name)
	if err != nil {
		return resourceMeta{}, false, err
	}
	content, exists, err := readOptional(rs.pathOf(safe))
	if err != nil {
		return resourceMeta{}, false, err
	}
	if !exists {
		return resourceMeta{}, false, nil
	}
	return resourceMeta{
		Name:   safe,
		SHA256: contentSHA(content),
		Size:   len(content),
	}, true, nil
}

// Read returns the raw bytes of a resource; found=false when it is absent.
func (rs *resourceStore) Read(name string) (content []byte, found bool, err error) {
	safe, err := sanitizeResourceName(name)
	if err != nil {
		return nil, false, err
	}
	raw, readErr := os.ReadFile(rs.pathOf(safe))
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, false, nil
		}
		return nil, false, readErr
	}
	return raw, true, nil
}

// List returns metadata for every stored resource, sorted by name. Temp files
// (leading dot) are skipped, so an in-flight upload never appears.
func (rs *resourceStore) List() ([]resourceMeta, error) {
	entries, err := os.ReadDir(rs.dir)
	if err != nil {
		return nil, err
	}
	var metas []resourceMeta
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		meta, found, statErr := rs.Stat(entry.Name())
		if statErr != nil || !found {
			continue
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	return metas, nil
}

// Delete removes a resource; found=false when it was already absent.
func (rs *resourceStore) Delete(name string) (found bool, err error) {
	safe, err := sanitizeResourceName(name)
	if err != nil {
		return false, err
	}
	if err = os.Remove(rs.pathOf(safe)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// previousPrefix marks the kept copy of a replaced resource (SPEC 159). The
// leading dot keeps it out of List and out of reach of the REST plane, whose
// names never start with one.
const previousPrefix = ".prev-"

func (rs *resourceStore) previousPathOf(name string) string {
	return filepath.Join(rs.dir, previousPrefix+name)
}

// PutKeepingPrevious is Put for a name a config still points at: before the
// swap it sets the current bytes aside, unless a kept copy already exists —
// the kept copy is always the last version a core is known to have run with,
// never an unconfirmed one. kept=false when there was nothing to keep.
func (rs *resourceStore) PutKeepingPrevious(name string, content []byte) (sha string, size int, kept bool, err error) {
	safe, err := sanitizeResourceName(name)
	if err != nil {
		return "", 0, false, err
	}
	previousPath := rs.previousPathOf(safe)
	if _, statErr := os.Stat(previousPath); statErr == nil {
		kept = true
	} else if !os.IsNotExist(statErr) {
		return "", 0, false, statErr
	} else if current, readErr := os.ReadFile(rs.pathOf(safe)); readErr == nil {
		if err = rs.writeAtomic(previousPath, current); err != nil {
			return "", 0, false, err
		}
		kept = true
	} else if !os.IsNotExist(readErr) {
		return "", 0, false, readErr
	}
	if err = rs.writeAtomic(rs.pathOf(safe), content); err != nil {
		return "", 0, kept, err
	}
	return contentSHA(string(content)), len(content), kept, nil
}

// RestorePrevious puts the kept copy of name back in place; restored=false
// when there is none.
func (rs *resourceStore) RestorePrevious(name string) (restored bool, err error) {
	safe, err := sanitizeResourceName(name)
	if err != nil {
		return false, err
	}
	if err = os.Rename(rs.previousPathOf(safe), rs.pathOf(safe)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// DropPrevious forgets the kept copy of name: the replacement is confirmed.
func (rs *resourceStore) DropPrevious(name string) error {
	safe, err := sanitizeResourceName(name)
	if err != nil {
		return err
	}
	if err = os.Remove(rs.previousPathOf(safe)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// keptNames lists the names that have a kept copy.
func (rs *resourceStore) keptNames() ([]string, error) {
	entries, err := os.ReadDir(rs.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), previousPrefix) {
			names = append(names, strings.TrimPrefix(entry.Name(), previousPrefix))
		}
	}
	return names, nil
}

// RestoreAllPrevious puts every kept copy back; DropAllPrevious forgets them
// all. Both return the affected names and keep going past a failing entry, so
// one bad file does not leave the rest unsettled.
func (rs *resourceStore) RestoreAllPrevious() (restored []string, err error) {
	names, err := rs.keptNames()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		done, restoreErr := rs.RestorePrevious(name)
		if restoreErr != nil {
			err = restoreErr
			continue
		}
		if done {
			restored = append(restored, name)
		}
	}
	return restored, err
}

func (rs *resourceStore) DropAllPrevious() (dropped []string, err error) {
	names, err := rs.keptNames()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if dropErr := rs.DropPrevious(name); dropErr != nil {
			err = dropErr
			continue
		}
		dropped = append(dropped, name)
	}
	return dropped, err
}
