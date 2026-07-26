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
func captureRunningConfig(options option.Options) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(options); err != nil {
		return ""
	}
	return buffer.String()
}
