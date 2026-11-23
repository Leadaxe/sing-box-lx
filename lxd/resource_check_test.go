//go:build with_lxd

package lxd

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/sagernet/sing-box/common/srs"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
)

const testRuleSetSource = `{"version":1,"rules":[{"domain_suffix":["example.org"]}]}`

func testRuleSetBinary(t *testing.T, suffix string) []byte {
	t.Helper()
	source := `{"version":1,"rules":[{"domain_suffix":["` + suffix + `"]}]}`
	compat, err := json.UnmarshalExtended[option.PlainRuleSetCompat]([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := compat.Upgrade()
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	if err = srs.Write(&buffer, plain, C.RuleSetVersion1); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func putResource(t *testing.T, url string, body []byte) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestCheckRuleSetContent(t *testing.T) {
	valid := testRuleSetBinary(t, "example.org")
	cases := []struct {
		name      string
		content   []byte
		checkable bool
		wantErr   bool
	}{
		{"set.srs", valid, true, false},
		{"SET.SRS", valid, true, false},
		{"set.srs", valid[:len(valid)/2], true, true},
		{"set.srs", []byte("not a rule-set"), true, true},
		{"set.srs", nil, true, true},
		{"set.json", []byte(testRuleSetSource), true, false},
		{"set.json", []byte(`{"version":1,"rules":[`), true, true},
		{"set.json", []byte(`{"version":99,"rules":[]}`), true, true},
		{"geoip.db", []byte("anything"), false, false},
		{"noext", []byte("anything"), false, false},
	}
	for _, tc := range cases {
		checkable, err := checkRuleSetContent(tc.name, tc.content)
		if checkable != tc.checkable || (err != nil) != tc.wantErr {
			t.Errorf("%s (%d bytes): checkable=%v err=%v, want checkable=%v wantErr=%v",
				tc.name, len(tc.content), checkable, err, tc.checkable, tc.wantErr)
		}
	}
}

// A rule-set the active config points at is replaced in place when the new
// bytes decode.
func TestResourcePutReplacesReferencedRuleSet(t *testing.T) {
	control, _ := newResourceTestController(t)
	if _, _, err := control.resources.Put("geosite-ru.srs", testRuleSetBinary(t, "old.example")); err != nil {
		t.Fatal(err)
	}
	control.activeContent = `{"path":"/x/resources/geosite-ru.srs"}`
	srv := resourceServer(t, control)

	updated := testRuleSetBinary(t, "new.example")
	if status := putResource(t, srv.URL+"/admin/resources/geosite-ru.srs", updated); status != http.StatusOK {
		t.Fatalf("PUT referenced rule-set status = %d, want 200", status)
	}
	if content, _, _ := control.resources.Read("geosite-ru.srs"); !bytes.Equal(content, updated) {
		t.Fatal("referenced rule-set was not replaced")
	}
}

// The same holds when only last-good points at it (core stopped), and for the
// source format.
func TestResourcePutReplacesRuleSetReferencedByLastGood(t *testing.T) {
	control, _ := newResourceTestController(t)
	if err := control.store.SaveLastGood(`{"path":"/x/resources/custom.json"}`); err != nil {
		t.Fatal(err)
	}
	srv := resourceServer(t, control)
	if status := putResource(t, srv.URL+"/admin/resources/custom.json", []byte(testRuleSetSource)); status != http.StatusOK {
		t.Fatalf("PUT rule-set referenced by last-good status = %d, want 200", status)
	}
}

// Bytes the core could not load never reach the disk under a referenced name.
func TestResourcePutRejectsBrokenReferencedRuleSet(t *testing.T) {
	control, _ := newResourceTestController(t)
	original := testRuleSetBinary(t, "old.example")
	if _, _, err := control.resources.Put("geosite-ru.srs", original); err != nil {
		t.Fatal(err)
	}
	control.activeContent = `{"path":"/x/resources/geosite-ru.srs"}`
	srv := resourceServer(t, control)

	if status := putResource(t, srv.URL+"/admin/resources/geosite-ru.srs", original[:len(original)/2]); status != http.StatusUnprocessableEntity {
		t.Fatalf("PUT broken rule-set status = %d, want 422", status)
	}
	if content, _, _ := control.resources.Read("geosite-ru.srs"); !bytes.Equal(content, original) {
		t.Fatal("broken upload replaced the referenced rule-set")
	}
}

// A free name is stored unchecked, as before: apply validates the config that
// starts referencing it.
func TestResourcePutFreeNameSkipsCheck(t *testing.T) {
	control, _ := newResourceTestController(t)
	srv := resourceServer(t, control)
	if status := putResource(t, srv.URL+"/admin/resources/draft.srs", []byte("not a rule-set")); status != http.StatusOK {
		t.Fatalf("PUT free name status = %d, want 200", status)
	}
}
