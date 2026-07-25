//go:build with_xhttp

package include

// Blank-import the XHTTP transport so its init() registers the "xhttp" client
// transport constructor with the v2ray registry. Gated by with_xhttp: without
// the tag this file is excluded, nothing registers, and "xhttp" stays an
// unknown transport type at runtime (correct). See SPECS/TASKS/002.
import _ "github.com/sagernet/sing-box/transport/v2rayxhttp"
