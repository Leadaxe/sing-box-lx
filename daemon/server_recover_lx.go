package daemon

import (
	"context"
	"runtime/debug"

	"github.com/sagernet/sing-box/log"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 103 — a gRPC handler runs in its own goroutine, so a panic there
// takes the whole daemon down with the stack in a stderr a Windows service
// does not have. The recover interceptors log it and answer codes.Internal.

func unaryRecoverInterceptor(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (response any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = recoveredPanic(info.FullMethod, r)
		}
	}()
	return handler(ctx, request)
}

func streamRecoverInterceptor(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = recoveredPanic(info.FullMethod, r)
		}
	}()
	return handler(server, stream)
}

func recoveredPanic(method string, r any) error {
	log.Error("daemon: panic serving ", method, ": ", r, "\n", string(debug.Stack()))
	return status.Errorf(codes.Internal, "internal panic: %v", r)
}
