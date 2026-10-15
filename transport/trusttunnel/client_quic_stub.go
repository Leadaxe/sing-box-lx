//go:build !with_quic

package trusttunnel

import "context"

const quicIncluded = false

func validateCongestionControl(string) error {
	return nil
}

func (c *Client) connectQUIC(context.Context) (sessionConn, error) {
	return nil, ErrQUICNotIncluded
}
