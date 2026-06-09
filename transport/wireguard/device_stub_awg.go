//go:build !with_awg

package wireguard

import (
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

// awgIpcLines is the no-`with_awg` stub. AmneziaWG obfuscation is not compiled
// in, so any endpoint that configures AWG fields is rejected with an explicit
// error instead of silently falling back to plain WireGuard — a silent
// downgrade would strip the obfuscation the user asked for. When no AWG field
// is set it returns "" and the endpoint behaves exactly like upstream
// WireGuard.
func awgIpcLines(o option.AmneziaWGOptions) (string, error) {
	if o.IsSet() {
		return "", E.New("AmneziaWG (awg) support is not included in this build, rebuild with -tags with_awg")
	}
	return "", nil
}
