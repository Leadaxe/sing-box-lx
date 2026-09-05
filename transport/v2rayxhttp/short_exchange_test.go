package v2rayxhttp

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// Regression guard for LxBox issue #100 (lx: SPEC 077): a strict DNS-over-TCP
// server behind an XHTTP `auto` → stream-one detour lost every answer. The DNS
// pool cancels its dial context right after DialContext returns; the pre-077
// guard, still armed until the download response, then closed the upload body
// (the server saw EOF and half-closed its backend) and reset the stream (the
// backend's answer, ~125 ms later, hit RST). Server capture on the issue: query
// → FIN → answer → RST. The acceptance criterion from the report is 100 of 100
// answers back through stream-one with no `context canceled`.
//
// The server here is DNS-shaped: it reads one fixed-size query from the
// streamed body, answers only after the client has finished sending it, and
// keeps the stream open for the next query, the way Xray keeps a TCP conn.

const (
	shortQueryLen  = 60
	shortAnswerLen = 76
	shortAnswerLag = 2 * time.Millisecond
	shortExchanges = 100
)

func answeringServer(t *testing.T) string {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		query := make([]byte, shortQueryLen)
		answer := make([]byte, shortAnswerLen)
		for {
			if _, err := io.ReadFull(r.Body, query); err != nil {
				return
			}
			time.Sleep(shortAnswerLag)
			copy(answer, query[:2]) // echo the transaction id
			if _, err := w.Write(answer); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: h2c.NewHandler(handler, &http2.Server{})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return listener.Addr().String()
}

// exchangeOnce writes one query with the given id and expects the answer to
// carry it back.
func exchangeOnce(t *testing.T, conn net.Conn, id uint16) {
	t.Helper()
	query := make([]byte, shortQueryLen)
	binary.BigEndian.PutUint16(query, id)
	if _, err := conn.Write(query); err != nil {
		t.Fatalf("exchange %d: write: %v", id, err)
	}
	conn.SetReadDeadline(time.Now().Add(writeFreeBudget))
	answer := make([]byte, shortAnswerLen)
	if _, err := io.ReadFull(conn, answer); err != nil {
		t.Fatalf("exchange %d: read: %v", id, err)
	}
	if got := binary.BigEndian.Uint16(answer); got != id {
		t.Fatalf("exchange %d: answer carries id %d", id, got)
	}
}

// TestStreamOneShortExchangesAfterDialCancel runs the failing shape of issue
// #100 twice over: one dial per query (what the pool ends up doing when every
// conn dies — 83 conn ids in the Android dump), and one dial reused for all
// queries (what the pool does once the conn lives). Each dial context is
// cancelled the moment DialContext returns.
func TestStreamOneShortExchangesAfterDialCancel(t *testing.T) {
	t.Run("dial-per-query", func(t *testing.T) {
		t.Parallel()
		client := liveH2CClient(t, answeringServer(t), modeStreamOne)
		for i := 0; i < shortExchanges; i++ {
			ctx, cancel := context.WithCancelCause(context.Background())
			conn, err := client.DialContext(ctx)
			cancel(nil)
			if err != nil {
				t.Fatalf("dial %d: %v", i, err)
			}
			exchangeOnce(t, conn, uint16(i))
			conn.Close()
		}
	})
	t.Run("pooled-conn", func(t *testing.T) {
		t.Parallel()
		client := liveH2CClient(t, answeringServer(t), modeStreamOne)
		ctx, cancel := context.WithCancelCause(context.Background())
		conn, err := client.DialContext(ctx)
		cancel(nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		for i := 0; i < shortExchanges; i++ {
			exchangeOnce(t, conn, uint16(i))
		}
	})
}
