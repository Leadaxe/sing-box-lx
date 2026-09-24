//go:build with_lxd

package lxd

import (
	"strings"
	"testing"
)

var (
	principalSystem           = aclPrincipal{SID: sidSystem}
	principalAdministrators   = aclPrincipal{SID: sidAdministrators}
	principalTrustedInstaller = aclPrincipal{SID: sidTrustedInstaller}
	principalAuthenticated    = aclPrincipal{SID: sidAuthenticated}
	principalUsers            = aclPrincipal{SID: sidUsers}
	principalUser             = aclPrincipal{SID: "S-1-5-21-1-2-3-1001", Name: `HOST\u`}
)

func allow(principal aclPrincipal, mask uint32, flags uint8) aclEntry {
	return aclEntry{Type: aceAccessAllowed, Flags: flags, Mask: mask, Principal: principal}
}

// stockVolumeRoot is C:\ as Windows ships it: Users read, Authenticated
// Users may create folders (container-inherit only), CREATOR OWNER and the
// modify grant for AU are inherit-only.
func stockVolumeRoot() securityFacts {
	return securityFacts{
		Owner:     principalSystem,
		Directory: true,
		DACL: []aclEntry{
			allow(principalAdministrators, accessFileAll, aceObjectInherit|aceContainerInherit),
			allow(principalSystem, accessFileAll, aceObjectInherit|aceContainerInherit),
			allow(principalUsers, accessFileRead, aceObjectInherit|aceContainerInherit),
			allow(principalAuthenticated, accessFileAppendData, aceContainerInherit),
			allow(principalAuthenticated, accessDelete|accessGenericWrite|accessGenericRead, aceObjectInherit|aceContainerInherit|aceInheritOnly),
			allow(aclPrincipal{SID: sidCreatorOwner}, accessGenericAll, aceObjectInherit|aceContainerInherit|aceInheritOnly),
		},
	}
}

// protectedCopy is a file of the set as install leaves it.
func protectedCopy(directory bool) securityFacts {
	flags := uint8(0)
	if directory {
		flags = aceObjectInherit | aceContainerInherit
	}
	return securityFacts{
		Owner:     principalAdministrators,
		Protected: true,
		Directory: directory,
		DACL: []aclEntry{
			allow(principalSystem, accessFileAll, flags),
			allow(principalAdministrators, accessFileAll, flags),
			allow(principalAuthenticated, accessFileRead, flags),
		},
	}
}

func withEntry(facts securityFacts, entry aclEntry) securityFacts {
	facts.DACL = append(append([]aclEntry(nil), facts.DACL...), entry)
	return facts
}

func TestACLPredicate(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		facts securityFacts
		level aclLevel
		want  string // "" = passes; otherwise a part of the error
	}{
		{"stock volume root as an ancestor", stockVolumeRoot(), aclAncestor, ""},
		{"Program Files owned by TrustedInstaller", securityFacts{Owner: principalTrustedInstaller, Directory: true, DACL: []aclEntry{
			allow(principalTrustedInstaller, accessFileAll, aceContainerInherit),
			allow(principalUsers, accessFileRead, aceObjectInherit|aceContainerInherit),
		}}, aclAncestor, ""},
		{"ancestor owned by a user", securityFacts{Owner: principalUser, Directory: true}, aclAncestor,
			`C:\p: owner HOST\u (S-1-5-21-1-2-3-1001) is not SYSTEM, Administrators or TrustedInstaller`},
		{"ancestor deletable by a user", withEntry(stockVolumeRoot(), allow(principalUser, accessDelete, 0)), aclAncestor,
			`HOST\u (S-1-5-21-1-2-3-1001) is granted DELETE, must not be writable by a non-administrative principal`},
		{"ancestor with FILE_DELETE_CHILD for users", withEntry(stockVolumeRoot(), allow(principalUsers, accessFileDeleteChild, 0)), aclAncestor,
			"Users (S-1-5-32-545) is granted FILE_DELETE_CHILD"},
		{"ancestor with WRITE_DAC for AU", withEntry(stockVolumeRoot(), allow(principalAuthenticated, accessWriteDAC, 0)), aclAncestor,
			"is granted WRITE_DAC"},
		{"ancestor with GENERIC_ALL for a user", withEntry(stockVolumeRoot(), allow(principalUser, accessGenericAll, 0)), aclAncestor,
			"is granted GENERIC_ALL"},
		{"ancestor: adding files is not replacing", withEntry(stockVolumeRoot(), allow(principalUser, accessFileWriteData|accessFileAppendData, 0)), aclAncestor, ""},
		{"inherit-only grants nothing here", withEntry(stockVolumeRoot(), allow(principalUser, accessGenericAll, aceObjectInherit|aceInheritOnly)), aclAncestor, ""},
		{"a deny entry never hurts", withEntry(stockVolumeRoot(), aclEntry{Type: aceAccessDenied, Mask: accessGenericAll, Principal: principalUser}), aclAncestor, ""},
		{"an object allow entry fails closed", withEntry(stockVolumeRoot(), aclEntry{Type: aceAccessAllowedObject, Mask: accessFileRead, Principal: aclPrincipal{SID: "(object entry)"}}), aclAncestor,
			"cannot be evaluated"},
		{"a callback allow entry fails closed", withEntry(stockVolumeRoot(), aclEntry{Type: aceAccessAllowedCallback, Mask: accessFileRead, Principal: principalUsers}), aclAncestor,
			"cannot be evaluated"},
		{"NULL DACL", securityFacts{Owner: principalSystem, Directory: true, NullDACL: true}, aclAncestor, "NULL DACL"},
		{"reparse point", securityFacts{Owner: principalSystem, ReparsePoint: true}, aclAncestor,
			`C:\p: is a reparse point, must be a real file or directory`},
		{"protected copy file", protectedCopy(false), aclProtected, ""},
		{"protected copy dir", protectedCopy(true), aclProtected, ""},
		{"copy owned by TrustedInstaller", securityFacts{Owner: principalTrustedInstaller}, aclProtected, "is not SYSTEM or Administrators"},
		{"copy writable by a user", withEntry(protectedCopy(false), allow(principalUser, accessFileWriteData, 0)), aclProtected,
			"is granted FILE_WRITE_DATA"},
		{"copy dir where users add files", withEntry(protectedCopy(true), allow(principalUsers, accessFileWriteData|accessFileAppendData, aceContainerInherit)), aclProtected,
			"is granted FILE_ADD_FILE, FILE_ADD_SUBDIRECTORY"},
		{"copy with writable attributes", withEntry(protectedCopy(false), allow(principalAuthenticated, accessFileWriteAttributes|accessFileWriteEA, 0)), aclProtected,
			"is granted FILE_WRITE_EA, FILE_WRITE_ATTRIBUTES"},
		{"copy readable by everyone", withEntry(protectedCopy(false), allow(aclPrincipal{SID: sidEveryone}, accessFileRead, 0)), aclProtected, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := aclViolation(`C:\p`, testCase.facts, testCase.level)
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("must pass, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("got %v, want it to contain %q", err, testCase.want)
			}
		})
	}
}

func TestServiceACL(t *testing.T) {
	ours := []aclEntry{
		allow(principalSystem, accessGenericAll, 0),
		allow(principalAdministrators, accessGenericAll, 0),
		allow(principalAuthenticated, 0x2008d, 0),
	}
	if err := serviceACLViolation(ours, false); err != nil {
		t.Fatalf("the install DACL must pass: %v", err)
	}
	for _, testCase := range []struct {
		name  string
		entry aclEntry
		want  string
	}{
		{"AU may reconfigure", allow(principalAuthenticated, accessServiceChangeConfig, 0), "SERVICE_CHANGE_CONFIG"},
		{"a user may take the DACL", allow(principalUser, accessWriteDAC, 0), "WRITE_DAC"},
		{"users own it all", allow(principalUsers, accessGenericAll, 0), "GENERIC_ALL"},
	} {
		if err := serviceACLViolation(append(append([]aclEntry(nil), ours...), testCase.entry), false); err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Fatalf("%s: got %v, want %q", testCase.name, err, testCase.want)
		}
	}
	// Start and stop for users are not a way to another binary.
	if err := serviceACLViolation(append(append([]aclEntry(nil), ours...), allow(principalUsers, 0x0010|0x0020, 0)), false); err != nil {
		t.Fatalf("start/stop rights must pass: %v", err)
	}
	if err := serviceACLViolation(nil, true); err == nil {
		t.Fatal("a NULL service DACL must fail")
	}
}

func TestACLForeignReaders(t *testing.T) {
	facts := securityFacts{
		Owner: principalUser,
		DACL: []aclEntry{
			allow(principalSystem, accessFileAll, 0),
			allow(principalUsers, accessFileRead, 0),
			allow(principalAuthenticated, accessFileAppendData, 0),
			allow(aclPrincipal{SID: sidEveryone}, accessGenericRead, aceInheritOnly),
		},
	}
	readers := foreignReaders(facts)
	if len(readers) != 2 || readers[0].SID != principalUser.SID || readers[1].SID != sidUsers {
		t.Fatalf("readers %+v, want the owner and Users only", readers)
	}
	if got := foreignReaders(protectedDataDir()); len(got) != 0 {
		t.Fatalf("a protected node has no foreign readers, got %+v", got)
	}
}

func protectedDataDir() securityFacts {
	return securityFacts{
		Owner:     principalAdministrators,
		Protected: true,
		Directory: true,
		DACL: []aclEntry{
			allow(principalSystem, accessFileAll, aceObjectInherit|aceContainerInherit),
			allow(principalAdministrators, accessFileAll, aceObjectInherit|aceContainerInherit),
		},
	}
}

func TestDataNodeProtected(t *testing.T) {
	if !dataNodeProtected(protectedDataDir(), true) {
		t.Fatal("the install DACL must be in the norm")
	}
	inherited := securityFacts{Owner: principalAdministrators, DACL: []aclEntry{
		allow(principalSystem, accessFileAll, aceInherited),
		allow(principalAdministrators, accessFileAll, aceInherited),
	}}
	if !dataNodeProtected(inherited, false) {
		t.Fatal("a file with the parent's entries is in the norm")
	}
	if dataNodeProtected(inherited, true) {
		t.Fatal("the root must cut inheritance off")
	}
	// What the daemon creates as SYSTEM is left alone; the root is not.
	systemOwned := inherited
	systemOwned.Owner = principalSystem
	if !dataNodeProtected(systemOwned, false) {
		t.Fatal("a SYSTEM-owned node below the root is in the norm")
	}
	systemRoot := protectedDataDir()
	systemRoot.Owner = principalSystem
	if dataNodeProtected(systemRoot, true) {
		t.Fatal("the root must be owned by Administrators")
	}
	for name, facts := range map[string]securityFacts{
		"a user owns it":       {Owner: principalUser, DACL: inherited.DACL},
		"users may read it":    withEntry(inherited, allow(principalUsers, accessFileRead, aceInherited)),
		"SYSTEM lost access":   {Owner: principalAdministrators, DACL: inherited.DACL[1:]},
		"empty DACL":           {Owner: principalAdministrators},
		"NULL DACL":            {Owner: principalAdministrators, NullDACL: true},
		"dir without inherit":  {Owner: principalAdministrators, Directory: true, DACL: inherited.DACL},
		"reparse point inside": {Owner: principalAdministrators, ReparsePoint: true, DACL: inherited.DACL},
	} {
		if dataNodeProtected(facts, false) {
			t.Fatalf("%s: must not be in the norm", name)
		}
	}
}

func TestCopyNodeInNorm(t *testing.T) {
	if !copyNodeInNorm(protectedCopy(false), false) || !copyNodeInNorm(protectedCopy(true), true) {
		t.Fatal("the install DACLs must be in the norm")
	}
	if copyNodeInNorm(protectedCopy(true), false) {
		t.Fatal("a file must not carry the directory's inheritance flags")
	}
	unprotected := protectedCopy(false)
	unprotected.Protected = false
	if copyNodeInNorm(unprotected, false) {
		t.Fatal("inheritance must be cut off")
	}
	if copyNodeInNorm(withEntry(protectedCopy(false), allow(principalUsers, accessFileRead, 0)), false) {
		t.Fatal("an extra entry is out of the norm")
	}
	systemOwned := protectedCopy(false)
	systemOwned.Owner = principalSystem
	if copyNodeInNorm(systemOwned, false) {
		t.Fatal("install makes Administrators the owner")
	}
}

func TestPrincipalName(t *testing.T) {
	if got := principalName(principalAuthenticated); got != "Authenticated Users (S-1-5-11)" {
		t.Fatal(got)
	}
	if got := principalName(aclPrincipal{SID: "S-1-5-21-9"}); got != "unknown account (S-1-5-21-9)" {
		t.Fatal(got)
	}
	if got := principalName(principalUser); got != `HOST\u (S-1-5-21-1-2-3-1001)` {
		t.Fatal(got)
	}
}
