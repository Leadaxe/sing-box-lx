package trusttunnel

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// udpFlowQueueSize bounds the datagrams queued for one flow; beyond it new
// datagrams are dropped, as a full socket buffer would.
const udpFlowQueueSize = 64

type udpFlowKey struct {
	source      netip.AddrPort
	destination netip.AddrPort
}

// udpSession is one _udp2 stream: a frame reader and a table of flows keyed by
// the frame 4-tuple.
type udpSession struct {
	service *Service
	ctx     context.Context
	writer  http.ResponseWriter
	user    string
	source  M.Socksaddr

	access sync.Mutex
	flows  map[udpFlowKey]*udpFlow
	closed bool

	writeAccess sync.Mutex
	writeClosed bool
}

func (s *Service) serveUDP(ctx context.Context, writer http.ResponseWriter, request *http.Request, username string, source M.Socksaddr) {
	writer.WriteHeader(http.StatusOK)
	err := flush(writer)
	if err != nil {
		return
	}
	s.logger.DebugContext(ctx, "trusttunnel: UDP stream from ", source)
	session := &udpSession{
		service: s,
		ctx:     ctx,
		writer:  writer,
		user:    username,
		source:  source,
		flows:   make(map[udpFlowKey]*udpFlow),
	}
	err = session.loop(request.Body)
	session.close()
	switch {
	case err == nil, err == io.EOF:
	case isStreamClosed(err), E.IsClosedOrCanceled(err):
		s.logger.DebugContext(ctx, "trusttunnel: UDP stream from ", source, " closed: ", err)
	default:
		s.logger.ErrorContext(ctx, "trusttunnel: UDP stream from ", source, ": ", err)
	}
}

func (s *udpSession) loop(body io.Reader) error {
	reader := frameReader{reader: body}
	for {
		source, destination, payload, err := reader.ReadClientFrame()
		if err != nil {
			return err
		}
		s.dispatch(source, destination, payload)
	}
}

func (s *udpSession) dispatch(source M.Socksaddr, destination M.Socksaddr, payload *buf.Buffer) {
	key := udpFlowKey{source.AddrPort(), destination.AddrPort()}
	s.access.Lock()
	if s.closed {
		s.access.Unlock()
		payload.Release()
		return
	}
	flow, loaded := s.flows[key]
	if !loaded {
		flow = &udpFlow{
			session:     s,
			key:         key,
			source:      source,
			destination: destination,
			packets:     make(chan *buf.Buffer, udpFlowQueueSize),
			done:        make(chan struct{}),
		}
		s.flows[key] = flow
	}
	s.access.Unlock()
	if !loaded {
		metadataSource := source
		if !source.Addr.IsValid() || source.Addr.IsUnspecified() {
			// A zero source (mihomo, sing-trusttunnel, our client) still means the
			// client: route rules by source keep working.
			metadataSource = s.source
		}
		go s.service.handler.NewPacketConnection(s.ctx, flow, Metadata{
			User:        s.user,
			Source:      metadataSource,
			Destination: destination,
			Network:     N.NetworkUDP,
		})
	}
	flow.push(payload)
}

func (s *udpSession) remove(flow *udpFlow) {
	s.access.Lock()
	defer s.access.Unlock()
	if s.flows[flow.key] == flow {
		delete(s.flows, flow.key)
	}
}

// close ends the stream: no frame is written any more and every flow is closed.
func (s *udpSession) close() {
	s.writeAccess.Lock()
	s.writeClosed = true
	s.writeAccess.Unlock()
	s.access.Lock()
	s.closed = true
	flows := s.flows
	s.flows = nil
	s.access.Unlock()
	for _, flow := range flows {
		flow.Close()
	}
}

func (s *udpSession) writeFrame(buffer *buf.Buffer, source M.Socksaddr, destination M.Socksaddr) error {
	if buffer.Start() < ServerFrameHeadroom {
		newBuffer := buf.NewSize(ServerFrameHeadroom + buffer.Len())
		newBuffer.Advance(ServerFrameHeadroom)
		_, err := newBuffer.Write(buffer.Bytes())
		buffer.Release()
		if err != nil {
			newBuffer.Release()
			return err
		}
		buffer = newBuffer
	}
	defer buffer.Release()
	err := EncodeServerFrame(buffer, source, destination)
	if err != nil {
		return err
	}
	s.writeAccess.Lock()
	defer s.writeAccess.Unlock()
	if s.writeClosed {
		return net.ErrClosed
	}
	_, err = s.writer.Write(buffer.Bytes())
	if err == nil {
		err = flush(s.writer)
	}
	return err
}

var (
	_ N.PacketConn    = (*udpFlow)(nil)
	_ N.FrontHeadroom = (*udpFlow)(nil)
)

// udpFlow is one 4-tuple of a _udp2 stream as a packet conn for the handler.
type udpFlow struct {
	session     *udpSession
	key         udpFlowKey
	source      M.Socksaddr
	destination M.Socksaddr
	packets     chan *buf.Buffer
	done        chan struct{}
	closeOnce   sync.Once
}

func (f *udpFlow) push(payload *buf.Buffer) {
	select {
	case <-f.done:
		payload.Release()
		return
	default:
	}
	select {
	case f.packets <- payload:
	default:
		payload.Release()
	}
}

func (f *udpFlow) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	select {
	case packet := <-f.packets:
		_, err := buffer.Write(packet.Bytes())
		packet.Release()
		return f.destination, err
	case <-f.done:
		return M.Socksaddr{}, net.ErrClosed
	}
}

// WritePacket frames a reply: the sender becomes the frame source, the flow's
// client address the frame destination.
func (f *udpFlow) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	select {
	case <-f.done:
		buffer.Release()
		return net.ErrClosed
	default:
	}
	sender := destination
	if !sender.IsIP() {
		sender = f.destination
	}
	return f.session.writeFrame(buffer, sender, f.source)
}

func (f *udpFlow) FrontHeadroom() int {
	return ServerFrameHeadroom
}

func (f *udpFlow) Close() error {
	f.closeOnce.Do(func() {
		close(f.done)
		f.session.remove(f)
		for {
			select {
			case packet := <-f.packets:
				packet.Release()
			default:
				return
			}
		}
	})
	return nil
}

func (f *udpFlow) LocalAddr() net.Addr {
	return f.destination
}

func (f *udpFlow) SetDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (f *udpFlow) SetReadDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (f *udpFlow) SetWriteDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (f *udpFlow) NeedAdditionalReadDeadline() bool {
	return true
}
