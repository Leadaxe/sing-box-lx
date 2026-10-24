//go:build with_utls && with_lx_utls_custom

package tls

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/stretchr/testify/require"
)

const customHelloTLSJSON = `{
  "enabled": true,
  "server_name": "example.com",
  "insecure": true,
  "utls": {
    "enabled": true,
    "fingerprint": "custom",
    "client_hello": {
      "cipher_suites": ["TLS_AES_256_GCM_SHA384", "TLS_AES_128_GCM_SHA256"],
      "compression_methods": ["NULL"],
      "extensions": [
        {"name": "server_name"},
        {"name": "supported_groups", "named_group_list": ["x25519", "secp256r1"]},
        {"name": "signature_algorithms", "supported_signature_algorithms": ["ecdsa_secp256r1_sha256", "rsa_pss_rsae_sha256"]},
        {"name": "key_share", "client_shares": [{"group": "x25519"}]},
        {"name": "supported_versions", "versions": ["TLS 1.3", "TLS 1.2"]},
        {"name": "psk_key_exchange_modes", "ke_modes": ["psk_dhe_ke"]},
		{"name": "generic", "id": 65000, "data_base64": "AQID"},
        {"name": "application_layer_protocol_negotiation", "protocol_name_list": ["http/1.1"]}
      ]
    }
  }
}`

type recordingTLSConn struct {
	net.Conn
	writes []byte
}

func (c *recordingTLSConn) Write(p []byte) (int, error) {
	c.writes = append(c.writes, p...)
	return c.Conn.Write(p)
}

func TestCustomUTLSClientHelloOnWire(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	var opts option.OutboundTLSOptions
	require.NoError(t, json.Unmarshal([]byte(customHelloTLSJSON), &opts))
	config, err := NewUTLSClient(context.Background(), nil, "example.com", opts)
	require.NoError(t, err)
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	recorded := &recordingTLSConn{Conn: conn}
	client, err := config.Client(recorded)
	require.NoError(t, err)
	require.NoError(t, client.HandshakeContext(context.Background()))
	require.Equal(t, uint16(0x0304), client.ConnectionState().Version)

	ciphers, extensions := parseClientHelloOrder(t, recorded.writes)
	require.Equal(t, []uint16{0x1302, 0x1301}, ciphers)
	require.Equal(t, []uint16{0, 10, 13, 51, 43, 45, 65000, 16}, extensions)
	require.True(t, bytes.Contains(recorded.writes, []byte{0xfd, 0xe8, 0, 3, 1, 2, 3}))
}

func TestCustomUTLSClientHelloInvalidConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		json string
	}{
		{"missing spec", `{"utls":{"enabled":true,"fingerprint":"custom"}}`},
		{"wrong profile", `{"utls":{"enabled":true,"fingerprint":"chrome","client_hello":{}}}`},
		{"bad cipher", `{"utls":{"enabled":true,"fingerprint":"custom","client_hello":{"cipher_suites":["not-a-cipher"],"compression_methods":["NULL"],"extensions":[]}}}`},
		{"missing generic id", `{"utls":{"enabled":true,"fingerprint":"custom","client_hello":{"cipher_suites":["TLS_AES_128_GCM_SHA256"],"compression_methods":["NULL"],"extensions":[{"name":"generic","data_base64":"AQID"}]}}}`},
		{"bad generic payload", `{"utls":{"enabled":true,"fingerprint":"custom","client_hello":{"cipher_suites":["TLS_AES_128_GCM_SHA256"],"compression_methods":["NULL"],"extensions":[{"name":"generic","id":65000,"data_base64":"not base64"}]}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var opts option.OutboundTLSOptions
			require.NoError(t, json.Unmarshal([]byte(tc.json), &opts))
			opts.Insecure = true
			_, err := NewUTLSClient(context.Background(), nil, "example.com", opts)
			require.Error(t, err)
		})
	}
}

func TestCustomUTLSClientHelloRealityWritesHello(t *testing.T) {
	var opts option.OutboundTLSOptions
	require.NoError(t, json.Unmarshal([]byte(customHelloTLSJSON), &opts))
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	opts.Reality = &option.OutboundRealityOptions{Enabled: true, PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())}
	config, err := NewRealityClient(context.Background(), nil, "example.com", opts)
	require.NoError(t, err)
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	require.NoError(t, server.SetReadDeadline(time.Now().Add(2*time.Second)))
	result := make(chan error, 1)
	go func() { _, err := config.Client(client); result <- err }()
	header := make([]byte, 5)
	_, err = io.ReadFull(server, header)
	require.NoError(t, err)
	record := make([]byte, 5+int(binary.BigEndian.Uint16(header[3:5])))
	copy(record, header)
	_, err = io.ReadFull(server, record[5:])
	require.NoError(t, err)
	ciphers, extensions := parseClientHelloOrder(t, record)
	require.Equal(t, []uint16{0x1302, 0x1301}, ciphers)
	require.Equal(t, []uint16{0, 10, 13, 51, 43, 45, 65000, 16}, extensions)
	server.Close()
	<-result
}

func parseClientHelloOrder(t *testing.T, data []byte) ([]uint16, []uint16) {
	t.Helper()
	require.GreaterOrEqual(t, len(data), 5)
	require.Equal(t, byte(22), data[0])
	recordLen := int(binary.BigEndian.Uint16(data[3:5]))
	require.GreaterOrEqual(t, len(data), 5+recordLen)
	p := data[5 : 5+recordLen]
	require.Equal(t, byte(1), p[0])
	p = p[4+2+32:]
	sessionLen := int(p[0])
	p = p[1+sessionLen:]
	cipherLen := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2:]
	ciphers := make([]uint16, 0, cipherLen/2)
	for i := 0; i < cipherLen; i += 2 {
		ciphers = append(ciphers, binary.BigEndian.Uint16(p[i:i+2]))
	}
	p = p[cipherLen:]
	p = p[1+int(p[0]):]
	extLen := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2 : 2+extLen]
	var extensions []uint16
	for len(p) > 0 {
		extensions = append(extensions, binary.BigEndian.Uint16(p[:2]))
		length := int(binary.BigEndian.Uint16(p[2:4]))
		p = p[4+length:]
	}
	return ciphers, extensions
}
