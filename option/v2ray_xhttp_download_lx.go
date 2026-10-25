package option

// V2RayXHTTPDownloadOptions describes an independent download-only XHTTP leg.
// TLS is independent: omitted TLS means cleartext, never inherited uplink TLS.
// A nil dialer inherits the parent dialer; an explicit object builds its own.
type V2RayXHTTPDownloadOptions struct {
	ServerOptions
	Dialer *DialerOptions `json:"dialer,omitempty"`
	OutboundTLSOptionsContainer
	XHTTP V2RayXHTTPOptions `json:"xhttp"`
}
