package v2rayxhttp

// lx: SPEC 060 — the requests of an XHTTP conn ride a context under the
// transport lifetime (SPEC 072), so the TLS handshake of a pooled HTTP
// connection the dial raises would not see the lx.sniff_guard attempt and the
// carrier record of the dial. They are carried over the way the connection
// test trace is.

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxsniffguard"
)

// sniffGuardRequestContext returns parent with the sniff guard attempt and
// the carrier record of the dial context dialCtx, when it has them.
func sniffGuardRequestContext(dialCtx, parent context.Context) context.Context {
	parent = lxsniffguard.ContextWithAttempt(parent, lxsniffguard.AttemptFromContext(dialCtx))
	return adapter.ContextWithExistingCarrierRecord(parent, adapter.CarrierRecordFromContext(dialCtx))
}
