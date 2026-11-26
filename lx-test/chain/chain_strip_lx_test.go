//go:build with_lx_chain

// lx: SPEC 165 С2 on a live node: trojan over TLS with `fragment: true` on
// position 1 of a chain behind shadowsocks. Inside the tunnel the split
// cannot wait for the kernel and sleeps fragment_fallback_delay between
// pieces; the default lx.detour drops it, "detour": false keeps upstream.
package chain

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
)

func selfSigned(t *testing.T, name string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func lines(text string) badoption.Listable[string] {
	return strings.Split(strings.TrimSpace(text), "\n")
}

func TestChainStripС2FragmentOnLiveNode(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()
	certificate, key := selfSigned(t, "trojan.test")

	measure := func(detour *option.LXDetour) time.Duration {
		inPort, trojanPort, mixedPort := freePort(t), freePort(t), freePort(t)
		options := option.Options{
			Log: &option.LogOptions{Level: "error"},
			Inbounds: []option.Inbound{
				ssServer("srv-in", inPort),
				{Type: C.TypeTrojan, Tag: "srv-trojan", Options: &option.TrojanInboundOptions{
					ListenOptions: listenOn(trojanPort), Users: []option.TrojanUser{{Password: "p"}},
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: &option.InboundTLSOptions{
						Enabled: true, Certificate: lines(certificate), Key: lines(key),
					}},
				}},
				{Type: C.TypeMixed, Tag: "mixed", Options: &option.HTTPMixedInboundOptions{ListenOptions: listenOn(mixedPort)}},
			},
			Outbounds: []option.Outbound{
				ssClient("o-in", inPort),
				{Type: C.TypeTrojan, Tag: "o-trojan", Options: &option.TrojanOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: trojanPort}, Password: "p",
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: &option.OutboundTLSOptions{
						Enabled: true, ServerName: "trojan.test", Insecure: true, Fragment: true,
						FragmentFallbackDelay: badoption.Duration(300 * time.Millisecond),
					}},
				}},
				{Type: C.TypeDirect, Tag: "direct"},
				{Type: C.TypeChain, Tag: "c", Options: &option.ChainOutboundOptions{Outbounds: []string{"o-in", "o-trojan"}}},
			},
			Route: &option.RouteOptions{
				Final: "c",
				Rules: []option.Rule{{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{Inbound: []string{"srv-in", "srv-trojan"}},
					RuleAction:     option.RuleAction{Action: C.RuleActionTypeRoute, RouteOptions: option.RouteActionOptions{Outbound: "direct"}},
				}}},
			},
		}
		if detour != nil {
			options.LX = &option.LXOptions{Detour: detour}
		}
		instance, err := box.New(box.Options{Context: include.Context(context.Background()), Options: options})
		require.NoError(t, err)
		require.NoError(t, instance.Start())
		defer instance.Close()
		proxyURL, _ := url.Parse("socks5://127.0.0.1:" + strconv.Itoa(int(mixedPort)))
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}, Timeout: 10 * time.Second}
		start := time.Now()
		resp, err := client.Get(target.URL)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, "ok", string(body))
		return time.Since(start)
	}
	off := false
	upstream := measure(&option.LXDetour{Bool: &off})
	stripped := measure(nil)
	t.Logf("detour: false %v, default %v", upstream, stripped)
	require.GreaterOrEqual(t, upstream, 300*time.Millisecond, "С5: upstream sleeps between the pieces inside the tunnel")
	require.Less(t, stripped, 300*time.Millisecond, "С2: the default drops the split inside the tunnel")
}
