package masque

// lx: SPECS/TASKS/074 — `vhttp: auto`: h3 first, h2 when the QUIC handshake does
// not complete. The dial legs themselves need a live endpoint, so these tests
// drive the decision logic (which leg is tried, what is remembered, how a
// cancelled dial is distinguished from an h3 verdict) rather than the network.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/transport/masque"
)

// scripted builds a connectLegs whose legs record their order and behave as told.
// An h3 leg with hang=true never returns until its context dies — the field
// failure mode this feature exists for (QUIC swallowed, no error ever).
func scripted(calls *[]string, h3Err error, h3Hang bool, h2Err error) *connectLegs {
	var mu sync.Mutex
	note := func(name string) {
		mu.Lock()
		*calls = append(*calls, name)
		mu.Unlock()
	}
	return &connectLegs{
		h3: func(ctx context.Context, budget time.Duration) (io.Closer, masque.IpConn, error) {
			note("h3")
			if h3Hang {
				// mimic the real leg: the budget caps the handshake, not the caller
				if budget > 0 {
					timer := time.NewTimer(budget)
					defer timer.Stop()
					select {
					case <-timer.C:
						return nil, nil, context.DeadlineExceeded
					case <-ctx.Done():
						return nil, nil, ctx.Err()
					}
				}
				<-ctx.Done()
				return nil, nil, ctx.Err()
			}
			if h3Err != nil {
				return nil, nil, h3Err
			}
			return stubCloser{}, nil, nil
		},
		h2: func(ctx context.Context) (io.Closer, masque.IpConn, error) {
			note("h2")
			if h2Err != nil {
				return nil, nil, h2Err
			}
			return stubCloser{}, nil, nil
		},
	}
}

type stubCloser struct{}

func (stubCloser) Close() error { return nil }

// newAutoOutbound builds the minimum Outbound the decision path touches.
func newAutoOutbound(auto bool, configured string) *Outbound {
	return &Outbound{
		network:     configured,
		autoMode:    auto,
		autoH3Delay: 50 * time.Millisecond,
		logger:      discardLogger{},
	}
}

func TestEffectiveNetworkStartsWithH3(t *testing.T) {
	o := newAutoOutbound(true, "h3")
	if got := o.effectiveNetwork(); got != "h3" {
		t.Fatalf("auto must start on h3, got %q", got)
	}
}

func TestEffectiveNetworkHonoursConfiguredWhenNotAuto(t *testing.T) {
	for _, network := range []string{"h3", "h2"} {
		o := newAutoOutbound(false, network)
		// even if something was remembered, a fixed vhttp wins
		o.rememberNetwork("h2")
		if got := o.effectiveNetwork(); got != network {
			t.Fatalf("configured %q must be used, got %q", network, got)
		}
	}
}

func TestRememberNetworkSticks(t *testing.T) {
	o := newAutoOutbound(true, "h3")
	o.rememberNetwork("h2")
	if got := o.effectiveNetwork(); got != "h2" {
		t.Fatalf("auto must reuse the winning leg, got %q", got)
	}
	o.rememberNetwork("h3")
	if got := o.effectiveNetwork(); got != "h3" {
		t.Fatalf("auto must follow the newest winner, got %q", got)
	}
}

func TestRememberNetworkIsRaceFree(t *testing.T) {
	o := newAutoOutbound(true, "h3")
	var done atomic.Int32
	for i := 0; i < 8; i++ {
		go func(i int) {
			for j := 0; j < 200; j++ {
				if i%2 == 0 {
					o.rememberNetwork("h3")
				} else {
					o.rememberNetwork("h2")
				}
				_ = o.effectiveNetwork()
			}
			done.Add(1)
		}(i)
	}
	deadline := time.After(5 * time.Second)
	for done.Load() < 8 {
		select {
		case <-deadline:
			t.Fatal("timeout")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestAutoTimeoutConstantIsSane(t *testing.T) {
	// Field data: a working h3 handshake lands in 250-700 ms even through two
	// proxy hops. The cap must sit clearly above that and clearly below the
	// several-tens-of-seconds a filtered path would otherwise burn.
	if autoH3Timeout < time.Second || autoH3Timeout > 10*time.Second {
		t.Fatalf("autoH3Timeout out of sane range: %v", autoH3Timeout)
	}
}

type discardLogger struct{}

func (discardLogger) Trace(...any)                         {}
func (discardLogger) Debug(...any)                         {}
func (discardLogger) Info(...any)                          {}
func (discardLogger) Warn(...any)                          {}
func (discardLogger) Error(...any)                         {}
func (discardLogger) Fatal(...any)                         {}
func (discardLogger) Panic(...any)                         {}
func (discardLogger) TraceContext(context.Context, ...any) {}
func (discardLogger) DebugContext(context.Context, ...any) {}
func (discardLogger) InfoContext(context.Context, ...any)  {}
func (discardLogger) WarnContext(context.Context, ...any)  {}
func (discardLogger) ErrorContext(context.Context, ...any) {}
func (discardLogger) FatalContext(context.Context, ...any) {}
func (discardLogger) PanicContext(context.Context, ...any) {}

// The point of the feature: a hanging h3 must not hang the dial — it must be
// capped and answered by h2.
func TestAutoFallsBackWhenH3Hangs(t *testing.T) {
	var calls []string
	o := newAutoOutbound(true, "h3")
	o.autoH3Delay = 40 * time.Millisecond
	o.legsForTest = scripted(&calls, nil, true, nil)

	start := time.Now()
	closer, _, network, err := o.connect(context.Background(), o.effectiveNetwork())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("auto must recover via h2, got %v", err)
	}
	if closer == nil || network != "h2" {
		t.Fatalf("expected an h2 tunnel, got network=%q closer=%v", network, closer)
	}
	if strings.Join(calls, ",") != "h3,h2" {
		t.Fatalf("h3 must be tried first: %v", calls)
	}
	if elapsed > time.Second {
		t.Fatalf("h3 leg was not bounded: %v", elapsed)
	}
	// the winning leg is remembered, so the next tunnel skips the h3 timeout
	if got := o.effectiveNetwork(); got != "h2" {
		t.Fatalf("h2 must be remembered, got %q", got)
	}
}

func TestAutoKeepsH3WhenItWorks(t *testing.T) {
	var calls []string
	o := newAutoOutbound(true, "h3")
	o.legsForTest = scripted(&calls, nil, false, errors.New("h2 must not be dialled"))

	_, _, network, err := o.connect(context.Background(), o.effectiveNetwork())
	if err != nil || network != "h3" {
		t.Fatalf("h3 success must be used as is: network=%q err=%v", network, err)
	}
	if strings.Join(calls, ",") != "h3" {
		t.Fatalf("h2 must not be dialled when h3 works: %v", calls)
	}
	if got := o.effectiveNetwork(); got != "h3" {
		t.Fatalf("h3 must be remembered, got %q", got)
	}
}

// A caller that gives up mid-dial is not an h3 verdict: falling back there would
// dial h2 on a context that is already dead, and would poison the memory.
func TestAutoDoesNotFallBackOnCallerCancellation(t *testing.T) {
	var calls []string
	o := newAutoOutbound(true, "h3")
	o.autoH3Delay = time.Second
	o.legsForTest = scripted(&calls, nil, true, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, _, _, err := o.connect(ctx, o.effectiveNetwork())
	if err == nil {
		t.Fatal("a cancelled dial must fail, not fall back")
	}
	if strings.Join(calls, ",") != "h3" {
		t.Fatalf("h2 must not be dialled after cancellation: %v", calls)
	}
	if o.autoNetwork.Load() != nil {
		t.Fatal("a cancelled dial must not be remembered as a verdict")
	}
}

// When both legs fail the error must name both, otherwise "h2 failed" hides why
// h3 was abandoned in the first place.
func TestAutoReportsBothLegsOnTotalFailure(t *testing.T) {
	var calls []string
	o := newAutoOutbound(true, "h3")
	o.legsForTest = scripted(&calls, errors.New("quic refused"), false, errors.New("tcp refused"))

	_, _, _, err := o.connect(context.Background(), o.effectiveNetwork())
	if err == nil {
		t.Fatal("expected failure")
	}
	message := err.Error()
	if !strings.Contains(message, "quic refused") || !strings.Contains(message, "tcp refused") {
		t.Fatalf("both legs must be reported, got %q", message)
	}
}

// A fixed vhttp keeps the old single-leg behaviour: no fallback, no memory.
func TestFixedNetworkNeverFallsBack(t *testing.T) {
	var calls []string
	o := newAutoOutbound(false, "h3")
	o.legsForTest = scripted(&calls, errors.New("quic refused"), false, nil)

	_, _, network, err := o.connect(context.Background(), o.effectiveNetwork())
	if err == nil {
		t.Fatal("vhttp: h3 must surface the h3 error")
	}
	if network != "h3" || strings.Join(calls, ",") != "h3" {
		t.Fatalf("no fallback allowed on a fixed vhttp: network=%q calls=%v", network, calls)
	}
}

// After h2 is remembered, the next dial starts there — the h3 timeout is paid once.
func TestRememberedLegIsTriedFirst(t *testing.T) {
	var calls []string
	o := newAutoOutbound(true, "h3")
	o.autoH3Delay = 40 * time.Millisecond
	o.legsForTest = scripted(&calls, nil, true, nil)
	if _, _, _, err := o.connect(context.Background(), o.effectiveNetwork()); err != nil {
		t.Fatalf("first dial: %v", err)
	}
	calls = nil
	if _, _, network, err := o.connect(context.Background(), o.effectiveNetwork()); err != nil || network != "h2" {
		t.Fatalf("second dial: network=%q err=%v", network, err)
	}
	if strings.Join(calls, ",") != "h2" {
		t.Fatalf("remembered leg must be tried first, got %v", calls)
	}
}

// Regression for the field bug: a slow UDP-socket dial (a detour, a whole chain
// of hops) must NOT consume the handshake budget — otherwise `auto` either falls
// back for the wrong reason or, as observed live, never falls back at all
// (h3 leg burned 1m6s against a 3 s cap). The budget starts after the socket.
func TestAutoBudgetCoversHandshakeNotSocketDial(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	o := newAutoOutbound(true, "h3")
	o.autoH3Delay = 40 * time.Millisecond
	socketDial := 250 * time.Millisecond
	o.legsForTest = &connectLegs{
		h3: func(ctx context.Context, budget time.Duration) (io.Closer, masque.IpConn, error) {
			mu.Lock()
			calls = append(calls, "h3")
			mu.Unlock()
			// slow socket dial, charged to the caller's context only
			select {
			case <-time.After(socketDial):
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			if budget <= 0 {
				t.Error("auto must pass a handshake budget")
			}
			// handshake then hangs; the budget must end it
			timer := time.NewTimer(budget)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil, nil, context.DeadlineExceeded
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		},
		h2: func(ctx context.Context) (io.Closer, masque.IpConn, error) {
			mu.Lock()
			calls = append(calls, "h2")
			mu.Unlock()
			return stubCloser{}, nil, nil
		},
	}
	_, _, network, err := o.connect(context.Background(), o.effectiveNetwork())
	if err != nil || network != "h2" {
		t.Fatalf("slow socket + hung handshake must still reach h2: network=%q err=%v", network, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(calls, ",") != "h3,h2" {
		t.Fatalf("expected h3 then h2, got %v", calls)
	}
}

// Ядро редизайна: h3-нога, застрявшая НАВСЕГДА (игнорирует и ctx, и бюджет —
// как quic-go, запаркованный в чужом cleanup), не должна держать дозвон.
// На прежней схеме (ждать возврата ноги) этот тест зависал бы.
func TestAutoFallsBackWhenH3StuckForever(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	o := newAutoOutbound(true, "h3")
	o.autoH3Delay = 50 * time.Millisecond
	stuck := make(chan struct{}) // никогда не закрывается
	o.legsForTest = &connectLegs{
		h3: func(ctx context.Context, budget time.Duration) (io.Closer, masque.IpConn, error) {
			mu.Lock()
			calls = append(calls, "h3")
			mu.Unlock()
			<-stuck // ни ctx, ни бюджет не уважаются — намеренно
			return nil, nil, errors.New("unreachable")
		},
		h2: func(ctx context.Context) (io.Closer, masque.IpConn, error) {
			mu.Lock()
			calls = append(calls, "h2")
			mu.Unlock()
			return stubCloser{}, nil, nil
		},
	}
	start := time.Now()
	_, _, network, err := o.connect(context.Background(), o.effectiveNetwork())
	elapsed := time.Since(start)
	if err != nil || network != "h2" {
		t.Fatalf("stuck h3 must not block the dial: network=%q err=%v", network, err)
	}
	if elapsed > time.Second {
		t.Fatalf("dial waited for the stuck leg: %v", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(calls, ",") != "h3,h2" {
		t.Fatalf("expected h3 then h2, got %v", calls)
	}
}

type trackingCloser struct{ closed chan struct{} }

func (c *trackingCloser) Close() error { close(c.closed); return nil }

// Поздний успех проигравшей ноги закрывается, а не течёт.
func TestAutoLateH3WinnerIsClosed(t *testing.T) {
	o := newAutoOutbound(true, "h3")
	o.autoH3Delay = 40 * time.Millisecond
	late := &trackingCloser{closed: make(chan struct{})}
	o.legsForTest = &connectLegs{
		h3: func(ctx context.Context, budget time.Duration) (io.Closer, masque.IpConn, error) {
			time.Sleep(150 * time.Millisecond) // возвращается ПОСЛЕ таймера
			return late, nil, nil
		},
		h2: func(ctx context.Context) (io.Closer, masque.IpConn, error) {
			return stubCloser{}, nil, nil
		},
	}
	_, _, network, err := o.connect(context.Background(), o.effectiveNetwork())
	if err != nil || network != "h2" {
		t.Fatalf("network=%q err=%v", network, err)
	}
	select {
	case <-late.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("late h3 success must be closed")
	}
}

// h2 упал, а живая-но-медленная h3 добежала позже бюджета — её результат
// используется, а не выбрасывается.
func TestAutoH2FailureWaitsForLateH3(t *testing.T) {
	o := newAutoOutbound(true, "h3")
	o.autoH3Delay = 40 * time.Millisecond
	o.legsForTest = &connectLegs{
		h3: func(ctx context.Context, budget time.Duration) (io.Closer, masque.IpConn, error) {
			time.Sleep(120 * time.Millisecond)
			return stubCloser{}, nil, nil
		},
		h2: func(ctx context.Context) (io.Closer, masque.IpConn, error) {
			return nil, nil, errors.New("tcp refused")
		},
	}
	_, _, network, err := o.connect(context.Background(), o.effectiveNetwork())
	if err != nil || network != "h3" {
		t.Fatalf("slow h3 must rescue a failed h2: network=%q err=%v", network, err)
	}
	if got := o.effectiveNetwork(); got != "h3" {
		t.Fatalf("h3 must be remembered, got %q", got)
	}
}

// warnRecorder captures Warn lines while discarding the rest of the logger
// surface. Shared by the resolveVHTTP and pump-death tests.
type warnRecorder struct {
	discardLogger
	mu    sync.Mutex
	warns []string
}

func (r *warnRecorder) Warn(args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warns = append(r.warns, fmt.Sprint(args...))
}

func (r *warnRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.warns)
}

// lx: SPEC 074 v2 — `auto` is the DEFAULT. An empty `vhttp` must resolve to
// auto mode starting on h3; a fixed-h3 default behind a TCP-only hop was a
// silent black hole (field case 2026-08-24).
func TestResolveVHTTPDefaultIsAuto(t *testing.T) {
	t.Parallel()
	network, autoMode, err := resolveVHTTP("", "", discardLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if !autoMode {
		t.Fatal("empty vhttp must default to auto mode")
	}
	if network != "h3" {
		t.Fatalf("auto must start its first leg on h3, got %q", network)
	}
}

// The default landing on the standard profile is not the user's mistake:
// degrade to h3 silently. Only an EXPLICIT `auto` earns the warning.
func TestResolveVHTTPStandardProfileWarnsOnlyWhenExplicit(t *testing.T) {
	t.Parallel()
	rec := &warnRecorder{}
	network, autoMode, err := resolveVHTTP("", "standard", rec)
	if err != nil {
		t.Fatal(err)
	}
	if autoMode || network != "h3" {
		t.Fatalf("default on standard must degrade to plain h3, got %q auto=%v", network, autoMode)
	}
	if rec.count() != 0 {
		t.Fatalf("default degradation must be silent, got %v", rec.warns)
	}

	network, autoMode, err = resolveVHTTP("auto", "standard", rec)
	if err != nil {
		t.Fatal(err)
	}
	if autoMode || network != "h3" {
		t.Fatalf("explicit auto on standard must degrade to plain h3, got %q auto=%v", network, autoMode)
	}
	if rec.count() != 1 {
		t.Fatalf("explicit auto on standard must warn exactly once, got %v", rec.warns)
	}
}

func TestResolveVHTTPExplicitValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		vhttp, profile string
		network        string
		autoMode       bool
	}{
		{"h3", "", "h3", false},
		{"h2", "", "h2", false},
		{"auto", "", "h3", true},
		{"h3", "standard", "h3", false},
	} {
		network, autoMode, err := resolveVHTTP(tc.vhttp, tc.profile, discardLogger{})
		if err != nil {
			t.Fatalf("%q/%q: %v", tc.vhttp, tc.profile, err)
		}
		if network != tc.network || autoMode != tc.autoMode {
			t.Fatalf("%q/%q: got (%q, %v), want (%q, %v)",
				tc.vhttp, tc.profile, network, autoMode, tc.network, tc.autoMode)
		}
	}
}

func TestResolveVHTTPRejects(t *testing.T) {
	t.Parallel()
	if _, _, err := resolveVHTTP("h1", "", discardLogger{}); err == nil {
		t.Fatal("invalid vhttp must be rejected")
	}
	if _, _, err := resolveVHTTP("h2", "standard", discardLogger{}); err == nil {
		t.Fatal("h2 on the standard profile must be rejected")
	}
}
