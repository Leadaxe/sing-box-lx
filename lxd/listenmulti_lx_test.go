//go:build with_lx_command

package lxd

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// secondLoopback returns a bindable address DIFFERENT from 127.0.0.1, so the
// multi-bind tests exercise two real listeners. 127.0.0.2 exists by default on
// linux but not on darwin, so fall back to any other IPv4 the host holds (a
// LAN or tunnel address) — that is also the shape the feature was built for.
// Skips when the host has exactly one usable address.
func secondLoopback(t *testing.T) string {
	t.Helper()
	candidates := []string{"127.0.0.2"}
	interfaceAddresses, err := net.InterfaceAddrs()
	if err == nil {
		for _, interfaceAddress := range interfaceAddresses {
			network, ok := interfaceAddress.(*net.IPNet)
			if !ok || network.IP.To4() == nil || network.IP.IsLoopback() {
				continue
			}
			candidates = append(candidates, network.IP.String())
		}
	}
	for _, candidate := range candidates {
		probe, probeErr := net.Listen("tcp", net.JoinHostPort(candidate, "0"))
		if probeErr == nil {
			_ = probe.Close()
			return candidate
		}
	}
	t.Skip("host has no second bindable IPv4 address")
	return ""
}

// freePort returns a port free on every given host, so the multi-bind below
// does not race a port that happens to be taken on one interface only.
func freePort(t *testing.T, hosts ...string) int {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		probe, err := net.Listen("tcp", net.JoinHostPort(hosts[0], "0"))
		if err != nil {
			t.Fatal(err)
		}
		port := probe.Addr().(*net.TCPAddr).Port
		_ = probe.Close()
		free := true
		for _, host := range hosts[1:] {
			other, otherErr := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
			if otherErr != nil {
				free = false
				break
			}
			_ = other.Close()
		}
		if free {
			return port
		}
	}
	t.Fatal("no port free on all hosts")
	return 0
}

// The feature's load-bearing property: with several addresses configured, the
// SAME control plane answers on every one of them — not just the first.
func TestRunServesEveryConfiguredAddress(t *testing.T) {
	t.Chdir(t.TempDir())
	second := secondLoopback(t)
	port := freePort(t, "127.0.0.1", second)

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{
			Listen:   ListenConfig{Address: []string{"127.0.0.1", second}, Port: uint16(port)},
			StateDir: t.TempDir(),
		})
	}()
	defer func() {
		// Run owns the process signal handlers; closing it from the test means
		// waiting for the serve error after the listeners go away. Shutting the
		// daemon down is not what this test asserts, so just let the process
		// teardown reap it — but make sure Run did not already return an error.
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned early: %v", err)
			}
		default:
		}
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	for _, host := range []string{"127.0.0.1", second} {
		address := net.JoinHostPort(host, strconv.Itoa(port))
		var lastErr error
		reached := false
		// The listeners come up before bootstrap, but the goroutine still has
		// to get scheduled; retry briefly instead of sleeping a fixed span.
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			response, err := client.Get("http://" + address + "/admin/status")
			if err == nil {
				response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("%s: /admin/status = %d, want 200", address, response.StatusCode)
				}
				reached = true
				break
			}
			lastErr = err
			time.Sleep(50 * time.Millisecond)
		}
		if !reached {
			t.Fatalf("%s never answered: %v", address, lastErr)
		}
	}
}

// A partial bind must be fatal: if one configured address cannot come up, the
// daemon must fail loudly rather than serve on the subset that worked — the
// operator asked for both, and a half-bound daemon is unreachable exactly
// where they expected it.
func TestRunFailsWhenOneAddressIsTaken(t *testing.T) {
	t.Chdir(t.TempDir())
	second := secondLoopback(t)
	port := freePort(t, "127.0.0.1", second)

	// Hold the SECOND address, so the first one binds fine and the failure can
	// only come from the multi-bind loop.
	holder, err := net.Listen("tcp", net.JoinHostPort(second, strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{
			Listen:   ListenConfig{Address: []string{"127.0.0.1", second}, Port: uint16(port)},
			StateDir: t.TempDir(),
		})
	}()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("a taken address must fail the daemon, not be skipped")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return when one address was unavailable")
	}

	// The listener that DID come up must be released on the way out, or the
	// operator's retry after fixing the file hits a phantom bind.
	retry, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("the successfully bound address was leaked: %v", err)
	}
	_ = retry.Close()
}
