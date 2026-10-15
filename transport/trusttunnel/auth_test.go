package trusttunnel

import (
	"testing"

	"github.com/sagernet/sing/common/auth"
)

func TestBasicAuthorization(t *testing.T) {
	for _, testCase := range []struct {
		username, password, expected string
	}{
		{"user", "pass", "Basic dXNlcjpwYXNz"},
		{"ab", "c", "Basic YWI6Yw=="},
		{"abc", "de", "Basic YWJjOmRl"},
		{"u", "pässwörd:x", "Basic dTpww6Rzc3fDtnJkOng="},
	} {
		if got := BasicAuthorization(testCase.username, testCase.password); got != testCase.expected {
			t.Errorf("%s:%s: got %s, want %s", testCase.username, testCase.password, got, testCase.expected)
		}
	}
}

func TestUserTable(t *testing.T) {
	var table userTable
	if _, ok := table.verify(BasicAuthorization("user", "pass")); ok {
		t.Fatal("empty table accepted credentials")
	}
	table.update([]auth.User{{Username: "user", Password: "pass"}, {Username: "other", Password: "x:y"}})
	if user, ok := table.verify(BasicAuthorization("user", "pass")); !ok || user != "user" {
		t.Fatal("valid credentials refused")
	}
	if user, ok := table.verify(BasicAuthorization("other", "x:y")); !ok || user != "other" {
		t.Fatal("password with a colon refused")
	}
	for _, value := range []string{
		"",
		BasicAuthorization("user", "wrong"),
		BasicAuthorization("nobody", "pass"),
		"Bearer dXNlcjpwYXNz",
		"Basic !!!",
	} {
		if _, ok := table.verify(value); ok {
			t.Errorf("accepted %q", value)
		}
	}
	table.update(nil)
	if _, ok := table.verify(BasicAuthorization("user", "pass")); ok {
		t.Fatal("cleared table accepted credentials")
	}
}
