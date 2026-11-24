package route

// lx:begin SPEC 138

import (
	"net"
	"testing"

	"github.com/sagernet/sing/common/bufio"
)

type finishWatcher struct {
	net.Conn
	replaceable bool
	finished    int
	halfClose   bool
}

func (w *finishWatcher) LxClientFinished(halfClose bool) {
	w.finished++
	w.halfClose = halfClose
}

func (w *finishWatcher) WriterReplaceable() bool { return w.replaceable }
func (w *finishWatcher) Upstream() any           { return w.Conn }

// layer is a wrapper over conn; replaceable tells whether the copy unwraps it for
// writes (a plain wrapper) or writes into it (a protocol layer: a chain hop, a mux
// stream) — then a half-close does not pass down to conn.
type layer struct {
	net.Conn
	replaceable bool
}

func (l *layer) WriterReplaceable() bool { return l.replaceable }
func (l *layer) Upstream() any           { return l.Conn }

// The watch on the write path the copy unwraps is told; one below a protocol
// layer the copy writes into is not.
func TestLxClientFinished_onlyOnWritePath(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	for _, c := range []struct {
		name       string
		outer      bool
		halfClose  bool
		wantCalled bool
	}{
		{"on the path, half-close", true, true, true},
		{"on the path, close", true, false, true},
		{"below a protocol layer", false, true, false},
	} {
		watcher := &finishWatcher{Conn: client, replaceable: true}
		var top net.Conn = bufio.NewCounterConn(&layer{Conn: watcher, replaceable: c.outer}, nil, nil)
		lxClientFinished(top, c.halfClose)
		if (watcher.finished == 1) != c.wantCalled {
			t.Fatalf("%s: told %d times", c.name, watcher.finished)
		}
		if c.wantCalled && watcher.halfClose != c.halfClose {
			t.Fatalf("%s: halfClose %v", c.name, watcher.halfClose)
		}
	}
}

// lx:end SPEC 138
