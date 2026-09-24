//go:build with_lxd && windows

package lxd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/windows"
)

// The Windows half of the protected-path invariant (SPEC 103 §2.2, §2.3):
// every component is opened without following links, its owner and DACL are
// read from that same handle and handed to the portable predicate
// (aclcheck.go). Writes go through handles too.

// Security descriptors of SPEC 103 §2.1. Owner Administrators; the copy is
// readable and executable by Authenticated Users (the launcher hashes it
// without privileges), writable by nobody else; the data dir is closed.
const (
	copyDirSDDL      = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FRFX;;;AU)"
	copyFileSDDL     = "O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FRFX;;;AU)"
	dataDirSDDL      = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	dataFileSDDL     = "O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)"
	dataInheritSDDL  = "O:BAD:"
	windowsSelfCheck = "run `sing-box lxd --service=install` to reinstall from a protected copy"
)

// lstatOwner: unix ownership does not exist here; the macOS copy code that
// reads it is never reached on Windows, and fails closed if it were.
func lstatOwner(path string) (ownerInfo, error) {
	return ownerInfo{}, E.New(path, ": file ownership is not available on this platform")
}

var windowsSelfCheckWords = selfCheckWords{
	refuseAs:   "a Windows service",
	runningAs:  "running elevated",
	risk:       "anyone who can replace that file or a library beside it runs code as an administrator",
	notService: "not the Windows service",
	remedy:     windowsSelfCheck,
	okKind:     "protected",
	service:    "windows service " + execCopyBase,
}

// platformSelfCheckEnv (SPEC 103 §2.11): the service is the `lxd` command
// under the SCM; an elevated token anywhere else only warns.
func platformSelfCheckEnv(daemon bool) selfCheckEnv {
	service := daemon && IsWindowsService()
	context := "elevated, outside the SCM"
	if !daemon {
		context = "elevated `run`"
	}
	return selfCheckEnv{
		privileged:     service || ServiceActionPrivileged(),
		serviceContext: service,
		context:        context,
		executable:     resolveOwnExecutable,
		check:          checkProtectedExecutable,
		subject:        describeProtectedOwner,
		words:          windowsSelfCheckWords,
	}
}

// checkProtectedExecutable applies the invariant to a running binary: the
// chain from the volume root to its directory, the directory, the binary and
// the library of its set lying beside it.
func checkProtectedExecutable(executable string) error {
	dir := filepath.Dir(executable)
	if err := checkProtectedAncestors(dir); err != nil {
		return err
	}
	if err := checkProtectedPath(dir, true); err != nil {
		return err
	}
	if err := checkProtectedPath(executable, false); err != nil {
		return err
	}
	library := filepath.Join(dir, cronetLibraryName)
	if _, err := os.Lstat(library); err == nil {
		return checkProtectedPath(library, false)
	}
	return nil
}

func describeProtectedOwner(path string) string {
	facts, err := readSecurityFacts(path)
	if err != nil {
		return path + " (" + err.Error() + ")"
	}
	return path + " (owner " + principalName(facts.Owner) + ")"
}

// openNoFollow opens a path component itself: a reparse point is opened as
// such, a directory through backup semantics.
func openNoFollow(path string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(name, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}

// readSecurityFacts reads one component's owner, DACL and kind from a handle
// opened without following links.
func readSecurityFacts(path string) (securityFacts, error) {
	handle, err := openNoFollow(path, windows.READ_CONTROL)
	if err != nil {
		return securityFacts{}, &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer windows.CloseHandle(handle)
	facts, _, err := securityOfHandle(handle)
	if err != nil {
		return securityFacts{}, E.Cause(err, "read the security of ", path)
	}
	return facts, nil
}

// securityOfHandle returns the facts and the file's link count.
func securityOfHandle(handle windows.Handle) (securityFacts, uint32, error) {
	var facts securityFacts
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return facts, 0, err
	}
	facts.ReparsePoint = information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
	facts.Directory = information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return facts, 0, err
	}
	facts, err = descriptorFacts(descriptor, facts)
	return facts, information.NumberOfLinks, err
}

// descriptorFacts translates a security descriptor into the predicate's
// input. An absent DACL is a NULL DACL: full access for everyone.
func descriptorFacts(descriptor *windows.SECURITY_DESCRIPTOR, facts securityFacts) (securityFacts, error) {
	if owner, _, err := descriptor.Owner(); err == nil && owner != nil {
		facts.Owner = principalOf(owner)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return facts, err
	}
	facts.Protected = control&windows.SE_DACL_PROTECTED != 0
	dacl, _, err := descriptor.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && dacl == nil) {
		facts.NullDACL = true
		return facts, nil
	}
	if err != nil {
		return facts, err
	}
	facts.DACL, err = aclEntries(dacl)
	return facts, err
}

func aclEntries(acl *windows.ACL) ([]aclEntry, error) {
	entries := make([]aclEntry, 0, acl.AceCount)
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &ace); err != nil {
			return nil, err
		}
		entry := aclEntry{Type: ace.Header.AceType, Flags: ace.Header.AceFlags, Mask: uint32(ace.Mask)}
		switch entry.Type {
		case aceAccessAllowed, aceAccessDenied, aceAccessAllowedCallback, aceAccessDeniedCallback:
			// These types keep the SID right after the mask.
			entry.Principal = principalOf((*windows.SID)(unsafe.Pointer(&ace.SidStart)))
		default:
			entry.Principal = aclPrincipal{SID: "(object entry)", Name: "an object access entry"}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func principalOf(sid *windows.SID) aclPrincipal {
	principal := aclPrincipal{SID: sid.String()}
	if account, domain, _, err := sid.LookupAccount(""); err == nil {
		principal.Name = account
		if domain != "" && !strings.EqualFold(domain, "BUILTIN") && !strings.EqualFold(domain, "NT AUTHORITY") && !strings.EqualFold(domain, "NT SERVICE") {
			principal.Name = domain + `\` + account
		}
	}
	return principal
}

// checkProtectedPath applies the copy-level rule to the copy dir or a file
// of the set.
func checkProtectedPath(path string, directory bool) error {
	facts, err := readSecurityFacts(path)
	if err != nil {
		return err
	}
	if err = aclViolation(path, facts, aclProtected); err != nil {
		return err
	}
	switch {
	case directory && !facts.Directory:
		return E.New(path, ": not a directory")
	case !directory && facts.Directory:
		return E.New(path, ": not a regular file")
	}
	return nil
}

// checkProtectedAncestors applies the ancestor rule from the volume root
// down to dir's parent, after checking that dir's volume is a fixed NTFS
// drive. A missing component is reported as such.
func checkProtectedAncestors(dir string) error {
	root, err := fixedNTFSVolume(dir)
	if err != nil {
		return err
	}
	chain, err := windowsPathChain(root, filepath.Dir(dir))
	if err != nil {
		return err
	}
	for _, component := range chain {
		facts, readErr := readSecurityFacts(component)
		if readErr != nil {
			return readErr
		}
		if err = aclViolation(component, facts, aclAncestor); err != nil {
			return err
		}
		if !facts.Directory {
			return E.New(component, ": not a directory")
		}
	}
	return nil
}

// windowsPathChain lists root, then every component below it down to path.
func windowsPathChain(root, path string) ([]string, error) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, `..\`) {
		return nil, E.New(path, ": not below its volume root ", root)
	}
	chain := []string{root}
	if relative == "." {
		return chain, nil
	}
	current := root
	for _, part := range strings.Split(relative, `\`) {
		current = filepath.Join(current, part)
		chain = append(chain, current)
	}
	return chain, nil
}

// fixedNTFSVolume returns the volume root of path and refuses anything but a
// fixed NTFS drive: removable and network volumes have no trustworthy ACLs.
func fixedNTFSVolume(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	if err = windows.GetVolumePathName(name, &buffer[0], uint32(len(buffer))); err != nil {
		return "", E.Cause(err, "resolve the volume of ", path)
	}
	root := windows.UTF16ToString(buffer)
	rootName, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", err
	}
	fileSystem := make([]uint16, 64)
	if windows.GetDriveType(rootName) != windows.DRIVE_FIXED ||
		windows.GetVolumeInformation(rootName, nil, 0, nil, nil, nil, &fileSystem[0], uint32(len(fileSystem))) != nil ||
		!strings.EqualFold(windows.UTF16ToString(fileSystem), "NTFS") {
		return "", E.New(path, ": not on a fixed NTFS volume")
	}
	return root, nil
}

// securityAttributes renders an SDDL string for CreateDirectory/CreateFile.
func securityAttributes(sddl string) (*windows.SecurityAttributes, error) {
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}, nil
}

// applySecurity replaces owner and DACL of an open handle with the SDDL's.
// protected cuts inheritance off; otherwise the DACL keeps what the parent
// passes down.
func applySecurity(handle windows.Handle, sddl string, protected bool) error {
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	information := windows.SECURITY_INFORMATION(windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION)
	if protected {
		information |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		information |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, information, owner, nil, dacl, nil)
}

// setOwnerAdministrators takes ownership through a WRITE_OWNER handle — what
// SeTakeOwnershipPrivilege grants even when the DACL denies everything else.
func setOwnerAdministrators(path string) error {
	handle, err := openNoFollow(path, windows.WRITE_OWNER)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, administrators, nil, nil, nil)
}

// protectCopyNode gives the copy dir or a file of the set the owner and DACL
// of SPEC 103 §2.1; changed=false when it already had them.
func protectCopyNode(out io.Writer, path string, directory bool) (bool, error) {
	handle, err := openNoFollow(path, windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER)
	if err != nil {
		return false, &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer windows.CloseHandle(handle)
	facts, _, err := securityOfHandle(handle)
	if err != nil {
		return false, E.Cause(err, "read the security of ", path)
	}
	if facts.ReparsePoint {
		return false, E.New(path, ": is a reparse point, must be a real file or directory")
	}
	if copyNodeInNorm(facts, directory) {
		return false, nil
	}
	sddl := copyFileSDDL
	if directory {
		sddl = copyDirSDDL
	}
	if err = applySecurity(handle, sddl, true); err != nil {
		return false, E.Cause(err, "protect ", path)
	}
	if out != nil && facts.Owner.SID != sidAdministrators {
		fmt.Fprintf(out, "lxd: took ownership of %s (was %s)\n", path, principalName(facts.Owner))
	}
	return true, nil
}

// createProtectedDir makes a directory with its DACL from birth — no window
// in which it inherits the parent's.
func createProtectedDir(path, sddl string) error {
	attributes, err := securityAttributes(sddl)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.CreateDirectory(name, attributes)
}

// ensureCopyDir creates the copy dir with the DACL of SPEC 103 §2.1, or
// brings an existing one to it with a line per change (§2.4 step 2).
func ensureCopyDir(out io.Writer, dir string) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if err = os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return E.Cause(err, "create ", filepath.Dir(dir))
		}
		if err = checkProtectedAncestors(dir); err != nil {
			return err
		}
		if err = createProtectedDir(dir, copyDirSDDL); err != nil {
			return E.Cause(err, "create ", dir)
		}
		fmt.Fprintln(out, "lxd: created", dir, "(Administrators, protected DACL)")
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeIrregular != 0 {
		return E.New(dir, ": is a reparse point, must be a real file or directory")
	}
	if !info.IsDir() {
		return E.New(dir, ": not a directory")
	}
	changed, err := protectCopyNode(out, dir, true)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintln(out, "lxd: replaced DACL on", dir)
	}
	return nil
}

// enablePrivilege switches one privilege of the process token on. The
// syscall wrapper cannot tell "not held" apart from success (the call
// succeeds with ERROR_NOT_ALL_ASSIGNED), so the last error is read directly.
func enablePrivilege(name string) error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return E.Cause(err, "open the process token")
	}
	defer token.Close()
	var luid windows.LUID
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	if err = windows.LookupPrivilegeValue(nil, namePointer, &luid); err != nil {
		return E.Cause(err, "look up ", name)
	}
	privileges := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges:     [1]windows.LUIDAndAttributes{{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}},
	}
	result, _, lastErr := procAdjustTokenPrivileges.Call(uintptr(token), 0, uintptr(unsafe.Pointer(&privileges)), 0, 0, 0)
	if result == 0 {
		return E.Cause(lastErr, "enable ", name)
	}
	if errors.Is(lastErr, windows.ERROR_NOT_ALL_ASSIGNED) {
		return E.New(name, " is not held by this token")
	}
	return nil
}

var procAdjustTokenPrivileges = windows.NewLazySystemDLL("advapi32.dll").NewProc("AdjustTokenPrivileges")
