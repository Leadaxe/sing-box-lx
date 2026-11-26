// lx: a silent endpoint must be reported as such.
//
// When Cloudflare accepts the QUIC connection but never answers CONNECT-IP, the
// error that surfaces is a quic-go idle timeout wrapped twice — by http3 and by
// dialCONNECTIP. Left alone it reads "dial connect-ip: read response: http3:
// parsing frame failed: timeout: no recent network activity", which points at
// frame parsing and hides the actual finding: the peer said nothing.
//
// These tests pin the two properties the diagnosis depends on: that errors.As
// still reaches the typed error through both layers of wrapping (so matching by
// type is sound), and that the original cause survives in the chain.
package masque

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sagernet/quic-go"
	E "github.com/sagernet/sing/common/exceptions"
)

// wrapLikeDialCONNECTIP reproduces the wrapping a real idle timeout goes
// through before ConnectTunnelH3 inspects it.
func wrapLikeDialCONNECTIP(inner error) error {
	return E.Cause(fmt.Errorf("http3: parsing frame failed: %w", inner), "read response")
}

// TestIdleTimeoutSurvivesWrapping: matching by type is only valid if the type
// is still reachable after http3's %w wrap and our own E.Cause.
func TestIdleTimeoutSurvivesWrapping(t *testing.T) {
	t.Parallel()
	err := wrapLikeDialCONNECTIP(&quic.IdleTimeoutError{})

	var idleErr *quic.IdleTimeoutError
	if !errors.As(err, &idleErr) {
		t.Fatalf("errors.As failed to reach *quic.IdleTimeoutError through %q", err)
	}
}

// TestIdleTimeoutMessageKeepsCause: the diagnosis replaces the leading context,
// not the chain — the underlying timeout must stay readable.
func TestIdleTimeoutMessageKeepsCause(t *testing.T) {
	t.Parallel()
	wrapped := wrapLikeDialCONNECTIP(&quic.IdleTimeoutError{})
	got := E.Cause(wrapped, "masque: CONNECT-IP timed out").Error()

	if !strings.HasPrefix(got, "masque: CONNECT-IP timed out") {
		t.Errorf("diagnosis must lead the message, got %q", got)
	}
	if !strings.Contains(got, "timeout: no recent network activity") {
		t.Errorf("original cause must survive, got %q", got)
	}
}

// TestNonIdleErrorIsNotClaimedAsTimeout: any other failure must keep falling
// through to the generic wrapping, or a real bug would be mislabelled.
func TestNonIdleErrorIsNotClaimedAsTimeout(t *testing.T) {
	t.Parallel()
	err := wrapLikeDialCONNECTIP(errors.New("connect-ip: server responded with 403"))

	var idleErr *quic.IdleTimeoutError
	if errors.As(err, &idleErr) {
		t.Fatal("a non-timeout failure must not match the idle-timeout branch")
	}
}
