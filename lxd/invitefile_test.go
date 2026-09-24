//go:build with_lxd

package lxd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeClientName(t *testing.T) {
	sixtyFour := strings.Repeat("я", 64)
	for _, testCase := range []struct {
		name, in, want, wantErr string
	}{
		{"empty is no name", "", "", ""},
		{"spaces only is no name", "   ", "", ""},
		{"trimmed", "  singbox-launcher-u  ", "singbox-launcher-u", ""},
		{"64 characters", sixtyFour, sixtyFour, ""},
		{"65 characters", sixtyFour + "x", "", "client name: 65 characters, at most 64 allowed"},
		{"inner space is printable", "my launcher", "my launcher", ""},
		{"control character", "bad\x07name", "", "client name: contains the non-printable character '\\a'"},
		{"newline", "two\nlines", "", "non-printable"},
		{"invalid UTF-8", "\xff", "", "not valid UTF-8"},
	} {
		got, err := NormalizeClientName(testCase.in)
		if testCase.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("%s: got %q, %v; want error %q", testCase.name, got, err, testCase.wantErr)
			}
			continue
		}
		if err != nil || got != testCase.want {
			t.Fatalf("%s: got %q, %v; want %q", testCase.name, got, err, testCase.want)
		}
	}
}

func TestInviteFileExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invite.txt")
	if err := CheckInviteFileAbsent(path); err != nil {
		t.Fatal(err)
	}
	file, err := CreateInviteFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = WriteInvite(file, "127.0.0.1:19091#fp#K7QM-XXNP-2RTD"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "127.0.0.1:19091#fp#K7QM-XXNP-2RTD\n" {
		t.Fatalf("invite file holds %q (%v)", content, err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Fatalf("invite file mode %v, want 0600", info.Mode().Perm())
		}
	}
	// An existing file refuses, and is left as it was.
	if err = CheckInviteFileAbsent(path); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("an existing file must be refused first, got %v", err)
	}
	if _, err = CreateInviteFile(path); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("an existing file must be refused, got %v", err)
	}
	if again, _ := os.ReadFile(path); string(again) != string(content) {
		t.Fatal("a refused create must not touch the existing file")
	}
	// A failed mint discards the file.
	discarded := filepath.Join(t.TempDir(), "failed.txt")
	file, err = CreateInviteFile(discarded)
	if err != nil {
		t.Fatal(err)
	}
	DiscardInviteFile(file)
	if _, err = os.Lstat(discarded); !os.IsNotExist(err) {
		t.Fatal("a discarded invite file must be gone")
	}
}

func TestInviteFileRefusesLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.txt")
	link := filepath.Join(dir, "invite.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := CreateInviteFile(link); err == nil {
		t.Fatal("a link at the invite path must be refused")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("nothing may be written through the link")
	}
}
