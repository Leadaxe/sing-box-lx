package trusttunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"testing"

	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/net/http2"
)

// TestHTTP2Preface reads the client connection preface off the wire.
func TestHTTP2Preface(t *testing.T) {
	serverTLS, clientTLS := newTestTLS(t)
	serverTLS.config.NextProtos = []string{http2.NextProtoTLS}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := NewClient(ClientOptions{
		Dialer:    &testDialer{},
		Server:    M.SocksaddrFromNet(listener.Addr()),
		TLSConfig: clientTLS,
		Username:  testUsername,
		Password:  testPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	type result struct {
		session sessionConn
		err     error
	}
	done := make(chan result, 1)
	go func() {
		session, err := client.connectHTTP2(testContext(t))
		done <- result{session, err}
	}()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tlsConn := tls.Server(conn, serverTLS.config)
	if err = tlsConn.HandshakeContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	preface := make([]byte, len(http2.ClientPreface))
	if _, err = io.ReadFull(tlsConn, preface); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(preface, []byte(http2.ClientPreface)) {
		t.Fatalf("preface %q", preface)
	}
	framer := http2.NewFramer(io.Discard, tlsConn)
	frame, err := framer.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	settingsFrame, ok := frame.(*http2.SettingsFrame)
	if !ok {
		t.Fatalf("first frame %v, expected SETTINGS", frame.Header())
	}
	var settings []http2.Setting
	settingsFrame.ForeachSetting(func(setting http2.Setting) error {
		settings = append(settings, setting)
		return nil
	})
	// Chrome sends 1:65536;2:0;4:6291456;6:262144. x/net fixes the order and
	// always adds MAX_FRAME_SIZE (Transport.MaxReadFrameSize unset clips to the
	// spec minimum).
	expected := []http2.Setting{
		{ID: http2.SettingEnablePush, Val: 0},
		{ID: http2.SettingInitialWindowSize, Val: chromeStreamWindow},
		{ID: http2.SettingMaxFrameSize, Val: 16384},
		{ID: http2.SettingMaxHeaderListSize, Val: chromeMaxHeaderListSize},
		{ID: http2.SettingHeaderTableSize, Val: chromeHeaderTableSize},
	}
	if len(settings) != len(expected) {
		t.Fatalf("SETTINGS %v, expected %v", settings, expected)
	}
	for i := range expected {
		if settings[i] != expected[i] {
			t.Fatalf("SETTINGS %v, expected %v", settings, expected)
		}
	}
	frame, err = framer.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	windowUpdate, ok := frame.(*http2.WindowUpdateFrame)
	if !ok {
		t.Fatalf("second frame %v, expected WINDOW_UPDATE", frame.Header())
	}
	if windowUpdate.StreamID != 0 || windowUpdate.Increment != chromeConnectionWindow {
		t.Fatalf("WINDOW_UPDATE stream %d increment %d, expected stream 0 increment %d", windowUpdate.StreamID, windowUpdate.Increment, chromeConnectionWindow)
	}
	t.Logf("SETTINGS %v WINDOW_UPDATE %d", settings, windowUpdate.Increment)
	connected := <-done
	if connected.err != nil {
		t.Fatal(connected.err)
	}
	connected.session.close()
}
