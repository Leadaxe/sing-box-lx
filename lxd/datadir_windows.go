//go:build with_lxd && windows

package lxd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/windows"
)

// prepareDataDir brings <ProgramData>\sing-box-lxd to the norm of SPEC 103
// §2.3 before anything is written into it — daemon.json included: a file
// with the secret created under a foreign owner would inherit a foreign
// DACL. Top-down, every node through a handle opened without following
// links: the root is owned by Administrators with the protected DACL; a
// node below it out of the norm gets owner Administrators and an explicit
// protected DACL of its own — never an intermediate empty or inherited-only
// DACL, which could read as a NULL DACL before inheritance applies. Files the
// daemon created as SYSTEM with the inherited entries are in the norm and
// left alone. A reparse point or a hard-linked file inside
// refuses: changing an ACL through a link changes someone else's object, and
// the daemon's writes as SYSTEM would follow it. The warnings name who could
// read an existing daemon.json before (§4.2 p. 6).
func prepareDataDir(out io.Writer, root string) ([]installWarning, error) {
	for _, privilege := range []string{"SeTakeOwnershipPrivilege", "SeRestorePrivilege"} {
		if err := enablePrivilege(privilege); err != nil {
			return nil, E.Cause(err, "take ownership of ", root)
		}
	}
	// Reading a tree whose DACL shuts administrators out needs backup
	// semantics; without the privilege the walk still takes ownership first.
	_ = enablePrivilege("SeBackupPrivilege")

	_, statErr := os.Lstat(filepath.Join(root, "state", daemonConfigFile))
	hadSecret := statErr == nil
	walker := &dataDirWalker{out: out, root: root}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		if err = os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
			return nil, E.Cause(err, "create ", filepath.Dir(root))
		}
		if err = createProtectedDir(root, dataDirSDDL); err != nil {
			return nil, E.Cause(err, "create ", root)
		}
		fmt.Fprintln(out, "lxd: created", root, "(Administrators, protected DACL)")
		walker.changed = true
	} else if err != nil {
		return nil, err
	}
	if err := walker.walk(root, true); err != nil {
		return nil, err
	}
	for _, sub := range []string{"state", "logs"} {
		path := filepath.Join(root, sub)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			if err = os.Mkdir(path, 0o700); err != nil {
				return nil, E.Cause(err, "create ", path)
			}
			fmt.Fprintln(out, "lxd: created", path)
			walker.changed = true
		}
	}
	if !walker.changed {
		fmt.Fprintln(out, "lxd: data dir", root, "is protected")
	}
	if !hadSecret {
		return nil, nil
	}
	var warnings []installWarning
	for _, reader := range walker.readers {
		text := "WARN: " + reader.path + " was readable by " + principalName(reader.principal) +
			" before this install; rotate the admin secret in daemon.json and re-pair clients if the host is shared"
		fmt.Fprintln(out, text)
		warnings = append(warnings, installWarning{Code: warningStateDirForeign, Text: text})
	}
	return warnings, nil
}

type dataDirWalker struct {
	out     io.Writer
	root    string
	changed bool
	readers []foreignReader
}

type foreignReader struct {
	path      string
	principal aclPrincipal
}

func (walker *dataDirWalker) walk(path string, isRoot bool) error {
	directory, err := walker.normalize(path, isRoot)
	if err != nil || !directory {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return E.Cause(err, "list ", path)
	}
	for _, entry := range entries {
		if err = walker.walk(filepath.Join(path, entry.Name()), false); err != nil {
			return err
		}
	}
	return nil
}

// normalize brings one node to the norm and reports whether it is a
// directory to descend into.
func (walker *dataDirWalker) normalize(path string, isRoot bool) (bool, error) {
	const access = windows.READ_CONTROL | windows.WRITE_DAC | windows.WRITE_OWNER
	handle, err := openNoFollow(path, access)
	var previousOwner string
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// Not even readable: take ownership blind, then the owner's implicit
		// READ_CONTROL and WRITE_DAC open it.
		if err = setOwnerAdministrators(path); err != nil {
			return false, E.Cause(err, "take ownership of ", path)
		}
		previousOwner = "an owner that denied administrators access"
		handle, err = openNoFollow(path, access)
	}
	if err != nil {
		return false, E.Cause(err, "open ", path)
	}
	defer windows.CloseHandle(handle)
	facts, links, err := securityOfHandle(handle)
	if err != nil {
		return false, E.Cause(err, "read the security of ", path)
	}
	if facts.ReparsePoint {
		return false, E.New(path, " is a reparse point; remove it: Remove-Item ", path)
	}
	if !facts.Directory && links > 1 {
		return false, E.New(path, " has ", links, " hard links; remove it: Remove-Item ", path)
	}
	walker.noteReaders(path, facts)
	if previousOwner == "" && dataNodeProtected(facts, isRoot) {
		return facts.Directory, nil
	}
	walker.changed = true
	if previousOwner != "" {
		fmt.Fprintf(walker.out, "lxd: took ownership of %s (was %s)\n", path, previousOwner)
	} else if facts.Owner.SID != sidAdministrators && (isRoot || facts.Owner.SID != sidSystem) {
		fmt.Fprintf(walker.out, "lxd: took ownership of %s (was %s)\n", path, principalName(facts.Owner))
	}
	sddl := dataFileSDDL
	if facts.Directory {
		sddl = dataDirSDDL
	}
	if err = applySecurity(handle, sddl, true); err != nil {
		return false, E.Cause(err, "replace the DACL on ", path)
	}
	fmt.Fprintln(walker.out, "lxd: replaced DACL on", path)
	return facts.Directory, nil
}

// noteReaders remembers who outside the allowlist owned the tree or could
// read the state before this install (SPEC 103 §4.2 p. 6), once per SID.
func (walker *dataDirWalker) noteReaders(path string, facts securityFacts) {
	state := filepath.Join(walker.root, "state")
	inState := samePathFold(path, state) || len(path) > len(state) && samePathFold(path[:len(state)+1], state+`\`)
	for _, principal := range foreignReaders(facts) {
		isOwner := principal.SID == facts.Owner.SID
		if !isOwner && !inState {
			continue
		}
		known := false
		for _, reader := range walker.readers {
			if reader.principal.SID == principal.SID {
				known = true
				break
			}
		}
		if !known {
			walker.readers = append(walker.readers, foreignReader{path: path, principal: principal})
		}
	}
}

// dataDirSummary is the informational line of the status report: owner and
// whether the root is protected. Without administrator rights the DACL
// usually cannot be read at all, which is the norm (SPEC 103 §4.2 p. 8).
func dataDirSummary(root string) string {
	facts, err := readSecurityFacts(root)
	switch {
	case os.IsNotExist(err):
		return root + " (absent)"
	case err != nil:
		return root + " (not readable here: " + err.Error() + "; expected without administrator rights)"
	case dataNodeProtected(facts, true):
		return root + " (owner " + principalName(facts.Owner) + ", protected DACL)"
	default:
		return root + " (owner " + principalName(facts.Owner) + ", NOT protected — --service=install brings it back)"
	}
}
