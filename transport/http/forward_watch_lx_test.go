package http

import (
	std_bufio "bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// watchUpstream stands for the routed connection: it reads requests from the
// server end of the pipe and either answers each with "ok" or stays silent.
type watchUpstream struct {
	silent bool
	conns  chan net.Conn
}

func (h *watchUpstream) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	h.conns <- conn
	reader := std_bufio.NewReader(conn)
	for {
		request, err := http.ReadRequest(reader)
		if err != nil {
			conn.Close()
			return
		}
		io.Copy(io.Discard, request.Body)
		if h.silent {
			continue
		}
		fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	}
}

func (h *watchUpstream) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
}

// noDeadlineConn refuses read deadlines, as a pipe or part of a detour does.
type noDeadlineConn struct{ net.Conn }

func (c noDeadlineConn) SetReadDeadline(time.Time) error { return errors.New("no deadlines") }

func startWatchProxy(t *testing.T, handler Handler, wrap func(net.Conn) net.Conn) net.Addr {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	server := NewServer(ServerOptions{Logger: logger.NOP(), HTTP1: true})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if wrap != nil {
				conn = wrap(conn)
			}
			go server.ServeConnection(context.Background(), conn, NewReader(conn), handler, M.SocksaddrFromNet(conn.RemoteAddr()), nil)
		}
	}()
	return listener.Addr()
}

const watchRequest = "GET http://203.0.113.9/ HTTP/1.1\r\nHost: 203.0.113.9\r\n\r\n"

// The client leaves while the upstream is silent: both ends of the pipe close.
func TestLXForwardClientGoneClosesUpstream(t *testing.T) {
	handler := &watchUpstream{silent: true, conns: make(chan net.Conn, 4)}
	addr := startWatchProxy(t, handler, nil)
	client, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(client, watchRequest); err != nil {
		t.Fatal(err)
	}
	upstream := <-handler.conns
	time.Sleep(100 * time.Millisecond)
	client.Close()

	upstream.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = upstream.Read(make([]byte, 1))
	if err == nil || isTimeout(err) {
		t.Fatalf("the routed end stayed open after the client left: %v", err)
	}
}

// Keep-alive: two requests one after the other on one connection.
func TestLXForwardKeepAlive(t *testing.T) {
	handler := &watchUpstream{conns: make(chan net.Conn, 4)}
	addr := startWatchProxy(t, handler, nil)
	client, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	reader := std_bufio.NewReader(client)
	for i := range 2 {
		if _, err = io.WriteString(client, watchRequest); err != nil {
			t.Fatal(err)
		}
		requireOK(t, reader, i)
	}
}

// Pipelining: the second request arrives before the first answer and is not lost.
func TestLXForwardPipelining(t *testing.T) {
	handler := &watchUpstream{conns: make(chan net.Conn, 4)}
	addr := startWatchProxy(t, handler, nil)
	client, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = io.WriteString(client, watchRequest+watchRequest); err != nil {
		t.Fatal(err)
	}
	reader := std_bufio.NewReader(client)
	requireOK(t, reader, 0)
	requireOK(t, reader, 1)
}

// A connection without read deadlines is served as upstream does.
func TestLXForwardNoDeadlines(t *testing.T) {
	handler := &watchUpstream{conns: make(chan net.Conn, 4)}
	addr := startWatchProxy(t, handler, func(conn net.Conn) net.Conn { return noDeadlineConn{conn} })
	client, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = io.WriteString(client, watchRequest); err != nil {
		t.Fatal(err)
	}
	requireOK(t, std_bufio.NewReader(client), 0)
}

func requireOK(t *testing.T, reader *std_bufio.Reader, i int) {
	t.Helper()
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("response %d: %v", i, err)
	}
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "ok") {
		t.Fatalf("response %d: %d %q", i, response.StatusCode, body)
	}
}
