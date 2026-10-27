//go:build with_lx_command

package daemon

import (
	"context"
	"net"
	"strings"
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
	boxDNS "github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport"
	"github.com/sagernet/sing-box/dns/transport/fakeip"
	"github.com/sagernet/sing-box/dns/transport/hosts"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// lx: SPEC 139 — app_dns carries every answer record as its own `answer`
// attr and names where the answer came from.

func TestRecordText_LX(t *testing.T) {
	record := &dns.A{
		Hdr: dns.RR_Header{Name: "example.org.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   net.IPv4(192, 0, 2, 1),
	}
	require.Equal(t, "A 300 192.0.2.1", recordText(record))
	https, err := dns.NewRR("example.org. 60 IN HTTPS 1 . alpn=h2,h3 ipv4hint=192.0.2.1")
	require.NoError(t, err)
	require.Equal(t, "HTTPS 60 1 . alpn=\"h2,h3\" ipv4hint=\"192.0.2.1\"", recordText(https))
}

func appDNSSource(t *testing.T, children func(ctx context.Context)) []lxtrace.Attr {
	trace := lxtrace.New(lxtrace.Options{})
	ctx, stage := lxtrace.Begin(lxtrace.With(context.Background(), trace), stageAppDNS, "example.org")
	children(ctx)
	attrAnswerSource(stage)
	stage.End(nil)
	var attrs []lxtrace.Attr
	for _, attr := range trace.Finish(nil).Stages[0].Attrs {
		if attr.Key == "source" || attr.Key == "cache_age" || attr.Key == "stale" {
			attrs = append(attrs, attr)
		}
	}
	return attrs
}

func TestAppDNSSource_LX(t *testing.T) {
	require.Empty(t, appDNSSource(t, func(context.Context) {}), "a rule answered: no source")

	require.Equal(t, []lxtrace.Attr{{Key: "source", Value: "network"}}, appDNSSource(t, func(ctx context.Context) {
		_, exchanged := lxtrace.Begin(ctx, "dns", "remote")
		exchanged.End(nil)
	}))

	require.Equal(t, []lxtrace.Attr{
		{Key: "source", Value: "cache"},
		{Key: "cache_age", Value: "42"},
	}, appDNSSource(t, func(ctx context.Context) {
		nestedCtx, rejected := lxtrace.Begin(ctx, "dns", "first")
		_, nested := lxtrace.Begin(nestedCtx, "dns", "nested") // not a direct child
		nested.End(nil)
		rejected.End(nil)
		_, cached := lxtrace.Begin(ctx, "dns", "remote")
		cached.Attr("source", "cache")
		cached.Attr("cache_age", "42")
		cached.Mark(lxtrace.Reused)
	}))
}

// appDNSServer answers net.test with A, AAAA (NODATA) and HTTPS, and nx.test
// with NXDOMAIN and a SOA, on a loopback UDP port.
func appDNSServer(t *testing.T) string {
	t.Helper()
	soa := func(name string) dns.RR {
		record, err := dns.NewRR(name + " 60 IN SOA ns.test. admin.test. 1 900 900 1800 30")
		require.NoError(t, err)
		return record
	}
	handler := dns.HandlerFunc(func(writer dns.ResponseWriter, request *dns.Msg) {
		response := new(dns.Msg)
		response.SetReply(request)
		question := request.Question[0]
		switch question.Name {
		case "net.test.":
			switch question.Qtype {
			case dns.TypeA:
				record, _ := dns.NewRR("net.test. 300 IN A 192.0.2.1")
				response.Answer = []dns.RR{record}
			case dns.TypeHTTPS:
				record, _ := dns.NewRR("net.test. 300 IN HTTPS 1 . alpn=h2,h3")
				response.Answer = []dns.RR{record}
			default:
				response.Ns = []dns.RR{soa("net.test.")}
			}
		default:
			response.Rcode = dns.RcodeNameError
			response.Ns = []dns.RR{soa("test.")}
		}
		writer.WriteMsg(response)
	})
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &dns.Server{PacketConn: packetConn, Handler: handler}
	go server.ActivateAndServe()
	t.Cleanup(func() { server.Shutdown() })
	return packetConn.LocalAddr().String()
}

// appDNSService is routerService with DNS: fake.test → fakeip, hosts.test →
// hosts, everything else → a UDP server on loopback.
func appDNSService(t *testing.T) *StartedService {
	t.Helper()
	host, port, err := net.SplitHostPort(appDNSServer(t))
	require.NoError(t, err)
	outboundRegistry := outbound.NewRegistry()
	direct.RegisterOutbound(outboundRegistry)
	dnsRegistry := boxDNS.NewTransportRegistry()
	transport.RegisterUDP(dnsRegistry)
	hosts.RegisterTransport(dnsRegistry)
	fakeip.RegisterTransport(dnsRegistry)
	ctx := box.Context(context.Background(), inbound.NewRegistry(), outboundRegistry, endpoint.NewRegistry(), dnsRegistry, boxservice.NewRegistry(), certificate.NewRegistry())
	options, err := json.UnmarshalExtendedContext[option.Options](ctx, []byte(`{
		"log": {"disabled": true},
		"dns": {
			"servers": [
				{"type": "udp", "tag": "remote", "server": "`+host+`", "server_port": `+port+`},
				{"type": "hosts", "tag": "hosts", "predefined": {"hosts.test": "192.0.2.7"}},
				{"type": "fakeip", "tag": "fake", "inet4_range": "198.18.0.0/15"}
			],
			"rules": [
				{"domain": "fake.test", "query_type": ["A", "AAAA"], "server": "fake"},
				{"domain": "hosts.test", "server": "hosts"}
			],
			"final": "remote"
		},
		"outbounds": [{"type": "direct", "tag": "direct"}]
	}`))
	require.NoError(t, err)
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	require.NoError(t, err)
	require.NoError(t, instance.Start())
	t.Cleanup(func() { instance.Close() })
	return &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance: &Instance{
			ctx:                   ctx,
			urlTestHistoryStorage: urltest.NewHistoryStorage(),
			outboundManager:       service.FromContext[adapter.OutboundManager](ctx),
			endpointManager:       service.FromContext[adapter.EndpointManager](ctx),
			router:                service.FromContext[adapter.Router](ctx),
		},
	}
}

// appDNSStages runs a DEPTH_DNS test for host and returns its app_dns stages
// by query type.
func appDNSStages(t *testing.T, startedService *StartedService, host string) map[string]*ConnectionTestStage {
	t.Helper()
	stream := newRecordingStream(context.Background())
	require.NoError(t, startedService.StartConnectionTest(&ConnectionTestRequest{
		Link:   "http://" + host + "/",
		AppDNS: true,
		Depth:  ConnectionTestDepth_DEPTH_DNS,
	}, stream))
	require.NotNil(t, stream.result())
	stages := make(map[string]*ConnectionTestStage)
	for _, stage := range stream.finalStages() {
		if stage.Kind == stageAppDNS {
			stages[attrOf(stage, "type")] = stage
		}
	}
	return stages
}

func attrsOf(stage *ConnectionTestStage, key string) []string {
	var values []string
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			values = append(values, attr.Value)
		}
	}
	return values
}

// Through a real DNS router and client: the answer records, the authority on
// NODATA / NXDOMAIN, and the source — network, then cache with its age, and
// local for fakeip and hosts.
func TestStartConnectionTest_AppDNS_LX(t *testing.T) {
	startedService := appDNSService(t)

	first := appDNSStages(t, startedService, "net.test")
	require.Equal(t, []string{"A 300 192.0.2.1"}, attrsOf(first["A"], "answer"))
	require.Equal(t, "network", attrOf(first["A"], "source"))
	require.Empty(t, attrOf(first["A"], "cache_age"))
	require.Equal(t, []string{`HTTPS 300 1 . alpn="h2,h3"`}, attrsOf(first["HTTPS"], "answer"))
	require.Empty(t, attrsOf(first["AAAA"], "answer"))
	require.Equal(t, []string{"net.test. SOA 30 ns.test. admin.test. 1 900 900 1800 30"}, attrsOf(first["AAAA"], "authority"))

	second := appDNSStages(t, startedService, "net.test")
	require.Equal(t, "cache", attrOf(second["A"], "source"))
	require.Equal(t, "0", attrOf(second["A"], "cache_age"))
	require.Len(t, attrsOf(second["A"], "answer"), 1)

	nx := appDNSStages(t, startedService, "nx.test")
	require.Equal(t, ConnectionTestStageResult_STAGE_FAILED, nx["A"].Result)
	require.Equal(t, "NXDOMAIN", attrOf(nx["A"], "rcode"))
	require.Equal(t, []string{"test. SOA 30 ns.test. admin.test. 1 900 900 1800 30"}, attrsOf(nx["A"], "authority"))

	fake := appDNSStages(t, startedService, "fake.test")
	require.Equal(t, "local", attrOf(fake["A"], "source"))
	require.Len(t, attrsOf(fake["A"], "answer"), 1)
	require.True(t, strings.HasPrefix(attrsOf(fake["A"], "answer")[0], "A "), attrsOf(fake["A"], "answer"))

	hostsStages := appDNSStages(t, startedService, "hosts.test")
	require.Equal(t, "local", attrOf(hostsStages["A"], "source"))
	require.Equal(t, []string{"A 600 192.0.2.7"}, attrsOf(hostsStages["A"], "answer"))
}
