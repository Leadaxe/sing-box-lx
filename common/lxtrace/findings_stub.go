//go:build !with_lx_command

package lxtrace

import (
	"net/netip"
	"time"
)

// Build-tag twin of findings_lx.go (SPEC 139 §6): the same exported surface,
// no findings.

type FindingLevel int

const (
	FindingInfo FindingLevel = iota
	FindingWarning
	FindingProblem
)

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

type Finding struct {
	Code   string
	Level  FindingLevel
	Stages []uint32
	Attrs  []Attr
}

type FindingState struct {
	Host           string
	ConnectIP      netip.Addr
	Completed      bool
	PackageName    string
	UID            int32
	TUN            []TUNState
	FakeIP         *FakeIPState
	RouteRules     []RouteRule
	NodeTypes      map[string]string
	QUICRejectedBy string
}

type TUNState struct {
	Tag             string
	IncludePackage  []string
	ExcludePackage  []string
	IncludeUID      []uint32
	ExcludeUID      []uint32
	IncludeUIDRange []string
	ExcludeUIDRange []string
	HasIPv6         bool
}

type FakeIPState struct {
	Inet4Range      netip.Prefix
	Inet6Range      netip.Prefix
	Persisted       bool
	TTL             time.Duration
	Inet4Wrap       FakeIPWrap
	Inet6Wrap       FakeIPWrap
	ConnectIPDomain string
	ConnectIPKnown  bool
	HostAddress     netip.Addr
}

type FakeIPWrap struct {
	Count    uint64
	Last     time.Time
	Interval time.Duration
}

type RouteRule struct {
	Description       string
	Action            string
	AddressConditions []string
}

func Findings(summary Summary, state FindingState) []Finding { return nil }
