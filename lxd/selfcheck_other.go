//go:build with_lxd && !darwin && !windows

package lxd

// platformSelfCheckEnv: the self-check guards the service this core installs
// itself — launchd on macOS, the SCM on Windows. Elsewhere `--service` only
// prints a recipe and the operator picks the binary, so nothing is checked
// and nothing is logged (SPEC 132).
func platformSelfCheckEnv(daemon bool) selfCheckEnv {
	return selfCheckEnv{}
}
