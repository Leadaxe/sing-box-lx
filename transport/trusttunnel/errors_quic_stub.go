//go:build !with_quic

package trusttunnel

func quicStreamClosedError(error) error {
	return nil
}
