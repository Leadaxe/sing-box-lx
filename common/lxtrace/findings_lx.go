//go:build with_lx_command

package lxtrace

import (
	"net/netip"
	"strconv"
	"strings"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// lx: SPEC 139 §6 — findings: codes the core computes from the trace snapshot
// and the core state, with stage references and attributes; the client writes
// the text. Every finder is a pure function of (Summary, FindingState) that
// returns zero or one finding. The handler gathers the state (TUN options,
// fakeip store, route rules) once the trace is finished.

// FindingLevel is the severity of a finding. Numeric values equal the proto
// enum ConnectionTestFindingLevel (SPEC 139 §3.1).
type FindingLevel int

const (
	FindingInfo FindingLevel = iota
	FindingWarning
	FindingProblem
)

// Finding codes (SPEC 139 §6).
const (
	FindingExcludedFromVPN                = "excluded_from_vpn"
	FindingResolveViaRulesInSourceContext = "resolve_via_rules_in_source_context"
	FindingDependencyOutsideRoute         = "dependency_outside_route"
	FindingResolveCycle                   = "resolve_cycle"
	FindingIPRulesNotEvaluated            = "ip_rules_not_evaluated"
	FindingFakeIPNotPersisted             = "fakeip_not_persisted"
	FindingFakeIPPoolWrapped              = "fakeip_pool_wrapped"
	FindingFakeIPUnknownAddress           = "fakeip_unknown_address"
	FindingFakeIPAddressReassigned        = "fakeip_address_reassigned"
	FindingFakeIPIPv6WithoutTUNIPv6       = "fakeip_ipv6_without_tun_ipv6"
	FindingHTTPSQueryRejected             = "https_query_rejected"
	FindingQUICRejected                   = "quic_rejected"
	FindingRealityNotRecognized           = "reality_not_recognized"
	FindingFailedAfterNoAck               = "failed_after_no_ack"
	FindingPartiallyReused                = "partially_reused"
)

// The causes a client lists for a finding whose cause is indistinguishable
// from the client side (SPEC 139 §7).
const (
	realityNotRecognizedCauses = "public_key,short_id,server_names,client_version"
	failedAfterNoAckCauses     = "credentials,server_to_target,mtu"
)

const (
	stageKindRoute     = "route"
	stageKindDial      = "dial"
	stageKindDetour    = "detour"
	stageKindDNS       = "dns"
	stageKindLookup    = "lookup"
	stageKindAppDNS    = "app_dns"
	stageKindGroup     = "group"
	stageKindReality   = "reality"
	resolverDNSRules   = "dns_rules"
	realityVerdictDest = "dest_cert"
)

// Finding is one computed finding.
type Finding struct {
	Code   string
	Level  FindingLevel
	Stages []uint32
	Attrs  []Attr
}

// FindingState is the core state the findings are computed against. The
// handler fills it; every field is optional, a finder whose input is missing
// returns nothing.
type FindingState struct {
	Host      string     // host of the test URL
	ConnectIP netip.Addr // the request's connectIP; invalid when unset
	// Completed is true when the path was completed to the requested depth
	// (no root cause).
	Completed bool

	PackageName string // source package (Android)
	UID         int32  // source uid; 0 — unknown

	// TUN holds the TUN inbounds the source enters: the source inbound when
	// it is a TUN, every TUN inbound when no inbound was given, none when the
	// source inbound is not a TUN.
	TUN []TUNState

	FakeIP *FakeIPState // nil without a fakeip DNS server

	// RouteRules is the route rule table in order: description and action as
	// the route stage names them, and the address conditions of each rule.
	RouteRules []RouteRule

	// NodeTypes maps an outbound or endpoint tag to its type.
	NodeTypes map[string]string

	// QUICRejectedBy names the route rule that rejects udp/443 to the test's
	// host (`route[i] <description> => <action>`); empty when udp/443 is not
	// rejected or was not evaluated.
	QUICRejectedBy string
}

// TUNState is the part of a TUN inbound's options the findings read.
type TUNState struct {
	Tag             string
	IncludePackage  []string
	ExcludePackage  []string
	IncludeUID      []uint32
	ExcludeUID      []uint32
	IncludeUIDRange []string // "start:end"
	ExcludeUIDRange []string
	HasIPv6         bool // the TUN has an IPv6 address
}

// FakeIPState is the read-only state of the fakeip store.
type FakeIPState struct {
	Inet4Range netip.Prefix
	Inet6Range netip.Prefix
	Persisted  bool
	TTL        time.Duration // TTL of a fakeip answer
	Inet4Wrap  FakeIPWrap
	Inet6Wrap  FakeIPWrap
	// The record the store holds for ConnectIP, when it is in a range.
	ConnectIPDomain string
	ConnectIPKnown  bool
	// The address the store currently holds for Host in ConnectIP's family.
	HostAddress netip.Addr
}

// FakeIPWrap is the pool-wrap history of one family.
type FakeIPWrap struct {
	Count    uint64
	Last     time.Time
	Interval time.Duration
}

// RouteRule is one route rule as the findings see it.
type RouteRule struct {
	Description string
	Action      string
	// AddressConditions names the conditions that need the destination
	// address: ip_cidr, ip_is_private, ip_version, rule_set=<tag> of a set
	// with IP CIDR rules.
	AddressConditions []string
}

func (s *FakeIPState) contains(address netip.Addr) bool {
	return s != nil && address.IsValid() && (s.Inet4Range.Contains(address) || s.Inet6Range.Contains(address))
}

// Findings computes every finding of SPEC 139 §6, in the order of its table.
func Findings(summary Summary, state FindingState) []Finding {
	tree := newSnapshotTree(summary)
	finders := []func(*snapshotTree, *FindingState) *Finding{
		findExcludedFromVPN,
		findResolveViaRulesInSourceContext,
		findDependencyOutsideRoute,
		findResolveCycle,
		findIPRulesNotEvaluated,
		findFakeIPNotPersisted,
		findFakeIPPoolWrapped,
		findFakeIPUnknownAddress,
		findFakeIPAddressReassigned,
		findFakeIPIPv6WithoutTUNIPv6,
		findHTTPSQueryRejected,
		findQUICRejected,
		findRealityNotRecognized,
		findFailedAfterNoAck,
		findPartiallyReused,
	}
	var findings []Finding
	for _, finder := range finders {
		if finding := finder(tree, &state); finding != nil {
			findings = append(findings, *finding)
		}
	}
	return findings
}

// snapshotTree indexes the final stage snapshots by id.
type snapshotTree struct {
	stages    []StageSnapshot
	byID      map[uint32]*StageSnapshot
	rootCause uint32
}

func newSnapshotTree(summary Summary) *snapshotTree {
	tree := &snapshotTree{
		stages:    summary.Stages,
		byID:      make(map[uint32]*StageSnapshot, len(summary.Stages)),
		rootCause: summary.RootCause,
	}
	for i := range tree.stages {
		tree.byID[tree.stages[i].ID] = &tree.stages[i]
	}
	return tree
}

func (t *snapshotTree) parent(stage *StageSnapshot) *StageSnapshot {
	if stage.Parent == 0 {
		return nil
	}
	return t.byID[stage.Parent]
}

// ancestor returns the nearest ancestor of stage of one of the kinds, nil
// when there is none.
func (t *snapshotTree) ancestor(stage *StageSnapshot, kinds ...string) *StageSnapshot {
	for current := t.parent(stage); current != nil; current = t.parent(current) {
		for _, kind := range kinds {
			if current.Kind == kind {
				return current
			}
		}
	}
	return nil
}

// isAncestor reports whether a is an ancestor of b.
func (t *snapshotTree) isAncestor(a, b *StageSnapshot) bool {
	for current := t.parent(b); current != nil; current = t.parent(current) {
		if current.ID == a.ID {
			return true
		}
	}
	return false
}

// route returns the top-level route stage, nil when none was recorded.
func (t *snapshotTree) route() *StageSnapshot {
	for i := range t.stages {
		if t.stages[i].Kind == stageKindRoute && t.stages[i].Parent == 0 {
			return &t.stages[i]
		}
	}
	return nil
}

func attrValue(stage *StageSnapshot, key string) string {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}

func attrValues(stage *StageSnapshot, key string) []string {
	var values []string
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			values = append(values, attr.Value)
		}
	}
	return values
}

func hasAttr(stage *StageSnapshot, key string) bool {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return true
		}
	}
	return false
}

func normalizeDomain(domain string) string {
	return strings.ToLower(strings.TrimSuffix(domain, "."))
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// excluded_from_vpn: the source package or uid does not enter the TUN, so the
// production path of the app bypasses the core and the trace is hypothetical.
func findExcludedFromVPN(_ *snapshotTree, state *FindingState) *Finding {
	if state.PackageName == "" && state.UID <= 0 {
		return nil
	}
	for _, tun := range state.TUN {
		reason, value := tunExclusion(tun, state.PackageName, state.UID)
		if reason == "" {
			continue
		}
		return &Finding{
			Code:  FindingExcludedFromVPN,
			Level: FindingWarning,
			Attrs: []Attr{{"inbound", tun.Tag}, {"reason", reason}, {"value", value}},
		}
	}
	return nil
}

func tunExclusion(tun TUNState, packageName string, uid int32) (reason string, value string) {
	if packageName != "" && containsString(tun.ExcludePackage, packageName) {
		return "exclude_package", packageName
	}
	if uid > 0 {
		if containsUID(tun.ExcludeUID, uint32(uid)) {
			return "exclude_uid", strconv.Itoa(int(uid))
		}
		if uidInRanges(tun.ExcludeUIDRange, uint32(uid)) {
			return "exclude_uid_range", strconv.Itoa(int(uid))
		}
	}
	if len(tun.IncludePackage) == 0 && len(tun.IncludeUID) == 0 && len(tun.IncludeUIDRange) == 0 {
		return "", ""
	}
	if packageName != "" && containsString(tun.IncludePackage, packageName) {
		return "", ""
	}
	if uid > 0 && (containsUID(tun.IncludeUID, uint32(uid)) || uidInRanges(tun.IncludeUIDRange, uint32(uid))) {
		return "", ""
	}
	// Not in any include list is a verdict only when every list in use could
	// be checked: a source given by uid alone may still be a listed package
	// (Android admits it by package), and a package without its uid may still
	// be in a uid list.
	if len(tun.IncludePackage) > 0 && packageName == "" {
		return "", ""
	}
	if (len(tun.IncludeUID) > 0 || len(tun.IncludeUIDRange) > 0) && uid <= 0 {
		return "", ""
	}
	if packageName != "" {
		return "include_package", packageName
	}
	return "include_uid", strconv.Itoa(int(uid))
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func containsUID(list []uint32, uid uint32) bool {
	for _, item := range list {
		if item == uid {
			return true
		}
	}
	return false
}

func uidInRanges(ranges []string, uid uint32) bool {
	for _, uidRange := range ranges {
		startText, endText, found := strings.Cut(uidRange, ":")
		if !found {
			continue
		}
		start, startErr := strconv.ParseUint(startText, 10, 32)
		end, endErr := strconv.ParseUint(endText, 10, 32)
		if startErr == nil && endErr == nil && uint64(uid) >= start && uint64(uid) <= end {
			return true
		}
	}
	return false
}

// resolve_via_rules_in_source_context: a `lookup` by the DNS rules under a
// `detour` — the node has a detour and no domain_resolver, so its server
// address is resolved by the rules in the context of this source.
func findResolveViaRulesInSourceContext(tree *snapshotTree, _ *FindingState) *Finding {
	var finding *Finding
	for i := range tree.stages {
		stage := &tree.stages[i]
		if stage.Kind != stageKindLookup || attrValue(stage, "resolver") != resolverDNSRules {
			continue
		}
		detour := tree.ancestor(stage, stageKindDetour)
		if detour == nil {
			continue
		}
		if finding == nil {
			finding = &Finding{Code: FindingResolveViaRulesInSourceContext, Level: FindingInfo}
		}
		finding.Stages = append(finding.Stages, stage.ID)
		if node := lookupOwner(tree, stage); node != "" {
			finding.Attrs = append(finding.Attrs, Attr{"node", node})
		}
		finding.Attrs = append(finding.Attrs, Attr{"domain", stage.Subject}, Attr{"detour", detour.Subject})
	}
	return finding
}

// lookupOwner names the node whose server address a lookup resolves. A
// domain handed to a detour belongs to the hop that handed it (the stage
// above the detour); any other lookup belongs to its nearest dial or detour.
func lookupOwner(tree *snapshotTree, lookup *StageSnapshot) string {
	hop := tree.ancestor(lookup, stageKindDial, stageKindDetour)
	if hop == nil {
		return ""
	}
	if hop.Kind == stageKindDetour && destinationHost(attrValue(hop, "destination")) == normalizeDomain(lookup.Subject) {
		if owner := tree.ancestor(hop, stageKindDial, stageKindDetour); owner != nil {
			return owner.Subject
		}
	}
	return hop.Subject
}

func destinationHost(destination string) string {
	if destination == "" {
		return ""
	}
	address := M.ParseSocksaddr(destination)
	if address.IsFqdn() {
		return normalizeDomain(address.Fqdn)
	}
	return address.AddrString()
}

// dependency_outside_route: a node that carries a DNS query the path needs
// (a DNS server's detour) but is not on the route itself.
func findDependencyOutsideRoute(tree *snapshotTree, state *FindingState) *Finding {
	onRoute := make(map[string]bool)
	for i := range tree.stages {
		stage := &tree.stages[i]
		if tree.ancestor(stage, stageKindDNS) != nil {
			continue
		}
		switch stage.Kind {
		case stageKindDial, stageKindDetour, stageKindGroup:
			onRoute[stage.Subject] = true
			if selected := attrValue(stage, "selected"); selected != "" {
				onRoute[selected] = true
			}
			for _, via := range strings.Split(attrValue(stage, "via_group"), ",") {
				if via != "" {
					onRoute[via] = true
				}
			}
		case stageKindRoute:
			if outbound := attrValue(stage, "outbound"); outbound != "" {
				onRoute[outbound] = true
			}
		}
	}
	var (
		finding  *Finding
		reported = make(map[string]int) // node → index of its `dependents` attr
		problem  bool
	)
	root := tree.byID[tree.rootCause]
	for i := range tree.stages {
		stage := &tree.stages[i]
		if stage.Kind != stageKindDial && stage.Kind != stageKindDetour {
			continue
		}
		if onRoute[stage.Subject] || !isProxyNode(state, stage.Subject) {
			continue
		}
		dns := tree.ancestor(stage, stageKindDNS)
		if dns == nil {
			continue
		}
		if root != nil && (root.ID == stage.ID || tree.isAncestor(stage, root)) {
			problem = true
		}
		dependent := dependentOf(tree, dns)
		if finding == nil {
			finding = &Finding{Code: FindingDependencyOutsideRoute}
		}
		finding.Stages = append(finding.Stages, stage.ID)
		if index, seen := reported[stage.Subject]; seen {
			if dependent != "" {
				var dependents []string
				if current := finding.Attrs[index].Value; current != "" {
					dependents = strings.Split(current, ",")
				}
				finding.Attrs[index].Value = strings.Join(appendUnique(dependents, dependent), ",")
			}
			continue
		}
		finding.Attrs = append(finding.Attrs, Attr{"node", stage.Subject}, Attr{"dns_server", dns.Subject})
		reported[stage.Subject] = len(finding.Attrs)
		finding.Attrs = append(finding.Attrs, Attr{"dependents", dependent})
	}
	if finding != nil && problem {
		finding.Level = FindingProblem
	}
	return finding
}

// dependentOf names what needs the answer of a DNS stage: the node whose
// server address the enclosing lookup resolves, or app_dns for the app's own
// query.
func dependentOf(tree *snapshotTree, dns *StageSnapshot) string {
	if lookup := tree.ancestor(dns, stageKindLookup); lookup != nil {
		if owner := lookupOwner(tree, lookup); owner != "" {
			return owner
		}
		return lookup.Subject
	}
	if appDNS := tree.ancestor(dns, stageKindAppDNS); appDNS != nil {
		return stageKindAppDNS
	}
	return ""
}

func isProxyNode(state *FindingState, tag string) bool {
	switch state.NodeTypes[tag] {
	case "direct", "block", "dns":
		return false
	}
	return tag != ""
}

// resolve_cycle: a lookup re-entered the same resolver/domain pair (§5.5).
func findResolveCycle(tree *snapshotTree, _ *FindingState) *Finding {
	var finding *Finding
	for i := range tree.stages {
		stage := &tree.stages[i]
		cycleWith := attrValue(stage, "cycle_with")
		if stage.Kind != stageKindLookup || cycleWith == "" {
			continue
		}
		if finding == nil {
			finding = &Finding{Code: FindingResolveCycle, Level: FindingProblem}
		}
		finding.Stages = append(finding.Stages, stage.ID)
		if ancestor, err := strconv.ParseUint(cycleWith, 10, 32); err == nil {
			finding.Stages = append(finding.Stages, uint32(ancestor))
		}
		finding.Attrs = append(finding.Attrs, Attr{"domain", stage.Subject}, Attr{"resolver", attrValue(stage, "resolver")})
	}
	return finding
}

// ip_rules_not_evaluated: the route decision was made while the destination
// address was unknown (a domain without resolve, a fakeip domain), so the
// rules above the selected one that need the address could not match. A rule
// above the selected one did not match by definition (PLAN §5): no insertion
// in matchRule is needed.
func findIPRulesNotEvaluated(tree *snapshotTree, state *FindingState) *Finding {
	route := tree.route()
	if route == nil || route.Result != OK || len(state.RouteRules) == 0 {
		return nil
	}
	destination := M.ParseSocksaddr(route.Subject)
	if !destination.IsFqdn() || hasAttr(route, "addresses") {
		return nil
	}
	selected := selectedRuleIndex(route, state.RouteRules)
	if selected < 0 {
		return nil
	}
	var rules []string
	for i := 0; i < selected; i++ {
		rule := state.RouteRules[i]
		if len(rule.AddressConditions) == 0 {
			continue
		}
		text := "route[" + strconv.Itoa(i) + "]"
		if rule.Description != "" {
			text += " " + rule.Description
		}
		rules = append(rules, text+" ("+strings.Join(rule.AddressConditions, ", ")+")")
	}
	if len(rules) == 0 {
		return nil
	}
	finding := &Finding{
		Code:   FindingIPRulesNotEvaluated,
		Level:  FindingWarning,
		Stages: []uint32{route.ID},
		Attrs:  []Attr{{"reason", "address_unknown"}, {"domain_origin", attrValue(route, "domain_origin")}},
	}
	for _, rule := range rules {
		finding.Attrs = append(finding.Attrs, Attr{"rule", rule})
	}
	return finding
}

// selectedRuleIndex finds the rule the route stage names: `final` is past the
// last rule; otherwise the first rule with the same description and action.
func selectedRuleIndex(route *StageSnapshot, rules []RouteRule) int {
	ruleText := attrValue(route, "rule")
	if ruleText == "final" {
		return len(rules)
	}
	action := attrValue(route, "action")
	for i, rule := range rules {
		if rule.Description == ruleText && rule.Action == action {
			return i
		}
	}
	return -1
}

// involvesFakeIP reports whether the test touched the fakeip store: connectIP
// in a range, a fakeip domain on the route, or an app_dns answer in a range.
func involvesFakeIP(tree *snapshotTree, state *FindingState) bool {
	if state.FakeIP == nil {
		return false
	}
	if state.FakeIP.contains(state.ConnectIP) {
		return true
	}
	if route := tree.route(); route != nil && attrValue(route, "domain_origin") == "fakeip" {
		return true
	}
	for i := range tree.stages {
		stage := &tree.stages[i]
		if stage.Kind != stageKindAppDNS {
			continue
		}
		for _, answer := range attrValues(stage, "answer") {
			if state.FakeIP.contains(answerAddress(answer)) {
				return true
			}
		}
	}
	return false
}

// answerAddress returns the address of an app_dns `answer` attribute
// (`A 600 198.18.0.1`, `name. AAAA 600 fc00::1`), invalid for other records.
func answerAddress(answer string) netip.Addr {
	fields := strings.Fields(answer)
	for i, field := range fields {
		if (field == "A" || field == "AAAA") && i+2 < len(fields) {
			address, err := netip.ParseAddr(fields[i+2])
			if err == nil {
				return address
			}
		}
	}
	return netip.Addr{}
}

// fakeip_not_persisted: the store keeps its records in memory only.
func findFakeIPNotPersisted(tree *snapshotTree, state *FindingState) *Finding {
	if !involvesFakeIP(tree, state) || state.FakeIP.Persisted {
		return nil
	}
	return &Finding{
		Code:  FindingFakeIPNotPersisted,
		Level: FindingWarning,
		Attrs: []Attr{{"storage", "memory"}},
	}
}

// fakeip_pool_wrapped: a pass over a range took less than the answer TTL, so
// an address an app still caches may already name another domain.
func findFakeIPPoolWrapped(tree *snapshotTree, state *FindingState) *Finding {
	if !involvesFakeIP(tree, state) || state.FakeIP.TTL <= 0 {
		return nil
	}
	var finding *Finding
	for _, family := range []struct {
		name   string
		prefix netip.Prefix
		wrap   FakeIPWrap
	}{
		{"inet4", state.FakeIP.Inet4Range, state.FakeIP.Inet4Wrap},
		{"inet6", state.FakeIP.Inet6Range, state.FakeIP.Inet6Wrap},
	} {
		if !family.prefix.IsValid() || family.wrap.Interval == 0 || family.wrap.Interval >= state.FakeIP.TTL {
			continue
		}
		if finding == nil {
			finding = &Finding{Code: FindingFakeIPPoolWrapped, Level: FindingWarning}
		}
		finding.Attrs = append(finding.Attrs,
			Attr{"range", family.prefix.String()},
			Attr{"size", prefixSize(family.prefix)},
			Attr{"wraps", strconv.FormatUint(family.wrap.Count, 10)},
			Attr{"wrap_time", strconv.FormatInt(int64(family.wrap.Interval/time.Second), 10)},
			Attr{"last_wrap", family.wrap.Last.UTC().Format(time.RFC3339)},
			Attr{"ttl", strconv.FormatInt(int64(state.FakeIP.TTL/time.Second), 10)},
		)
	}
	return finding
}

// prefixSize is the number of addresses in prefix: decimal below 2^63, else 2^N.
func prefixSize(prefix netip.Prefix) string {
	hostBits := prefix.Addr().BitLen() - prefix.Bits()
	if hostBits < 63 {
		return strconv.FormatUint(uint64(1)<<hostBits, 10)
	}
	return "2^" + strconv.Itoa(hostBits)
}

// fakeip_unknown_address: connectIP is a fakeip address the store has no
// record for; the router refuses the connection.
func findFakeIPUnknownAddress(tree *snapshotTree, state *FindingState) *Finding {
	if !state.FakeIP.contains(state.ConnectIP) || state.FakeIP.ConnectIPKnown {
		return nil
	}
	finding := &Finding{
		Code:  FindingFakeIPUnknownAddress,
		Level: FindingProblem,
		Attrs: []Attr{{"address", state.ConnectIP.String()}, {"range", fakeIPRangeOf(state.FakeIP, state.ConnectIP)}},
	}
	if route := tree.route(); route != nil {
		finding.Stages = []uint32{route.ID}
	}
	return finding
}

// fakeip_address_reassigned: connectIP is a fakeip address that now belongs
// to another domain; the connection goes there, not to the test's host.
func findFakeIPAddressReassigned(tree *snapshotTree, state *FindingState) *Finding {
	if !state.FakeIP.contains(state.ConnectIP) || !state.FakeIP.ConnectIPKnown || state.Host == "" {
		return nil
	}
	if normalizeDomain(state.FakeIP.ConnectIPDomain) == normalizeDomain(state.Host) {
		return nil
	}
	finding := &Finding{
		Code:  FindingFakeIPAddressReassigned,
		Level: FindingProblem,
		Attrs: []Attr{
			{"address", state.ConnectIP.String()},
			{"domain", state.FakeIP.ConnectIPDomain},
			{"expected", state.Host},
		},
	}
	if state.FakeIP.HostAddress.IsValid() {
		finding.Attrs = append(finding.Attrs, Attr{"expected_address", state.FakeIP.HostAddress.String()})
	}
	if route := tree.route(); route != nil {
		finding.Stages = []uint32{route.ID}
	}
	return finding
}

func fakeIPRangeOf(state *FakeIPState, address netip.Addr) string {
	if state.Inet4Range.Contains(address) {
		return state.Inet4Range.String()
	}
	return state.Inet6Range.String()
}

// fakeip_ipv6_without_tun_ipv6: AAAA answers with an IPv6 fakeip address, but
// no TUN the source enters has an IPv6 address, so the app cannot reach it.
func findFakeIPIPv6WithoutTUNIPv6(tree *snapshotTree, state *FindingState) *Finding {
	if state.FakeIP == nil || !state.FakeIP.Inet6Range.IsValid() || len(state.TUN) == 0 {
		return nil
	}
	for _, tun := range state.TUN {
		if tun.HasIPv6 {
			return nil
		}
	}
	for i := range tree.stages {
		stage := &tree.stages[i]
		if stage.Kind != stageKindAppDNS || attrValue(stage, "type") != "AAAA" {
			continue
		}
		for _, answer := range attrValues(stage, "answer") {
			address := answerAddress(answer)
			if !state.FakeIP.Inet6Range.Contains(address) {
				continue
			}
			return &Finding{
				Code:   FindingFakeIPIPv6WithoutTUNIPv6,
				Level:  FindingWarning,
				Stages: []uint32{stage.ID},
				Attrs:  []Attr{{"address", address.String()}, {"inbound", state.TUN[0].Tag}},
			}
		}
	}
	return nil
}

// https_query_rejected: the app's HTTPS query was rejected by a DNS rule, so
// the client gets no ECH and no ALPN hints.
func findHTTPSQueryRejected(tree *snapshotTree, _ *FindingState) *Finding {
	for i := range tree.stages {
		stage := &tree.stages[i]
		if stage.Kind != stageKindAppDNS || attrValue(stage, "type") != "HTTPS" {
			continue
		}
		rules := attrValues(stage, "dns_rule")
		if len(rules) == 0 {
			continue
		}
		rule := rules[len(rules)-1]
		if !strings.HasPrefix(dnsRuleAction(rule), "reject") {
			continue
		}
		return &Finding{
			Code:   FindingHTTPSQueryRejected,
			Level:  FindingInfo,
			Stages: []uint32{stage.ID},
			Attrs:  []Attr{{"rule", rule}},
		}
	}
	return nil
}

// dnsRuleAction extracts the action of a `dns_rule` attribute
// (`dns[0] query_type=HTTPS => reject (HTTPS)`).
func dnsRuleAction(rule string) string {
	index := strings.LastIndex(rule, " => ")
	if index < 0 {
		return ""
	}
	return rule[index+len(" => "):]
}

// quic_rejected: udp/443 is rejected by a route rule, so a browser falls back
// to TCP. A UDP test reads it from its own route stage; a TCP test from the
// handler's evaluation of the rules for udp/443 to the same host.
func findQUICRejected(tree *snapshotTree, state *FindingState) *Finding {
	if route := tree.route(); route != nil && attrValue(route, "network") == "udp" &&
		strings.HasPrefix(attrValue(route, "action"), "reject") && M.ParseSocksaddr(route.Subject).Port == 443 {
		return &Finding{
			Code:   FindingQUICRejected,
			Level:  FindingInfo,
			Stages: []uint32{route.ID},
			Attrs:  []Attr{{"rule", attrValue(route, "rule")}},
		}
	}
	if state.QUICRejectedBy == "" {
		return nil
	}
	return &Finding{
		Code:  FindingQUICRejected,
		Level: FindingInfo,
		Attrs: []Attr{{"rule", state.QUICRejectedBy}},
	}
}

// reality_not_recognized: the REALITY server served the dest certificate.
// The causes are indistinguishable from the client side and are listed.
func findRealityNotRecognized(tree *snapshotTree, _ *FindingState) *Finding {
	var finding *Finding
	for i := range tree.stages {
		stage := &tree.stages[i]
		if stage.Kind != stageKindReality || attrValue(stage, "verdict") != realityVerdictDest {
			continue
		}
		if finding == nil {
			finding = &Finding{Code: FindingRealityNotRecognized, Level: FindingProblem}
		}
		finding.Stages = append(finding.Stages, stage.ID)
		if hop := tree.ancestor(stage, stageKindDial, stageKindDetour); hop != nil {
			finding.Attrs = append(finding.Attrs, Attr{"node", hop.Subject})
		}
		finding.Attrs = append(finding.Attrs, Attr{"server_name", stage.Subject})
	}
	if finding != nil {
		finding.Attrs = append(finding.Attrs, Attr{"causes", realityNotRecognizedCauses})
	}
	return finding
}

// failed_after_no_ack: the path broke after a stage the protocol does not
// confirm (a VLESS/Trojan/Shadowsocks header): the server may have refused the
// credentials, failed to reach the target, or lost large packets.
func findFailedAfterNoAck(tree *snapshotTree, _ *FindingState) *Finding {
	root := tree.byID[tree.rootCause]
	if root == nil {
		return nil
	}
	rootEnd := uint64(root.StartMs) + uint64(root.DurationMs)
	var noAck *StageSnapshot
	for i := range tree.stages {
		stage := &tree.stages[i]
		if stage.Result != NoAck || stage.ID == root.ID || uint64(stage.StartMs) > rootEnd {
			continue
		}
		if tree.isAncestor(root, stage) {
			continue // a failure enclosing the header is not "after" it
		}
		noAck = stage // the last header sent before the failure
	}
	if noAck == nil {
		return nil
	}
	finding := &Finding{
		Code:   FindingFailedAfterNoAck,
		Level:  FindingProblem,
		Stages: []uint32{noAck.ID, root.ID},
	}
	if hop := tree.ancestor(noAck, stageKindDial, stageKindDetour); hop != nil {
		finding.Attrs = append(finding.Attrs, Attr{"node", hop.Subject})
	}
	if protocol := attrValue(noAck, "protocol"); protocol != "" {
		finding.Attrs = append(finding.Attrs, Attr{"protocol", protocol})
	}
	finding.Attrs = append(finding.Attrs, Attr{"failed", root.Kind}, Attr{"causes", failedAfterNoAckCauses})
	return finding
}

// partially_reused: the path was completed, but some stages were REUSED, so
// the result does not prove the path comes up from scratch.
func findPartiallyReused(tree *snapshotTree, state *FindingState) *Finding {
	if !state.Completed || tree.rootCause != 0 {
		return nil
	}
	var (
		stages []uint32
		kinds  []string
	)
	for i := range tree.stages {
		stage := &tree.stages[i]
		if stage.Result != Reused {
			continue
		}
		stages = append(stages, stage.ID)
		kinds = appendUnique(kinds, stage.Kind)
	}
	if len(stages) == 0 {
		return nil
	}
	return &Finding{
		Code:   FindingPartiallyReused,
		Level:  FindingInfo,
		Stages: stages,
		Attrs:  []Attr{{"count", strconv.Itoa(len(stages))}, {"kinds", strings.Join(kinds, ",")}},
	}
}
