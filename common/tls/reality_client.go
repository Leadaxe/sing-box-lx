//go:build with_utls

package tls

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	mRand "math/rand"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unsafe"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/debug"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/common/ntp"
	aTLS "github.com/sagernet/sing/common/tls"

	utls "github.com/metacubex/utls"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/net/http2"
)

var _ ConfigCompat = (*RealityClientConfig)(nil)

type RealityClientConfig struct {
	ctx       context.Context
	uClient   *UTLSClientConfig
	publicKey []byte
	shortID   [8]byte
	keyShare  string // lx: SPEC 089 — C.RealityKeyShare*
}

func NewRealityClient(ctx context.Context, logger logger.ContextLogger, serverAddress string, options option.OutboundTLSOptions) (Config, error) {
	return newRealityClient(ctx, logger, serverAddress, options, false)
}

func newRealityClient(ctx context.Context, logger logger.ContextLogger, serverAddress string, options option.OutboundTLSOptions, allowEmptyServerName bool) (Config, error) {
	if options.UTLS == nil || !options.UTLS.Enabled {
		return nil, E.New("uTLS is required by reality client")
	}
	if options.Spoof != "" || options.SpoofMethod != "" {
		return nil, E.New("spoof is unsupported in reality")
	}

	uClient, err := newUTLSClient(ctx, logger, serverAddress, options, allowEmptyServerName)
	if err != nil {
		return nil, err
	}

	publicKey, err := base64.RawURLEncoding.DecodeString(options.Reality.PublicKey)
	if err != nil {
		return nil, E.Cause(err, "decode public_key")
	}
	if len(publicKey) != 32 {
		return nil, E.New("invalid public_key")
	}
	// lx: SPEC 090 — hex.Decode writes len(src)/2 bytes without checking dst; the post-decode check never runs
	if len(options.Reality.ShortID) > 16 {
		return nil, E.New("invalid short_id")
	}
	var shortID [8]byte
	_, err = hex.Decode(shortID[:], []byte(options.Reality.ShortID))
	if err != nil {
		return nil, E.Cause(err, "decode short_id")
	}
	// lx: SPEC 089 — reject typos at load time; "classic" must not silently
	// become the default.
	switch options.Reality.KeyShare {
	case C.RealityKeyShareDefault, C.RealityKeyShareHybrid, C.RealityKeyShareClassical:
	default:
		return nil, E.New("unknown reality key_share: ", options.Reality.KeyShare, " (expected \"hybrid\" or \"classical\")")
	}

	var config Config = &RealityClientConfig{ctx, uClient.(*UTLSClientConfig), publicKey, shortID, options.Reality.KeyShare}
	if options.KernelRx || options.KernelTx {
		if !C.IsLinux {
			return nil, E.New("kTLS is only supported on Linux")
		}
		config = &KTLSClientConfig{
			Config:   config,
			logger:   logger,
			kernelTx: options.KernelTx,
			kernelRx: options.KernelRx,
		}
	}
	return config, nil
}

func (e *RealityClientConfig) ServerName() string {
	return e.uClient.ServerName()
}

func (e *RealityClientConfig) SetServerName(serverName string) {
	e.uClient.SetServerName(serverName)
}

func (e *RealityClientConfig) NextProtos() []string {
	return e.uClient.NextProtos()
}

func (e *RealityClientConfig) SetNextProtos(nextProto []string) {
	e.uClient.SetNextProtos(nextProto)
}

func (e *RealityClientConfig) HandshakeTimeout() time.Duration {
	return e.uClient.HandshakeTimeout()
}

func (e *RealityClientConfig) SetHandshakeTimeout(timeout time.Duration) {
	e.uClient.SetHandshakeTimeout(timeout)
}

func (e *RealityClientConfig) STDConfig() (*STDConfig, error) {
	return nil, E.New("unsupported usage for reality")
}

func (e *RealityClientConfig) Client(conn net.Conn) (Conn, error) {
	return ClientHandshake(context.Background(), conn, e)
}

func (e *RealityClientConfig) ClientHandshake(ctx context.Context, conn net.Conn) (aTLS.Conn, error) {
	uConn, verifier, err := e.prepareClientHello(conn)
	if err != nil {
		return nil, err
	}

	err = uConn.HandshakeContext(ctx)
	if err != nil {
		return nil, err
	}

	if debug.Enabled {
		fmt.Printf("REALITY Conn.Verified: %v\n", verifier.verified)
	}

	if !verifier.verified {
		go realityClientFallback(e.ctx, uConn, e.uClient.ServerName(), e.uClient.id)
		return nil, E.New("reality verification failed")
	}

	return &realityClientConnWrapper{uConn}, nil
}

// prepareClientHello builds the REALITY ClientHello on conn without sending it:
// the utls UConn with the fingerprint's hello, the key_share policy applied
// (SPEC 089), and the session id sealed with the AuthKey. Split from
// ClientHandshake so tests can inspect what would go on the wire. lx: SPEC 088/089.
func (e *RealityClientConfig) prepareClientHello(conn net.Conn) (*utls.UConn, *realityVerifier, error) {
	verifier := &realityVerifier{
		serverName: e.uClient.ServerName(),
	}
	uConfig := e.uClient.config.Clone()
	uConfig.InsecureSkipVerify = true
	uConfig.SessionTicketsDisabled = true
	uConfig.VerifyPeerCertificate = verifier.VerifyPeerCertificate
	// lx: SPEC 088 — the same `fragment` / `record_fragment` wrappers the plain
	// uTLS client gets; upstream builds the UConn on the bare conn, so those
	// flags (and the SPEC 060 detour default) were written to the config and
	// ignored here.
	conn, err := e.uClient.wrapClientConn(conn)
	if err != nil {
		return nil, nil, err
	}
	uConn := utls.UClient(conn, uConfig, e.uClient.id)
	verifier.UConn = uConn
	// lx: SPEC 083 — апстрим здесь вырезал X25519MLKEM768 из supported_groups
	// и key_share (костыль под utls v1.7.2, где Ecdhe гибрида и чистого X25519
	// не различались). XTLS/REALITY@8cdf7bf (Xray ≥ v26.9.8) отвергает
	// ClientHello без гибридного шара — молча проксирует на dest, у нас это
	// «reality verification failed». metacubex/utls ≥ 1.8 кладёт ключи
	// раздельно (Ecdhe / MlkemEcdhe), фильтр больше не нужен: отпечаток
	// шлёт то, что заложено в его спеке (Chrome — GREASE, MLKEM, X25519).
	err = uConn.BuildHandshakeState()
	if err != nil {
		return nil, nil, err
	}
	// lx: SPEC 089 — `reality.key_share` поверх отпечатка. `classical` — тот
	// самый апстримный фильтр, но по выбору узла: гибридный ClientHello
	// (~1.9 КБ, два TCP-сегмента) кое-где теряется в сети, а классический
	// (~0.5 КБ) проходит — ценой совместимости с Xray ≥ v26.9.8. `hybrid` —
	// проверка, что отпечаток шар несёт: без неё узел на `edge` умирает как
	// «reality verification failed», неотличимо от чужого ключа.
	switch e.keyShare {
	case C.RealityKeyShareClassical:
		for _, extension := range uConn.Extensions {
			if curves, isCurves := extension.(*utls.SupportedCurvesExtension); isCurves {
				curves.Curves = common.Filter(curves.Curves, func(curveID utls.CurveID) bool {
					return curveID != utls.X25519MLKEM768
				})
			}
			if keyShares, isKeyShares := extension.(*utls.KeyShareExtension); isKeyShares {
				keyShares.KeyShares = common.Filter(keyShares.KeyShares, func(share utls.KeyShare) bool {
					return share.Group != utls.X25519MLKEM768
				})
			}
		}
		// Second build re-marshals the hello from the filtered extensions
		// (the preset is applied once; see utls buildHandshakeState).
		err = uConn.BuildHandshakeState()
		if err != nil {
			return nil, nil, err
		}
	case C.RealityKeyShareHybrid:
		if !realityHelloCarriesHybridShare(uConn) {
			return nil, nil, E.New("reality key_share \"hybrid\": fingerprint ", e.uClient.id.Client, " ", e.uClient.id.Version, " carries no X25519MLKEM768 key share")
		}
	}

	if len(uConfig.NextProtos) > 0 {
		for _, extension := range uConn.Extensions {
			if alpnExtension, isALPN := extension.(*utls.ALPNExtension); isALPN {
				alpnExtension.AlpnProtocols = uConfig.NextProtos
				break
			}
		}
	}

	hello := uConn.HandshakeState.Hello
	hello.SessionId = make([]byte, 32)
	copy(hello.Raw[39:], hello.SessionId)

	var nowTime time.Time
	if uConfig.Time != nil {
		nowTime = uConfig.Time()
	} else {
		nowTime = time.Now()
	}
	binary.BigEndian.PutUint64(hello.SessionId, uint64(nowTime.Unix()))

	// lx: SPEC 053 — Xray v26.7.11 (XTLS/Xray-core@af7eb68) включил
	// minClientVer=26.3.27 по умолчанию. Апстримный `1, 8, 1` ниже порога:
	// сервер не отдаёт ошибку, а молча проксирует на камуфляжный dest.
	// Объявляем ровно минимум — сравнение там `>=`, выше не нужно.
	hello.SessionId[0] = 26
	hello.SessionId[1] = 3
	hello.SessionId[2] = 27
	binary.BigEndian.PutUint32(hello.SessionId[4:], uint32(time.Now().Unix()))
	copy(hello.SessionId[8:], e.shortID[:])
	if debug.Enabled {
		fmt.Printf("REALITY hello.sessionId[:16]: %v\n", hello.SessionId[:16])
	}
	publicKey, err := ecdh.X25519().NewPublicKey(e.publicKey)
	if err != nil {
		return nil, nil, err
	}
	keyShareKeys := uConn.HandshakeState.State13.KeyShareKeys
	if keyShareKeys == nil {
		return nil, nil, E.New("nil KeyShareKeys")
	}
	// lx: SPEC 083 — AuthKey считается по тому же шару, что берёт сервер:
	// чистый X25519, если он есть в ClientHello, иначе X25519-часть
	// X25519MLKEM768 (порядок выбора XTLS/REALITY@8cdf7bf, как у Xray-клиента).
	ecdheKey := keyShareKeys.Ecdhe
	if ecdheKey == nil {
		ecdheKey = keyShareKeys.MlkemEcdhe
	}
	if ecdheKey == nil {
		return nil, nil, E.New("nil ecdheKey")
	}
	authKey, err := ecdheKey.ECDH(publicKey)
	if err != nil {
		return nil, nil, err
	}
	if authKey == nil {
		return nil, nil, E.New("nil auth_key")
	}
	verifier.authKey = authKey
	_, err = hkdf.New(sha256.New, authKey, hello.Random[:20], []byte("REALITY")).Read(authKey)
	if err != nil {
		return nil, nil, err
	}
	aesBlock, _ := aes.NewCipher(authKey)
	aesGcmCipher, _ := cipher.NewGCM(aesBlock)
	aesGcmCipher.Seal(hello.SessionId[:0], hello.Random[20:], hello.SessionId[:16], hello.Raw)
	copy(hello.Raw[39:], hello.SessionId)
	if debug.Enabled {
		fmt.Printf("REALITY hello.sessionId: %v\n", hello.SessionId)
		fmt.Printf("REALITY uConn.AuthKey: %v\n", authKey)
	}
	return uConn, verifier, nil
}

// realityHelloCarriesHybridShare reports whether the built ClientHello offers
// an X25519MLKEM768 key share — the wire fact the REALITY server checks. lx: SPEC 089.
func realityHelloCarriesHybridShare(uConn *utls.UConn) bool {
	for _, extension := range uConn.Extensions {
		if keyShares, isKeyShares := extension.(*utls.KeyShareExtension); isKeyShares {
			for _, share := range keyShares.KeyShares {
				if share.Group == utls.X25519MLKEM768 {
					return true
				}
			}
		}
	}
	return false
}

func realityClientFallback(ctx context.Context, uConn net.Conn, serverName string, fingerprint utls.ClientHelloID) {
	defer uConn.Close()
	client := &http.Client{
		Transport: &http2.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string, config *tls.Config) (net.Conn, error) {
				return uConn, nil
			},
			TLSClientConfig: &tls.Config{
				Time:    ntp.TimeFuncFromContext(ctx),
				RootCAs: adapter.RootPoolFromContext(ctx),
			},
		},
	}
	request, _ := http.NewRequest("GET", "https://"+serverName, nil)
	request.Header.Set("User-Agent", fingerprint.Client)
	request.AddCookie(&http.Cookie{Name: "padding", Value: strings.Repeat("0", mRand.Intn(32)+30)})
	response, err := client.Do(request)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
}

func (e *RealityClientConfig) Clone() Config {
	return &RealityClientConfig{
		e.ctx,
		e.uClient.Clone().(*UTLSClientConfig),
		e.publicKey,
		e.shortID,
		e.keyShare,
	}
}

type realityVerifier struct {
	*utls.UConn
	serverName string
	authKey    []byte
	verified   bool
}

func (c *realityVerifier) VerifyPeerCertificate(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	p, _ := reflect.TypeFor[utls.Conn]().FieldByName("peerCertificates")
	certs := *(*([]*x509.Certificate))(unsafe.Add(unsafe.Pointer(c.Conn), p.Offset))
	if pub, ok := certs[0].PublicKey.(ed25519.PublicKey); ok {
		h := hmac.New(sha512.New, c.authKey)
		h.Write(pub)
		if bytes.Equal(h.Sum(nil), certs[0].Signature) {
			c.verified = true
			return nil
		}
	}
	opts := x509.VerifyOptions{
		DNSName:       c.serverName,
		Intermediates: x509.NewCertPool(),
	}
	for _, cert := range certs[1:] {
		opts.Intermediates.AddCert(cert)
	}
	if _, err := certs[0].Verify(opts); err != nil {
		return err
	}
	return nil
}

type realityClientConnWrapper struct {
	*utls.UConn
}

func (c *realityClientConnWrapper) ConnectionState() tls.ConnectionState {
	state := c.Conn.ConnectionState()
	//nolint:staticcheck
	return tls.ConnectionState{
		Version:                     state.Version,
		HandshakeComplete:           state.HandshakeComplete,
		DidResume:                   state.DidResume,
		CipherSuite:                 state.CipherSuite,
		NegotiatedProtocol:          state.NegotiatedProtocol,
		NegotiatedProtocolIsMutual:  state.NegotiatedProtocolIsMutual,
		ServerName:                  state.ServerName,
		PeerCertificates:            state.PeerCertificates,
		VerifiedChains:              state.VerifiedChains,
		SignedCertificateTimestamps: state.SignedCertificateTimestamps,
		OCSPResponse:                state.OCSPResponse,
		TLSUnique:                   state.TLSUnique,
	}
}

func (c *realityClientConnWrapper) Upstream() any {
	return c.UConn
}

// Due to low implementation quality, the reality server intercepted half close and caused memory leaks.
// We fixed it by calling Close() directly.
func (c *realityClientConnWrapper) CloseWrite() error {
	return c.Close()
}

func (c *realityClientConnWrapper) ReaderReplaceable() bool {
	return true
}

func (c *realityClientConnWrapper) WriterReplaceable() bool {
	return false
}
