package daemon

import (
	"testing"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 149 — disable_taildrop reaches the sender as FailedPrecondition;
// the inbox carries the sender's stable node ID.

func TestTaildropDisabledCode_LX(t *testing.T) {
	err := taildropError(E.Cause(adapter.ErrTaildropDisabled, "send"))
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code: %v", err)
	}
}

func TestTaildropInboxSenderID_LX(t *testing.T) {
	inbox := taildropInboxToProto("ts", &adapter.TaildropInbox{
		Files: []*adapter.TaildropFile{{Name: "note.txt", SenderName: "phone", SenderID: "nSENDER1CNTRL"}, {Name: "old.txt"}},
	})
	if inbox.Files[0].SenderID != "nSENDER1CNTRL" || inbox.Files[1].SenderID != "" {
		t.Fatalf("files: %+v", inbox.Files)
	}
}
