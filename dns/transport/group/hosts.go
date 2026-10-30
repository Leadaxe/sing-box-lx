package group

// SPEC 143 — the `hosts` layer of a group. Servers listed under `hosts` are
// asked in key order before target selection:
//
//   - hit: an A/AAAA query for a name the member knows (PreferredDomain of
//     the canonical name, the check the preferred_by rule item uses) that the
//     member answers with NOERROR — the response is returned as is, including
//     an empty NOERROR when the name has addresses of the other family only;
//   - miss: anything else, including a member that claimed the name and then
//     answered otherwise (a hosts file re-read in between) — the next hosts
//     member is asked, then the query takes the network path unchanged.
//
// Hosts members have no health: no ERROR or WIN records, no fan, no survival,
// never current, absent from GroupState. A miss records nothing.

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"

	mDNS "github.com/miekg/dns"
)

type hostsMember struct {
	member
	preferred adapter.DNSTransportWithPreferredDomain
}

// startHosts resolves the `hosts` tags. Only the hosts type is accepted: the
// layer relies on PreferredDomain and on NXDOMAIN meaning "not mine".
func (t *Transport) startHosts(transportManager adapter.DNSTransportManager) error {
	var hostsMembers []*hostsMember
	for _, hostsTag := range t.hostsTags {
		rawTransport, loaded := transportManager.Transport(hostsTag)
		if !loaded {
			return E.New("group[", t.Tag(), "]: DNS server not found: ", hostsTag)
		}
		if rawTransport.Type() != C.DNSTypeHosts {
			return E.New("group[", t.Tag(), "]: hosts accepts only servers of type hosts, got ", rawTransport.Type(), ": ", hostsTag)
		}
		preferred, isPreferred := rawTransport.(adapter.DNSTransportWithPreferredDomain)
		if !isPreferred {
			return E.New("group[", t.Tag(), "]: server of type hosts does not report the names it knows: ", hostsTag)
		}
		hostsMembers = append(hostsMembers, &hostsMember{
			member:    member{tag: hostsTag, transport: rawTransport},
			preferred: preferred,
		})
	}
	// An Exchange may arrive before Start (see selectTarget).
	t.access.Lock()
	t.hosts = hostsMembers
	t.access.Unlock()
	return nil
}

// exchangeHosts runs the layer; the bool reports a hit. The exchange is local and
// never blocks, so it runs outside access.
func (t *Transport) exchangeHosts(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, bool) {
	if len(t.hostsTags) == 0 || len(message.Question) == 0 {
		return nil, false
	}
	question := message.Question[0]
	if question.Qtype != mDNS.TypeA && question.Qtype != mDNS.TypeAAAA {
		return nil, false
	}
	t.access.Lock()
	hostsMembers := t.hosts
	t.access.Unlock()
	domain := mDNS.CanonicalName(question.Name)
	for _, current := range hostsMembers {
		if !current.preferred.PreferredDomain(domain) {
			continue
		}
		response, err, rtt := t.timedExchange(ctx, &current.member, message)
		if err != nil || response == nil || response.Rcode != mDNS.RcodeSuccess {
			continue
		}
		// The dns_member stage (SPEC 139) is recorded once the hit is known:
		// a stage is visible from its Begin, and a miss must leave none. The
		// exchange dials nothing, so no child stage is lost.
		_, stage := beginMemberLx(ctx, &current.member)
		endMemberLx(ctx, stage, response, nil)
		t.traceAttempt(ctx, &current.member, response, nil, rtt)
		t.recordEffective(ctx, &current.member)
		t.logger.DebugContext(ctx, "group[", t.Tag(), "]: hosts hit via ", current.tag)
		return response, true
	}
	return nil, false
}
