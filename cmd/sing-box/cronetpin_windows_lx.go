//go:build with_lxd && windows && with_purego && with_naive_outbound

package main

import (
	"os"
	"path/filepath"

	"github.com/sagernet/cronet-go"
)

// lx: SPEC 103 §4.2 p. 2 — naive loads libcronet.dll lazily, from the
// executable's directory and then from every PATH entry; under SYSTEM a
// user-writable directory in the system PATH would hand it a planted
// library. When the set carries no libcronet.dll, the load is pinned to the
// executable's directory up front: it fails, cronet remembers the failure
// (its load runs once), and a naive outbound gets that error instead of a
// PATH search.
func init() {
	pinCronetLibrary = func() {
		executable, err := os.Executable()
		if err != nil {
			return
		}
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			executable = resolved
		}
		library := filepath.Join(filepath.Dir(executable), "libcronet.dll")
		if _, err = os.Lstat(library); err == nil {
			return
		}
		_ = cronet.LoadLibrary(library)
	}
}
