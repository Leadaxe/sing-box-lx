//go:build with_lx_command

// Package lxd hosts the sing-box core in-process behind a long-lived control
// listener. The listener belongs to the daemon process, not to the box
// instance: config applies swap the instance under a live server, so control
// clients keep their connections and streams across reloads — the property
// the SIGHUP path of `sing-box run` cannot provide. One port carries two
// planes: gRPC daemon.StartedService (observability, shared with the Android
// line) and the admin REST plane (apply/rollback/start/stop, enrollment).
// SPECS/TASKS/055-LXD_DAEMON_SKELETON, 056-LXD_APPLY_ROLLBACK, 057-LXD_MTLS_SERVICE.
package lxd

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type Options struct {
	// ConfigPath is the optional seed config, used only when no last-good is
	// recorded. Empty = start with no core and wait for the first apply.
	ConfigPath string
	// ConfigForce, when set, always boots from this file, overriding any
	// recorded last-good; after a successful start it becomes the last-good.
	ConfigForce string
	// Run forces the core up regardless of the recorded run-state.
	Run    bool
	Listen string
	Secret string
	// TLS enables the mTLS control plane. When false the daemon serves plain
	// h2c (loopback-only, dev).
	TLS      bool
	StateDir string
}

const (
	logMaxLines       = 3000
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 120 * time.Second
	shutdownGrace     = 3 * time.Second
)

// Run brings the control listener up FIRST and only then the core: a broken or
// absent config must leave the daemon reachable, because the control channel
// is needed exactly when the data plane is down.
func Run(ctx context.Context, options Options) error {
	stateStore, err := newStore(options.StateDir)
	if err != nil {
		return err
	}
	startedService := daemon.NewStartedService(daemon.ServiceOptions{
		Context:     ctx,
		LogMaxLines: logMaxLines,
	})
	control := &controller{
		ctx:      ctx,
		service:  startedService,
		store:    stateStore,
		validate: execSelfCheck,
	}

	var serverIdentity *identity
	if options.TLS {
		serverIdentity, err = loadOrCreateServerIdentity(options.StateDir, time.Now())
		if err != nil {
			startedService.Close()
			return err
		}
		control.clients, err = newClientRegistry(stateStore)
		if err != nil {
			startedService.Close()
			return err
		}
		control.serverFingerprint = serverIdentity.fingerprint
		control.advertiseAddr = options.Listen
	}

	grpcServer := daemon.NewServer(startedService, options.Secret)
	adminHandler := control.adminHandler(options.Secret)
	httpServer := &http.Server{
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		Handler: h2c.NewHandler(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.ProtoMajor == 2 && strings.HasPrefix(request.Header.Get("Content-Type"), "application/grpc") {
				// gRPC observability plane: pin the client cert too, so it is
				// not an open door around the admin plane's mTLS gate.
				if control.clients != nil {
					if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 ||
						!control.clients.isTrusted(fingerprintOf(request.TLS.PeerCertificates[0].Raw)) {
						http.Error(writer, "client certificate not trusted", http.StatusUnauthorized)
						return
					}
				}
				grpcServer.ServeHTTP(writer, request)
				return
			}
			adminHandler.ServeHTTP(writer, request)
		}), &http2.Server{IdleTimeout: idleTimeout}),
	}

	listener, err := net.Listen("tcp", options.Listen)
	if err != nil {
		startedService.Close()
		return E.Cause(err, "listen control channel")
	}
	if options.TLS {
		listener = tls.NewListener(listener, tlsConfig(serverIdentity, control.clients))
	}
	log.Info("lxd: control channel listening at ", listener.Addr())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.Serve(listener)
	}()

	// bootstrap runs under applyAccess so it cannot race the first REST apply.
	control.applyAccess.Lock()
	bootstrap(ctx, control, stateStore, options)
	control.applyAccess.Unlock()

	if options.TLS {
		maybePrintInvite(control, serverIdentity, options.Listen)
	}

	// Buffer > 1 so a SIGTERM arriving during a long SIGHUP apply is not lost.
	osSignals := make(chan os.Signal, 4)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(osSignals)
	for {
		select {
		case err = <-serveErr:
			shutdown(startedService, httpServer, grpcServer)
			return E.Cause(err, "control channel")
		case osSignal := <-osSignals:
			if osSignal == syscall.SIGHUP {
				reloadFromFile(ctx, control, options)
				continue
			}
			log.Info("lxd: ", osSignal.String(), ", shutting down")
			// Serialize teardown with any in-flight apply.
			control.applyAccess.Lock()
			shutdown(startedService, httpServer, grpcServer)
			control.applyAccess.Unlock()
			return nil
		}
	}
}

func shutdown(startedService *daemon.StartedService, httpServer *http.Server, grpcServer interface{ Stop() }) {
	_ = startedService.CloseService()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	grpcServer.Stop()
	startedService.Close()
}

// bootstrap decides the boot config and whether to start the core. Precedence:
// --config-force wins; else recorded last-good; else the -c seed. The core is
// started when --run is set or the recorded run-state says it was running.
// A staged candidate is never booted — an apply interrupted by a process death
// is reported via /admin/status and boots the last config known to work.
// Caller holds applyAccess.
func bootstrap(ctx context.Context, control *controller, stateStore *store, options Options) {
	if pendingSHA, interrupted := stateStore.PendingSHA(); interrupted {
		log.Warn("lxd: interrupted apply detected (candidate ", shortSHA(pendingSHA), "), booting last-good")
		control.stateAccess.Lock()
		control.interruptedApply = true
		control.stateAccess.Unlock()
		stateStore.ClearPending()
	}

	bootConfig, source := resolveBootConfig(stateStore, options)
	if bootConfig == "" {
		log.Info("lxd: no config to boot from — idle, waiting for the first apply")
		return
	}
	shouldRun := options.Run || stateStore.WasRunning()
	if !shouldRun {
		log.Info("lxd: config loaded from ", source, " but run-state is stopped — core not started")
		return
	}
	if err := control.service.StartOrReloadService(ctx, bootConfig, nil); err != nil {
		control.setFatal(err)
		log.Error(E.Cause(err, "lxd: boot from "+source+"; staying up — fix and apply over the admin plane"))
		return
	}
	_ = stateStore.SetWasRunning(true)
	if source != "last-good" {
		if err := stateStore.SaveLastGood(bootConfig); err != nil {
			log.Error(E.Cause(err, "lxd: persist boot config as last-good"))
		}
	}
	control.setActive(bootConfig, "")
	log.Info("lxd: core started from ", source)
}

func resolveBootConfig(stateStore *store, options Options) (content string, source string) {
	if options.ConfigForce != "" {
		forced, err := os.ReadFile(options.ConfigForce)
		if err != nil {
			log.Error(E.Cause(err, "lxd: read --config-force file"))
			return "", ""
		}
		return string(forced), "config-force"
	}
	if lastGood, loaded, err := stateStore.LoadLastGood(); err != nil {
		log.Error(E.Cause(err, "lxd: read last-good"))
	} else if loaded {
		return lastGood, "last-good"
	}
	if options.ConfigPath != "" {
		seed, err := os.ReadFile(options.ConfigPath)
		if err != nil {
			log.Error(E.Cause(err, "lxd: read seed config"))
			return "", ""
		}
		return string(seed), "seed"
	}
	return "", ""
}

func reloadFromFile(ctx context.Context, control *controller, options Options) {
	path := options.ConfigForce
	if path == "" {
		path = options.ConfigPath
	}
	if path == "" {
		log.Warn("lxd: SIGHUP ignored — started without a config file; use the admin plane")
		return
	}
	content, err := os.ReadFile(path)
	if err != nil {
		log.Error(E.Cause(err, "lxd: reload: read config"))
		return
	}
	switch result := control.Apply(ctx, string(content)); result.Outcome {
	case applyApplied:
		log.Info("lxd: reload applied")
	case applyRejected:
		log.Error(E.Cause(result.Err, "lxd: reload rejected, instance untouched"))
	case applyError:
		log.Error(E.Cause(result.Err, "lxd: reload failed (infrastructure)"))
	default:
		if result.RolledBack {
			log.Error(E.Cause(result.Err, "lxd: reload failed, rolled back to last-good"))
		} else {
			log.Error(E.Cause(result.Err, "lxd: reload failed"))
		}
	}
}

// maybePrintInvite prints a one-time enrollment invite on a fresh install
// (no clients yet): address#server-fingerprint#code. The launcher pins the
// server by the fingerprint and registers with the code.
func maybePrintInvite(control *controller, serverIdentity *identity, listen string) {
	if control.clients.count() > 0 {
		return
	}
	code, err := control.clients.mintCode()
	if err != nil {
		log.Error(E.Cause(err, "lxd: mint enrollment code"))
		return
	}
	log.Info("lxd: no clients registered — copy this invite into the launcher:")
	log.Info("     ", listen, "#", serverIdentity.fingerprint, "#", code)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12] + "…"
	}
	return sha
}

// tlsConfig requires client certs and pins them against the trusted registry.
// Enrollment (POST /admin/enroll) runs over the same TLS but must be reachable
// before the client is trusted, so client-cert verification is request-time,
// not handshake-time: we ask for the cert but verify trust only for non-enroll
// routes (the admin handler gates that). Here we accept any presented client
// cert at the TLS layer and let the pin be enforced per-route.
func tlsConfig(serverIdentity *identity, clients *clientRegistry) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{serverIdentity.tlsCert},
		ClientAuth:   tls.RequestClientCert,
		MinVersion:   tls.VersionTLS12,
	}
}
