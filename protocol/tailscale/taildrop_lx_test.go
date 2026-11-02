//go:build with_gvisor

package tailscale

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/tailscale/ipn/ipnlocal"
	"github.com/sagernet/tailscale/tailcfg"
)

// lx: SPEC 149 — disable_taildrop refuses peer uploads, local sends and the
// share flag; received files carry the sender's stable node ID.

type taildropTestHandler struct {
	backend *ipnlocal.LocalBackend
}

func (h taildropTestHandler) Peer() tailcfg.NodeView               { return tailcfg.NodeView{} }
func (h taildropTestHandler) PeerCaps() tailcfg.PeerCapMap         { return nil }
func (h taildropTestHandler) CanDebug() bool                       { return false }
func (h taildropTestHandler) Self() tailcfg.NodeView               { return tailcfg.NodeView{} }
func (h taildropTestHandler) LocalBackend() *ipnlocal.LocalBackend { return h.backend }
func (h taildropTestHandler) IsSelfUntagged() bool                 { return true }
func (h taildropTestHandler) RemoteAddr() netip.AddrPort           { return netip.AddrPort{} }
func (h taildropTestHandler) Logf(format string, a ...any)         {}

func TestTaildropDisabledRefusesPeerRequests_LX(t *testing.T) {
	backend := new(ipnlocal.LocalBackend)
	// taildrop is nil: reaching handlePeerRequest would panic.
	registerTaildropEndpoint(backend, &Endpoint{taildropDisabled: true})
	defer unregisterTaildropEndpoint(backend)
	for _, method := range []string{http.MethodPut, http.MethodGet} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, "/v0/put/file.txt", strings.NewReader("content"))
		handleTaildropRequest(taildropTestHandler{backend: backend}, recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s: status %d", method, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), adapter.ErrTaildropDisabled.Error()) {
			t.Fatalf("%s: body %q", method, recorder.Body.String())
		}
	}
}

func TestTaildropDisabledRefusesSend_LX(t *testing.T) {
	// server is nil: reaching the backend would panic.
	for _, started := range []bool{false, true} {
		endpoint := &Endpoint{taildropDisabled: true}
		endpoint.started.Store(started)
		err := endpoint.SendTaildropFile(context.Background(), "peer", "file.txt", 1, bytes.NewReader([]byte{0}), nil)
		if !errors.Is(err, adapter.ErrTaildropDisabled) {
			t.Fatalf("started=%v: %v", started, err)
		}
		canShareFiles, targets := endpoint.taildropTargets()
		if canShareFiles || targets != nil {
			t.Fatalf("started=%v: canShareFiles=%v targets=%v", started, canShareFiles, targets)
		}
	}
}

func TestTaildropInboxSenderID_LX(t *testing.T) {
	manager := newTaildropManager(context.Background(), log.NewNOPFactory().NewLogger("tailscale"), "ts", t.TempDir(), nil)
	content := []byte("hello")
	err := manager.putFile("nSENDER1CNTRL", "phone", "note.txt", bytes.NewReader(content), 0, int64(len(content)), func() {})
	if err != nil {
		t.Fatal(err)
	}
	inbox := manager.inbox()
	if len(inbox.Files) != 1 {
		t.Fatalf("files: %d", len(inbox.Files))
	}
	file := inbox.Files[0]
	if file.SenderID != "nSENDER1CNTRL" || file.SenderName != "phone" {
		t.Fatalf("sender: id=%q name=%q", file.SenderID, file.SenderName)
	}
	err = manager.deleteFile(file.Name)
	if err != nil {
		t.Fatal(err)
	}
	manager.access.Lock()
	_, found := manager.senderIDs[file.Name]
	manager.access.Unlock()
	if found {
		t.Fatal("sender ID left after delete")
	}
	// A file found on disk without a record (received before a restart).
	restarted := newTaildropManager(context.Background(), log.NewNOPFactory().NewLogger("tailscale"), "ts", manager.directory, nil)
	err = manager.putFile("nSENDER1CNTRL", "phone", "kept.txt", bytes.NewReader(content), 0, int64(len(content)), func() {})
	if err != nil {
		t.Fatal(err)
	}
	inbox = restarted.inbox()
	if len(inbox.Files) != 1 || inbox.Files[0].SenderID != "" || inbox.Files[0].SenderName != "" {
		t.Fatalf("restarted inbox: %+v", inbox.Files)
	}
}
