//go:build windows && with_xhttp

package v2rayxhttp

import (
	"context"
	"crypto/sha256"
	stdTLS "crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	N "github.com/sagernet/sing/common/network"
	"golang.org/x/net/http2"
)

func officialXrayBackend(t *testing.T, executable string) (string, *stdTLS.Config) {
	t.Helper()
	file, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	file.Close()
	if err != nil || hex.EncodeToString(hash.Sum(nil)) != "c77a00eb84fb93ae044c2d771a1cabcc033b81e487253fbc35b12df6879c1e15" {
		t.Fatal("official Xray 26.10.21 fixture checksum mismatch")
	}
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); conn.SetDeadline(time.Now().Add(10 * time.Second)); io.Copy(conn, conn) }()
		}
	}()
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backend := reserve.Addr().String()
	port := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	echoPort := echo.Addr().(*net.TCPAddr).Port
	certificate, err := tls.GenerateKeyPair(nil, nil, time.Now, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	material := t.TempDir()
	certPath, keyPath := filepath.Join(material, "server.pem"), filepath.Join(material, "server.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})
	keyDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("fixture certificate invalid")
	}
	proxyTLS := &stdTLS.Config{ServerName: "localhost", RootCAs: roots, NextProtos: []string{"h2"}}
	config := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "vless",
			"settings":       map[string]any{"clients": []any{map[string]any{"id": "11111111-1111-4111-8111-111111111111"}}, "decryption": "none"},
			"streamSettings": map[string]any{"network": "xhttp", "security": "tls", "tlsSettings": map[string]any{"alpn": []string{"h2"}, "certificates": []any{map[string]any{"certificateFile": certPath, "keyFile": keyPath}}}, "xhttpSettings": map[string]any{"path": "/shared/", "mode": "auto"}}}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{"redirect": echo.Addr().String(),
			"finalRules": []any{map[string]any{"action": "allow", "network": "tcp", "ip": []string{"127.0.0.1/32"}, "port": echoPort}}}}},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "xray.json")
	if err = os.WriteFile(configPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, executable, "run", "-c", configPath)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	logs, err := os.Create(filepath.Join(t.TempDir(), "xray.log"))
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout = logs
	command.Stderr = logs
	if err = command.Start(); err != nil {
		logs.Close()
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Xray fixture did not exit")
		}
		logs.Close()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("Xray fixture exited: %v", err)
		default:
		}
		conn, err := net.DialTimeout("tcp", backend, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return backend, proxyTLS
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Xray fixture did not listen")
	return "", nil
}

// Two independently authenticated frontends converge on ONE official Xray
// backend. Its VLESS handshake and TCP echo prove pairing beyond HTTP status200.
func TestIndependentDownloadOfficialXray(t *testing.T) {
	executable := os.Getenv("NOVA_XRAY_BINARY")
	if executable == "" {
		t.Skip("set NOVA_XRAY_BINARY to the pinned official Windows fixture")
	}
	backend, backendTLS := officialXrayBackend(t, executable)
	target, _ := url.Parse("https://" + backend)
	pairs := [][2]int{{1, 1}, {1, 2}, {2, 1}, {2, 2}}
	if newHTTP3DownloadServer != nil {
		pairs = append(pairs, [2]int{1, 3}, [2]int{2, 3}, [2]int{3, 1}, [2]int{3, 2}, [2]int{3, 3})
	}
	for _, mode := range []string{modeAuto, modePacketUp, modeStreamUp} {
		for _, pair := range pairs {
			t.Run(fmt.Sprintf("%s/h%d-h%d", mode, pair[0], pair[1]), func(t *testing.T) {
				var upRequests, downGets, wrongLeg atomic.Int32
				front := func(prefix string, up bool) http.Handler {
					proxy := httputil.NewSingleHostReverseProxy(target)
					original := proxy.Director
					proxy.Director = func(r *http.Request) {
						original(r)
						r.URL.Path = "/shared/" + strings.TrimPrefix(r.URL.Path, prefix)
						r.URL.RawPath = ""
					}
					proxy.FlushInterval = -1
					transport := &http2.Transport{TLSClientConfig: backendTLS.Clone(), DisableCompression: true}
					proxy.Transport = transport
					t.Cleanup(transport.CloseIdleConnections)
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if !strings.HasPrefix(r.URL.Path, prefix) {
							wrongLeg.Add(1)
							http.Error(w, "wrong path", 400)
							return
						}
						if up {
							upRequests.Add(1)
							if r.Method == http.MethodGet {
								wrongLeg.Add(1)
							}
						} else {
							if r.Method != http.MethodGet {
								wrongLeg.Add(1)
							}
							downGets.Add(1)
						}
						proxy.ServeHTTP(w, r)
					})
				}
				up, upTLS := downloadTestEndpoint(t, pair[0], front("/up/", true))
				down, downTLS := downloadTestEndpoint(t, pair[1], front("/down/", false))
				var tlsConfig tls.Config
				var err error
				if upTLS != nil {
					tlsConfig, err = tls.NewClient(context.Background(), log.NewNOPFactory().Logger(), "localhost", *upTLS)
					if err != nil {
						t.Fatal(err)
					}
				}
				transport, err := NewClient(context.Background(), N.SystemDialer, up, option.V2RayXHTTPOptions{Mode: mode, Path: "/up/",
					DownloadSettings: &option.V2RayXHTTPDownloadOptions{ServerOptions: option.ServerOptions{Server: down.AddrString(), ServerPort: down.Port},
						OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: downTLS}, XHTTP: option.V2RayXHTTPOptions{Path: "/down/"}}}, tlsConfig)
				if err != nil {
					t.Fatal(err)
				}
				client := transport.(*Client)
				defer client.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				conn, err := client.DialContext(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				uuid, _ := hex.DecodeString("11111111111141118111111111111111")
				request := append([]byte{0}, uuid...)
				request = append(request, 0, 1)
				// Freedom is forced to the local echo fixture, including a final allow rule.
				port := make([]byte, 2)
				binary.BigEndian.PutUint16(port, 80)
				request = append(request, port...)
				request = append(request, 1, 127, 0, 0, 1)
				request = append(request, []byte("hello")...)
				if _, err = conn.Write(request); err != nil {
					t.Fatal(err)
				}
				header := make([]byte, 2)
				if _, err = io.ReadFull(conn, header); err != nil {
					t.Fatal(err)
				}
				if header[0] != 0 {
					t.Fatal("invalid VLESS response version")
				}
				if header[1] > 0 {
					if _, err = io.CopyN(io.Discard, conn, int64(header[1])); err != nil {
						t.Fatal(err)
					}
				}
				payload := make([]byte, 5)
				if _, err = io.ReadFull(conn, payload); err != nil || string(payload) != "hello" {
					t.Fatalf("official Xray echo %q: %v", payload, err)
				}
				if upRequests.Load() == 0 || downGets.Load() == 0 || wrongLeg.Load() != 0 {
					t.Fatal("official traffic did not use both intended legs")
				}
				conn.Close()
				assertNoDownloadLeases(t, client)
			})
		}
	}
}
