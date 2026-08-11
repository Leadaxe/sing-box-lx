package tls

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"strings"

	"github.com/sagernet/sing-box/common/badtls"
	"github.com/sagernet/sing-box/common/tlsspoof"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
)

var errMissingServerName = E.New("missing server_name or insecure=true")

func parseTLSSpoofOptions(serverName string, options option.OutboundTLSOptions) (string, tlsspoof.Method, error) {
	spoof, method, err := tlsspoof.ParseOptions(options.Spoof, options.SpoofMethod)
	if err != nil {
		return "", 0, err
	}
	if spoof == "" {
		return "", 0, nil
	}
	if options.DisableSNI || serverName == "" || M.ParseAddr(serverName).IsValid() {
		return "", 0, E.New("`spoof` requires TLS ClientHello with SNI")
	}
	if strings.EqualFold(spoof, serverName) {
		return "", 0, E.New("`spoof` must differ from `server_name`")
	}
	return spoof, method, nil
}

func applyTLSSpoof(conn net.Conn, spoof string, method tlsspoof.Method) (net.Conn, error) {
	if spoof == "" {
		return conn, nil
	}
	return tlsspoof.NewConn(conn, method, spoof)
}

func NewDialerFromOptions(ctx context.Context, logger logger.ContextLogger, dialer N.Dialer, serverAddress string, options option.OutboundTLSOptions) (N.Dialer, error) {
	return NewDialerFromClientOptions(dialer, ClientOptions{
		Context:       ctx,
		Logger:        logger,
		ServerAddress: serverAddress,
		Options:       options,
	})
}

// NewDialerFromClientOptions is NewDialerFromOptions for callers that need to
// set the extra ClientOptions fields (notably DialedThroughDetour, SPEC 060).
// lx.
func NewDialerFromClientOptions(dialer N.Dialer, clientOptions ClientOptions) (N.Dialer, error) {
	if !clientOptions.Options.Enabled {
		return dialer, nil
	}
	config, err := NewClientWithOptions(clientOptions)
	if err != nil {
		return nil, err
	}
	return NewDialer(dialer, config), nil
}

func NewClient(ctx context.Context, logger logger.ContextLogger, serverAddress string, options option.OutboundTLSOptions) (Config, error) {
	return NewClientWithOptions(ClientOptions{
		Context:       ctx,
		Logger:        logger,
		ServerAddress: serverAddress,
		Options:       options,
	})
}

type ClientOptions struct {
	Context              context.Context
	Logger               logger.ContextLogger
	ServerAddress        string
	Options              option.OutboundTLSOptions
	AllowEmptyServerName bool
	KTLSCompatible       bool
	// DialedThroughDetour marks that this client's TCP leg is carried by another
	// outbound rather than the local network stack. It turns on ClientHello
	// record fragmentation by default — see applyDetourFragmentDefault.
	// lx: SPEC 060.
	DialedThroughDetour bool
}

// DialedThroughDetour reports whether an outbound with these dialer options
// sends its bytes through another outbound instead of the local network stack.
//
// Only an explicit `detour` is visible here, which is all that is needed: the
// other way a dialer routes through another outbound, dialer.Options
// .DefaultOutbound, is set solely by common/httpclient (the internal client for
// rule-sets and subscriptions). Proxy outbounds never take that path — for them
// "through someone else's server" always arrives from the config as `detour`.
//
// Kept here so the condition has exactly one definition: every TLS outbound
// passes its DialerOptions through this rather than testing `Detour != ""`
// itself. lx: SPEC 060.
func DialedThroughDetour(options option.DialerOptions) bool {
	return options.Detour != ""
}

// applyDetourFragmentDefault enables record fragmentation when the connection is
// carried by another outbound and the config asked for no fragmentation itself.
//
// Why: under a detour the leg forwards our ClientHello from its own address. If
// the PMTU beyond that leg is smaller than the ClientHello, the packet is
// dropped and the ICMP "fragmentation needed" never reaches us — the handshake
// simply hangs for ~15s and dies as "EOF". Measured against live nodes: a 1488 B
// ClientHello passes where 1502 B disappears, and the threshold belongs to the
// path behind the leg, not to any protocol. Splitting the first TLS record puts
// every piece under it.
//
// Cost is confined to the handshake: tlsfragment only rewrites the first record,
// so steady-state traffic is untouched (0.1s vs a 12s timeout in measurement).
//
// An explicit `fragment` or `record_fragment` in the config always wins — this
// only fills in the case where the user asked for nothing. lx: SPEC 060.
func applyDetourFragmentDefault(options *ClientOptions) {
	if !options.DialedThroughDetour {
		return
	}
	if options.Options.Fragment || options.Options.RecordFragment {
		return // user made a choice; do not second-guess it
	}
	options.Options.RecordFragment = true
}

func NewClientWithOptions(options ClientOptions) (Config, error) {
	if !options.Options.Enabled {
		return nil, nil
	}
	// lx: SPEC 060 — before any engine sees the options, so REALITY and uTLS
	// clients get the same default as the standard one.
	applyDetourFragmentDefault(&options)
	if !options.KTLSCompatible {
		if options.Options.KernelTx {
			options.Logger.Warn("enabling kTLS TX in current scenarios will definitely reduce performance, please checkout https://sing-box.sagernet.org/configuration/shared/tls/#kernel_tx")
		}
	}
	if options.Options.KernelRx {
		options.Logger.Warn("enabling kTLS RX will definitely reduce performance, please checkout https://sing-box.sagernet.org/configuration/shared/tls/#kernel_rx")
	}
	switch options.Options.Engine {
	case "", C.TLSEngineGo:
	case C.TLSEngineApple:
		return newAppleClient(options.Context, options.Logger, options.ServerAddress, options.Options, options.AllowEmptyServerName)
	case C.TLSEngineWindows:
		return newWindowsClient(options.Context, options.Logger, options.ServerAddress, options.Options, options.AllowEmptyServerName)
	default:
		return nil, E.New("unknown tls engine: ", options.Options.Engine)
	}
	if options.Options.Reality != nil && options.Options.Reality.Enabled {
		return newRealityClient(options.Context, options.Logger, options.ServerAddress, options.Options, options.AllowEmptyServerName)
	} else if options.Options.UTLS != nil && options.Options.UTLS.Enabled {
		return newUTLSClient(options.Context, options.Logger, options.ServerAddress, options.Options, options.AllowEmptyServerName)
	}
	return newSTDClient(options.Context, options.Logger, options.ServerAddress, options.Options, options.AllowEmptyServerName)
}

func ClientHandshake(ctx context.Context, conn net.Conn, config Config) (Conn, error) {
	tlsConn, err := aTLS.ClientHandshake(ctx, conn, config)
	if err != nil {
		return nil, err
	}
	readWaitConn, err := badtls.NewReadWaitConn(tlsConn)
	if err == nil {
		return readWaitConn, nil
	} else if err != os.ErrInvalid {
		return nil, err
	}
	return tlsConn, nil
}

type Dialer interface {
	N.Dialer
	DialTLSContext(ctx context.Context, destination M.Socksaddr) (Conn, error)
}

type defaultDialer struct {
	dialer N.Dialer
	config Config
}

func NewDialer(dialer N.Dialer, config Config) Dialer {
	return &defaultDialer{dialer, config}
}

func (d *defaultDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if N.NetworkName(network) != N.NetworkTCP {
		return nil, os.ErrInvalid
	}
	return d.DialTLSContext(ctx, destination)
}

func (d *defaultDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

func (d *defaultDialer) DialTLSContext(ctx context.Context, destination M.Socksaddr) (Conn, error) {
	return d.dialContext(ctx, destination, true)
}

func (d *defaultDialer) dialContext(ctx context.Context, destination M.Socksaddr, echRetry bool) (Conn, error) {
	conn, err := d.dialer.DialContext(ctx, N.NetworkTCP, destination)
	if err != nil {
		return nil, err
	}
	tlsConn, err := aTLS.ClientHandshake(ctx, conn, d.config)
	if err != nil {
		conn.Close()
		var echErr *tls.ECHRejectionError
		if echRetry && errors.As(err, &echErr) && len(echErr.RetryConfigList) > 0 {
			if echConfig, isECH := d.config.(ECHCapableConfig); isECH {
				echConfig.SetECHConfigList(echErr.RetryConfigList)
				return d.dialContext(ctx, destination, false)
			}
		}
		return nil, err
	}
	return tlsConn, nil
}

func (d *defaultDialer) Upstream() any {
	return d.dialer
}
