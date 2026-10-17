//go:build with_lxd

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
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"

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
	Run bool
	// Listen is the control channel bind spec: one or more addresses serving
	// the same two planes.
	Listen ListenConfig
	Secret string
	// TLS enables the mTLS control plane. When false the daemon serves plain
	// h2c (loopback-only, dev).
	TLS      bool
	StateDir string
	// LogFile, when set, is the daemon-owned rotated log: Run re-points the
	// process's stdout/stderr at it (unless stdout is a terminal — dev runs
	// keep their screen). Empty = leave stdio alone. The Log* limits mirror
	// daemon.json (zero = default).
	LogFile        string
	LogMaxSizeMB   int
	LogMaxBackups  int
	LogMaxAgeHours int
	// LogTakenOver: the caller already took the log over with TakeOverLog
	// (the Windows service does it before its self-check, so a refusal
	// lands in the file); Run leaves stdio alone and only reports LogFile.
	LogTakenOver bool
	// DHCPLeaseFiles overrides the client directory's lease paths (SPEC 066);
	// empty = the platform defaults.
	DHCPLeaseFiles []string
	// Commands are daemon.json's named commands (SPEC 128); empty = the
	// /admin/commands routes are not registered.
	Commands map[string]string
	// Hooks is daemon.json's `hooks` object (SPEC 135); nil = no key.
	Hooks *HooksConfig
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
	// Log ownership first, before anything logs.
	if !options.LogTakenOver {
		if release := TakeOverLog(options); release != nil {
			defer release()
		}
	}
	// The core needs the service registry the CLI puts into its context
	// (include.Context); without it the first apply panics inside the core.
	if service.RegistryFromContext(ctx) == nil {
		return E.New("lxd: context without service registry")
	}

	stateStore, err := newStore(options.StateDir)
	if err != nil {
		return err
	}
	resourcesDir := filepath.Join(options.StateDir, "resources")
	resourceStore, err := newResourceStore(resourcesDir)
	if err != nil {
		return err
	}
	// Operator labels for the client directory (SPEC 066). A corrupt labels
	// file must NOT keep the daemon down — the whole design principle here is
	// that the control channel comes up even when the payload is broken. The
	// failure is logged loudly and the daemon runs with no labels; a later
	// PUT rewrites the file cleanly.
	labels, err := newLabelStore(stateStore)
	if err != nil {
		log.Warn(E.Cause(err, "lxd: client labels disabled"))
		labels = emptyLabelStore(stateStore)
	}
	startedService := daemon.NewStartedService(daemon.ServiceOptions{
		Context:     ctx,
		LogMaxLines: logMaxLines,
	})
	absStateDir := options.StateDir
	if abs, absErr := filepath.Abs(options.StateDir); absErr == nil {
		absStateDir = abs
	}
	ownExecutable, err := resolveOwnExecutable()
	if err != nil {
		log.Warn(E.Cause(err, "lxd: /admin/info will not name the executable"))
	}
	control := &controller{
		// serviceStats wraps the same startedService: the stats endpoint needs
		// the traffic counters, which the narrow reloader interface does not
		// expose (SPEC 065). Wrapping keeps reloader unit-testable as-is.
		service:          observableService{StartedService: startedService},
		store:            stateStore,
		resources:        resourceStore,
		validate:         execSelfCheck,
		infoStateDir:     absStateDir,
		infoResourcesDir: filepath.Join(absStateDir, "resources"),
		infoLogPath:      options.LogFile,
		infoTLS:          options.TLS,
		startedAt:        time.Now(),
		executable:       newExecutableIdentity(ownExecutable),
		platform:         readPlatformInfo(),
		memory:           newMemoryCache(),
		clientInfo:       newClientInfo(labels, options.DHCPLeaseFiles),
		host:             newHostCache(absStateDir),
	}
	control.advertiseAddr = options.Listen.Advertise()
	if len(options.Commands) > 0 {
		if commandsSupported {
			control.commands, err = newCommandRunner(options.Commands, commandOutcomeDir(options.LogFile, absStateDir),
				commandEnv(control.platform, absStateDir, ownExecutable))
			if err != nil {
				startedService.Close()
				return err
			}
		} else {
			log.Warn("lxd: daemon.json commands are ignored on this platform (unix only)")
		}
	}
	// Lifecycle hooks (SPEC 135): the directory and the stubs exist whether or
	// not the hooks are enabled, so the admin sees where to write.
	if hooksSupported {
		control.hooks, err = newHookRunner(options.Hooks, absStateDir, options.LogFile,
			commandEnv(control.platform, absStateDir, ownExecutable), stateStore)
		if err != nil {
			startedService.Close()
			return err
		}
	} else if options.Hooks != nil {
		log.Warn("lxd: daemon.json hooks are ignored on this platform (unix only)")
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
	}

	// gRPC plane auth: under mTLS the h2c wrapper below already pins every
	// gRPC request to a trusted client certificate, so the Bearer interceptor
	// adds nothing but a second credential the launcher would have to know —
	// the certificate IS the client's identity (SPEC 057 revision: the secret
	// is an operator credential, not a client one). Plain h2c (dev) keeps the
	// secret as its only gate, as before.
	grpcSecret := options.Secret
	if options.TLS {
		grpcSecret = ""
	}
	grpcServer := daemon.NewServer(startedService, grpcSecret)
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
			serveAdminRecovered(adminHandler, writer, request)
		}), &http2.Server{IdleTimeout: idleTimeout}),
	}

	// Every configured address must come up: a partial bind is a daemon that
	// looks healthy while being unreachable exactly where the operator asked
	// for it — the launcher then fails to connect with no hint why. Listeners
	// already open are closed before returning, so the ports are free for the
	// retry after the operator fixes the file.
	var listeners []net.Listener
	for _, address := range options.Listen.Addresses() {
		listener, listenErr := net.Listen("tcp", address)
		if listenErr != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			startedService.Close()
			return E.Cause(listenErr, "listen control channel at ", address)
		}
		if options.TLS {
			listener = tls.NewListener(listener, tlsConfig(serverIdentity))
		}
		listeners = append(listeners, listener)
		log.Info("lxd: control channel listening at ", listener.Addr())
	}
	if len(listeners) == 0 {
		startedService.Close()
		return E.New("listen control channel: no address configured")
	}

	// Signals are registered before bootstrap: the first core start can be
	// slow (TUN, binds), and a SIGTERM arriving during it must lead to a clean
	// teardown, not the runtime default kill. Buffer > 1 so a SIGTERM arriving
	// during a long SIGHUP apply is not lost.
	osSignals := make(chan os.Signal, 4)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(osSignals)

	// applyAccess is taken BEFORE the listener starts serving, so the first
	// REST apply cannot race bootstrap.
	control.applyAccess.Lock()
	// Buffered per listener: teardown closes them all and every Serve returns
	// ErrServerClosed, so an unbuffered send would leak the goroutines that
	// lose the race to the one value the select below reads.
	serveErr := make(chan error, len(listeners))
	for _, listener := range listeners {
		go func(listener net.Listener) {
			serveErr <- httpServer.Serve(listener)
		}(listener)
	}
	bootstrap(ctx, control, stateStore, options)
	control.applyAccess.Unlock()

	if options.TLS {
		maybePrintInvite(control, serverIdentity, options.Listen.Advertise())
	}

	for {
		select {
		case err = <-serveErr:
			// One listener dying takes the daemon down, exactly as a single
			// one always did: the remaining addresses are not a fallback the
			// operator asked for, and a daemon silently serving on a subset of
			// its configured addresses is the failure mode the all-or-nothing
			// bind above exists to prevent.
			//
			// Same teardown discipline as the signal path: serialize with any
			// in-flight apply so a half-done pipeline cannot record a config
			// that never reached STARTED.
			control.applyAccess.Lock()
			control.closed = true
			shutdown(control, startedService, httpServer, grpcServer)
			control.applyAccess.Unlock()
			return E.Cause(err, "control channel")
		case osSignal := <-osSignals:
			if osSignal == syscall.SIGHUP {
				reloadFromFile(ctx, control, options)
				continue
			}
			log.Info("lxd: ", osSignal.String(), ", shutting down")
			// Serialize teardown with any in-flight apply; closed keeps
			// queued admin requests from resurrecting the core afterwards.
			control.applyAccess.Lock()
			control.closed = true
			shutdown(control, startedService, httpServer, grpcServer)
			control.applyAccess.Unlock()
			return nil
		case <-ctx.Done():
			// The owner cancelled: the Windows SCM handler on Stop/Shutdown
			// (SPEC 103 §2.10). Same teardown as a SIGTERM; on unix nobody
			// cancels this context, so nothing changes there.
			log.Info("lxd: stop requested, shutting down")
			control.applyAccess.Lock()
			control.closed = true
			shutdown(control, startedService, httpServer, grpcServer)
			control.applyAccess.Unlock()
			return nil
		}
	}
}

// TakeOverLog makes the daemon own its log file: under a service manager
// (stdout is not a terminal) the daemon takes the log over from launchd's
// plain append redirect (or the SCM's missing stdio) and rotates it; in a
// terminal the log stays on the operator's screen. release stops the
// rotation; nil when nothing was taken over.
func TakeOverLog(options Options) (release func()) {
	if options.LogFile == "" || !logRotationSupported || stdoutIsTerminal() {
		return nil
	}
	rotator := newLogRotator(options.LogFile, options.LogMaxSizeMB, options.LogMaxBackups, options.LogMaxAgeHours)
	if err := rotator.Start(); err != nil {
		log.Warn(E.Cause(err, "lxd: log rotation disabled"))
		return nil
	}
	return rotator.Stop
}

// serveAdminRecovered runs the admin plane under a panic guard: net/http
// recovers a handler panic by closing the connection (the client sees EOF)
// and prints the stack to stderr, which a service does not have. Here the
// panic goes to the daemon log and, if nothing was sent yet, answers a JSON
// 500.
func serveAdminRecovered(handler http.Handler, writer http.ResponseWriter, request *http.Request) {
	tracked := &sentTracker{ResponseWriter: writer}
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if r == http.ErrAbortHandler {
			panic(r)
		}
		log.Error("lxd: panic serving ", request.Method, " ", request.URL.Path, ": ", r, "\n", string(debug.Stack()))
		if !tracked.sent {
			writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": fmt.Sprint("internal panic: ", r)})
		}
	}()
	handler.ServeHTTP(tracked, request)
}

// sentTracker records whether the response header has gone out.
type sentTracker struct {
	http.ResponseWriter
	sent bool
}

func (w *sentTracker) WriteHeader(statusCode int) {
	w.sent = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *sentTracker) Write(data []byte) (int, error) {
	w.sent = true
	return w.ResponseWriter.Write(data)
}

func (w *sentTracker) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// shutdown tears the process down: the core first, then post_stop(shutdown)
// while the hook still has the init system's stop budget (SPEC 135 §2.5 —
// shutdownGrace does not apply to it), then the control planes. Caller holds
// applyAccess.
func shutdown(control *controller, startedService *daemon.StartedService, httpServer *http.Server, grpcServer interface{ Stop() }) {
	previous, wasRunning := control.liveFacts()
	_ = startedService.CloseService()
	if wasRunning {
		control.hooks.postStop(hookOrigin{reason: hookReasonShutdown}, previous, "")
	}
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
	// A core_snapshot.json left by the previous process = it died without post_stop:
	// announce that end first, before anything else starts (SPEC 135 §2.4).
	control.hooks.recoverAtBoot()
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
	// Run intent: --run forces; a recorded intent ("1"/"0" on disk) is obeyed;
	// fresh state with a config to boot from defaults to running — this keeps
	// the SPEC 056 contract (`lxd -c seed.json` starts the core on first run)
	// and boots 056-era state dirs that predate the run-state file. Only an
	// explicit /admin/stop records "stopped".
	wasRunning, recorded := stateStore.WasRunning()
	shouldRun := options.Run || wasRunning || !recorded
	if !shouldRun {
		log.Info("lxd: config loaded from ", source, " but the recorded run-state is stopped — core not started (use --run or POST /admin/start)")
		return
	}
	origin := hookOrigin{reason: hookReasonBoot}
	if err := control.hooks.preStart(origin, configFacts(bootConfig, nil)); err != nil {
		// Fail-closed: the gate said no, the core does not start.
		control.setFatal(err)
		log.Error(E.Cause(err, "lxd: boot from "+source+" cancelled by the pre_start hook; staying up"))
		return
	}
	if err := startRetryingBusy(ctx, control.service, bootConfig, bootBusyRetrySleep); err != nil {
		control.setFatal(err)
		log.Error(E.Cause(err, "lxd: boot from "+source+"; staying up — fix and apply over the admin plane"))
		// Every pre_start is answered by exactly one post_*: the start that
		// vanished is a post_stop(fatal) with the facts of the config.
		control.hooks.postStop(hookOrigin{reason: hookReasonFatal}, configFacts(bootConfig, nil), err.Error())
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
	control.hooks.postStart(origin, control.mustLiveFacts(), "")
}

// Boot retry on a busy device (SPEC 129 §2.6). `/etc/init.d/sing-box-lxd
// restart` under procd starts the new process before the old one has released
// its TUN: the kernel keeps the interface until the old fd closes, and the new
// core's TUNSETIFF fails with EBUSY a few seconds too early. Seen live on
// `lxd update` (RouteRich, 2026-10-12): the daemon went fatal while the old
// tun was still being torn down, and the VPN stayed down until a manual
// restart. The window is the old daemon's teardown, seconds at most, so the
// boot retries the whole start while the error is "device busy" — the
// instance is closed on a failed start, nothing leaks between attempts.
// Any other error is final at once: a bad config does not get better by
// waiting. Boot only — a REST apply runs with the old instance already closed
// by this same process, so there is nobody to wait for.
const (
	bootBusyRetryWindow = 10 * time.Second
	bootBusyRetryStep   = 500 * time.Millisecond
)

// bootBusyRetrySleep is the pause between attempts; tests replace it.
var bootBusyRetrySleep = func(ctx context.Context, step time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(step):
		return true
	}
}

func startRetryingBusy(ctx context.Context, service reloader, content string, sleep func(context.Context, time.Duration) bool) error {
	deadline := time.Now().Add(bootBusyRetryWindow)
	for attempt := 1; ; attempt++ {
		err := service.StartOrReloadService(ctx, content, nil)
		if err == nil || !isDeviceBusy(err) || !time.Now().Before(deadline) {
			return err
		}
		if attempt == 1 {
			log.Warn(E.Cause(err, "lxd: boot: device busy (the previous instance is still shutting down?), retrying for up to ", bootBusyRetryWindow))
		}
		if !sleep(ctx, bootBusyRetryStep) {
			return err
		}
	}
}

// isDeviceBusy recognizes EBUSY anywhere in the cause chain; the text form
// is the fallback for an error that was flattened to a string on its way up.
func isDeviceBusy(err error) bool {
	return errors.Is(err, syscall.EBUSY) || strings.Contains(err.Error(), "device or resource busy")
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
	switch result := control.Apply(withHookOrigin(ctx, hookReasonSighup, ""), string(content)); result.Outcome {
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
//
// The code is a trust-granting secret with no TTL, so it must never end up in
// a persistent log: a service's stdout/stderr land in a log file any local
// user may read, and a code lying there indefinitely lets them enroll their
// own client. The full invite is printed only when stdout is a terminal (the
// operator's screen); under a service manager only a hint is logged — the
// operator mints a fresh invite on demand with `lxd client add`.
func maybePrintInvite(control *controller, serverIdentity *identity, listen string) {
	if control.clients.count() > 0 {
		return
	}
	if !stdoutIsTerminal() {
		log.Info("lxd: no clients registered — run `sing-box lxd client add` on this host to mint an enrollment invite")
		return
	}
	code, err := control.clients.mintCode("")
	if err != nil {
		log.Error(E.Cause(err, "lxd: mint enrollment code"))
		return
	}
	fmt.Println("lxd: no clients registered — copy this invite into the launcher:")
	fmt.Println("     " + listen + "#" + serverIdentity.fingerprint + "#" + code)
}

func stdoutIsTerminal() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12] + "…"
	}
	return sha
}

// tlsConfig serves the daemon's self-signed identity. Client-cert verification
// is request-time, not handshake-time: enrollment (POST /admin/enroll) runs
// over the same TLS but must be reachable before the client is trusted, so the
// handshake merely requests the cert and the pin is enforced per route.
// NextProtos must advertise h2: the gRPC plane is HTTP/2-only, and without
// ALPN the connection degrades to HTTP/1.1, cutting gRPC off entirely
// (net/http wires HTTP/2 into Serve() only via ALPN negotiation).
func tlsConfig(serverIdentity *identity) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{serverIdentity.tlsCert},
		ClientAuth:   tls.RequestClientCert,
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}
}
