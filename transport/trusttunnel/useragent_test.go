package trusttunnel

import (
	"runtime"
	"testing"
)

func TestUserAgent(t *testing.T) {
	platform := map[string]string{
		"darwin":  "Mac",
		"windows": "Windows",
		"linux":   "Linux",
		"android": "Android",
		"ios":     "iOS",
	}[runtime.GOOS]
	if platform == "" {
		platform = "Linux"
	}
	if Platform() != platform {
		t.Fatalf("platform %q, want %q", Platform(), platform)
	}
	if got := UserAgent("sing-box-lx"); got != platform+" sing-box-lx" {
		t.Errorf("user agent %q", got)
	}
	if got := UserAgentUDP(); got != platform+" _udp2" {
		t.Errorf("UDP user agent %q", got)
	}
	if got := UserAgentCheck(); got != platform+" unknown" {
		t.Errorf("check user agent %q", got)
	}
}
