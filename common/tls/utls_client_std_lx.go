//go:build with_utls

package tls

import (
	"crypto/tls"
	"slices"
)

// lxSTDConfig builds a crypto/tls client config equivalent to this uTLS config
// for transports that cannot take a uTLS ClientHello (lx: SPEC 104 — XHTTP over
// HTTP/3, where quic-go builds its own ClientHello). STDConfig() refuses uTLS by
// design, so this sits beside it instead of changing it.
//
// The fields are the ones newUTLSClient fills; a new field there that affects
// certificate verification must be carried over here (SPEC 104 §9).
func (c *UTLSClientConfig) lxSTDConfig() (*STDConfig, error) {
	config := c.config
	stdConfig := &tls.Config{
		Time:                  config.Time,
		RootCAs:               config.RootCAs,
		InsecureSkipVerify:    config.InsecureSkipVerify,
		VerifyPeerCertificate: config.VerifyPeerCertificate,
		NextProtos:            slices.Clone(config.NextProtos),
		MinVersion:            config.MinVersion,
		MaxVersion:            config.MaxVersion,
		CipherSuites:          slices.Clone(config.CipherSuites),
		ServerName:            config.ServerName,
	}
	for _, certificate := range config.Certificates {
		stdConfig.Certificates = append(stdConfig.Certificates, tls.Certificate{
			Certificate:                 certificate.Certificate,
			PrivateKey:                  certificate.PrivateKey,
			OCSPStaple:                  certificate.OCSPStaple,
			SignedCertificateTimestamps: certificate.SignedCertificateTimestamps,
			Leaf:                        certificate.Leaf,
		})
	}
	if c.disableSNI {
		// uTLS verifies the name through InsecureServerNameToVerify; crypto/tls
		// has no such field, so do what STDClientConfig does for disable_sni.
		stdConfig.ServerName = ""
		if c.verifyServerName {
			stdConfig.InsecureSkipVerify = true
			stdConfig.VerifyConnection = verifyConnection(stdConfig.RootCAs, stdConfig.Time, c.serverName)
		}
	}
	return stdConfig, nil
}
