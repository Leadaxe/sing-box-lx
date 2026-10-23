package endpoint

// lx: SPEC 030 §8 (issue #50) — the close watchdog over closeEndpoints.

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
)

// blockingEndpoint is an endpoint whose Close blocks until released. Only the
// lifecycle methods are implemented; the embedded interface stays nil.
type blockingEndpoint struct {
	adapter.Endpoint
	tag     string
	release chan struct{}
}

func (e *blockingEndpoint) Type() string { return "fake" }
func (e *blockingEndpoint) Tag() string  { return e.tag }

func (e *blockingEndpoint) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage == adapter.StartStateInitialize {
		scope.Add(e.Close)
	}
	return nil
}

func (e *blockingEndpoint) Close() error {
	<-e.release
	return nil
}

type syncBuffer struct {
	access sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.access.Lock()
	defer b.access.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.access.Lock()
	defer b.access.Unlock()
	return b.buffer.String()
}

func TestCloseEndpointsWatchdogDumpsStuckTag(t *testing.T) {
	saved := closeWatchdogThreshold
	closeWatchdogThreshold = 100 * time.Millisecond
	t.Cleanup(func() { closeWatchdogThreshold = saved })

	output := &syncBuffer{}
	factory := log.NewDefaultFactory(context.Background(), log.Formatter{DisableTimestamp: true, DisableColors: true}, output, "", nil, false)
	if err := factory.Start(); err != nil {
		t.Fatal(err)
	}
	logger := factory.NewLogger("endpoint")

	stuck := &blockingEndpoint{tag: "wg-stuck", release: make(chan struct{})}
	quick := &blockingEndpoint{tag: "wg-quick", release: make(chan struct{})}
	close(quick.release)
	manager := NewManager(logger, nil)
	manager.endpoints = []adapter.Endpoint{stuck, quick}
	root := adapter.NewScope(context.Background(), logger)
	if err := manager.Start(adapter.StartStateInitialize, root); err != nil {
		t.Fatal(err)
	}

	closed := make(chan error, 1)
	go func() { closed <- root.Close() }()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(output.String(), "goroutine dump end") {
		if time.Now().After(deadline) {
			t.Fatalf("watchdog did not dump within 5s; log so far:\n%s", output.String())
		}
		select {
		case err := <-closed:
			t.Fatalf("close returned before the stuck endpoint was released: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	logged := output.String()
	if !strings.Contains(logged, "close endpoints: still running after 0.10s; not returned yet: [wg-stuck]") {
		t.Fatalf("header must name only the stuck endpoint:\n%s", logged)
	}
	if !strings.Contains(logged, "(*blockingEndpoint).Close") {
		t.Fatal("dump must show the stuck Close frame")
	}

	close(stuck.release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not complete after the endpoint was released — the watchdog must not break the join")
	}
	if !strings.Contains(output.String(), "close endpoints: completed after ") {
		t.Fatalf("completion after a fired watchdog must be logged:\n%s", output.String())
	}
}

func TestCloseEndpointsWatchdogSilentOnFastClose(t *testing.T) {
	output := &syncBuffer{}
	factory := log.NewDefaultFactory(context.Background(), log.Formatter{DisableTimestamp: true, DisableColors: true}, output, "", nil, false)
	if err := factory.Start(); err != nil {
		t.Fatal(err)
	}
	logger := factory.NewLogger("endpoint")
	quick := &blockingEndpoint{tag: "wg-quick", release: make(chan struct{})}
	close(quick.release)
	manager := NewManager(logger, nil)
	manager.endpoints = []adapter.Endpoint{quick}
	root := adapter.NewScope(context.Background(), logger)
	if err := manager.Start(adapter.StartStateInitialize, root); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "goroutine dump") || strings.Contains(output.String(), "still running") {
		t.Fatalf("no watchdog output expected on a fast close:\n%s", output.String())
	}
}
