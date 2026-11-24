package masque

// lx:begin SPEC 054 / SPEC 138
//
// MASQUE carries IP packets (CONNECT-IP) and opens every connection itself on the
// gVisor stack of its session: a site that refuses answers with an RST, and the
// open fails with ECONNREFUSED — the site's answer, not a dead node.

import "github.com/sagernet/sing-box/adapter"

var _ adapter.DestinationDialerLx = (*Outbound)(nil)

func (o *Outbound) DialsDestinationLx() bool { return true }

// lx:end SPEC 054 / SPEC 138
