// lx: SPEC 084 — регрессия ABBA-дедлока interrupt.Group (issue #20).
//
// Две группы, как у вложенных selector'ов: входящее соединение обёрнуто
// внешней группой, затем внутренней (его Close идёт B→A), а исходящее —
// внутренней, затем внешней (Close идёт A→B). Пока Interrupt и Close-обёртки
// закрывали нижележащее соединение ПОД мьютексом группы, одновременные
// Interrupt(B) и Close исходящего брали два замка в противоположном порядке.
//
// Сценарий детерминирован «затвором»: первой записью в B стоит conn, чей Close
// блокируется до сигнала. Interrupt(B) входит в него (на старом коде — держа B);
// Close исходящего берёт A и (на старом коде) встаёт на B; после отпускания
// затвора Interrupt идёт к обёртке входящего и встаёт на A — дедлок. На новом
// коде замки на время Close не удерживаются, и обе стороны завершаются.

package interrupt

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
)

// closeProbe — заглушка, удовлетворяющая net.Conn, net.PacketConn и N.PacketConn
// разом: из всего интерфейса группе нужен только Close, остальное — пустышки.
type closeProbe struct {
	closed atomic.Int32
}

func (p *closeProbe) Close() error                                { p.closed.Add(1); return nil }
func (p *closeProbe) Read([]byte) (int, error)                    { return 0, io.EOF }
func (p *closeProbe) Write(b []byte) (int, error)                 { return len(b), nil }
func (p *closeProbe) ReadFrom([]byte) (int, net.Addr, error)      { return 0, nil, io.EOF }
func (p *closeProbe) WriteTo(b []byte, _ net.Addr) (int, error)   { return len(b), nil }
func (p *closeProbe) ReadPacket(*buf.Buffer) (M.Socksaddr, error) { return M.Socksaddr{}, io.EOF }
func (p *closeProbe) WritePacket(*buf.Buffer, M.Socksaddr) error  { return nil }
func (p *closeProbe) LocalAddr() net.Addr                         { return nil }
func (p *closeProbe) RemoteAddr() net.Addr                        { return nil }
func (p *closeProbe) SetDeadline(time.Time) error                 { return nil }
func (p *closeProbe) SetReadDeadline(time.Time) error             { return nil }
func (p *closeProbe) SetWriteDeadline(time.Time) error            { return nil }

// gateConn — conn, чей Close сообщает о входе и ждёт отпускания.
type gateConn struct {
	closeProbe
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGateConn() *gateConn {
	return &gateConn{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gateConn) Close() error {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return g.closeProbe.Close()
}

// abbaCase строит пару «входящее B(A(raw))» / «исходящее A(B(raw))» для одного
// вида обёртки и отдаёт исходящую обёртку — её Close идёт в порядке A→B.
type abbaCase struct {
	name  string
	build func(a, b *Group, rawIn, rawDial *closeProbe) io.Closer
}

var abbaCases = []abbaCase{
	{"Conn", func(a, b *Group, rawIn, rawDial *closeProbe) io.Closer {
		b.NewConn(a.NewConn(rawIn, true), true)
		return a.NewConn(b.NewConn(rawDial, false), false)
	}},
	{"PacketConn", func(a, b *Group, rawIn, rawDial *closeProbe) io.Closer {
		b.NewPacketConn(a.NewPacketConn(rawIn, true), true)
		return a.NewPacketConn(b.NewPacketConn(rawDial, false), false)
	}},
}

func TestLxGroupNestedInterruptCloseNoDeadlock(t *testing.T) {
	for _, tc := range abbaCases {
		t.Run(tc.name, func(t *testing.T) {
			groupA, groupB := NewGroup(), NewGroup() // A = внешний selector, B = внутренний
			gate := newGateConn()
			groupB.NewConn(gate, true) // первая запись B: Interrupt упрётся в неё
			rawIn, rawDial := &closeProbe{}, &closeProbe{}
			dial := tc.build(groupA, groupB, rawIn, rawDial)

			interruptDone := make(chan struct{})
			go func() { groupB.Interrupt(true); close(interruptDone) }()
			select {
			case <-gate.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("Interrupt не дошёл до затвора — окружение теста нерабочее")
			}

			closeDone := make(chan struct{})
			go func() { dial.Close(); close(closeDone) }()
			time.Sleep(50 * time.Millisecond) // дать Close взять A (на старом коде — и встать на B)
			close(gate.release)

			for _, w := range []struct {
				name string
				ch   chan struct{}
			}{{"Interrupt(B)", interruptDone}, {"Close исходящего", closeDone}} {
				select {
				case <-w.ch:
				case <-time.After(3 * time.Second):
					t.Fatalf("%s не завершился за 3 с — ABBA-дедлок между группами", w.name)
				}
			}
			if rawIn.closed.Load() == 0 {
				t.Error("входящее соединение не закрыто Interrupt'ом")
			}
			if rawDial.closed.Load() == 0 {
				t.Error("исходящее соединение не закрыто")
			}
			if n := groupB.connections.Len(); n != 0 {
				t.Errorf("в B осталось %d записей после Interrupt", n)
			}
			if n := groupA.connections.Len(); n != 0 {
				t.Errorf("в A осталось %d записей после закрытий", n)
			}
		})
	}
}

// Семантика Interrupt не изменилась: внешние соединения рвутся только по запросу.
func TestLxGroupInterruptExternalOnlyOnRequest(t *testing.T) {
	g := NewGroup()
	internal, external := &closeProbe{}, &closeProbe{}
	g.NewConn(internal, false)
	g.NewConn(external, true)

	g.Interrupt(false)
	if internal.closed.Load() != 1 || external.closed.Load() != 0 {
		t.Fatalf("Interrupt(false): internal=%d external=%d, ожидали 1/0", internal.closed.Load(), external.closed.Load())
	}
	if n := g.connections.Len(); n != 1 {
		t.Fatalf("после Interrupt(false) в списке %d записей, ожидали 1", n)
	}

	g.Interrupt(true)
	if external.closed.Load() != 1 {
		t.Fatalf("Interrupt(true) не закрыл внешнее соединение")
	}
	if n := g.connections.Len(); n != 0 {
		t.Fatalf("после Interrupt(true) в списке %d записей, ожидали 0", n)
	}
}

// Обёртка, закрытая после Interrupt, не паникует на снятом элементе и всё равно
// закрывает нижележащее — двойной Close допускается, как и до фикса.
func TestLxGroupCloseAfterInterruptIsIdempotent(t *testing.T) {
	g := NewGroup()
	raw := &closeProbe{}
	wrapped := g.NewConn(raw, false)

	g.Interrupt(false)
	if err := wrapped.Close(); err != nil {
		t.Fatalf("Close после Interrupt: %v", err)
	}
	if n := g.connections.Len(); n != 0 {
		t.Fatalf("в списке %d записей, ожидали 0", n)
	}
	if raw.closed.Load() != 2 {
		t.Fatalf("нижележащее закрыто %d раз, ожидали 2 (Interrupt + обёртка)", raw.closed.Load())
	}
}
