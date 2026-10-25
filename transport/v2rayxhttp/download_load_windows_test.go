//go:build windows && with_xhttp

package v2rayxhttp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	N "github.com/sagernet/sing/common/network"
	"golang.org/x/net/http2"
)

func downloadPoolLimitError(client *Client) error {
	for index, manager := range []*xmuxManager{client.xmux, client.download.xmux} {
		manager.access.Lock()
		for _, pooled := range manager.clients {
			usage := pooled.getOpenUsage()
			if usage < 0 || manager.concurrency > 0 && usage > manager.concurrency {
				manager.access.Unlock()
				return fmt.Errorf("direction %d XMUX usage=%d limit=%d", index, usage, manager.concurrency)
			}
		}
		manager.access.Unlock()
	}
	return nil
}

// This is a real persistent VLESS exchange, not repeated HTTP status checks.
// Extended runs use NOVA_XHTTP_SOAK_SECONDS=300 (per mode) and go test -timeout=30m.
func TestIndependentDownloadOfficialXrayConcurrent(t *testing.T) {
	executable := os.Getenv("NOVA_XRAY_BINARY")
	if executable == "" {
		t.Skip("set NOVA_XRAY_BINARY to the pinned official Windows fixture")
	}
	duration := 5 * time.Second
	if raw := os.Getenv("NOVA_XHTTP_SOAK_SECONDS"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 1 || seconds > 1800 {
			t.Fatal("invalid NOVA_XHTTP_SOAK_SECONDS")
		}
		duration = time.Duration(seconds) * time.Second
	}
	for _, mode := range []string{modeAuto, modePacketUp, modeStreamUp} {
		t.Run(mode, func(t *testing.T) {
			backend, backendTLS := officialXrayBackendLifetime(t, executable, duration+30*time.Second)
			target, _ := url.Parse("https://" + backend)
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
						downGets.Add(1)
						if r.Method != http.MethodGet {
							wrongLeg.Add(1)
						}
					}
					proxy.ServeHTTP(w, r)
				})
			}
			up, upTLS := downloadTestEndpoint(t, 2, front("/load-up/", true))
			down, downTLS := downloadTestEndpoint(t, 2, front("/load-down/", false))
			config, err := tls.NewClient(context.Background(), log.NewNOPFactory().Logger(), "localhost", *upTLS)
			if err != nil {
				t.Fatal(err)
			}
			transport, err := NewClient(context.Background(), N.SystemDialer, up, option.V2RayXHTTPOptions{
				Mode: mode, Path: "/load-up/", Xmux: &option.V2RayXHTTPXmuxOptions{MaxConcurrency: "3"},
				DownloadSettings: &option.V2RayXHTTPDownloadOptions{
					ServerOptions:               option.ServerOptions{Server: down.AddrString(), ServerPort: down.Port},
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: downTLS},
					XHTTP:                       option.V2RayXHTTPOptions{Path: "/load-down/", Xmux: &option.V2RayXHTTPXmuxOptions{MaxConcurrency: "2"}},
				},
			}, config)
			if err != nil {
				t.Fatal(err)
			}
			client := transport.(*Client)
			defer client.Close()
			if client.xmux == client.download.xmux {
				t.Fatal("shared directional XMUX pool")
			}
			ctx, cancel := context.WithTimeout(context.Background(), duration+20*time.Second)
			defer cancel()
			const workers = 12
			start := make(chan struct{})
			errors := make(chan error, workers)
			var wg sync.WaitGroup
			var active, peak atomic.Int32
			var exchanges atomic.Int64
			finish := time.Now().Add(duration)
			for worker := 0; worker < workers; worker++ {
				wg.Add(1)
				go func(worker int) {
					defer wg.Done()
					<-start
					err := func() error {
						conn, err := client.DialContext(ctx)
						if err != nil {
							return err
						}
						defer conn.Close()
						now := active.Add(1)
						defer active.Add(-1)
						for old := peak.Load(); now > old; old = peak.Load() {
							if peak.CompareAndSwap(old, now) {
								break
							}
						}
						payload := bytes.Repeat([]byte{byte(worker + 1)}, 16*1024)
						received := make([]byte, len(payload))
						for sequence := uint64(0); ; sequence++ {
							if err := downloadPoolLimitError(client); err != nil {
								return err
							}
							binary.BigEndian.PutUint64(payload, uint64(worker))
							binary.BigEndian.PutUint64(payload[8:], sequence)
							request := payload
							if sequence == 0 {
								uuid, _ := hex.DecodeString("11111111111141118111111111111111")
								prefix := append([]byte{0}, uuid...)
								prefix = append(prefix, 0, 1, 0, 80, 1, 127, 0, 0, 1)
								request = append(prefix, payload...)
							}
							conn.SetDeadline(time.Now().Add(10 * time.Second))
							if _, err = io.Copy(conn, bytes.NewReader(request)); err != nil {
								return err
							}
							if sequence == 0 {
								header := make([]byte, 2)
								if _, err = io.ReadFull(conn, header); err != nil {
									return err
								}
								if header[0] != 0 {
									return fmt.Errorf("invalid VLESS response version")
								}
								if _, err = io.CopyN(io.Discard, conn, int64(header[1])); err != nil {
									return err
								}
							}
							if _, err = io.ReadFull(conn, received); err != nil {
								return err
							}
							if !bytes.Equal(payload, received) {
								return fmt.Errorf("cross-session/corrupt echo worker=%d sequence=%d", worker, sequence)
							}
							exchanges.Add(1)
							if time.Now().After(finish) {
								return nil
							}
							select {
							case <-ctx.Done():
								return ctx.Err()
							case <-time.After(100 * time.Millisecond):
							}
						}
					}()
					if err != nil {
						errors <- fmt.Errorf("worker %d: %w", worker, err)
						cancel()
					}
				}(worker)
			}
			close(start)
			wg.Wait()
			close(errors)
			for err := range errors {
				t.Error(err)
			}
			if t.Failed() {
				return
			}
			if peak.Load() != workers || exchanges.Load() < workers*2 {
				t.Fatalf("insufficient concurrent exchange: peak=%d exchanges=%d", peak.Load(), exchanges.Load())
			}
			if upRequests.Load() == 0 || downGets.Load() != workers || wrongLeg.Load() != 0 {
				t.Fatalf("wrong legs up=%d down=%d wrong=%d", upRequests.Load(), downGets.Load(), wrongLeg.Load())
			}
			assertNoDownloadLeases(t, client)
			t.Logf("persistent H2/H2 VLESS echo: duration=%s workers=%d peak=%d exchanges=%d bytes=%d", duration, workers, peak.Load(), exchanges.Load(), exchanges.Load()*16*1024)
		})
	}
}
