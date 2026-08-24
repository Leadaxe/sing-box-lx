// Package masque implements the MASQUE (CONNECT-IP / RFC 9484) adapter.Outbound
// — the outbound-registry layer over transport/masque, primarily targeting
// Cloudflare WARP. It builds a userspace gVisor stack per tunnel and pumps IP
// packets between it and the CONNECT-IP tunnel (h3 or h2). See SPEC 021.
//
// Not to be confused with transport/wireguard/masque_awg.go (SPEC 009 AmneziaWG
// "masquerade" obfuscation) — unrelated despite the name.
package masque

import (
	"context"
	"crypto/ecdsa"
	stdtls "crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/masque"
	"github.com/sagernet/sing-box/transport/wireguard"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[option.MASQUEOutboundOptions](registry, C.TypeMASQUE, NewOutbound)
}

const defaultMTU uint32 = 1280

// defaultIdleTimeout is how long a tunnel stays up with no traffic before it is
// suspended (torn down) to free the userspace stack, pumps and QUIC keepalive.
const defaultIdleTimeout = 5 * time.Minute

// maxH2MTU caps the userspace MTU on the h2 transport: one IP packet becomes one
// HTTP/2 DATA frame, which must fit the default SETTINGS_MAX_FRAME_SIZE (16384)
// minus the capsule type+length varint header.
const maxH2MTU = 16000

// lx: SPEC 074 — how long `vhttp: auto` waits for the h3 (QUIC) handshake before
// falling back to h2. Chosen from field data: a working WARP h3 handshake lands
// in 250-700 ms even through two proxy hops, while a filtered path produces no
// reply at all — so a few seconds separates "slow" from "never" without making a
// genuinely slow-but-working path lose h3.
const autoH3Timeout = 3 * time.Second

type Outbound struct {
	outbound.Adapter
	ctx         context.Context
	logger      logger.ContextLogger
	dialer      N.Dialer
	dnsRouter   adapter.DNSRouter
	profile     masque.Profile
	idleTimeout time.Duration

	server    M.Socksaddr
	uri       string
	network   string // "h3" | "h2"
	mtu       uint32
	prefixes  []netip.Prefix
	tlsConfig *stdtls.Config
	// h2TLSClient is the shared-layer TLS client used by the h2 transport; nil on
	// h3, which hands tlsConfig to quic-go directly. lx: SPEC 021 Ф4.
	h2TLSClient tls.Config
	quicConfig  *quic.Config

	// Tunnel lifecycle. The current live tunnel is a *session; it is nil
	// whenever no tunnel is up (before the first dial, after an idle-suspend, or
	// after the tunnel died). A fresh session is built lazily on the next dial.
	// runMu guards sess and closed. (lx: SPEC 021 B1/C1/C2 — stateless idle,
	// self-healing reconnect, no leaked half-open tunnel.)
	runMu  sync.Mutex
	sess   *session
	closed bool

	// lx:begin masque-auto
	// SPEC 074 — `vhttp: auto`. autoMode means "try h3, fall back to h2"; the
	// h2 leg's TLS config differs only by ALPN, so it is prepared up front.
	// autoNetwork remembers which leg last worked, so only the first tunnel of a
	// process pays the h3 timeout: subsequent dials go straight to the winner.
	// Atomic because openSession runs under runMu but the value is also read by
	// Network()/logging outside it.
	autoMode    bool
	autoH3Delay time.Duration
	autoNetwork atomic.Pointer[string]
	// legsForTest substitutes the two dial legs in unit tests; nil in production.
	legsForTest *connectLegs
	// lx:end masque-auto
}

// session is one established MASQUE tunnel with its userspace stack and pumps.
type session struct {
	device wireguard.Device
	closer io.Closer
	ipConn masque.IpConn
	ctx    context.Context
	cancel context.CancelFunc
	// activity holds the UnixNano of the last packet in either direction, used
	// by the idle watcher. Atomic so pumps update it lock-free.
	activity atomic.Int64
	teardown sync.Once
}

func (s *session) markActivity(now int64) { s.activity.Store(now) }

func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.MASQUEOutboundOptions) (adapter.Outbound, error) {
	profile, err := masque.ParseProfile(options.Profile)
	if err != nil {
		return nil, err
	}

	// lx: SPEC 062 — fold the deprecated flat fields onto `vhttp` and `tls`
	// before anything reads them, so the rest of this function sees one shape.
	if err = resolveLegacyOptions(ctx, &options); err != nil {
		return nil, err
	}

	network := options.VHTTP
	if network == "" {
		network = "h3"
	}
	if network != "h3" && network != "h2" && network != "auto" {
		return nil, E.New("masque: invalid vhttp: ", network, " (expected h3, h2 or auto)")
	}
	// lx:begin masque-auto
	// SPEC 074 — `auto` races nothing: it tries h3 first and falls back to h2 when
	// the QUIC handshake does not complete in time. The h2 leg is unavailable on
	// the standard profile, so `auto` there is just h3 (with a warning, since the
	// user asked for a fallback that cannot exist).
	autoMode := network == "auto"
	if autoMode && options.Profile == "standard" {
		logger.Warn("masque: `vhttp: auto` has no h2 fallback on the standard profile — using h3 only")
		autoMode = false
		network = "h3"
	}
	if autoMode {
		network = "h3"
	}
	// lx:end masque-auto
	if network == "h2" && options.Profile == "standard" {
		return nil, E.New("masque: vhttp h2 is not implemented for the standard profile")
	}
	if autoMode {
		// lx: SPEC 074 — in `auto` the h2 leg does carry TLS records over TCP, so
		// fragmentation is not universally ignored; report against "auto".
		warnUnsupportedTLSOptions(ctx, logger, "auto", options.TLS)
	} else {
		warnUnsupportedTLSOptions(ctx, logger, network, options.TLS)
	}

	prefixes, err := parsePrefixes(options.IP, options.IPv6)
	if err != nil {
		return nil, err
	}

	var privKey *ecdsa.PrivateKey
	var pubKey *ecdsa.PublicKey
	if profile.PinPublicKey {
		if options.PrivateKey == "" || options.PublicKey == "" {
			return nil, E.New("masque: private_key and public_key are required for the cloudflare profile")
		}
	}
	if options.PrivateKey != "" {
		privKey, err = parseECPrivateKey(options.PrivateKey)
		if err != nil {
			return nil, E.Cause(err, "parse private_key")
		}
	}
	if options.PublicKey != "" {
		pubKey, err = parseECPublicKey(options.PublicKey)
		if err != nil {
			return nil, E.Cause(err, "parse public_key")
		}
	}

	uri := options.URI
	if uri == "" {
		uri = profile.DefaultURI
	}
	if uri == "" {
		return nil, E.New("masque: uri is required for the standard profile")
	}

	// lx: SPEC 062 — `tls.disable_sni` sends no SNI at all, which an empty
	// `server_name` cannot express (it falls back to the profile default). Some
	// endpoints only present their real certificate to a ClientHello without
	// one, so this needs to stay reachable.
	sni := options.TLS.ServerName
	if options.TLS.DisableSNI {
		sni = ""
	} else {
		if sni == "" {
			sni = profile.DefaultSNI
		}
		if sni == "" {
			sni = options.Server
		}
	}

	tlsConfig, err := masque.PrepareTLSConfig(profile, privKey, pubKey, sni, options.TLS.Insecure)
	if err != nil {
		return nil, err
	}
	if network == "h2" {
		// PrepareTLSConfig defaults ALPN to h3; the h2 path needs h2.
		tlsConfig.NextProtos = []string{"h2"}
	}
	// lx: SPEC 074 — `auto` may switch to h2 at runtime, so the h2 TLS config is
	// built alongside the h3 one. ALPN differs per leg, hence the separate copy.
	var h2TLSConfig *stdtls.Config
	if autoMode {
		h2TLSConfig = tlsConfig.Clone()
		h2TLSConfig.NextProtos = []string{"h2"}
	}

	// lx: SPEC 021 Ф4 — the h2 path runs its TLS through the shared common/tls
	// client instead of a bare crypto/tls.Client, so masque stops being the one
	// outbound that misses everything that layer provides (ClientHello
	// fragmentation above all — see SPEC 021 §TLS-слой).
	//
	// The pinning stays masque-specific: cloudflare authenticates the endpoint by
	// its ECDSA public key, not by a chain, and common/tls has no notion of that.
	// It is layered on top via VerifyConnection on the returned STDConfig, so the
	// shared layer needs no masque-shaped hole in it.
	//
	// h3 is deliberately untouched: quic.DialEarly wants a *crypto/tls.Config and
	// QUIC does not carry TLS over TCP at all, so neither the wrapper nor the
	// fragmentation applies there.
	var h2TLSClient tls.Config
	if network == "h2" {
		h2TLSClient, err = buildH2TLSClient(ctx, logger, options, sni, tlsConfig)
		if err != nil {
			return nil, err
		}
	}
	// lx: SPEC 074 — same for `auto`: prepare the h2 client up front so the
	// fallback costs one dial, not a rebuild of the outbound.
	if autoMode {
		h2TLSClient, err = buildH2TLSClient(ctx, logger, options, sni, h2TLSConfig)
		if err != nil {
			return nil, err
		}
	}

	mtu := options.MTU
	if mtu == 0 {
		mtu = defaultMTU
	}
	// On h2, each IP packet becomes one HTTP/2 DATA frame; a packet larger than
	// the default max frame size (16384) would be silently rejected (GOAWAY).
	// lx: SPEC 021 A5.
	if (network == "h2" || autoMode) && mtu > maxH2MTU {
		// lx: SPEC 074 — `auto` may end up on h2, so the h2 ceiling applies to it
		// as well; rejecting at start beats a fallback that cannot work.
		return nil, E.New("masque: mtu ", mtu, " too large for h2 (max ", maxH2MTU, ")")
	}

	// lx: SPEC 028 — QUIC parity with hysteria2/tuic: leave the outer UDP
	// socket free to fragment instead of forcing DF. Matters when this outbound
	// is itself the bottom leg of a nested-tunnel chain (WG/AWG over MASQUE) or
	// when the physical path MTU is below the QUIC packet size ceiling (1452).
	// `udp_fragment: false` restores DF.
	options.UDPFragmentDefault = true
	outboundDialer, err := dialer.New(ctx, options.DialerOptions, options.ServerIsDomain())
	if err != nil {
		return nil, err
	}

	// Idle-suspend window: after this long with no traffic the tunnel + stack +
	// pumps are torn down and rebuilt on the next dial. Default keeps a modest
	// window; 0 in config disables suspend (tunnel stays up until Close).
	idleTimeout := defaultIdleTimeout
	if options.IdleTimeout > 0 {
		idleTimeout = time.Duration(options.IdleTimeout)
	} else if options.IdleTimeout < 0 {
		idleTimeout = 0 // explicit disable
	}

	// QUIC keepalive: only needed to survive the server idle-timeout while the
	// tunnel is up. With idle-suspend on, a short window means we usually tear
	// down before keepalive matters; keep it configurable, default 30s.
	keepAlive := 30 * time.Second
	if options.KeepAlivePeriod > 0 {
		keepAlive = time.Duration(options.KeepAlivePeriod)
	} else if options.KeepAlivePeriod < 0 {
		keepAlive = 0
	}
	quicConfig := &quic.Config{
		EnableDatagrams:   true,
		InitialPacketSize: 1242,
		KeepAlivePeriod:   keepAlive,
	}

	networkList := options.NetworkList.Build()

	return &Outbound{
		Adapter:     outbound.NewAdapterWithDialerOptions(C.TypeMASQUE, tag, networkList, options.DialerOptions),
		ctx:         ctx,
		logger:      logger,
		dialer:      outboundDialer,
		dnsRouter:   service.FromContext[adapter.DNSRouter](ctx),
		profile:     profile,
		idleTimeout: idleTimeout,
		server:      options.ServerOptions.Build(),
		uri:         uri,
		network:     network,
		autoMode:    autoMode,
		autoH3Delay: autoH3Timeout,
		mtu:         mtu,
		prefixes:    prefixes,
		tlsConfig:   tlsConfig,
		h2TLSClient: h2TLSClient,
		quicConfig:  quicConfig,
	}, nil
}

// buildH2TLSClient wires the h2 TLS handshake through the shared common/tls
// client, carrying over everything PrepareTLSConfig decided (ALPN, SNI, the
// client certificate, and the pinning verifier) and adding what only the shared
// layer can give — ClientHello fragmentation.
//
// masque has no `tls` block in its config: its TLS is derived from `profile`
// plus the key material, so the OutboundTLSOptions handed to the shared layer is
// synthesised here rather than taken from the user.
//
// lx: SPEC 021 Ф4.
func buildH2TLSClient(
	ctx context.Context,
	logger log.ContextLogger,
	options option.MASQUEOutboundOptions,
	serverName string,
	prepared *stdtls.Config,
) (tls.Config, error) {
	// Start from what the user configured (SPEC 062: `tls` is theirs now), then
	// impose the parts masque decides itself.
	tlsOptions := common.PtrValueOrDefault(options.TLS)
	tlsOptions.Enabled = true
	tlsOptions.ServerName = serverName
	// ALPN follows the transport, never the config — warnUnsupportedTLSOptions
	// already told the user their value is ignored.
	tlsOptions.ALPN = prepared.NextProtos
	// Chain verification is never what authenticates a WARP endpoint: the SNI
	// deliberately does not match it. Pinning (or an explicit opt-out) is
	// re-attached below, on the concrete *tls.Config.
	tlsOptions.Insecure = true
	client, err := tls.NewClientWithOptions(tls.ClientOptions{
		Context:       ctx,
		Logger:        logger,
		ServerAddress: serverName,
		Options:       tlsOptions,
		// An empty server_name is legitimate here: some endpoints only present
		// their real certificate when the ClientHello carries no SNI at all.
		AllowEmptyServerName: true,
		// lx: SPEC 060 — masque over a detour is exactly the case that surfaced
		// the PMTU black hole; fragment the ClientHello by default there.
		DialedThroughDetour: tls.DialedThroughDetour(options.DialerOptions),
	})
	if err != nil {
		return nil, err
	}
	stdConfig, err := client.STDConfig()
	if err != nil {
		return nil, E.Cause(err, "masque: h2 requires a standard TLS engine")
	}
	// Re-attach the masque-specific parts. PrepareTLSConfig already resolved
	// whether pinning applies (profile) and whether it was waived
	// (skip_cert_verify); mirroring its result keeps a single source of truth.
	stdConfig.Certificates = prepared.Certificates
	stdConfig.InsecureSkipVerify = prepared.InsecureSkipVerify
	stdConfig.VerifyConnection = prepared.VerifyConnection
	stdConfig.ServerName = prepared.ServerName
	return client, nil
}

// ensureSession returns a live tunnel session, building one lazily if none is
// up (first dial, or after an idle-suspend / tunnel death). Safe under
// concurrent dials — runMu serializes establishment, so only one tunnel is
// ever built per gap.
func (o *Outbound) ensureSession(ctx context.Context) (*session, error) {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	if o.closed {
		return nil, E.New("masque: outbound closed")
	}
	if o.sess != nil {
		return o.sess, nil
	}

	device, err := wireguard.NewDevice(wireguard.DeviceOptions{
		Context: o.ctx,
		Logger:  o.logger,
		MTU:     o.mtu,
		Address: o.prefixes,
	})
	if err != nil {
		return nil, E.Cause(err, "create userspace stack")
	}
	if err = device.Start(); err != nil {
		return nil, E.Cause(err, "start userspace stack")
	}

	// Transport-phase logging: the dial failure mode (UDP left the device vs. no
	// reply came back) is otherwise invisible without a goroutine dump. lx: SPEC 021.
	network := o.effectiveNetwork()
	o.logger.DebugContext(ctx, "masque: establishing ", network, " tunnel to ", o.server, " (sni=", o.tlsConfig.ServerName, ")")
	var closer io.Closer
	var ipConn masque.IpConn
	closer, ipConn, network, err = o.connect(ctx, network)
	if err != nil {
		o.logger.WarnContext(ctx, "masque: ", network, " tunnel to ", o.server, " failed: ", err)
		_ = device.Close()
		return nil, err
	}
	o.logger.InfoContext(ctx, "masque: ", network, " tunnel established to ", o.server)

	sessCtx, cancel := context.WithCancel(o.ctx)
	s := &session{
		device: device,
		closer: closer,
		ipConn: ipConn,
		ctx:    sessCtx,
		cancel: cancel,
	}
	s.markActivity(time.Now().UnixNano())
	o.sess = s

	go o.pumpToTunnel(s)
	go o.pumpFromTunnel(s)
	if o.idleTimeout > 0 {
		go o.idleWatcher(s)
	}

	return s, nil
}

// teardownSession closes the session's tunnel + stack and clears it as current
// (if it still is). Idempotent per session (sync.Once) and generation-guarded:
// tearing down a stale session never disturbs a newer one. Closing ipConn also
// unblocks whichever pump is parked in a blocking read (context cancellation
// alone cannot interrupt ReceiveDatagram / recvCh). lx: SPEC 021 C1/C2.
func (o *Outbound) teardownSession(s *session) {
	s.teardown.Do(func() {
		s.cancel()
		if s.ipConn != nil {
			_ = s.ipConn.Close()
		}
		if s.closer != nil {
			_ = s.closer.Close()
		}
		if s.device != nil {
			_ = s.device.Close()
		}
		o.runMu.Lock()
		if o.sess == s {
			o.sess = nil // next dial rebuilds a fresh tunnel (self-healing)
		}
		o.runMu.Unlock()
	})
}

// idleWatcher suspends the tunnel after idleTimeout of no traffic, freeing the
// gVisor netstack, pumps and QUIC keepalive. The next dial rebuilds it. lx:
// SPEC 021 B1.
func (o *Outbound) idleWatcher(s *session) {
	// Poll at a fraction of the idle window (bounded) so suspend fires promptly
	// without a busy tick.
	interval := o.idleTimeout / 4
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			idleFor := time.Duration(time.Now().UnixNano() - s.activity.Load())
			if idleFor >= o.idleTimeout {
				o.logger.DebugContext(o.ctx, "masque: idle-suspend tunnel after ", idleFor)
				o.teardownSession(s)
				return
			}
		}
	}
}

// lx:begin masque-auto

// connectLegs is the unit-test seam for the two dial legs (they need a live
// endpoint otherwise). nil in production.
type connectLegs struct {
	h3 func(context.Context, time.Duration) (io.Closer, masque.IpConn, error)
	h2 func(context.Context) (io.Closer, masque.IpConn, error)
}

// SPEC 074 — one dial attempt, with the `auto` fallback folded in.
//
// Returns the network the tunnel actually came up on, so the caller logs the
// truth rather than the configured value.
func (o *Outbound) connect(ctx context.Context, network string) (io.Closer, masque.IpConn, string, error) {
	connectH3, connectH2 := o.connectH3WithBudget, o.connectH2
	if o.legsForTest != nil {
		connectH3, connectH2 = o.legsForTest.h3, o.legsForTest.h2
	}
	// Fixed h3 keeps the unbounded handshake; only `auto` needs a cap.
	h3Budget := time.Duration(0)
	if o.autoMode {
		h3Budget = o.autoH3Delay
	}
	if network == "h2" {
		closer, ipConn, err := connectH2(ctx)
		if err == nil && o.autoMode {
			o.rememberNetwork("h2")
		}
		return closer, ipConn, "h2", err
	}
	if !o.autoMode {
		closer, ipConn, err := connectH3(ctx, 0)
		return closer, ipConn, "h3", err
	}
	// h3 first — DETACHED. The caller must never wait for the h3 leg to return:
	// on a wedged path the leg can sit inside third-party cleanup for minutes
	// (quic-go's Transport.Close runs an http2 body Close that parks on the
	// stream's donec; x/net even carries a TODO admitting the wait is unbounded).
	// So the decision to fall back is taken on a wall-clock timer, not on the
	// leg returning; a leg that answers late is drained in the background and a
	// late success is closed. The leg still carries its own handshake budget —
	// on paths where contexts are honoured it yields a clean early error, and
	// the fallback then starts before the timer.
	h3Ctx, h3Cancel := context.WithCancel(ctx)
	type legOutcome struct {
		closer io.Closer
		ipConn masque.IpConn
		err    error
	}
	h3Done := make(chan legOutcome, 1)
	go func() {
		closer, ipConn, err := connectH3(h3Ctx, h3Budget)
		h3Done <- legOutcome{closer, ipConn, err}
	}()
	drainH3 := func() {
		// Consume the leg's eventual result without anybody waiting on it.
		go func() {
			outcome := <-h3Done
			h3Cancel()
			if outcome.err == nil {
				o.logger.Debug("masque: late h3 tunnel to ", o.server, " discarded (h2 already won)")
				_ = outcome.closer.Close()
			}
		}()
	}
	budget := time.NewTimer(o.autoH3Delay)
	defer budget.Stop()
	var (
		h3Err      error
		h3Returned bool // the leg's single result is already consumed
	)
	select {
	case outcome := <-h3Done:
		h3Returned = true
		h3Cancel()
		if outcome.err == nil {
			o.rememberNetwork("h3")
			return outcome.closer, outcome.ipConn, "h3", nil
		}
		if ctx.Err() != nil {
			// The caller gave up (dial cancelled): not an h3 verdict, do not fall back.
			return nil, nil, "h3", outcome.err
		}
		h3Err = outcome.err
		o.logger.InfoContext(ctx, "masque: h3 to ", o.server, " did not come up (", h3Err, "); falling back to h2")
	case <-budget.C:
		// Leg still running — possibly wedged. Abandon it, do not wait.
		h3Err = E.New("h3 handshake exceeded ", o.autoH3Delay)
		o.logger.InfoContext(ctx, "masque: ", h3Err, " to ", o.server, "; falling back to h2 (h3 attempt abandoned)")
		h3Cancel()
	case <-ctx.Done():
		h3Cancel()
		drainH3()
		return nil, nil, "h3", ctx.Err()
	}

	h2Closer, h2Conn, h2Err := connectH2(ctx)
	if h2Err == nil {
		drainH3()
		o.rememberNetwork("h2")
		return h2Closer, h2Conn, "h2", nil
	}
	// h2 failed. If the h3 leg is still out there it may yet succeed (a slow but
	// alive path where the budget was simply too tight) — that result is worth
	// waiting for now that there is nothing better to offer. (If the leg already
	// returned, its single result is consumed — there is nothing to wait for.)
	if !h3Returned {
		select {
		case outcome := <-h3Done:
			h3Cancel()
			if outcome.err == nil {
				o.rememberNetwork("h3")
				return outcome.closer, outcome.ipConn, "h3", nil
			}
			h3Err = outcome.err
		case <-ctx.Done():
			drainH3()
		}
	}
	// Report both legs: "h2 failed" alone would hide why we left h3.
	return nil, nil, "h2", E.Errors(E.Cause(h3Err, "h3"), E.Cause(h2Err, "h2"))
}

// effectiveNetwork is the leg to try first: the configured one, or — in `auto` —
// whichever leg last succeeded, so only the first tunnel pays the h3 timeout.
func (o *Outbound) effectiveNetwork() string {
	if !o.autoMode {
		return o.network
	}
	if remembered := o.autoNetwork.Load(); remembered != nil {
		return *remembered
	}
	return "h3"
}

func (o *Outbound) rememberNetwork(network string) {
	if previous := o.autoNetwork.Load(); previous != nil && *previous == network {
		return
	}
	o.autoNetwork.Store(&network)
}

// lx:end masque-auto

func (o *Outbound) connectH3(ctx context.Context) (io.Closer, masque.IpConn, error) {
	return o.connectH3WithBudget(ctx, 0)
}

// connectH3WithBudget dials h3, optionally capping the QUIC handshake.
//
// lx: SPEC 074 — the budget starts once the UDP socket is up, deliberately: the
// socket dial can itself be slow (it may run through a detour / a whole chain of
// hops), and spending the handshake budget on it would make `auto` fall back for
// the wrong reason — or, worse, never reach the fallback at all. What `auto`
// needs to bound is the handshake: the failure mode is a ClientHello that leaves
// and never gets an answer, which produces no error of its own.
func (o *Outbound) connectH3WithBudget(ctx context.Context, handshakeBudget time.Duration) (io.Closer, masque.IpConn, error) {
	udpConn, err := o.dialer.DialContext(ctx, N.NetworkUDP, o.server)
	if err != nil {
		return nil, nil, E.Cause(err, "dial udp")
	}
	// UDP socket is up; the QUIC handshake is the next (and most commonly stuck)
	// step — a hang here means our ClientHello left but no ServerHello came back
	// (e.g. inbound UDP:443 filtered), distinct from the socket failing above.
	o.logger.DebugContext(ctx, "masque: udp socket to ", o.server, " up, starting QUIC handshake")
	handshakeCtx := ctx
	if handshakeBudget > 0 {
		var cancel context.CancelFunc
		handshakeCtx, cancel = context.WithTimeout(ctx, handshakeBudget)
		defer cancel()
	}
	quicConn, err := quic.DialEarly(handshakeCtx, bufio.NewUnbindPacketConn(udpConn), udpConn.RemoteAddr(), o.tlsConfig, o.quicConfig)
	if err != nil {
		_ = udpConn.Close()
		return nil, nil, E.Cause(err, "dial quic")
	}
	tr, ipConn, err := masque.ConnectTunnelH3(ctx, o.profile, quicConn, o.uri)
	if err != nil {
		_ = udpConn.Close()
		return nil, nil, err
	}
	return &h3Closer{transport: tr, rawConn: udpConn}, ipConn, nil
}

func (o *Outbound) connectH2(ctx context.Context) (io.Closer, masque.IpConn, error) {
	c, err := o.dialer.DialContext(ctx, N.NetworkTCP, o.server)
	if err != nil {
		return nil, nil, E.Cause(err, "dial tcp")
	}
	// lx: SPEC 021 Ф4 — handshake through the shared TLS client, so the h2 path
	// gets ClientHello fragmentation (and the rest of common/tls) instead of a
	// bare crypto/tls.Client. Pinning rides along inside h2TLSClient.
	tlsConn, err := tls.ClientHandshake(ctx, c, o.h2TLSClient)
	if err != nil {
		_ = c.Close()
		return nil, nil, E.Cause(err, "tls handshake")
	}
	return masque.ConnectTunnelH2(ctx, o.profile, tlsConn, o.uri)
}

// pumpToTunnel reads outgoing IP packets from the userspace stack and writes
// them into the MASQUE tunnel. On any exit it tears the session down, which
// unblocks the paired pump and lets the next dial rebuild the tunnel.
func (o *Outbound) pumpToTunnel(s *session) {
	defer o.teardownSession(s)
	device := s.device
	batch := device.BatchSize()
	bufs := make([][]byte, batch)
	sizes := make([]int, batch)
	for i := range bufs {
		bufs[i] = make([]byte, int(o.mtu)+80)
	}
	for s.ctx.Err() == nil {
		count, err := device.Read(bufs, sizes, 0)
		if err != nil {
			return
		}
		s.markActivity(time.Now().UnixNano())
		for i := 0; i < count; i++ {
			icmp, werr := s.ipConn.WritePacket(bufs[i][:sizes[i]])
			if werr != nil {
				return
			}
			if len(icmp) > 0 {
				_, _ = device.Write([][]byte{icmp}, 0)
			}
		}
	}
}

// pumpFromTunnel reads incoming IP packets from the MASQUE tunnel and injects
// them into the userspace stack.
func (o *Outbound) pumpFromTunnel(s *session) {
	defer o.teardownSession(s)
	for s.ctx.Err() == nil {
		packet, err := s.ipConn.ReadPacket()
		if err != nil {
			return
		}
		s.markActivity(time.Now().UnixNano())
		if _, err = s.device.Write([][]byte{packet}, 0); err != nil {
			return
		}
	}
}

func (o *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s, err := o.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	// The userspace stack operates at L3 and cannot resolve domains; resolve
	// here (as the WireGuard endpoint does) and dial the resulting IPs.
	if destination.IsDomain() {
		addresses, err := o.lookup(ctx, destination.Fqdn)
		if err != nil {
			return nil, err
		}
		return N.DialSerial(ctx, s.device, network, destination, addresses)
	}
	if !destination.Addr.IsValid() {
		return nil, E.New("invalid destination: ", destination)
	}
	return s.device.DialContext(ctx, network, destination)
}

func (o *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s, err := o.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	if destination.IsDomain() {
		addresses, err := o.lookup(ctx, destination.Fqdn)
		if err != nil {
			return nil, err
		}
		packetConn, destinationAddress, err := N.ListenSerial(ctx, s.device, destination, addresses)
		if err != nil {
			return nil, err
		}
		if destinationAddress.IsValid() && destination != M.SocksaddrFrom(destinationAddress, destination.Port) {
			return bufio.NewNATPacketConn(bufio.NewPacketConn(packetConn), M.SocksaddrFrom(destinationAddress, destination.Port), destination), nil
		}
		return packetConn, nil
	}
	if !destination.Addr.IsValid() {
		return nil, E.New("invalid destination: ", destination)
	}
	return s.device.ListenPacket(ctx, destination)
}

func (o *Outbound) lookup(ctx context.Context, domain string) ([]netip.Addr, error) {
	if o.dnsRouter == nil {
		return nil, E.New("masque: no DNS router available to resolve ", domain)
	}
	return o.dnsRouter.Lookup(ctx, domain, adapter.DNSQueryOptions{})
}

func (o *Outbound) Close() error {
	o.runMu.Lock()
	if o.closed {
		o.runMu.Unlock()
		return nil
	}
	o.closed = true
	s := o.sess
	o.sess = nil
	o.runMu.Unlock()
	if s != nil {
		o.teardownSession(s)
	}
	return nil
}

type h3Closer struct {
	transport io.Closer
	rawConn   net.Conn
}

func (c *h3Closer) Close() error {
	if c.transport != nil {
		_ = c.transport.Close()
	}
	if c.rawConn != nil {
		_ = c.rawConn.Close()
	}
	return nil
}

func parsePrefixes(ip, ipv6 string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, 2)
	if ip != "" {
		if !strings.Contains(ip, "/") {
			ip += "/32"
		}
		prefix, err := netip.ParsePrefix(ip)
		if err != nil {
			return nil, E.Cause(err, "parse ip")
		}
		prefixes = append(prefixes, prefix)
	}
	if ipv6 != "" {
		if !strings.Contains(ipv6, "/") {
			ipv6 += "/128"
		}
		prefix, err := netip.ParsePrefix(ipv6)
		if err != nil {
			return nil, E.Cause(err, "parse ipv6")
		}
		prefixes = append(prefixes, prefix)
	}
	if len(prefixes) == 0 {
		return nil, E.New("masque: at least one of ip/ipv6 is required")
	}
	return prefixes, nil
}

func parseECPrivateKey(b64 string) (*ecdsa.PrivateKey, error) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	return x509.ParseECPrivateKey(der)
}

func parseECPublicKey(b64 string) (*ecdsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, E.New("public_key is not an ECDSA key")
	}
	return ecPub, nil
}
