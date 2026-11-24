package route

// lx:begin SPEC 138 round_robin: the client finished sending
//
// A round_robin group watches its connections for the first byte from the
// destination (protocol/group, urltest_rr_firstbyte_lx.go). After the client's
// first write the watch steps aside for writes, so the copy half-closes (or, with
// no half-close below, closes) the node's connection past it. The watch learns of
// it here: a close without a byte after the client finished sending is no failure,
// and a close that follows the client's end of stream at once is no client giving
// up. Only a watch on the write path the copy used is told — the path that
// connectionCopy unwraps to reach the writer it half-closes; a watch below a
// protocol layer that does not pass the half-close down (a chain hop, a mux
// stream) is left alone.

import (
	"io"
	"net"

	"github.com/sagernet/sing/common"
	N "github.com/sagernet/sing/common/network"
)

// lxClientFinishWatcher is a connection wrapper that wants to know that the client
// finished sending: halfClose is true when the copy half-closes the writer below
// it, false when it closes the connection.
type lxClientFinishWatcher interface {
	LxClientFinished(halfClose bool)
}

// lxClientFinished tells every watcher on the write path of destination — the
// path N.UnwrapCountWriter and N.UnwrapWriter follow in connectionCopy — that the
// client finished sending.
func lxClientFinished(destination net.Conn, halfClose bool) {
	var writer io.Writer = destination
	for writer != nil {
		if watcher, isWatcher := writer.(lxClientFinishWatcher); isWatcher {
			watcher.LxClientFinished(halfClose)
		}
		writer = lxNextWriter(writer)
	}
}

// lxNextWriter is one step of N.UnwrapCountWriter / N.UnwrapWriter; nil where they stop.
func lxNextWriter(writer io.Writer) io.Writer {
	if counter, isCounter := writer.(N.WriteCounter); isCounter {
		upstream, _ := counter.UnwrapWriter()
		return upstream
	}
	if u, ok := writer.(N.WriterWithUpstream); !ok || !u.WriterReplaceable() {
		return nil
	}
	if u, ok := writer.(N.WithUpstreamWriter); ok {
		upstream, _ := u.UpstreamWriter().(io.Writer)
		return upstream
	}
	if u, ok := writer.(common.WithUpstream); ok {
		upstream, _ := u.Upstream().(io.Writer)
		return upstream
	}
	return nil
}

// lx:end SPEC 138
