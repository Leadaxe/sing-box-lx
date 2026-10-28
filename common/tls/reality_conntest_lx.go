//go:build with_utls

package tls

// lx: SPEC 139 — the `reality` verdict of a REALITY ClientHandshake. Without a
// trace in ctx it costs one ctx.Value lookup and records nothing.

import (
	"context"
	"crypto/x509"
	"errors"

	"github.com/sagernet/sing-box/common/lxtrace"
	E "github.com/sagernet/sing/common/exceptions"
)

// REALITY verdicts (SPEC 139 §4).
const (
	realityVerdictAccepted    = "accepted"     // the server recognized the client
	realityVerdictDestCert    = "dest_cert"    // the server served the dest certificate: the client was not recognized
	realityVerdictInvalidCert = "invalid_cert" // the certificate is neither the REALITY one nor valid for the server name
)

var errRealityDestCert = E.New("reality: the server did not recognize the client and served the dest certificate")

// realityVerdictLx writes the `reality` stage once the handshake has returned:
// verified → accepted; a completed handshake without verification → the dest
// certificate was served; a certificate verification error → invalid_cert.
// Any other handshake error (reset, EOF, timeout) gives no verdict.
func realityVerdictLx(ctx context.Context, config Config, verified bool, err error) {
	if lxtrace.From(ctx) == nil {
		return
	}
	var verdict string
	var stageErr error
	switch {
	case err == nil && verified:
		verdict = realityVerdictAccepted
	case err == nil:
		verdict, stageErr = realityVerdictDestCert, errRealityDestCert
	case isCertificateError(err):
		verdict, stageErr = realityVerdictInvalidCert, err
	default:
		return
	}
	_, stage := lxtrace.Begin(ctx, "reality", config.ServerName())
	stage.Attr("verdict", verdict)
	stage.End(stageErr)
}

func isCertificateError(err error) bool {
	var (
		unknownAuthority x509.UnknownAuthorityError
		hostname         x509.HostnameError
		invalid          x509.CertificateInvalidError
		systemRoots      x509.SystemRootsError
	)
	return errors.As(err, &unknownAuthority) || errors.As(err, &hostname) ||
		errors.As(err, &invalid) || errors.As(err, &systemRoots)
}
