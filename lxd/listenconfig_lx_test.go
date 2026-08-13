//go:build with_lxd

package lxd

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The string form must survive byte-for-byte: daemon.json files written before
// the object form existed are in the field, and a parse change there would
// strand installed daemons.
func TestListenConfigStringForm(t *testing.T) {
	var config DaemonConfig
	if err := json.Unmarshal([]byte(`{"listen":"127.0.0.1:19091","tls":true}`), &config); err != nil {
		t.Fatal(err)
	}
	if got := config.Listen.Addresses(); !reflect.DeepEqual(got, []string{"127.0.0.1:19091"}) {
		t.Fatalf("addresses = %v", got)
	}
	if got := config.Listen.Advertise(); got != "127.0.0.1:19091" {
		t.Fatalf("advertise = %q", got)
	}
	// A single address marshals back as a string, so save-after-load does not
	// rewrite an operator's file into the other form.
	encoded, err := json.Marshal(config.Listen)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `"127.0.0.1:19091"` {
		t.Fatalf("round-trip = %s, want the string form", encoded)
	}
}

func TestListenConfigObjectForm(t *testing.T) {
	var config DaemonConfig
	raw := `{"listen":{"address":["192.168.10.1","127.0.0.1"],"port":19091}}`
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.10.1:19091", "127.0.0.1:19091"}
	if got := config.Listen.Addresses(); !reflect.DeepEqual(got, want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}
	// Advertise is the FIRST entry — file order is the operator's preference.
	if got := config.Listen.Advertise(); got != "192.168.10.1:19091" {
		t.Fatalf("advertise = %q", got)
	}
	encoded, err := json.Marshal(config.Listen)
	if err != nil {
		t.Fatal(err)
	}
	var reparsed ListenConfig
	if err = json.Unmarshal(encoded, &reparsed); err != nil {
		t.Fatalf("re-parse own output: %v", err)
	}
	if !reflect.DeepEqual(reparsed.Addresses(), want) {
		t.Fatalf("re-parsed = %v, want %v", reparsed.Addresses(), want)
	}
}

// IPv6 hosts must survive the bare-host + shared-port join, which is exactly
// where a naive host+":"+port concatenation breaks.
func TestListenConfigIPv6(t *testing.T) {
	var config ListenConfig
	if err := json.Unmarshal([]byte(`{"address":["::1"],"port":19091}`), &config); err != nil {
		t.Fatal(err)
	}
	if got := config.Advertise(); got != "[::1]:19091" {
		t.Fatalf("advertise = %q, want bracketed IPv6", got)
	}
}

func TestListenConfigRejects(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		raw     string
		wantSub string
	}{
		{"empty string", `""`, "empty address"},
		{"empty object", `{}`, "no address"},
		{"empty list", `{"address":[]}`, "no address"},
		{"netmask", `{"address":["192.168.10.1/32"],"port":19091}`, "netmasks are not supported"},
		{"bare host without port", `{"address":["192.168.10.1"]}`, "expected \"host:port\""},
		{"port twice", `{"address":["192.168.10.1:1234"],"port":19091}`, "already carries a port"},
		{"duplicate", `{"address":["127.0.0.1","127.0.0.1"],"port":19091}`, "duplicate address"},
		{"unknown field", `{"addresses":["127.0.0.1"],"port":19091}`, "expected"},
		{"wrong type", `1234`, "expected"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var config ListenConfig
			err := json.Unmarshal([]byte(testCase.raw), &config)
			if err == nil {
				t.Fatalf("%s parsed without error as %+v", testCase.raw, config)
			}
			if !strings.Contains(err.Error(), testCase.wantSub) {
				t.Fatalf("error %q does not mention %q", err, testCase.wantSub)
			}
		})
	}
}

// A malformed listen must fail the whole file: LoadDaemonConfig reporting
// success with a zero Listen would silently fall back to the dev default.
func TestLoadDaemonConfigRejectsBadListen(t *testing.T) {
	dir := t.TempDir()
	raw := `{"listen":{"address":["10.0.0.1/24"],"port":19091},"tls":true}`
	if err := os.WriteFile(daemonConfigPath(dir), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadDaemonConfig(dir); err == nil {
		t.Fatal("a netmask in daemon.json must be an error, not a silent default")
	}
}
