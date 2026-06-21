// lx: Reality detection for XHTTP mode=auto resolution.
//
// Xray's splithttp resolves mode=auto to stream-one when the TLS layer is Reality
// (transport/internet/splithttp/dialer.go). We mirror that. The catch is build-tag
// isolation: the concrete Reality client config types live in common/tls behind
// //go:build with_utls, while this package is built behind with_xhttp. A direct
// type assertion (tlsConfig.(*tls.RealityClientConfig)) would create a hard
// with_xhttp -> with_utls dependency and break a with_xhttp build that lacks
// with_utls (CONSTITUTION §3.2: each feature behind its own tag, no cross-coupling).
//
// So we detect Reality by the runtime type NAME via reflection, which needs no
// compile-time reference to the with_utls-only types. The kTLS wrapper
// (*tls.KTLSClientConfig) embeds the inner Config, so we unwrap it and re-check.
package v2rayxhttp

import (
	"reflect"

	"github.com/sagernet/sing-box/common/tls"
)

// realityConfigTypeName is the unqualified type name of common/tls.RealityClientConfig.
// Matched as a suffix of reflect type String() (e.g. "*tls.RealityClientConfig").
const realityConfigTypeName = "RealityClientConfig"

// ktlsConfigTypeName is the kTLS wrapper that embeds an inner Config (which may be
// a Reality config). Unwrapped via its embedded "Config" field.
const ktlsConfigTypeName = "KTLSClientConfig"

// tlsConfigIsReality reports whether tlsConfig is (or wraps) a Reality client
// config, used to resolve mode=auto to stream-one. It matches by type name to
// avoid a compile-time dependency on the with_utls-only Reality/kTLS types.
func tlsConfigIsReality(tlsConfig tls.Config) bool {
	return typeIsReality(tlsConfig, 0)
}

// typeIsReality walks at most a few wrapper levels (kTLS) to find a Reality config.
// depth guards against any pathological self-referential wrapping.
func typeIsReality(v any, depth int) bool {
	if v == nil || depth > 4 {
		return false
	}
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil {
		return false
	}
	name := t.Name()
	if name == realityConfigTypeName {
		return true
	}
	if name == ktlsConfigTypeName {
		// Unwrap the embedded inner Config (common/tls.KTLSClientConfig embeds Config).
		if inner := embeddedConfig(v); inner != nil {
			return typeIsReality(inner, depth+1)
		}
	}
	return false
}

// embeddedConfig returns the value of the embedded "Config" field on a kTLS
// wrapper, or nil if absent/not addressable. Uses reflection so this package
// needs no compile-time knowledge of the wrapper's concrete type.
func embeddedConfig(v any) any {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	f := rv.FieldByName("Config")
	if !f.IsValid() || !f.CanInterface() {
		return nil
	}
	return f.Interface()
}
