//go:build with_lx_command

package daemon

import (
	"bytes"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
)

// captureRunningConfig renders the post-override options — the exact struct handed to
// box.New (tun AutoRedirect/packages applied, OOM-killer service injected) — as canonical
// indented JSON, once, at instance construction (SPEC 037). Same encoder as FormatConfig,
// so the output shape matches what the client already knows from config formatting.
// Best-effort by design: a marshal failure yields "" (GetRunningConfig then reports
// Unavailable) rather than failing service start over an observability snapshot.
//
// The root `lx` block is shown canonical (SPEC 098): the deprecated
// route.lx_idle_* aliases are folded into lx.wg.* on this by-value copy —
// ResolveLX replaces the LX and Route pointers instead of writing through them,
// so the caller's options, and the alias warnings box.New reports from them,
// are untouched. A resolve error leaves the snapshot as given; box.New fails on
// the same error right after.
func captureRunningConfig(options option.Options) string {
	_, _, _ = option.ResolveLX(&options)
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(options); err != nil {
		return ""
	}
	return buffer.String()
}
