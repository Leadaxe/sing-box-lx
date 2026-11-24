// lx:begin SPEC 138 round_robin stage 1 stand follow-up tests

package group

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// staleHistory writes history of tag from before a connection that starts now,
// and proves the network alive (a first byte of the group) for any such connection.
func (f *shiftFixture) staleHistory(tag string) {
	f.g.history.StoreURLTestHistory(tag, &adapter.URLTestHistory{Time: time.Now().Add(-time.Second), Delay: 100})
	f.s.balancer.heal.lastByte.Store(rrClock(time.Now().Add(time.Hour)))
}

// A proxy node killed a second after its probe: its connection does not open; the
// first check strikes it out — fresh history is no answer for a node under
// suspicion — and the key does not shift.
func TestRRStand_killedRightAfterProbe(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.staleHistory("a")
	f.fb["a"].dead.Store(true)
	key := f.keyOn(t, "site.example", 0)
	if _, answered := f.dial(t, key, "site.example"); !answered {
		t.Fatal("the retry through b answers")
	}
	f.waitCheck(t)
	if f.probes("a") == 0 {
		t.Fatal("a is probed live")
	}
	if f.g.history.LoadURLTestHistory("a") != nil || !samePool(f.pool(), "d", "b", "c") {
		t.Fatalf("a is struck out by the first check, pool %v", f.pool())
	}
	if shift, _ := f.shift(key, "site.example"); shift != 0 {
		t.Fatalf("a dead node shifts no key, got %d", shift)
	}
	if f.fails("a") != 0 {
		t.Fatal("a dead node gets no confirmed failure")
	}
}

// fakeEndpoints is an endpoint manager that knows the tags of the endpoints.
type fakeEndpoints struct {
	adapter.EndpointManager
	tags map[string]bool
}

func (m *fakeEndpoints) Get(tag string) (adapter.Endpoint, bool) {
	if m.tags[tag] {
		return nil, true
	}
	return nil, false
}

// A proxy node's dial fails in the gap after a check, with history from before the
// connection: no verdict — the retry only, no shift, no count. An endpoint (it goes
// to the destination on open itself) keeps the shortcut by fresh history.
func TestRRStand_proxyDialFailureNoVerdictInGap(t *testing.T) {
	for _, endpoint := range []bool{false, true} {
		f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
		if endpoint {
			f.s.ctx = service.ContextWith[adapter.EndpointManager](context.Background(), &fakeEndpoints{tags: map[string]bool{"a": true}})
		}
		f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
		f.staleHistory("a")
		end := time.Now()
		f.s.balancer.heal.lastCheckEnd.Store(&end)
		key := f.keyOn(t, "blocked.example", 0)
		conn, ok := f.s.rrHealDial(key, N.NetworkTCP, siteAddr("blocked.example"), f.fb["a"], E.New("dial failed"), f.pickAt(key, "blocked.example", time.Now()))
		if !ok || f.dials("b") != 1 {
			t.Fatalf("endpoint=%v: the retry through b", endpoint)
		}
		conn.Write([]byte("hi"))
		buffer := make([]byte, 8)
		if n, _ := conn.Read(buffer); n == 0 {
			t.Fatalf("endpoint=%v: the retry answers", endpoint)
		}
		conn.Close()
		shift, _ := f.shift(key, "blocked.example")
		if endpoint {
			if shift != 1 || f.fails("a") != 1 {
				t.Fatalf("endpoint: alive by fresh history, shift 1 and one failure; got %d, %d", shift, f.fails("a"))
			}
			continue
		}
		if shift != 0 || f.fails("a") != 0 {
			t.Fatalf("proxy: no verdict in the gap, no shift and no count; got %d, %d", shift, f.fails("a"))
		}
		if f.probes("a") != 0 {
			t.Fatal("proxy: the gap, no probe")
		}
	}
}

// The client gave up on a hanging node after 5 s or more: the check runs on the
// usual terms and strikes a dead node out. Sooner, or a connection test: nothing.
func TestRRStand_cancelAfterWaitTriggersCheck(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].dead.Store(true)
	ctx := f.keyOn(t, "site.example", 0)
	f.s.rrHealDial(ctx, N.NetworkTCP, siteAddr("site.example"), f.fb["a"], context.Canceled, f.pickAt(ctx, "site.example", time.Now().Add(-time.Second)))
	if f.s.balancer.heal.running.Load() || f.probes("a") != 0 {
		t.Fatal("a cancel after a short wait is no reason for a check")
	}
	f.s.rrHealDial(ctx, N.NetworkTCP, siteAddr("site.example"), f.fb["a"], context.Canceled, f.pickAt(ctx, "site.example", time.Now().Add(-rrHangAfter)))
	f.waitCheck(t)
	if f.g.history.LoadURLTestHistory("a") != nil || !samePool(f.pool(), "d", "b", "c") {
		t.Fatalf("a hanging dead node is struck out, pool %v", f.pool())
	}
	if f.dials("b") != 0 {
		t.Fatal("a cancel gets no retry")
	}
	if f.s.balancer.memory.shifts.size.Load() != 0 {
		t.Fatal("a cancel shifts no key")
	}
}

// A connection closed from our side with nothing received after the client sent,
// 5 s or more after it started: the check runs. An interrupt of the group is no
// reason.
func TestRRStand_ownCloseAfterWaitTriggersCheck(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].dead.Store(true)
	watch := func() *rrFirstByteConn {
		client, server := net.Pipe()
		t.Cleanup(func() { server.Close() })
		go func() {
			buffer := make([]byte, 8)
			server.Read(buffer)
		}()
		conn := newRRFirstByteConn(client, &rrConnWatch{s: f.s, member: "a", start: time.Now().Add(-rrHangAfter)})
		conn.Write([]byte("hi"))
		return conn
	}
	// The interrupt of the group (a selection update) closes it: no check.
	f.g.history.StoreURLTestHistory("b", &adapter.URLTestHistory{Time: time.Now(), Delay: 10})
	f.g.setSelectedTCPLx(f.fb["c"])
	interrupted := watch()
	f.g.interruptGroup.NewConn(interrupted, false)
	f.g.performSelectionUpdate(false)
	if interrupted.state.Load()&rrStateClosed == 0 {
		t.Fatal("precondition: the interrupt closed the connection")
	}
	time.Sleep(20 * time.Millisecond)
	if f.s.balancer.heal.running.Load() || f.probes("a") != 0 {
		t.Fatal("an interrupt of the group is no reason for a check")
	}
	// Closed by the client: the check strikes a out.
	watch().Close()
	f.waitCheck(t)
	if f.g.history.LoadURLTestHistory("a") != nil {
		t.Fatal("a hanging dead node is struck out")
	}
}

// A check that ended without a probe (A alive by fresh history) leaves no gap.
func TestRRStand_checkWithoutProbeLeavesNoGap(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	f.fresh("a", "b")
	f.nodes["a"].siteDown.Store(true)
	f.s.balancer.heal.lastByte.Store(rrClock(time.Now().Add(time.Hour)))
	conn, _ := f.failAt(context.Background(), "a", time.Now().Add(-time.Second))
	closeConn(conn)
	waitUntil(t, func() bool { return !f.s.balancer.heal.running.Load() })
	if f.probes("a") != 0 {
		t.Fatal("precondition: no probe")
	}
	if f.s.balancer.heal.lastCheckEnd.Load() != nil {
		t.Fatal("a check without probes sets no gap")
	}
}

// captureLogger keeps the debug lines.
type captureLogger struct {
	log.ContextLogger
	access sync.Mutex
	lines  []string
}

func (l *captureLogger) Debug(args ...any) {
	l.access.Lock()
	defer l.access.Unlock()
	l.lines = append(l.lines, fmt.Sprint(args...))
}

// The "own network" line names every node probed, the third included.
func TestRRStand_ownNetworkLogNamesThird(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	logger := &captureLogger{ContextLogger: f.s.logger}
	f.s.logger = logger
	for _, node := range f.fb {
		node.dead.Store(true)
	}
	f.fail(context.Background(), "a")
	f.waitCheck(t)
	if f.probes("c") == 0 {
		t.Fatal("precondition: c probed as the third")
	}
	logger.access.Lock()
	defer logger.access.Unlock()
	for _, line := range logger.lines {
		if strings.Contains(line, "local network assumed down") {
			if !strings.Contains(line, "c silent") {
				t.Fatalf("the third is missing: %s", line)
			}
			return
		}
	}
	t.Fatal("no own-network line")
}

// lx:end SPEC 138
