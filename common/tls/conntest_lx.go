package tls

// lx: SPEC 139 — `tls` stage of ClientHandshake. Without a trace in ctx it
// costs one ctx.Value lookup and records nothing.

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/sagernet/sing-box/common/lxtrace"
)

func beginHandshakeLx(ctx context.Context, config Config) (context.Context, *lxtrace.Stage) {
	if lxtrace.From(ctx) == nil {
		return ctx, nil
	}
	ctx, stage := lxtrace.Begin(ctx, "tls", config.ServerName())
	if protos := config.NextProtos(); len(protos) > 0 {
		stage.Attr("alpn", strings.Join(protos, ","))
	}
	stage.Attr("engine", fmt.Sprintf("%T", config))
	return ctx, stage
}

func endHandshakeLx(stage *lxtrace.Stage, conn Conn, err error) {
	if stage == nil {
		return
	}
	if err == nil && conn != nil {
		state := conn.ConnectionState()
		stage.Attr("version", tls.VersionName(state.Version))
		if state.NegotiatedProtocol != "" {
			stage.Attr("negotiated_alpn", state.NegotiatedProtocol)
		}
	}
	stage.End(err)
}
