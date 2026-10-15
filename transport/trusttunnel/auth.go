package trusttunnel

import (
	"encoding/base64"
	"sync/atomic"

	"github.com/sagernet/sing/common/auth"
	sHTTP "github.com/sagernet/sing/protocol/http"
)

// BasicAuthorization returns the proxy-authorization value for the credentials.
//
// The official endpoint looks the header value up as a string in a map keyed by
// the standard, padded base64 of "user:pass", so the encoding has to be exactly
// that one.
func BasicAuthorization(username string, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

// userTable holds the server's credentials and can be replaced at runtime.
type userTable struct {
	authenticator atomic.Pointer[auth.Authenticator]
}

func (t *userTable) update(users []auth.User) {
	t.authenticator.Store(auth.NewAuthenticator(users))
}

// verify parses a proxy-authorization value and checks it against the table.
// The comparison is on the parsed credentials, so a non-canonical base64 of
// correct credentials is accepted where the official endpoint would refuse it.
func (t *userTable) verify(authorization string) (string, bool) {
	authenticator := t.authenticator.Load()
	if authenticator == nil || authorization == "" {
		return "", false
	}
	username, password, ok := sHTTP.ParseBasicAuth(authorization)
	if !ok || !authenticator.Verify(username, password) {
		return "", false
	}
	return username, true
}
