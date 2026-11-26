package http

// lx:begin SPEC 138 an HTTP proxy that refused the destination
//
// sing's HTTP client (protocol/http/client.go) returns a plain error for a CONNECT
// answered with a status other than 200: "authentication required" (407),
// "method not allowed" (405) and "unexpected status: <status>" for the rest. The
// statuses that say the destination failed — 403 (refused by the proxy's policy
// for this destination), 502 (bad gateway: the proxy could not reach it), 503 and
// 504 — mark the error as the destination's (adapter.DestinationRefusedLx). Every
// other error (the dial to the proxy, EOF during the exchange, a timeout, 407,
// 405) is the node's.

import (
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/adapter"
)

const lxUnexpectedStatus = "unexpected status: "

func lxDestinationRefused(tag string, err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	index := strings.Index(message, lxUnexpectedStatus)
	if index < 0 {
		return err
	}
	status := message[index+len(lxUnexpectedStatus):]
	if end := strings.IndexByte(status, ' '); end >= 0 {
		status = status[:end]
	}
	switch code, _ := strconv.Atoi(status); code {
	case 403, 502, 503, 504:
		return &adapter.DestinationRefusedLx{Tag: tag, Err: err}
	}
	return err
}

// lx:end SPEC 138
