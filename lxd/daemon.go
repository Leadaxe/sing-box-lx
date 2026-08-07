//go:build with_lx_command

// Package lxd hosts the sing-box core in-process behind a long-lived gRPC
// control channel (daemon.StartedService). The channel belongs to the daemon
// process, not to the box instance: config reloads swap the instance under a
// live server, so control clients keep their connections and streams across
// reloads — the property the SIGHUP path of `sing-box run` cannot provide.
// SPECS/TASKS/055-LXD_DAEMON_SKELETON.
package lxd

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
)

type Options struct {
	ConfigPath string
	Listen     string
	Secret     string
}

const logMaxLines = 3000

// Run brings the control listener up FIRST and only then the core: a broken
// config must leave the daemon reachable (status FATAL), because the control
// channel is needed exactly when the data plane is down. SIGHUP re-reads the
// config file and swaps the box instance in-process; SIGINT/SIGTERM shut the
// daemon down.
func Run(ctx context.Context, options Options) error {
	content, err := os.ReadFile(options.ConfigPath)
	if err != nil {
		return E.Cause(err, "read config")
	}
	startedService := daemon.NewStartedService(daemon.ServiceOptions{
		Context:     ctx,
		LogMaxLines: logMaxLines,
	})
	grpcServer := daemon.NewServer(startedService, options.Secret)
	listener, err := net.Listen("tcp", options.Listen)
	if err != nil {
		startedService.Close()
		return E.Cause(err, "listen control channel")
	}
	log.Info("lxd: control channel listening at ", listener.Addr())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- grpcServer.Serve(listener)
	}()
	if err = startedService.StartOrReloadService(ctx, string(content), nil); err != nil {
		log.Error(E.Cause(err, "lxd: start service; staying up in FATAL — fix the config and send SIGHUP"))
	}
	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(osSignals)
	for {
		select {
		case err = <-serveErr:
			_ = startedService.CloseService()
			startedService.Close()
			return E.Cause(err, "control channel")
		case osSignal := <-osSignals:
			if osSignal == syscall.SIGHUP {
				content, err = os.ReadFile(options.ConfigPath)
				if err != nil {
					log.Error(E.Cause(err, "lxd: reload: read config"))
					continue
				}
				if err = startedService.StartOrReloadService(ctx, string(content), nil); err != nil {
					log.Error(E.Cause(err, "lxd: reload; staying up in FATAL — fix the config and send SIGHUP"))
				}
				continue
			}
			log.Info("lxd: ", osSignal.String(), ", shutting down")
			closeErr := startedService.CloseService()
			grpcServer.Stop()
			startedService.Close()
			return closeErr
		}
	}
}
