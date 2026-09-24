//go:build with_lxd

package lxd

import (
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

// The "protected path" invariant of the Windows copy (SPEC 103 §2.2) as a
// predicate over plain values: SID strings and access masks with the
// constants defined here, no x/sys/windows. execsafe_windows.go reads the
// owner and the DACL from a handle and feeds them in; the table test runs on
// any CI host.

// Well-known SIDs of the allowlist.
const (
	sidSystem           = "S-1-5-18"
	sidAdministrators   = "S-1-5-32-544"
	sidTrustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
	sidAuthenticated    = "S-1-5-11"
	sidUsers            = "S-1-5-32-545"
	sidEveryone         = "S-1-1-0"
	sidCreatorOwner     = "S-1-3-0"
)

// ACE types (winnt.h). Only these can appear in a DACL; the audit and label
// types live in the SACL.
const (
	aceAccessAllowed               = 0x0
	aceAccessDenied                = 0x1
	aceAccessAllowedCompound       = 0x4
	aceAccessAllowedObject         = 0x5
	aceAccessDeniedObject          = 0x6
	aceAccessAllowedCallback       = 0x9
	aceAccessDeniedCallback        = 0xA
	aceAccessAllowedCallbackObject = 0xB
	aceAccessDeniedCallbackObject  = 0xC
)

// ACE flags.
const (
	aceObjectInherit    = 0x01
	aceContainerInherit = 0x02
	aceInheritOnly      = 0x08
	aceInherited        = 0x10
)

// Access rights. For a directory FILE_WRITE_DATA is FILE_ADD_FILE and
// FILE_APPEND_DATA is FILE_ADD_SUBDIRECTORY — the same bits.
const (
	accessFileReadData        = 0x00000001
	accessFileWriteData       = 0x00000002
	accessFileAppendData      = 0x00000004
	accessFileWriteEA         = 0x00000010
	accessFileDeleteChild     = 0x00000040
	accessFileWriteAttributes = 0x00000100
	accessDelete              = 0x00010000
	accessWriteDAC            = 0x00040000
	accessWriteOwner          = 0x00080000
	accessGenericAll          = 0x10000000
	accessGenericWrite        = 0x40000000
	accessGenericRead         = 0x80000000
	accessFileAll             = 0x001F01FF
	// accessFileRead is FILE_GENERIC_READ | FILE_GENERIC_EXECUTE ("FRFX").
	accessFileRead = 0x001200A9

	// Service rights (winsvc.h).
	accessServiceChangeConfig = 0x0002
)

// Forbidden to a foreign SID (SPEC 103 §2.2): an ancestor must not be
// replaceable, the copy dir and the set must not be writable at all.
const (
	ancestorForbidden  = accessDelete | accessWriteDAC | accessWriteOwner | accessGenericWrite | accessGenericAll | accessFileDeleteChild
	protectedWriteMask = ancestorForbidden | accessFileWriteData | accessFileAppendData | accessFileWriteEA | accessFileWriteAttributes
	// serviceForbidden: what a foreign SID must not hold on the SCM service
	// (SPEC 103 §2.9): the right to point it at another binary or to take
	// the service over.
	serviceForbidden = accessServiceChangeConfig | accessWriteDAC | accessWriteOwner | accessDelete | accessGenericWrite | accessGenericAll
	// readMask: any of these lets a SID read the file's content.
	readMask = accessFileReadData | accessGenericRead | accessGenericAll
)

// aclPrincipal is a SID with its account name for the messages; the name
// may be empty.
type aclPrincipal struct {
	SID  string
	Name string
}

// aclEntry is one ACE of a DACL.
type aclEntry struct {
	Type      uint8
	Flags     uint8
	Mask      uint32
	Principal aclPrincipal
}

// securityFacts is what the invariant reads about one path component.
type securityFacts struct {
	Owner        aclPrincipal
	DACL         []aclEntry
	NullDACL     bool // no DACL at all: everyone has full access
	Protected    bool // SE_DACL_PROTECTED: inheritance from the parent is off
	ReparsePoint bool
	Directory    bool
}

// aclLevel selects the rule of SPEC 103 §2.2.
type aclLevel int

const (
	// aclAncestor: a component from the volume root down to the copy dir's
	// parent — owner in the allowlist, nothing that lets a foreign SID
	// replace what lies below.
	aclAncestor aclLevel = iota
	// aclProtected: the copy dir and every file of the set — owned by
	// SYSTEM or Administrators, not writable by a foreign SID at all.
	aclProtected
)

// administrativeSID: the allowlist — SYSTEM, Administrators, TrustedInstaller.
func administrativeSID(sid string) bool {
	return sid == sidSystem || sid == sidAdministrators || sid == sidTrustedInstaller
}

// principalName renders "<name> (<SID>)", naming the well-known SIDs when
// the account lookup gave nothing.
func principalName(principal aclPrincipal) string {
	name := principal.Name
	if name == "" {
		switch principal.SID {
		case sidSystem:
			name = "SYSTEM"
		case sidAdministrators:
			name = "Administrators"
		case sidTrustedInstaller:
			name = "TrustedInstaller"
		case sidAuthenticated:
			name = "Authenticated Users"
		case sidUsers:
			name = "Users"
		case sidEveryone:
			name = "Everyone"
		case sidCreatorOwner:
			name = "CREATOR OWNER"
		default:
			name = "unknown account"
		}
	}
	return name + " (" + principal.SID + ")"
}

// aclViolation is the invariant for ONE path component (SPEC 103 §2.2).
// Reparse points, a NULL DACL and allowing ACE types other than
// ACCESS_ALLOWED fail closed; inherit-only ACEs grant nothing on this object
// and denying ACEs only take away, so neither is counted.
func aclViolation(path string, facts securityFacts, level aclLevel) error {
	if facts.ReparsePoint {
		return E.New(path, ": is a reparse point, must be a real file or directory")
	}
	ownerAllowed := administrativeSID(facts.Owner.SID)
	if level == aclProtected {
		ownerAllowed = facts.Owner.SID == sidSystem || facts.Owner.SID == sidAdministrators
	}
	if !ownerAllowed {
		if level == aclProtected {
			return E.New(path, ": owner ", principalName(facts.Owner), " is not SYSTEM or Administrators")
		}
		return E.New(path, ": owner ", principalName(facts.Owner), " is not SYSTEM, Administrators or TrustedInstaller")
	}
	if facts.NullDACL {
		return E.New(path, ": has a NULL DACL (full access for everyone), must be protected")
	}
	forbidden := uint32(ancestorForbidden)
	if level == aclProtected {
		forbidden = protectedWriteMask
	}
	for _, entry := range facts.DACL {
		if entry.Flags&aceInheritOnly != 0 {
			continue
		}
		switch entry.Type {
		case aceAccessDenied, aceAccessDeniedObject, aceAccessDeniedCallback, aceAccessDeniedCallbackObject:
			continue
		case aceAccessAllowed:
		default:
			return E.New(path, ": ", principalName(entry.Principal), " holds an access entry of type ", entry.Type, " that cannot be evaluated, must be a plain allow or deny entry")
		}
		if administrativeSID(entry.Principal.SID) {
			continue
		}
		if granted := entry.Mask & forbidden; granted != 0 {
			return E.New(path, ": ", principalName(entry.Principal), " is granted ", describeFileRights(granted, facts.Directory), ", must not be writable by a non-administrative principal")
		}
	}
	return nil
}

// serviceACLViolation judges the SCM service's DACL (SPEC 103 §2.9): a
// foreign SID must not hold SERVICE_CHANGE_CONFIG, WRITE_DAC, WRITE_OWNER,
// DELETE, GENERIC_WRITE or GENERIC_ALL. The same fail-closed rules as files.
func serviceACLViolation(entries []aclEntry, nullDACL bool) error {
	if nullDACL {
		return E.New("the service has a NULL DACL (full access for everyone)")
	}
	for _, entry := range entries {
		if entry.Flags&aceInheritOnly != 0 {
			continue
		}
		switch entry.Type {
		case aceAccessDenied, aceAccessDeniedObject, aceAccessDeniedCallback, aceAccessDeniedCallbackObject:
			continue
		case aceAccessAllowed:
		default:
			return E.New("the service DACL gives ", principalName(entry.Principal), " an access entry of type ", entry.Type, " that cannot be evaluated")
		}
		if administrativeSID(entry.Principal.SID) {
			continue
		}
		if granted := entry.Mask & serviceForbidden; granted != 0 {
			return E.New("the service DACL grants ", principalName(entry.Principal), " ", describeServiceRights(granted), ", which lets it point the service at another binary")
		}
	}
	return nil
}

// foreignReaders lists who outside the allowlist could read a component's
// content: its owner, and every SID an allowing ACE gives a read right
// (SPEC 103 §4.2 p. 6). A NULL DACL is readable by everyone.
func foreignReaders(facts securityFacts) []aclPrincipal {
	var readers []aclPrincipal
	add := func(principal aclPrincipal) {
		for _, known := range readers {
			if known.SID == principal.SID {
				return
			}
		}
		readers = append(readers, principal)
	}
	if facts.Owner.SID != "" && !administrativeSID(facts.Owner.SID) {
		add(facts.Owner)
	}
	if facts.NullDACL {
		add(aclPrincipal{SID: sidEveryone})
	}
	for _, entry := range facts.DACL {
		if entry.Flags&aceInheritOnly != 0 || administrativeSID(entry.Principal.SID) {
			continue
		}
		switch entry.Type {
		case aceAccessAllowed, aceAccessAllowedCompound, aceAccessAllowedObject, aceAccessAllowedCallback, aceAccessAllowedCallbackObject:
			if entry.Mask&readMask != 0 {
				add(entry.Principal)
			}
		}
	}
	return readers
}

// dataNodeProtected: a node of the data dir is in the norm of SPEC 103 §2.3
// — owned by Administrators, no ACE for a SID outside the allowlist, and
// SYSTEM and Administrators both hold full access. The root must also have
// inheritance cut off; below it, inherited and explicit entries are equal.
func dataNodeProtected(facts securityFacts, root bool) bool {
	if facts.ReparsePoint || facts.NullDACL || facts.Owner.SID != sidAdministrators {
		return false
	}
	if root && !facts.Protected {
		return false
	}
	var systemFull, administratorsFull bool
	for _, entry := range facts.DACL {
		if !administrativeSID(entry.Principal.SID) {
			return false
		}
		if entry.Type != aceAccessAllowed || entry.Flags&aceInheritOnly != 0 {
			continue
		}
		full := entry.Mask&accessFileAll == accessFileAll || entry.Mask&accessGenericAll != 0
		// A directory's grant must also reach what the daemon creates in it.
		if facts.Directory && entry.Flags&(aceObjectInherit|aceContainerInherit) != aceObjectInherit|aceContainerInherit {
			full = false
		}
		if full && entry.Principal.SID == sidSystem {
			systemFull = true
		}
		if full && entry.Principal.SID == sidAdministrators {
			administratorsFull = true
		}
	}
	return systemFull && administratorsFull
}

// describeFileRights names the bits of a mask for a message, reading the
// data bits as a directory's rights where the component is one.
func describeFileRights(mask uint32, directory bool) string {
	names := []struct {
		bit       uint32
		file, dir string
	}{
		{accessGenericAll, "GENERIC_ALL", "GENERIC_ALL"},
		{accessGenericWrite, "GENERIC_WRITE", "GENERIC_WRITE"},
		{accessDelete, "DELETE", "DELETE"},
		{accessWriteDAC, "WRITE_DAC", "WRITE_DAC"},
		{accessWriteOwner, "WRITE_OWNER", "WRITE_OWNER"},
		{accessFileDeleteChild, "FILE_DELETE_CHILD", "FILE_DELETE_CHILD"},
		{accessFileWriteData, "FILE_WRITE_DATA", "FILE_ADD_FILE"},
		{accessFileAppendData, "FILE_APPEND_DATA", "FILE_ADD_SUBDIRECTORY"},
		{accessFileWriteEA, "FILE_WRITE_EA", "FILE_WRITE_EA"},
		{accessFileWriteAttributes, "FILE_WRITE_ATTRIBUTES", "FILE_WRITE_ATTRIBUTES"},
	}
	var parts []string
	for _, name := range names {
		if mask&name.bit == 0 {
			continue
		}
		if directory {
			parts = append(parts, name.dir)
		} else {
			parts = append(parts, name.file)
		}
	}
	return strings.Join(parts, ", ")
}

func describeServiceRights(mask uint32) string {
	names := []struct {
		bit  uint32
		name string
	}{
		{accessGenericAll, "GENERIC_ALL"},
		{accessGenericWrite, "GENERIC_WRITE"},
		{accessServiceChangeConfig, "SERVICE_CHANGE_CONFIG"},
		{accessWriteDAC, "WRITE_DAC"},
		{accessWriteOwner, "WRITE_OWNER"},
		{accessDelete, "DELETE"},
	}
	var parts []string
	for _, name := range names {
		if mask&name.bit != 0 {
			parts = append(parts, name.name)
		}
	}
	return strings.Join(parts, ", ")
}

// copyNodeInNorm: the copy dir or a file of the set already has owner and
// DACL of SPEC 103 §2.1 — Administrators, protected, exactly SYSTEM and
// Administrators with full access and Authenticated Users with read and
// execute.
func copyNodeInNorm(facts securityFacts, directory bool) bool {
	if facts.Owner.SID != sidAdministrators || !facts.Protected || facts.NullDACL || len(facts.DACL) != 3 {
		return false
	}
	flags := uint8(0)
	if directory {
		flags = aceObjectInherit | aceContainerInherit
	}
	want := map[string]uint32{sidSystem: accessFileAll, sidAdministrators: accessFileAll, sidAuthenticated: accessFileRead}
	for _, entry := range facts.DACL {
		mask, found := want[entry.Principal.SID]
		if !found || entry.Type != aceAccessAllowed || entry.Flags != flags || entry.Mask != mask {
			return false
		}
		delete(want, entry.Principal.SID)
	}
	return len(want) == 0
}
