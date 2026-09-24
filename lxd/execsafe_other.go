//go:build with_lxd && !unix && !windows

package lxd

import E "github.com/sagernet/sing/common/exceptions"

// lstatOwner: file ownership is a unix notion; the root-owned invariant
// cannot be evaluated here, so every walk fails closed.
func lstatOwner(path string) (ownerInfo, error) {
	return ownerInfo{}, E.New(path, ": file ownership is not available on this platform")
}

// platformSelfCheckEnv: no privileged service exists here, nothing to check.
func platformSelfCheckEnv(daemon bool) selfCheckEnv {
	return selfCheckEnv{}
}
