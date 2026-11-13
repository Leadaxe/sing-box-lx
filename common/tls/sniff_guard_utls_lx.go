//go:build with_utls

package tls

// lx: SPEC 060 — the uTLS engines under lx.sniff_guard; REALITY writes its
// ClientHello through the wrapper of its uTLS config (SPEC 088).

func (c *UTLSClientConfig) sniffGuardNodeLx() sniffGuardNode {
	return sniffGuardNode{c.fragment, c.recordFragment, c.fragmentChosen, c.fragmentFallbackDelay}
}

func (e *RealityClientConfig) sniffGuardNodeLx() sniffGuardNode {
	return e.uClient.sniffGuardNodeLx()
}
