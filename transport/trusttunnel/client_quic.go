//go:build with_quic

package trusttunnel

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/congestion"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-quic"
	congestion_meta1 "github.com/sagernet/sing-quic/congestion_meta1"
	congestion_meta2 "github.com/sagernet/sing-quic/congestion_meta2"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
)

const quicIncluded = true

// maxClientIdleTimeout is the QUIC idle timeout of the client: the official
// endpoint closes a session after client_listener_timeout_secs (600) anyway.
const maxClientIdleTimeout = 600 * time.Second

func init() {
	quicAuthenticationError = isQUICAuthenticationError
}

func isQUICAuthenticationError(err error) bool {
	var applicationError *quic.ApplicationError
	if errors.As(err, &applicationError) && applicationError.ErrorCode == errorCodeAuthenticationRequired {
		return true
	}
	var http3Error *http3.Error
	return errors.As(err, &http3Error) && http3Error.ErrorCode == errorCodeAuthenticationRequired
}

func validateCongestionControl(name string) error {
	_, err := congestionControlFunc(context.Background(), name)
	return err
}

func congestionControlFunc(ctx context.Context, name string) (func(conn *quic.Conn) congestion.CongestionControl, error) {
	timeFunc := ntp.TimeFuncFromContext(ctx)
	if timeFunc == nil {
		timeFunc = time.Now
	}
	switch name {
	case "", "bbr":
		return func(conn *quic.Conn) congestion.CongestionControl {
			return congestion_meta2.NewBbrSenderWithProfile(conn.InitialPacketSize(), congestion_meta2.ProfileStandard)
		}, nil
	case "cubic":
		return func(conn *quic.Conn) congestion.CongestionControl {
			return congestion_meta1.NewCubicSender(congestion_meta1.DefaultClock{TimeFunc: timeFunc}, conn.InitialPacketSize(), false)
		}, nil
	case "new_reno", "reno":
		return func(conn *quic.Conn) congestion.CongestionControl {
			return congestion_meta1.NewCubicSender(congestion_meta1.DefaultClock{TimeFunc: timeFunc}, conn.InitialPacketSize(), true)
		}, nil
	default:
		return nil, E.New("trusttunnel: unknown QUIC congestion control: ", name)
	}
}

func (c *Client) connectQUIC(ctx context.Context) (sessionConn, error) {
	congestionControl, err := congestionControlFunc(c.ctx, c.congestion)
	if err != nil {
		return nil, err
	}
	udpConn, err := c.dialer.DialContext(ctx, N.NetworkUDP, c.server)
	if err != nil {
		return nil, E.Cause(err, "dial udp")
	}
	// No keepalive: the session may time out silently while idle and is
	// reopened by the next request. The idle window of the client is applied
	// by idleWatcher, which spares a session with open streams; the QUIC idle
	// timeout counts packets, not streams, so it stays at the server's figure
	// to keep a silent stream (an idle TCP connection) alive as long as HTTP/2
	// would.
	quicConn, err := qtls.Dial(ctx, udpConn, c.tlsConfig, &quic.Config{
		MaxIncomingStreams: -1,
		MaxIdleTimeout:     maxClientIdleTimeout,
	})
	if err != nil {
		udpConn.Close()
		return nil, E.Cause(err, "QUIC handshake")
	}
	if negotiated := quicConn.ConnectionState().TLS.NegotiatedProtocol; negotiated != http3.NextProtoH3 {
		quicConn.CloseWithError(0, "")
		udpConn.Close()
		return nil, E.New("server negotiated ALPN ", strconv.Quote(negotiated), ", expected h3")
	}
	quicConn.SetCongestionControl(congestionControl(quicConn))
	transport := &http3.Transport{DisableCompression: true}
	clientConn := transport.NewClientConn(quicConn)
	// quic-go does not own the socket: release it when the connection ends.
	go func() {
		<-quicConn.Context().Done()
		udpConn.Close()
	}()
	return &http3Session{
		quicConn:   quicConn,
		clientConn: clientConn,
		closeSocket: func() {
			udpConn.Close()
		},
	}, nil
}

type http3Session struct {
	quicConn    *quic.Conn
	clientConn  *http3.ClientConn
	closeSocket func()
}

func (s *http3Session) roundTrip(ctx context.Context, authority string, header http.Header, withBody bool) (*rawStream, error) {
	stream, err := s.clientConn.OpenRequestStream(ctx)
	if err != nil {
		return nil, qtls.WrapError(err)
	}
	abort := func() {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
	}
	err = stream.SendRequestHeader(&http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Host: authority},
		Host:   authority,
		Header: header,
	})
	if err != nil {
		abort()
		return nil, qtls.WrapError(err)
	}
	if !withBody {
		stream.Close()
	}
	stop := context.AfterFunc(ctx, abort)
	response, err := stream.ReadResponse()
	if !stop() {
		err = ctx.Err()
	}
	if err != nil {
		abort()
		return nil, qtls.WrapError(err)
	}
	return &rawStream{
		response: response,
		reader:   stream,
		writer:   stream,
		closeFunc: func() {
			stream.Close()
			stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeNoError))
		},
		deadline:  stream,
		wrapError: qtls.WrapError,
	}, nil
}

func (s *http3Session) alive() bool {
	return s.quicConn.Context().Err() == nil
}

// close drops the socket before the connection, so that no CONNECTION_CLOSE
// leaves: an idle teardown is local, the server times the session out itself.
func (s *http3Session) close() error {
	s.closeSocket()
	return s.quicConn.CloseWithError(0, "")
}
