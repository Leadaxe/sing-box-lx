//go:build with_lxd && windows

package main

import "golang.org/x/sys/windows"

// lx: SPEC 103 §2.14 — a privileged core (the service, or a classic run from
// the protected copy) must not load a DLL planted in its working directory
// or in a writable PATH entry. From here on, a library loaded without a full
// path is looked up in the application directory and System32 only; this
// also covers the dependencies of a library loaded by full path. The package
// main init runs after every dependency's and before main.
//
// SetDefaultDllDirectories exists from Windows 8 (Windows 7 with KB2533623);
// the x/sys wrapper panics on a missing procedure, so it is looked up first.
func init() {
	procedure := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetDefaultDllDirectories")
	if procedure.Find() != nil {
		return
	}
	_ = windows.SetDefaultDllDirectories(windows.LOAD_LIBRARY_SEARCH_APPLICATION_DIR | windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
}
