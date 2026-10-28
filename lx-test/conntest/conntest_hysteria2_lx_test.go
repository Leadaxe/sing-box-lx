//go:build with_lx_command

// lx: SPECS/TASKS/139-CONNECTION_TEST — Hysteria2 on the stand (release 2,
// stage 10). An external module (sing-quic) whose QUIC handshake and
// authentication happen in one call: the trace has one `dial` for the hop,
// the handler classifies its failure by the error (`class`), and under fresh
// marks it reuse=uncontrolled, since the module keeps its own QUIC
// connection (PLAN §3.3).
package conntest

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

const hysteria2Password = "conntest-hysteria2"

// startHysteria2Server starts a server box with one hysteria2 inbound (UDP,
// so not behind a TCP gate) to direct, resolving through the stand DNS.
func (s *stand) startHysteria2Server(t *testing.T) uint16 {
	t.Helper()
	certPath, keyPath := writeCertificate(t)
	port := freeUDPPort(t)
	server := map[string]any{
		"log": map[string]any{"disabled": true},
		"dns": map[string]any{
			"servers": []any{map[string]any{"type": "tcp", "tag": "stand-dns", "server": "127.0.0.1", "server_port": s.dnsPort}},
		},
		"inbounds": []any{map[string]any{
			"type": "hysteria2", "tag": "H", "listen": "127.0.0.1", "listen_port": port,
			"users": []any{map[string]any{"name": "u", "password": hysteria2Password}},
			"tls":   map[string]any{"enabled": true, "server_name": "node2.test", "certificate_path": certPath, "key_path": keyPath},
		}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"final": "direct", "default_domain_resolver": "stand-dns"},
	}
	ctx := include.Context(context.Background())
	options, err := json.UnmarshalExtendedContext[option.Options](ctx, mustJSON(t, server))
	require.NoError(t, err)
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	require.NoError(t, err)
	require.NoError(t, instance.Start())
	t.Cleanup(func() { instance.Close() })
	return port
}

func freeUDPPort(t *testing.T) uint16 {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	port := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()
	return uint16(port)
}

// newHysteria2Client starts a client whose route sends target.test to the
// hysteria2 node HY2 with the given password.
func (s *stand) newHysteria2Client(t *testing.T, port uint16, password string) *daemon.StartedService {
	t.Helper()
	profile := map[string]any{
		"log": map[string]any{"disabled": true},
		"dns": map[string]any{
			"servers": []any{map[string]any{"type": "tcp", "tag": "dns", "server": "127.0.0.1", "server_port": s.dnsPort}},
			"final":   "dns",
		},
		"inbounds": []any{map[string]any{"type": "mixed", "tag": "mixed", "listen": "127.0.0.1", "listen_port": freePort(t)}},
		"outbounds": []any{
			map[string]any{
				"type": "hysteria2", "tag": "HY2", "server": "127.0.0.1", "server_port": port, "password": password,
				"tls": map[string]any{"enabled": true, "server_name": "node2.test", "insecure": true},
			},
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"route": map[string]any{
			"rules": []any{map[string]any{"domain_suffix": []string{"target.test"}, "outbound": "HY2"}},
			"final": "direct",
		},
		"experimental": map[string]any{"cache_file": map[string]any{"enabled": true, "path": filepath.Join(t.TempDir(), "cache.db")}},
	}
	ctx := include.Context(context.Background())
	service := daemon.NewStartedService(daemon.ServiceOptions{Context: ctx})
	require.NoError(t, service.StartOrReloadService(ctx, string(mustJSON(t, profile)), nil))
	t.Cleanup(func() {
		service.CloseService()
		service.Close()
	})
	return service
}

func TestConnectionTestHysteria2(t *testing.T) {
	s := newStand(t)
	port := s.startHysteria2Server(t)
	request := func(fresh bool) *daemon.ConnectionTestRequest {
		return &daemon.ConnectionTestRequest{
			Link:    s.targetURL("target.test", "/"),
			Source:  &daemon.ConnectionTestSource{Inbound: "mixed", SourceIP: "127.0.0.1"},
			Fresh:   fresh,
			Timeout: 10000,
		}
	}

	// H1 — through the router to the hysteria2 node at DEPTH_HTTP: the path
	// completes and the hop is one `dial` stage; a successful hysteria2 dial
	// has no `class` (it classifies failures only) and no proxy_header (the
	// protocol confirms its own handshake).
	t.Run("H1_Path", func(t *testing.T) {
		service := s.newHysteria2Client(t, port, hysteria2Password)
		run := runTest(t, service, request(false))
		require.Zero(t, run.result.RootCause)
		require.EqualValues(t, http.StatusOK, run.result.HttpStatus)
		require.Equal(t, targetBody, string(run.result.Body))
		require.Equal(t, "HY2", attr(run.topLevel(t, "route"), "outbound"))
		dial := run.topLevel(t, "dial")
		require.Equal(t, "HY2", dial.Subject)
		require.Equal(t, stageOK, dial.Result)
		require.Empty(t, attr(dial, "class"))
		require.Empty(t, attr(dial, "reuse"), "reuse=uncontrolled is for fresh only")
		require.Empty(t, run.find("proxy_header", "", dial))
		require.Equal(t, stageOK, run.topLevel(t, "http").Result)
	})

	// H2 — the same with fresh: sing-quic cannot be told to open a new QUIC
	// connection, so the dial is marked reuse=uncontrolled (SPEC 139 §5.6).
	t.Run("H2_FreshUncontrolled", func(t *testing.T) {
		service := s.newHysteria2Client(t, port, hysteria2Password)
		run := runTest(t, service, request(true))
		require.Zero(t, run.result.RootCause)
		require.EqualValues(t, http.StatusOK, run.result.HttpStatus)
		dial := run.topLevel(t, "dial")
		require.Equal(t, stageOK, dial.Result)
		require.Equal(t, "uncontrolled", attr(dial, "reuse"))
	})

	// H3 — wrong password: the test fails at the hysteria2 dial, which is the
	// root cause and is classified as an auth failure by its error.
	t.Run("H3_WrongPassword", func(t *testing.T) {
		service := s.newHysteria2Client(t, port, "wrong")
		run := runTest(t, service, request(false))
		require.Zero(t, run.result.HttpStatus)
		require.NotZero(t, run.result.RootCause)
		dial := run.topLevel(t, "dial")
		require.Equal(t, "HY2", dial.Subject)
		require.Equal(t, stageFailed, dial.Result)
		require.Equal(t, dial.Id, run.result.RootCause, "root cause must be the hysteria2 dial in\n%s", treeString(run.stages))
		require.Equal(t, "auth", attr(dial, "class"), "dial error: %s", dial.Error)
		require.Equal(t, stageSkipped, run.topLevel(t, "http").Result)
	})
}
