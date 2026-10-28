//go:build with_lx_command

package daemon

import (
	"context"
	"net/netip"
	"testing"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/certificate"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	boxservice "github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	boxDNS "github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport/fakeip"
	"github.com/sagernet/sing-box/dns/transport/hosts"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

// lx: SPEC 139 §6 — findings on the wire: a real box with a fakeip server
// (memory storage), an ip_cidr rule above the final and a udp/443 reject,
// with the running-config snapshot the rule and TUN options are read from.
// Criterion 7: connectIP from the fakeip range without a record — route
// FAILED + fakeip_unknown_address; with another domain's record —
// fakeip_address_reassigned.

func findingsService(t *testing.T) (*StartedService, adapter.FakeIPStore) {
	t.Helper()
	outboundRegistry := outbound.NewRegistry()
	direct.RegisterOutbound(outboundRegistry)
	dnsRegistry := boxDNS.NewTransportRegistry()
	fakeip.RegisterTransport(dnsRegistry)
	hosts.RegisterTransport(dnsRegistry)
	ctx := box.Context(context.Background(), inbound.NewRegistry(), outboundRegistry, endpoint.NewRegistry(), dnsRegistry, boxservice.NewRegistry(), certificate.NewRegistry())
	options, err := json.UnmarshalExtendedContext[option.Options](ctx, []byte(`{
		"log": {"disabled": true},
		"dns": {
			"servers": [
				{"type": "hosts", "tag": "hosts"},
				{"type": "fakeip", "tag": "fake", "inet4_range": "198.18.0.0/15"}
			],
			"final": "hosts"
		},
		"outbounds": [{"type": "direct", "tag": "direct"}],
		"route": {
			"rules": [
				{"ip_cidr": ["10.0.0.0/8"], "outbound": "direct"},
				{"network": "udp", "port": 443, "action": "reject"}
			],
			"final": "direct"
		}
	}`))
	require.NoError(t, err)
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	require.NoError(t, err)
	require.NoError(t, instance.Start())
	t.Cleanup(func() { instance.Close() })
	store := service.FromContext[adapter.DNSTransportManager](ctx).FakeIP().Store()
	return &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance: &Instance{
			ctx:                   ctx,
			urlTestHistoryStorage: urltest.NewHistoryStorage(),
			outboundManager:       service.FromContext[adapter.OutboundManager](ctx),
			endpointManager:       service.FromContext[adapter.EndpointManager](ctx),
			router:                service.FromContext[adapter.Router](ctx),
			runningConfig:         captureRunningConfig(options),
		},
	}, store
}

func runFindingsTest(t *testing.T, startedService *StartedService, request *ConnectionTestRequest) (*ConnectionTestResult, []*ConnectionTestStage) {
	t.Helper()
	stream := newRecordingStream(context.Background())
	require.NoError(t, startedService.StartConnectionTest(request, stream))
	result := stream.result()
	require.NotNil(t, result)
	require.Empty(t, result.Error)
	return result, stream.finalStages()
}

func wireFinding(result *ConnectionTestResult, code string) *ConnectionTestFinding {
	for _, finding := range result.Findings {
		if finding.Code == code {
			return finding
		}
	}
	return nil
}

func findingAttrOf(finding *ConnectionTestFinding, key string) string {
	for _, attr := range finding.Attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}

func TestStartConnectionTest_FakeIPUnknownAddress_LX(t *testing.T) {
	startedService, _ := findingsService(t)
	result, stages := runFindingsTest(t, startedService, &ConnectionTestRequest{
		Link:      "http://fake.test/",
		ConnectIP: "198.18.5.5",
		Depth:     ConnectionTestDepth_DEPTH_ROUTE,
	})
	route := findStage(stages, stageRoute)
	require.NotNil(t, route, kindsOf(stages))
	require.Equal(t, ConnectionTestStageResult_STAGE_FAILED, route.Result)
	require.Equal(t, "no_record", attrOf(route, "fakeip"))
	require.Equal(t, route.Id, result.RootCause)

	finding := wireFinding(result, lxtrace.FindingFakeIPUnknownAddress)
	require.NotNil(t, finding, result.Findings)
	require.Equal(t, ConnectionTestFindingLevel_FINDING_PROBLEM, finding.Level)
	require.Equal(t, []uint32{route.Id}, finding.Stages)
	require.Equal(t, "198.18.5.5", findingAttrOf(finding, "address"))
	require.NotNil(t, wireFinding(result, lxtrace.FindingFakeIPNotPersisted), "memory storage")
	require.Nil(t, wireFinding(result, lxtrace.FindingFakeIPAddressReassigned))
	require.Nil(t, wireFinding(result, lxtrace.FindingPartiallyReused))
}

func TestStartConnectionTest_FakeIPAddressReassigned_LX(t *testing.T) {
	startedService, store := findingsService(t)
	address, err := store.Create("other.test", false)
	require.NoError(t, err)
	result, stages := runFindingsTest(t, startedService, &ConnectionTestRequest{
		Link:      "https://fake.test/",
		ConnectIP: address.String(),
		Depth:     ConnectionTestDepth_DEPTH_ROUTE,
	})
	route := findStage(stages, stageRoute)
	require.NotNil(t, route, kindsOf(stages))
	require.Equal(t, ConnectionTestStageResult_STAGE_OK, route.Result)
	require.Equal(t, "fakeip", attrOf(route, "domain_origin"))
	require.Zero(t, result.RootCause)

	finding := wireFinding(result, lxtrace.FindingFakeIPAddressReassigned)
	require.NotNil(t, finding, result.Findings)
	require.Equal(t, "other.test", findingAttrOf(finding, "domain"))
	require.Equal(t, "fake.test", findingAttrOf(finding, "expected"))

	// The route was decided on a domain: the ip_cidr rule above the final
	// could not be evaluated.
	finding = wireFinding(result, lxtrace.FindingIPRulesNotEvaluated)
	require.NotNil(t, finding, result.Findings)
	require.Equal(t, "route[0] ip_cidr=10.0.0.0/8 (ip_cidr)", findingAttrOf(finding, "rule"))

	// https on 443: the browser's QUIC attempt meets the udp/443 reject.
	finding = wireFinding(result, lxtrace.FindingQUICRejected)
	require.NotNil(t, finding, result.Findings)
	require.Contains(t, findingAttrOf(finding, "rule"), "route[1]")

	// The test read the store and wrote nothing: no record for fake.test.
	_, known := fakeip.LxAddressOf(store, "fake.test", false)
	require.False(t, known)
	domain, known := fakeip.LxDomainOf(store, address)
	require.True(t, known)
	require.Equal(t, "other.test", domain)
}

// Outside the fakeip range no fakeip finding appears, and an http (not
// https) test says nothing about QUIC.
func TestStartConnectionTest_NoFakeIPFindings_LX(t *testing.T) {
	startedService, _ := findingsService(t)
	result, _ := runFindingsTest(t, startedService, &ConnectionTestRequest{
		Link:      "http://plain.test/",
		ConnectIP: netip.MustParseAddr("203.0.113.1").String(),
		Depth:     ConnectionTestDepth_DEPTH_ROUTE,
	})
	for _, finding := range result.Findings {
		t.Fatalf("unexpected finding %s", finding.Code)
	}
}

// The TUN state of excluded_from_vpn and fakeip_ipv6_without_tun_ipv6 comes
// from the config snapshot: the source inbound when it is a TUN, every TUN
// without one, none for another inbound type.
func TestConnectionTestTUNs_LX(t *testing.T) {
	options := &option.Options{Inbounds: []option.Inbound{
		{Type: C.TypeTun, Tag: "tun-in", Options: &option.TunInboundOptions{
			Address:        []netip.Prefix{netip.MustParsePrefix("172.19.0.1/30")},
			ExcludePackage: []string{"com.app"},
		}},
		{Type: C.TypeTun, Tag: "tun6", Options: &option.TunInboundOptions{
			Address: []netip.Prefix{netip.MustParsePrefix("fdfe:dcba:9876::1/126")},
		}},
		{Type: C.TypeMixed, Tag: "mixed-in", Options: &option.HTTPMixedInboundOptions{}},
	}}
	all := connectionTestTUNs(options, "")
	require.Len(t, all, 2)
	require.Equal(t, []string{"com.app"}, all[0].ExcludePackage)
	require.False(t, all[0].HasIPv6)
	require.True(t, all[1].HasIPv6)
	require.Len(t, connectionTestTUNs(options, "tun6"), 1)
	require.Empty(t, connectionTestTUNs(options, "mixed-in"))

	findings := lxtrace.Findings(lxtrace.Summary{}, lxtrace.FindingState{PackageName: "com.app", TUN: connectionTestTUNs(options, "tun-in")})
	require.Len(t, findings, 1)
	require.Equal(t, lxtrace.FindingExcludedFromVPN, findings[0].Code)
}

// Address conditions are collected through logical rules.
func TestRuleAddressConditions_LX(t *testing.T) {
	rule := option.Rule{Type: C.RuleTypeLogical, LogicalOptions: option.LogicalRule{RawLogicalRule: option.RawLogicalRule{
		Mode: C.LogicalTypeAnd,
		Rules: []option.Rule{
			{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{IPIsPrivate: true}}},
			{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{IPCIDR: []string{"10.0.0.0/8"}, Domain: []string{"x.test"}}}},
			{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{Domain: []string{"y.test"}}}},
		},
	}}}
	require.Equal(t, []string{"ip_is_private", "ip_cidr"}, ruleAddressConditions(nil, rule, nil))
}
