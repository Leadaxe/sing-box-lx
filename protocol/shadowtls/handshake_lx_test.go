package shadowtls

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-shadowtls"
)

// lx: handshake-timeout — the v3 handshake over a plain TLS config waited on the
// caller's context alone; it now gets the config's handshake timeout like every
// other path through tls.ClientHandshake.
func TestLxShadowTLSHandshakeBounded(t *testing.T) {
	var blocking shadowtls.TLSHandshakeFunc = func(ctx context.Context, _ net.Conn, _ shadowtls.TLSSessionIDGeneratorFunc) error {
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	err := boundTLSHandshakeLx(blocking, 100*time.Millisecond)(context.Background(), nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want a deadline, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("returned after %v", elapsed)
	}

	// An earlier caller deadline still wins.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start = time.Now()
	_ = boundTLSHandshakeLx(blocking, time.Minute)(ctx, nil, nil)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the caller's deadline was ignored: %v", elapsed)
	}

	// A completed handshake is unaffected, and zero keeps the function as is.
	var quick shadowtls.TLSHandshakeFunc = func(context.Context, net.Conn, shadowtls.TLSSessionIDGeneratorFunc) error { return nil }
	if err := boundTLSHandshakeLx(quick, time.Minute)(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if boundTLSHandshakeLx(nil, 0) != nil {
		t.Fatal("zero timeout must return the handshake unchanged")
	}
}
