package wireguard

// lx: SPEC 020 level 3 — closing the tun device safely.
//
// Teardown may already have released the tun device, so by the time Close runs
// e.tunDevice can legitimately be nil. Upstream's Close ends in a bare
// `return e.tunDevice.Close()`, which panics on that path.
//
// The guard lives here, as a one-line call site in Close, rather than as a
// multi-line block inlined into that function. Both sides of a merge otherwise
// append to the same spot at the end of Close and git splices them together
// without reporting a conflict: our guard is kept AND upstream's bare return is
// left dangling below it, dead but reachable when tunDevice is nil. That is
// exactly what broke TestPortAddressesSurviveTeardown after the 217-commit
// merge, and the same shape bit us in the 235-commit one. A single call keeps
// our delta small enough that an upstream change to that line conflicts loudly
// instead of merging quietly.
func (e *Endpoint) closeTunDevice() error {
	if e.tunDevice == nil {
		return nil
	}
	err := e.tunDevice.Close()
	e.tunDevice = nil
	return err
}
