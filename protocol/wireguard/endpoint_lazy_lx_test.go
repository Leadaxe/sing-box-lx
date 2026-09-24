package wireguard

// lx: SPEC 097 — lazy device build, endpoint side. The devices are fakes
// injected through the transport's build seam: a real wireguard-go device runs
// on top of them (stays down, no network), and the tests count builds and
// live devices without depending on with_gvisor.

import (
	"context"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/wireguard"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/wireguard-go/device"
	wgTun "github.com/sagernet/wireguard-go/tun"
)

// deviceCounter counts fake device builds and the devices alive right now.
type deviceCounter struct {
	builds  atomic.Int32
	live    atomic.Int32
	maxLive atomic.Int32
	// gate, when set, blocks every build until it is closed.
	gate chan struct{}
}

// countDeviceBuilds swaps the transport's device constructor for fakes for the
// duration of the test. Register it before any endpoint so its restore runs
// after the endpoints' Close cleanups.
func countDeviceBuilds(t *testing.T) *deviceCounter {
	t.Helper()
	counter := &deviceCounter{}
	restore := wireguard.SetNewDeviceFnForTest(func(options wireguard.DeviceOptions) (wireguard.Device, error) {
		if counter.gate != nil {
			<-counter.gate
		}
		counter.builds.Add(1)
		live := counter.live.Add(1)
		for {
			seen := counter.maxLive.Load()
			if live <= seen || counter.maxLive.CompareAndSwap(seen, live) {
				break
			}
		}
		return &fakeTunDevice{counter: counter, events: make(chan wgTun.Event), closed: make(chan struct{})}, nil
	})
	t.Cleanup(restore)
	return counter
}

type fakeTunDevice struct {
	counter   *deviceCounter
	closeOnce sync.Once
	events    chan wgTun.Event
	closed    chan struct{}
}

func (d *fakeTunDevice) File() *os.File { return nil }
func (d *fakeTunDevice) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	<-d.closed
	return 0, os.ErrClosed
}
func (d *fakeTunDevice) Write(bufs [][]byte, offset int) (int, error) { return len(bufs), nil }
func (d *fakeTunDevice) MTU() (int, error)                            { return 1408, nil }
func (d *fakeTunDevice) Name() (string, error)                        { return "fake", nil }
func (d *fakeTunDevice) Events() <-chan wgTun.Event                   { return d.events }
func (d *fakeTunDevice) BatchSize() int                               { return 1 }
func (d *fakeTunDevice) Close() error {
	d.closeOnce.Do(func() {
		close(d.closed)
		close(d.events)
		d.counter.live.Add(-1)
	})
	return nil
}

func (d *fakeTunDevice) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return nil, os.ErrInvalid
}

func (d *fakeTunDevice) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}
func (d *fakeTunDevice) Start() error                    { return nil }
func (d *fakeTunDevice) SetDevice(device *device.Device) {}
func (d *fakeTunDevice) Inet4Address() netip.Addr        { return netip.MustParseAddr("10.0.0.2") }
func (d *fakeTunDevice) Inet6Address() netip.Addr        { return netip.Addr{} }

// newLazyTestEndpoint builds a lazy endpoint over a real (lazy) transport
// endpoint; its devices come from countDeviceBuilds. Closed at cleanup.
func newLazyTestEndpoint(t *testing.T, tag string, budget *BuildBudget) *Endpoint {
	t.Helper()
	transport, err := wireguard.NewEndpoint(wireguard.EndpointOptions{
		Context:    context.Background(),
		Logger:     logger.NOP(),
		Name:       tag,
		MTU:        1408,
		Address:    []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")},
		PrivateKey: "iOx8sYFBnQjKMkTfTBaLZ5+DEHU4S3vzcSLp+HDaOWc=",
		Peers: []wireguard.PeerOptions{{
			Endpoint:   M.ParseSocksaddr("192.0.2.1:51820"),
			PublicKey:  "eBpqZlJmSVBGNW9BOEZ4S3lRZTNQd0RhbWFnZTBTNTA=",
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
		}},
		LazyDevice: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := &Endpoint{
		Adapter:  endpoint.NewAdapter(C.TypeWireGuard, tag, []string{N.NetworkTCP}, nil),
		ctx:      context.Background(),
		logger:   logger.NOP(),
		endpoint: transport,
		lazy:     true,
		budget:   budget,
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func startLazy(t *testing.T, w *Endpoint) {
	t.Helper()
	for _, stage := range []adapter.StartStage{adapter.StartStateStart, adapter.StartStatePostStart} {
		if err := w.Start(stage); err != nil {
			t.Fatalf("start stage %d: %v", stage, err)
		}
	}
}

func dialOnce(w *Endpoint) {
	// The fake device refuses the dial itself; only the wake/build matters.
	_, _ = w.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("10.0.0.9:80"))
}

func assertState(t *testing.T, w *Endpoint, want string) {
	t.Helper()
	if got := w.IdleState().State; got != want {
		t.Fatalf("state: got %q, want %q", got, want)
	}
}

// PLAN test 1: a lazy start builds nothing and reports never_built.
func TestLazyStart_buildsNoDevice(t *testing.T) {
	counter := countDeviceBuilds(t)
	w := newLazyTestEndpoint(t, "wg-lazy", nil)
	startLazy(t, w)
	if n := counter.builds.Load(); n != 0 {
		t.Fatalf("lazy start built %d devices, want 0", n)
	}
	if !w.endpoint.TornDown() {
		t.Fatal("the transport must still have no device")
	}
	assertState(t, w, adapter.EndpointStateNeverBuilt)
	if !w.torndown.Load() || !w.idleAsleep.Load() || w.started.Load() || !w.neverBuilt.Load() {
		t.Fatal("a lazy endpoint starts at level 3: torndown && idleAsleep && !started && neverBuilt")
	}
	if w.lastActivity.Load() == 0 || w.sleepSince.Load() == 0 {
		t.Fatal("the idle and sleep clocks must be stamped at start")
	}
	// The idle tick must leave a never-built endpoint alone.
	w.SuspendIfIdle(false, 0, 0)
	w.TeardownIfSlept(time.Nanosecond)
	if n := counter.builds.Load(); n != 0 {
		t.Fatalf("idle tick built %d devices", n)
	}
	assertState(t, w, adapter.EndpointStateNeverBuilt)
}

// PLAN test 2: the first dial builds exactly one device, ten parallel first
// dials too.
func TestLazyFirstDial_buildsOnce(t *testing.T) {
	counter := countDeviceBuilds(t)
	w := newLazyTestEndpoint(t, "wg-lazy", nil)
	startLazy(t, w)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dialOnce(w)
		}()
	}
	wg.Wait()
	if n := counter.builds.Load(); n != 1 {
		t.Fatalf("10 parallel first dials built %d devices, want 1", n)
	}
	assertState(t, w, adapter.EndpointStateUp)
	if w.neverBuilt.Load() || w.torndown.Load() || !w.started.Load() {
		t.Fatal("after the first build the endpoint is up and no longer never-built")
	}
	if w.dialsInFlight.Load() != 0 {
		t.Fatal("every dial must leave the in-flight count")
	}
	dialOnce(w)
	if n := counter.builds.Load(); n != 1 {
		t.Fatalf("a dial on a built endpoint rebuilt it (%d builds)", n)
	}
}

// PLAN test 6 (endpoint half): never_built → building → up → asleep → torn_down.
func TestLazyStateTransitions(t *testing.T) {
	counter := countDeviceBuilds(t)
	counter.gate = make(chan struct{})
	w := newLazyTestEndpoint(t, "wg-lazy", nil)
	startLazy(t, w)
	assertState(t, w, adapter.EndpointStateNeverBuilt)

	done := make(chan struct{})
	go func() {
		defer close(done)
		dialOnce(w)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for w.IdleState().State != adapter.EndpointStateBuilding {
		if time.Now().After(deadline) {
			t.Fatal("never observed the building state")
		}
		time.Sleep(time.Millisecond)
	}
	close(counter.gate)
	<-done
	assertState(t, w, adapter.EndpointStateUp)

	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, time.Minute, 0)
	assertState(t, w, adapter.EndpointStateAsleep)
	if idle := w.IdleState(); idle.IdleSince < time.Hour || idle.SleepSince <= 0 {
		t.Fatalf("asleep: idle clocks not reported: %+v", idle)
	}

	w.sleepSince.Store(time.Now().Add(-time.Hour).UnixNano())
	w.TeardownIfSlept(time.Minute)
	assertState(t, w, adapter.EndpointStateTornDown)
	if counter.live.Load() != 0 {
		t.Fatal("teardown must close the device")
	}

	dialOnce(w)
	assertState(t, w, adapter.EndpointStateUp)
	if n := counter.builds.Load(); n != 2 {
		t.Fatalf("wake after teardown must rebuild once more, builds=%d", n)
	}
	_ = w.Close()
	assertState(t, w, adapter.EndpointStateDown)
}

// PLAN test 3: Close before the first dial is clean — no panic, no device
// built, no goroutine left behind, and a later dial does not resurrect it.
func TestLazyCloseBeforeFirstDial(t *testing.T) {
	counter := countDeviceBuilds(t)
	before := runtime.NumGoroutine()
	w := newLazyTestEndpoint(t, "wg-lazy", nil)
	startLazy(t, w)
	if err := w.Close(); err != nil {
		t.Fatalf("Close of a never-built endpoint: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	dialOnce(w)
	if n := counter.builds.Load(); n != 0 {
		t.Fatalf("a dial after Close built %d devices", n)
	}
	for _, stage := range []adapter.StartStage{adapter.StartStateStart, adapter.StartStatePostStart} {
		if err := w.Start(stage); err == nil {
			t.Fatal("Start after Close must refuse")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines leaked: %d before, %d after", before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Close of a built lazy endpoint releases its devices and goroutines.
func TestLazyCloseAfterBuild_noLeak(t *testing.T) {
	counter := countDeviceBuilds(t)
	before := runtime.NumGoroutine()
	w := newLazyTestEndpoint(t, "wg-lazy", nil)
	startLazy(t, w)
	dialOnce(w)
	if counter.live.Load() != 1 {
		t.Fatal("precondition: one live device")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if counter.live.Load() != 0 {
		t.Fatal("Close must close the device")
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines leaked: %d before, %d after", before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// SPEC acceptance 1: probes over K lazy endpoints under a budget of N keep at
// most N devices alive at once, and every endpoint still gets built.
func TestLazyBudget_probesKeepAtMostN(t *testing.T) {
	counter := countDeviceBuilds(t)
	budget := newTestBudget(t, 2, nil)
	const endpoints = 6
	var all []*Endpoint
	for i := range endpoints {
		w := newLazyTestEndpoint(t, "wg-"+string(rune('a'+i)), budget)
		startLazy(t, w)
		all = append(all, w)
	}
	var wg sync.WaitGroup
	for _, w := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dialOnce(w)
		}()
	}
	waitOrFail(t, &wg, 10*time.Second)
	if max := counter.maxLive.Load(); max > 2 {
		t.Fatalf("at most 2 devices may live at once, saw %d", max)
	}
	if n := counter.builds.Load(); n < endpoints {
		t.Fatalf("every endpoint must be built once, builds=%d", n)
	}
	if snapshot := budget.Snapshot(); len(snapshot.Built) > 2 || len(snapshot.Building) != 0 {
		t.Fatalf("budget left over budget: %+v", snapshot)
	}
	for _, w := range all {
		if state := w.IdleState().State; state != adapter.EndpointStateUp && state != adapter.EndpointStateTornDown {
			t.Fatalf("%s: unexpected state %q", w.Tag(), state)
		}
	}
}

func waitOrFail(t *testing.T, wg *sync.WaitGroup, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("timed out: deadlock?")
	}
}

// initLazyBuild reads lx.wg from the box context: lazy only with lazy_build and
// never for listen-mode; one budget per box slot, shared by its endpoints; no
// budget when neither lazy_build nor build_max is set.
func TestInitLazyBuild_fromContext(t *testing.T) {
	boxContext := func(wg option.LXWGResolved) context.Context {
		ctx := service.ContextWithPtr(context.Background(), &option.LXResolved{WG: wg})
		return service.ContextWithPtr(ctx, &adapter.LXBuildBudgetSlot{})
	}
	ctx := boxContext(option.LXWGResolved{LazyBuild: true, BuildMax: 3})
	first, second, listener := &Endpoint{}, &Endpoint{}, &Endpoint{}
	first.initLazyBuild(ctx, option.WireGuardEndpointOptions{})
	second.initLazyBuild(ctx, option.WireGuardEndpointOptions{})
	listener.initLazyBuild(ctx, option.WireGuardEndpointOptions{ListenPort: 51820})
	if !first.lazy || !second.lazy {
		t.Fatal("lazy_build makes a dialing endpoint lazy")
	}
	if listener.lazy {
		t.Fatal("a listen-mode endpoint is never lazy")
	}
	if first.budget == nil || first.budget != second.budget || first.budget != listener.budget {
		t.Fatal("the endpoints of one box share one budget")
	}
	if first.budget.Snapshot().Max != 3 {
		t.Fatal("the budget carries build_max")
	}

	other := &Endpoint{}
	other.initLazyBuild(boxContext(option.LXWGResolved{LazyBuild: true}), option.WireGuardEndpointOptions{})
	if other.budget == nil || other.budget == first.budget {
		t.Fatal("every box gets its own budget")
	}

	capOnly := &Endpoint{}
	capOnly.initLazyBuild(boxContext(option.LXWGResolved{BuildMax: 2}), option.WireGuardEndpointOptions{})
	if capOnly.lazy || capOnly.budget == nil {
		t.Fatal("build_max without lazy_build: devices built at start, budget kept")
	}

	off := &Endpoint{}
	off.initLazyBuild(boxContext(option.LXWGResolved{}), option.WireGuardEndpointOptions{})
	if off.lazy || off.budget != nil {
		t.Fatal("with the keys off nothing changes")
	}
	bare := &Endpoint{}
	bare.initLazyBuild(context.Background(), option.WireGuardEndpointOptions{})
	if bare.lazy || bare.budget != nil {
		t.Fatal("a context without the lx block changes nothing")
	}
}
