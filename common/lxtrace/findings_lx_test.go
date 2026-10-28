//go:build with_lx_command

package lxtrace

import (
	"net/netip"
	"reflect"
	"testing"
	"time"
)

// lx: SPEC 139 §6 — every finding code on a synthetic trace: one positive
// case and the negative case closest to it.

// traceBuilder assembles final stage snapshots: ids in begin order, each
// stage 10 ms after the previous one and lasting 5 ms unless set.
type traceBuilder struct {
	stages    []StageSnapshot
	rootCause uint32
}

func (b *traceBuilder) add(parent uint32, kind, subject string, result Result, attrs ...string) uint32 {
	id := uint32(len(b.stages) + 1)
	stage := StageSnapshot{
		ID:         id,
		Parent:     parent,
		Kind:       kind,
		Subject:    subject,
		StartMs:    id * 10,
		DurationMs: 5,
		Result:     result,
	}
	for i := 0; i+1 < len(attrs); i += 2 {
		stage.Attrs = append(stage.Attrs, Attr{Key: attrs[i], Value: attrs[i+1]})
	}
	if result == Failed {
		stage.Error = "failed"
	}
	b.stages = append(b.stages, stage)
	return id
}

func (b *traceBuilder) summary() Summary {
	return Summary{Stages: b.stages, RootCause: b.rootCause}
}

func findingOf(findings []Finding, code string) *Finding {
	for i := range findings {
		if findings[i].Code == code {
			return &findings[i]
		}
	}
	return nil
}

func findingAttr(finding *Finding, key string) string {
	for _, attr := range finding.Attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}

func requireFinding(t *testing.T, findings []Finding, code string, level FindingLevel) *Finding {
	t.Helper()
	finding := findingOf(findings, code)
	if finding == nil {
		t.Fatalf("no %s in %+v", code, findings)
	}
	if finding.Level != level {
		t.Fatalf("%s level = %d, want %d", code, finding.Level, level)
	}
	return finding
}

func requireNoFinding(t *testing.T, findings []Finding, code string) {
	t.Helper()
	if finding := findingOf(findings, code); finding != nil {
		t.Fatalf("unexpected %s: %+v", code, finding)
	}
}

func TestFindingsEmptyTrace(t *testing.T) {
	if findings := Findings(Summary{}, FindingState{}); len(findings) != 0 {
		t.Fatalf("findings on an empty trace: %+v", findings)
	}
}

func TestFindingExcludedFromVPN(t *testing.T) {
	cases := []struct {
		name   string
		tun    TUNState
		pkg    string
		uid    int32
		reason string
	}{
		{"exclude_package", TUNState{Tag: "tun-in", ExcludePackage: []string{"com.app"}}, "com.app", 0, "exclude_package"},
		{"include_package_missing", TUNState{Tag: "tun-in", IncludePackage: []string{"com.other"}}, "com.app", 0, "include_package"},
		{"include_package_listed", TUNState{Tag: "tun-in", IncludePackage: []string{"com.app"}}, "com.app", 0, ""},
		{"exclude_uid", TUNState{Tag: "tun-in", ExcludeUID: []uint32{10123}}, "", 10123, "exclude_uid"},
		{"exclude_uid_range", TUNState{Tag: "tun-in", ExcludeUIDRange: []string{"10000:10200"}}, "", 10123, "exclude_uid_range"},
		{"include_uid_range_hit", TUNState{Tag: "tun-in", IncludeUIDRange: []string{"10000:10200"}}, "", 10123, ""},
		{"include_uid_missing", TUNState{Tag: "tun-in", IncludeUID: []uint32{1}}, "", 10123, "include_uid"},
		{"no_lists", TUNState{Tag: "tun-in"}, "com.app", 10123, ""},
		// A uid-only source may be a listed package; a package without its uid
		// may be in a uid list: neither is a verdict.
		{"include_package_uid_only", TUNState{Tag: "tun-in", IncludePackage: []string{"com.app"}}, "", 10123, ""},
		{"include_uid_package_only", TUNState{Tag: "tun-in", IncludeUID: []uint32{10123}}, "com.app", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := Findings(Summary{}, FindingState{PackageName: tc.pkg, UID: tc.uid, TUN: []TUNState{tc.tun}})
			if tc.reason == "" {
				requireNoFinding(t, findings, FindingExcludedFromVPN)
				return
			}
			finding := requireFinding(t, findings, FindingExcludedFromVPN, FindingWarning)
			if findingAttr(finding, "reason") != tc.reason || findingAttr(finding, "inbound") != "tun-in" {
				t.Fatalf("attrs = %+v, want reason %s", finding.Attrs, tc.reason)
			}
		})
	}
	// No source identity, or no TUN the source enters: nothing to say.
	requireNoFinding(t, Findings(Summary{}, FindingState{TUN: []TUNState{{ExcludePackage: []string{"x"}}}}), FindingExcludedFromVPN)
	requireNoFinding(t, Findings(Summary{}, FindingState{PackageName: "x"}), FindingExcludedFromVPN)
}

// Appendix A: vless-nl has detour awg-de and no domain_resolver; awg-de
// resolves nl.example.net by the DNS rules.
func appendixTrace() (*traceBuilder, map[string]uint32) {
	b := &traceBuilder{}
	ids := make(map[string]uint32)
	ids["route"] = b.add(0, "route", "198.18.0.42:443", OK, "rule", "domain_suffix=youtube.com", "action", "route(proxy)", "outbound", "proxy", "domain_origin", "fakeip")
	ids["group"] = b.add(0, "group", "proxy", OK, "selected", "vless-nl")
	ids["dial"] = b.add(0, "dial", "vless-nl", OK, "destination", "www.youtube.com:443")
	ids["detour"] = b.add(ids["dial"], "detour", "awg-de", OK, "destination", "nl.example.net:443", "passed", "domain")
	ids["lookup"] = b.add(ids["detour"], "lookup", "nl.example.net", OK, "resolver", "dns_rules")
	ids["dns"] = b.add(ids["lookup"], "dns", "dns-remote", OK)
	ids["dns_dial"] = b.add(ids["dns"], "detour", "ss-pl", OK, "destination", "dns.example.org:443")
	ids["dns_header"] = b.add(ids["dns_dial"], "proxy_header", "dns.example.org:443", NoAck)
	ids["wake_lookup"] = b.add(ids["detour"], "lookup", "de.example.net", OK, "resolver", "domain_resolver:dns-remote")
	ids["wake_dns"] = b.add(ids["wake_lookup"], "dns", "dns-remote", OK)
	ids["wake_dns_dial"] = b.add(ids["wake_dns"], "detour", "ss-pl", OK, "destination", "dns.example.org:443")
	ids["tcp"] = b.add(ids["detour"], "tcp_connect", "198.51.100.20:443", OK, "inside", "awg-de")
	ids["tls"] = b.add(ids["dial"], "tls", "www.microsoft.com", OK)
	ids["reality"] = b.add(ids["tls"], "reality", "www.microsoft.com", OK, "verdict", "accepted")
	ids["header"] = b.add(ids["dial"], "proxy_header", "www.youtube.com:443", NoAck, "protocol", "vless")
	ids["http"] = b.add(0, "http", "https://www.youtube.com/generate_204", OK, "status", "204")
	return b, ids
}

func TestFindingResolveViaRulesInSourceContext(t *testing.T) {
	b, ids := appendixTrace()
	finding := requireFinding(t, Findings(b.summary(), FindingState{}), FindingResolveViaRulesInSourceContext, FindingInfo)
	if !reflect.DeepEqual(finding.Stages, []uint32{ids["lookup"]}) {
		t.Fatalf("stages = %v, want the dns_rules lookup %d", finding.Stages, ids["lookup"])
	}
	if findingAttr(finding, "node") != "vless-nl" || findingAttr(finding, "domain") != "nl.example.net" || findingAttr(finding, "detour") != "awg-de" {
		t.Fatalf("attrs = %+v", finding.Attrs)
	}

	// A dns_rules lookup with no detour above it (the endpoint dialed directly).
	b = &traceBuilder{}
	dial := b.add(0, "dial", "wg", OK)
	b.add(dial, "lookup", "example.com", OK, "resolver", "dns_rules")
	requireNoFinding(t, Findings(b.summary(), FindingState{}), FindingResolveViaRulesInSourceContext)
}

func TestFindingDependencyOutsideRoute(t *testing.T) {
	b, ids := appendixTrace()
	state := FindingState{NodeTypes: map[string]string{"ss-pl": "shadowsocks", "vless-nl": "vless", "awg-de": "awg"}}
	finding := requireFinding(t, Findings(b.summary(), state), FindingDependencyOutsideRoute, FindingInfo)
	if !reflect.DeepEqual(finding.Stages, []uint32{ids["dns_dial"], ids["wake_dns_dial"]}) {
		t.Fatalf("stages = %v", finding.Stages)
	}
	if findingAttr(finding, "node") != "ss-pl" || findingAttr(finding, "dns_server") != "dns-remote" {
		t.Fatalf("attrs = %+v", finding.Attrs)
	}
	if dependents := findingAttr(finding, "dependents"); dependents != "vless-nl,awg-de" {
		t.Fatalf("dependents = %q, want vless-nl,awg-de (Appendix A)", dependents)
	}

	// Criterion 2: the DNS detour node is down — its stage holds the root
	// cause, and the finding becomes a problem.
	b, ids = appendixTrace()
	failedConnect := b.add(ids["dns_dial"], "tcp_connect", "203.0.113.50:8388", Failed)
	b.rootCause = failedConnect
	requireFinding(t, Findings(b.summary(), state), FindingDependencyOutsideRoute, FindingProblem)

	// A DNS server over `direct`, or over a node that is on the route, is no
	// outside dependency.
	b = &traceBuilder{}
	dial := b.add(0, "dial", "vless-nl", OK)
	lookup := b.add(dial, "lookup", "nl.example.net", OK, "resolver", "default_domain_resolver")
	dns := b.add(lookup, "dns", "dns-remote", OK)
	b.add(dns, "detour", "direct", OK)
	b.add(dns, "detour", "vless-nl", OK)
	state.NodeTypes["direct"] = "direct"
	requireNoFinding(t, Findings(b.summary(), state), FindingDependencyOutsideRoute)
}

func TestFindingResolveCycle(t *testing.T) {
	b := &traceBuilder{}
	dial := b.add(0, "dial", "a", OK)
	outer := b.add(dial, "lookup", "a.example", OK, "resolver", "domain_resolver:dns-a")
	dns := b.add(outer, "dns", "dns-a", OK)
	inner := b.add(dns, "lookup", "a.example", Failed, "resolver", "domain_resolver:dns-a", "cycle_with", "2")
	b.rootCause = inner
	finding := requireFinding(t, Findings(b.summary(), FindingState{}), FindingResolveCycle, FindingProblem)
	if !reflect.DeepEqual(finding.Stages, []uint32{inner, outer}) {
		t.Fatalf("stages = %v, want [%d %d]", finding.Stages, inner, outer)
	}
	if findingAttr(finding, "domain") != "a.example" || findingAttr(finding, "resolver") != "domain_resolver:dns-a" {
		t.Fatalf("attrs = %+v", finding.Attrs)
	}
	b, _ = appendixTrace()
	requireNoFinding(t, Findings(b.summary(), FindingState{}), FindingResolveCycle)
}

func TestFindingIPRulesNotEvaluated(t *testing.T) {
	rules := []RouteRule{
		{Description: "ip_cidr=10.0.0.0/8", Action: "route(direct)", AddressConditions: []string{"ip_cidr"}},
		{Description: "domain_suffix=ru", Action: "route(direct)"},
		{Description: "rule_set=geoip-ru", Action: "route(direct)", AddressConditions: []string{"rule_set=geoip-ru"}},
		{Description: "domain_suffix=youtube.com", Action: "route(proxy)"},
		{Description: "ip_is_private=true", Action: "route(direct)", AddressConditions: []string{"ip_is_private"}},
	}
	b := &traceBuilder{}
	route := b.add(0, "route", "www.youtube.com:443", OK, "rule", "domain_suffix=youtube.com", "action", "route(proxy)", "domain_origin", "fakeip")
	finding := requireFinding(t, Findings(b.summary(), FindingState{RouteRules: rules}), FindingIPRulesNotEvaluated, FindingWarning)
	if !reflect.DeepEqual(finding.Stages, []uint32{route}) {
		t.Fatalf("stages = %v", finding.Stages)
	}
	var flagged []string
	for _, attr := range finding.Attrs {
		if attr.Key == "rule" {
			flagged = append(flagged, attr.Value)
		}
	}
	want := []string{"route[0] ip_cidr=10.0.0.0/8 (ip_cidr)", "route[2] rule_set=geoip-ru (rule_set=geoip-ru)"}
	if !reflect.DeepEqual(flagged, want) {
		t.Fatalf("flagged = %v, want %v (rules below the selected one are not listed)", flagged, want)
	}
	if findingAttr(finding, "domain_origin") != "fakeip" {
		t.Fatalf("attrs = %+v", finding.Attrs)
	}

	// final: every rule is above the selection.
	b = &traceBuilder{}
	b.add(0, "route", "example.com:443", OK, "rule", "final", "action", "route", "domain_origin", "request")
	finding = requireFinding(t, Findings(b.summary(), FindingState{RouteRules: rules}), FindingIPRulesNotEvaluated, FindingWarning)
	if len(finding.Attrs) != 2+3 {
		t.Fatalf("attrs = %+v, want three rules", finding.Attrs)
	}

	// The address is known: an IP destination, or a domain with resolved addresses.
	for _, stage := range [][]string{
		{"203.0.113.1:443", "rule", "final", "action", "route"},
		{"example.com:443", "rule", "final", "action", "route", "addresses", "203.0.113.1"},
	} {
		b = &traceBuilder{}
		b.add(0, "route", stage[0], OK, stage[1:]...)
		requireNoFinding(t, Findings(b.summary(), FindingState{RouteRules: rules}), FindingIPRulesNotEvaluated)
	}
}

func fakeIPState() *FakeIPState {
	return &FakeIPState{
		Inet4Range: netip.MustParsePrefix("198.18.0.0/15"),
		Inet6Range: netip.MustParsePrefix("fc00::/18"),
		TTL:        600 * time.Second,
		Persisted:  true,
	}
}

func fakeIPRouteTrace() *traceBuilder {
	b := &traceBuilder{}
	b.add(0, "route", "www.youtube.com:443", OK, "rule", "final", "action", "route", "domain_origin", "fakeip")
	return b
}

func TestFindingFakeIPNotPersisted(t *testing.T) {
	state := FindingState{FakeIP: fakeIPState()}
	state.FakeIP.Persisted = false
	requireFinding(t, Findings(fakeIPRouteTrace().summary(), state), FindingFakeIPNotPersisted, FindingWarning)

	// The test did not touch fakeip.
	b := &traceBuilder{}
	b.add(0, "route", "www.youtube.com:443", OK, "rule", "final", "action", "route", "domain_origin", "request")
	requireNoFinding(t, Findings(b.summary(), state), FindingFakeIPNotPersisted)

	// An app_dns answer in the range touches it.
	b = &traceBuilder{}
	b.add(0, "app_dns", "www.youtube.com", OK, "type", "A", "answer", "A 600 198.18.0.42")
	requireFinding(t, Findings(b.summary(), state), FindingFakeIPNotPersisted, FindingWarning)

	state.FakeIP.Persisted = true
	requireNoFinding(t, Findings(fakeIPRouteTrace().summary(), state), FindingFakeIPNotPersisted)
}

func TestFindingFakeIPPoolWrapped(t *testing.T) {
	last := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	state := FindingState{FakeIP: fakeIPState()}
	state.FakeIP.Inet4Wrap = FakeIPWrap{Count: 3, Last: last, Interval: 90 * time.Second}
	finding := requireFinding(t, Findings(fakeIPRouteTrace().summary(), state), FindingFakeIPPoolWrapped, FindingWarning)
	want := []Attr{
		{"range", "198.18.0.0/15"}, {"size", "131072"}, {"wraps", "3"},
		{"wrap_time", "90"}, {"last_wrap", "2026-10-28T12:00:00Z"}, {"ttl", "600"},
	}
	if !reflect.DeepEqual(finding.Attrs, want) {
		t.Fatalf("attrs = %+v, want %+v", finding.Attrs, want)
	}

	// A pass slower than the TTL is harmless.
	state.FakeIP.Inet4Wrap.Interval = time.Hour
	requireNoFinding(t, Findings(fakeIPRouteTrace().summary(), state), FindingFakeIPPoolWrapped)
	// One wrap after start: the first pass may have resumed mid-range from a
	// persisted store, so there is no interval to judge turnover by.
	state.FakeIP.Inet4Wrap = FakeIPWrap{Count: 1, Last: last}
	requireNoFinding(t, Findings(fakeIPRouteTrace().summary(), state), FindingFakeIPPoolWrapped)
	// No wrap at all.
	state.FakeIP.Inet4Wrap = FakeIPWrap{}
	requireNoFinding(t, Findings(fakeIPRouteTrace().summary(), state), FindingFakeIPPoolWrapped)

	if size := prefixSize(netip.MustParsePrefix("fc00::/18")); size != "2^110" {
		t.Fatalf("IPv6 size = %s", size)
	}
}

func TestFindingFakeIPUnknownAddress(t *testing.T) {
	b := &traceBuilder{}
	route := b.add(0, "route", "198.18.5.5:443", Failed, "network", "tcp", "domain_origin", "fakeip", "fakeip", "no_record")
	b.rootCause = route
	state := FindingState{Host: "www.youtube.com", ConnectIP: netip.MustParseAddr("198.18.5.5"), FakeIP: fakeIPState()}
	finding := requireFinding(t, Findings(b.summary(), state), FindingFakeIPUnknownAddress, FindingProblem)
	if !reflect.DeepEqual(finding.Stages, []uint32{route}) || findingAttr(finding, "address") != "198.18.5.5" || findingAttr(finding, "range") != "198.18.0.0/15" {
		t.Fatalf("finding = %+v", finding)
	}
	requireNoFinding(t, Findings(b.summary(), state), FindingFakeIPAddressReassigned)

	// Outside the range, or a known record: not this finding.
	state.ConnectIP = netip.MustParseAddr("203.0.113.1")
	requireNoFinding(t, Findings(b.summary(), state), FindingFakeIPUnknownAddress)
	state.ConnectIP = netip.MustParseAddr("198.18.5.5")
	state.FakeIP.ConnectIPKnown, state.FakeIP.ConnectIPDomain = true, "www.youtube.com"
	requireNoFinding(t, Findings(b.summary(), state), FindingFakeIPUnknownAddress)
}

func TestFindingFakeIPAddressReassigned(t *testing.T) {
	b := &traceBuilder{}
	route := b.add(0, "route", "other.example:443", OK, "rule", "final", "action", "route", "domain_origin", "fakeip")
	state := FindingState{Host: "www.youtube.com", ConnectIP: netip.MustParseAddr("198.18.0.9"), FakeIP: fakeIPState()}
	state.FakeIP.ConnectIPKnown, state.FakeIP.ConnectIPDomain = true, "other.example"
	state.FakeIP.HostAddress = netip.MustParseAddr("198.18.0.12")
	finding := requireFinding(t, Findings(b.summary(), state), FindingFakeIPAddressReassigned, FindingProblem)
	want := []Attr{{"address", "198.18.0.9"}, {"domain", "other.example"}, {"expected", "www.youtube.com"}, {"expected_address", "198.18.0.12"}}
	if !reflect.DeepEqual(finding.Stages, []uint32{route}) || !reflect.DeepEqual(finding.Attrs, want) {
		t.Fatalf("finding = %+v", finding)
	}
	// The record names the host itself (case and the root dot do not matter).
	state.FakeIP.ConnectIPDomain = "WWW.YouTube.com."
	requireNoFinding(t, Findings(b.summary(), state), FindingFakeIPAddressReassigned)
}

func TestFindingFakeIPIPv6WithoutTUNIPv6(t *testing.T) {
	b := &traceBuilder{}
	aaaa := b.add(0, "app_dns", "www.youtube.com", OK, "type", "AAAA", "answer", "AAAA 600 fc00::2a")
	state := FindingState{FakeIP: fakeIPState(), TUN: []TUNState{{Tag: "tun-in"}}}
	finding := requireFinding(t, Findings(b.summary(), state), FindingFakeIPIPv6WithoutTUNIPv6, FindingWarning)
	if !reflect.DeepEqual(finding.Stages, []uint32{aaaa}) || findingAttr(finding, "address") != "fc00::2a" || findingAttr(finding, "inbound") != "tun-in" {
		t.Fatalf("finding = %+v", finding)
	}
	state.TUN[0].HasIPv6 = true
	requireNoFinding(t, Findings(b.summary(), state), FindingFakeIPIPv6WithoutTUNIPv6)

	// A real IPv6 answer is not a fakeip one.
	b = &traceBuilder{}
	b.add(0, "app_dns", "www.youtube.com", OK, "type", "AAAA", "answer", "www.youtube.com. AAAA 300 2001:db8::1")
	requireNoFinding(t, Findings(b.summary(), FindingState{FakeIP: fakeIPState(), TUN: []TUNState{{Tag: "tun-in"}}}), FindingFakeIPIPv6WithoutTUNIPv6)
}

func TestFindingHTTPSQueryRejected(t *testing.T) {
	b := &traceBuilder{}
	b.add(0, "app_dns", "www.youtube.com", OK, "type", "A", "dns_rule", "dns[3] => route(dns-fake) (A)")
	https := b.add(0, "app_dns", "www.youtube.com", Failed, "type", "HTTPS", "dns_rule", "dns[0] query_type=HTTPS => reject (HTTPS)")
	finding := requireFinding(t, Findings(b.summary(), FindingState{}), FindingHTTPSQueryRejected, FindingInfo)
	if !reflect.DeepEqual(finding.Stages, []uint32{https}) || findingAttr(finding, "rule") != "dns[0] query_type=HTTPS => reject (HTTPS)" {
		t.Fatalf("finding = %+v", finding)
	}
	b = &traceBuilder{}
	b.add(0, "app_dns", "www.youtube.com", OK, "type", "HTTPS", "dns_rule", "final")
	requireNoFinding(t, Findings(b.summary(), FindingState{}), FindingHTTPSQueryRejected)
}

func TestFindingQUICRejected(t *testing.T) {
	// A UDP test reads it from its own route stage.
	b := &traceBuilder{}
	route := b.add(0, "route", "www.youtube.com:443", OK, "network", "udp", "rule", "protocol=quic", "action", "reject")
	finding := requireFinding(t, Findings(b.summary(), FindingState{}), FindingQUICRejected, FindingInfo)
	if !reflect.DeepEqual(finding.Stages, []uint32{route}) || findingAttr(finding, "rule") != "protocol=quic" {
		t.Fatalf("finding = %+v", finding)
	}
	// A TCP test from the handler's evaluation of udp/443.
	b = &traceBuilder{}
	b.add(0, "route", "www.youtube.com:443", OK, "network", "tcp", "rule", "final", "action", "route")
	requireNoFinding(t, Findings(b.summary(), FindingState{}), FindingQUICRejected)
	finding = requireFinding(t, Findings(b.summary(), FindingState{QUICRejectedBy: "route[1] protocol=quic => reject"}), FindingQUICRejected, FindingInfo)
	if findingAttr(finding, "rule") != "route[1] protocol=quic => reject" || len(finding.Stages) != 0 {
		t.Fatalf("finding = %+v", finding)
	}
	// udp to another port is not QUIC to the target.
	b = &traceBuilder{}
	b.add(0, "route", "www.youtube.com:53", OK, "network", "udp", "rule", "port=53", "action", "reject")
	requireNoFinding(t, Findings(b.summary(), FindingState{}), FindingQUICRejected)
}

func TestFindingRealityNotRecognized(t *testing.T) {
	b := &traceBuilder{}
	dial := b.add(0, "dial", "vless-nl", Failed)
	tls := b.add(dial, "tls", "www.microsoft.com", Failed)
	reality := b.add(tls, "reality", "www.microsoft.com", Failed, "verdict", "dest_cert")
	b.rootCause = reality
	finding := requireFinding(t, Findings(b.summary(), FindingState{}), FindingRealityNotRecognized, FindingProblem)
	want := []Attr{{"node", "vless-nl"}, {"server_name", "www.microsoft.com"}, {"causes", "public_key,short_id,server_names,client_version"}}
	if !reflect.DeepEqual(finding.Stages, []uint32{reality}) || !reflect.DeepEqual(finding.Attrs, want) {
		t.Fatalf("finding = %+v", finding)
	}
	for _, verdict := range []string{"accepted", "invalid_cert"} {
		b = &traceBuilder{}
		b.add(0, "reality", "www.microsoft.com", OK, "verdict", verdict)
		requireNoFinding(t, Findings(b.summary(), FindingState{}), FindingRealityNotRecognized)
	}
}

func TestFindingFailedAfterNoAck(t *testing.T) {
	b := &traceBuilder{}
	dial := b.add(0, "dial", "trojan-nl", OK)
	header := b.add(dial, "proxy_header", "example.com:443", NoAck, "protocol", "trojan")
	targetTLS := b.add(0, "target_tls", "example.com", Failed)
	b.rootCause = targetTLS
	finding := requireFinding(t, Findings(b.summary(), FindingState{}), FindingFailedAfterNoAck, FindingProblem)
	if !reflect.DeepEqual(finding.Stages, []uint32{header, targetTLS}) {
		t.Fatalf("stages = %v", finding.Stages)
	}
	if findingAttr(finding, "node") != "trojan-nl" || findingAttr(finding, "protocol") != "trojan" || findingAttr(finding, "failed") != "target_tls" {
		t.Fatalf("attrs = %+v", finding.Attrs)
	}

	// A failure before the header went out is not after it.
	b = &traceBuilder{}
	early := b.add(0, "tcp_connect", "203.0.113.1:443", Failed)
	b.add(0, "proxy_header", "example.com:443", NoAck)
	b.stages[0].DurationMs = 1
	b.rootCause = early
	requireNoFinding(t, Findings(b.summary(), FindingState{}), FindingFailedAfterNoAck)

	// No root cause: nothing broke.
	b = &traceBuilder{}
	b.add(0, "proxy_header", "example.com:443", NoAck)
	requireNoFinding(t, Findings(b.summary(), FindingState{}), FindingFailedAfterNoAck)
}

func TestFindingPartiallyReused(t *testing.T) {
	b := &traceBuilder{}
	b.add(0, "app_dns", "example.com", OK)
	dns := b.add(1, "dns", "dns-remote", Reused, "source", "cache")
	b.add(0, "dial", "awg", OK)
	handshake := b.add(3, "wg_handshake", "awg", Reused)
	finding := requireFinding(t, Findings(b.summary(), FindingState{Completed: true}), FindingPartiallyReused, FindingInfo)
	if !reflect.DeepEqual(finding.Stages, []uint32{dns, handshake}) || findingAttr(finding, "kinds") != "dns,wg_handshake" || findingAttr(finding, "count") != "2" {
		t.Fatalf("finding = %+v", finding)
	}
	// The path was not completed.
	requireNoFinding(t, Findings(b.summary(), FindingState{}), FindingPartiallyReused)
	b.rootCause = 3
	requireNoFinding(t, Findings(b.summary(), FindingState{Completed: true}), FindingPartiallyReused)
}

// The Appendix A path yields exactly the findings its caption lists, in the
// order of the SPEC §6 table.
func TestFindingsAppendixOrder(t *testing.T) {
	b, _ := appendixTrace()
	b.add(0, "app_dns", "www.youtube.com", Failed, "type", "HTTPS", "dns_rule", "dns[0] => reject (HTTPS)")
	findings := Findings(b.summary(), FindingState{NodeTypes: map[string]string{"ss-pl": "shadowsocks"}})
	var codes []string
	for _, finding := range findings {
		codes = append(codes, finding.Code)
	}
	want := []string{FindingResolveViaRulesInSourceContext, FindingDependencyOutsideRoute, FindingHTTPSQueryRejected}
	if !reflect.DeepEqual(codes, want) {
		t.Fatalf("codes = %v, want %v", codes, want)
	}
}
