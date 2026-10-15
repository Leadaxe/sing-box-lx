package trusttunnel

import (
	"context"
	"strings"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func validOutboundOptions() option.TrustTunnelOutboundOptions {
	return option.TrustTunnelOutboundOptions{
		ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 443},
		Username:      "user",
		Password:      "secret",
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
			TLS: &option.OutboundTLSOptions{Enabled: true, ServerName: "vpn.example.com"},
		},
	}
}

func TestOutboundValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		modify func(options *option.TrustTunnelOutboundOptions)
		want   string
	}{
		{"tls missing", func(options *option.TrustTunnelOutboundOptions) { options.TLS = nil }, C.ErrTLSRequired.Error()},
		{"tls disabled", func(options *option.TrustTunnelOutboundOptions) { options.TLS.Enabled = false }, C.ErrTLSRequired.Error()},
		{"ip server without server_name", func(options *option.TrustTunnelOutboundOptions) { options.TLS.ServerName = "" }, "tls.server_name is required"},
		{"ipv6 server without server_name", func(options *option.TrustTunnelOutboundOptions) {
			options.Server = "::1"
			options.TLS.ServerName = ""
		}, "tls.server_name is required"},
		{"empty username", func(options *option.TrustTunnelOutboundOptions) { options.Username = "" }, "missing username"},
		{"empty password", func(options *option.TrustTunnelOutboundOptions) { options.Password = "" }, "missing password"},
		{"icmp", func(options *option.TrustTunnelOutboundOptions) { options.Network = "tcp\nicmp" }, "unsupported network: icmp"},
		{"quic with utls", func(options *option.TrustTunnelOutboundOptions) {
			options.QUIC = true
			options.TLS.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"}
		}, "uTLS is not supported with quic"},
		{"bad congestion control", func(options *option.TrustTunnelOutboundOptions) { options.QUICCongestionControl = "bbr2" }, "unknown quic_congestion_control: bbr2"},
		{"alpn without h2", func(options *option.TrustTunnelOutboundOptions) {
			options.TLS.ALPN = badoption.Listable[string]{"http/1.1"}
		}, "tls.alpn must contain h2"},
		{"alpn without h3 on quic", func(options *option.TrustTunnelOutboundOptions) {
			options.QUIC = true
			options.TLS.ALPN = badoption.Listable[string]{"h2"}
		}, "tls.alpn must contain h3"},
	}
	for _, testCase := range cases {
		options := validOutboundOptions()
		testCase.modify(&options)
		_, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().Logger(), "tt", options)
		if err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("%s: expected %q, got %v", testCase.name, testCase.want, err)
		}
	}
}

// A domain server needs the box's DNS managers in the context (dial fields);
// the stand covers it, these cases stay on an IP literal.
func TestOutboundAcceptsValidOptions(t *testing.T) {
	t.Parallel()
	for _, modify := range []func(options *option.TrustTunnelOutboundOptions){
		func(options *option.TrustTunnelOutboundOptions) {},
		func(options *option.TrustTunnelOutboundOptions) {
			options.TLS.ALPN = badoption.Listable[string]{"h2", "http/1.1"}
		},
		func(options *option.TrustTunnelOutboundOptions) {
			options.Network = "udp"
			options.QUICCongestionControl = "reno"
		},
	} {
		options := validOutboundOptions()
		modify(&options)
		outbound, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().Logger(), "tt", options)
		if err != nil {
			t.Fatalf("%+v: %v", options, err)
		}
		if outbound.Type() != C.TypeTrustTunnel {
			t.Fatalf("type = %s", outbound.Type())
		}
		outbound.(*Outbound).Close()
	}
}

func TestOutboundIdleWindow(t *testing.T) {
	t.Parallel()
	duration := func(value time.Duration) *badoption.Duration {
		converted := badoption.Duration(value)
		return &converted
	}
	cases := []struct {
		node   *badoption.Duration
		global time.Duration
		want   time.Duration
	}{
		{nil, 0, defaultIdleTimeout},
		{nil, time.Minute, time.Minute},
		{duration(2 * time.Minute), time.Minute, 2 * time.Minute},
		{duration(0), time.Minute, 0},
		{duration(0), 0, 0},
	}
	for _, testCase := range cases {
		if got := idleWindow(testCase.node, testCase.global); got != testCase.want {
			t.Errorf("idleWindow(%v, %v) = %v, want %v", testCase.node, testCase.global, got, testCase.want)
		}
	}
}
