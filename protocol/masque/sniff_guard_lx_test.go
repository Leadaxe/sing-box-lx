package masque

// lx: SPEC 060 — the `auto` legs under lx.sniff_guard: h3 lives by the quic
// label, h2 by the tls label; a fast h3 failure is redone with the opposite
// decision before the fallback, a silent one falls back to h2 and leaves its
// pending pair to the next h3 leg.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/masque"

	"github.com/stretchr/testify/require"
)

var errPinned = errors.New("remote endpoint has a different public key than pinned")

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func sniffGuardOutbound(t *testing.T) (*Outbound, *lxsniffguard.Node) {
	t.Helper()
	section := func(method string) option.LXSniffGuardSectionResolved {
		return option.LXSniffGuardSectionResolved{Method: method, Delay: 300 * time.Millisecond, DetourTypes: map[string]string{
			option.LXSniffGuardDirect:   option.LXSniffGuardClean,
			option.LXSniffGuardWildcard: option.LXSniffGuardSuspect,
		}}
	}
	g := lxsniffguard.New(lxsniffguard.Config{
		Policy: option.LXSniffGuardResolved{
			TLS:  section(option.LXSniffGuardTLSRecordFragment),
			QUIC: section(option.LXSniffGuardQUICDecoy),
		},
		Nodes: map[string]lxsniffguard.NodeInfo{"warp": {Type: "masque", SNIMismatch: true}},
	})
	require.NotNil(t, g)
	o := newAutoOutbound(true, "h3")
	o.sniffGuard = g.Node("warp")
	return o, o.sniffGuard
}

// guardLegs scripts one attempt of each leg: the handshake marks the attempt
// on the direct path and h3(protect) / h2(protect) is its outcome.
func guardLegs(calls *[]string, h3 func(ctx context.Context, protect bool) error, h2 func(protect bool) error) *connectLegs {
	var access sync.Mutex
	note := func(leg string, protect bool) {
		access.Lock()
		*calls = append(*calls, fmt.Sprint(leg, ":", protect))
		access.Unlock()
	}
	return &connectLegs{
		h3Once: func(ctx context.Context, _ time.Duration) (io.Closer, masque.IpConn, error) {
			protect := lxsniffguard.AttemptFromContext(ctx).Decide(lxsniffguard.ProtoQUIC, lxsniffguard.Path{Direct: true}, true).Protect
			note("h3", protect)
			if err := h3(ctx, protect); err != nil {
				return nil, nil, err
			}
			return nopCloser{}, nil, nil
		},
		h2Once: func(ctx context.Context) (io.Closer, masque.IpConn, error) {
			protect := lxsniffguard.AttemptFromContext(ctx).Decide(lxsniffguard.ProtoTLS, lxsniffguard.Path{Direct: true}, true).Protect
			note("h2", protect)
			if err := h2(protect); err != nil {
				return nil, nil, err
			}
			return nopCloser{}, nil, nil
		},
	}
}

func protectedOnly(_ context.Context, protect bool) error {
	if protect {
		return nil
	}
	return errPinned
}

func h2Alive(bool) error { return nil }

// A fast h3 failure is redone with the opposite decision inside the leg; the
// pair moves the quic label and leaves the tls one alone.
func TestAutoH3FastFailureRetriedBeforeFallback(t *testing.T) {
	o, node := sniffGuardOutbound(t)
	o.autoH3Delay = time.Second
	var calls []string
	o.legsForTest = guardLegs(&calls, protectedOnly, h2Alive)

	_, _, network, err := o.connect(context.Background(), "h3")
	require.NoError(t, err)
	require.Equal(t, "h3", network)
	require.Equal(t, "h3:false,h3:true", strings.Join(calls, ","))
	require.True(t, node.Peek(lxsniffguard.ProtoQUIC, lxsniffguard.Path{Direct: true}).Protect)
	require.False(t, node.Peek(lxsniffguard.ProtoTLS, lxsniffguard.Path{Direct: true}).Protect)

	// Both decisions fail fast: two h3 attempts, then the fallback to h2.
	calls = nil
	o.forgetNetwork()
	o.legsForTest = guardLegs(&calls, func(context.Context, bool) error { return errPinned }, h2Alive)
	_, _, network, err = o.connect(context.Background(), "h3")
	require.NoError(t, err)
	require.Equal(t, "h2", network)
	require.Equal(t, "h3:true,h3:false,h2:false", strings.Join(calls, ","))
}

// A silent h3 falls back to h2 as before; its pending pair goes to the next
// h3 leg, which then goes with the opposite decision.
func TestAutoSilentH3LeavesPairForNextH3Leg(t *testing.T) {
	o, node := sniffGuardOutbound(t)
	o.autoH3Delay = 30 * time.Millisecond
	var calls []string
	returned := make(chan struct{}, 4)
	silentUnprotected := func(ctx context.Context, protect bool) error {
		defer func() { returned <- struct{}{} }()
		if protect {
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}
	o.legsForTest = guardLegs(&calls, silentUnprotected, h2Alive)

	_, _, network, err := o.connect(context.Background(), "h3")
	require.NoError(t, err)
	require.Equal(t, "h2", network)
	<-returned
	time.Sleep(20 * time.Millisecond) // the abandoned leg settles after it returned
	require.Equal(t, "h3:false,h2:false", strings.Join(calls, ","))

	calls = nil
	_, _, network, err = o.connect(context.Background(), "h3")
	require.NoError(t, err)
	require.Equal(t, "h3", network)
	require.Equal(t, "h3:true", strings.Join(calls, ","), "the next h3 leg takes the pending pair")
	require.True(t, node.Peek(lxsniffguard.ProtoQUIC, lxsniffguard.Path{Direct: true}).Protect)
	require.False(t, node.Peek(lxsniffguard.ProtoTLS, lxsniffguard.Path{Direct: true}).Protect, "the h2 leg is another label")
}

// Without the guard the legs run once each, as upstream.
func TestAutoLegsWithoutGuard(t *testing.T) {
	o := newAutoOutbound(true, "h3")
	o.autoH3Delay = time.Second
	var calls []string
	o.legsForTest = guardLegs(&calls, func(context.Context, bool) error { return errPinned }, h2Alive)
	_, _, network, err := o.connect(context.Background(), "h3")
	require.NoError(t, err)
	require.Equal(t, "h2", network)
	require.Equal(t, "h3:false,h2:false", strings.Join(calls, ","))
}
