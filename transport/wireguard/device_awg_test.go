//go:build with_awg

package wireguard

import (
	"strings"
	"testing"

	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

func TestAwgIpcLines(t *testing.T) {
	t.Parallel()
	lines, err := awgIpcLines(option.AmneziaWGOptions{
		Jc:   5,
		Jmin: 10,
		Jmax: 50,
		S1:   28,
		S2:   121,
		S3:   25,
		S4:   9,
		H1:   "43613244-384550127",
		H2:   "826869626-2105069164",
		H3:   "2124774725-2141151992",
		H4:   "2144594503-2146278491",
		I1:   "<b 0x0844>",
	})
	require.NoError(t, err)
	require.Equal(t,
		"\njc=5\njmin=10\njmax=50\ns1=28\ns2=121\ns3=25\ns4=9"+
			"\nh1=43613244-384550127\nh2=826869626-2105069164"+
			"\nh3=2124774725-2141151992\nh4=2144594503-2146278491"+
			"\ni1=<b 0x0844>",
		lines)
}

func TestAwgIpcLinesSingleHeaders(t *testing.T) {
	t.Parallel()
	lines, err := awgIpcLines(option.AmneziaWGOptions{
		H1: "1",
		H2: "2",
		H3: "3",
		H4: "4",
	})
	require.NoError(t, err)
	require.Equal(t, "\nh1=1\nh2=2\nh3=3\nh4=4", lines)
}

func TestAwgIpcLinesUnsetHeadersOmitted(t *testing.T) {
	t.Parallel()
	lines, err := awgIpcLines(option.AmneziaWGOptions{
		Jc: 4,
		H2: "10-20",
	})
	require.NoError(t, err)
	require.Equal(t, "\njc=4\nh2=10-20", lines)
}

// A plain WireGuard endpoint must produce byte-identical device config to
// upstream even in a with_awg build.
func TestAwgIpcLinesPlainWireGuard(t *testing.T) {
	t.Parallel()
	lines, err := awgIpcLines(option.AmneziaWGOptions{})
	require.NoError(t, err)
	require.Equal(t, "", lines)
}

// Options built in code (libbox/launcher) bypass JSON validation; the ipc
// layer must reject garbage with the key name instead of feeding it to uapi.
func TestAwgIpcLinesInvalidHeader(t *testing.T) {
	t.Parallel()
	_, err := awgIpcLines(option.AmneziaWGOptions{H3: "100-50"})
	require.Error(t, err)
	require.ErrorContains(t, err, "h3")
}

// jmin > jmax makes amneziawg-go's rand.Int argument <= 0, which panics in the
// retransmit-timer goroutine. awgIpcLines must reject it at config time instead
// — and must not panic doing so.
func TestAwgIpcLinesJminGreaterThanJmax(t *testing.T) {
	t.Parallel()
	require.NotPanics(t, func() {
		_, err := awgIpcLines(option.AmneziaWGOptions{Jc: 5, Jmin: 70, Jmax: 40})
		require.Error(t, err)
		require.ErrorContains(t, err, "jmin")
		require.ErrorContains(t, err, "jmax")
	})
}

// A valid junk range (jmin <= jmax, the shape every real awg2 export uses) and
// a fully-disabled junk config must both pass.
func TestAwgIpcLinesValidJunkRange(t *testing.T) {
	t.Parallel()
	_, err := awgIpcLines(option.AmneziaWGOptions{Jc: 4, Jmin: 40, Jmax: 70})
	require.NoError(t, err)
	_, err = awgIpcLines(option.AmneziaWGOptions{H1: "1"}) // junk off, only a header
	require.NoError(t, err)
}

// ip=quic is a dynamic decoy: awgIpcLines emits no static i1/i2 for it (a
// baked CPS blob would repeat the same DCID and ciphertext on every handshake),
// and awgDecoyFunc hands the device a generator that builds ONE fresh Initial
// per call — the whole ClientHello in one QUIC packet (a multi-packet start is
// dropped on the WARP path, LxBox §618). The non-QUIC profiles stay static CPS.
func TestAwgIpcLinesQUICDynamicDecoy(t *testing.T) {
	t.Parallel()
	const sni = "www.google.com"
	for _, ib := range []string{"chrome", "chrome-full", "firefox", ""} {
		o := option.AmneziaWGOptions{Id: sni, Ip: "quic", Ib: ib}
		lines, err := awgIpcLines(o)
		require.NoError(t, err, "ib=%q", ib)
		require.Empty(t, ipcValue(t, lines, "i1"), "ib=%q: quic decoy is not a static i1", ib)
		require.Empty(t, ipcValue(t, lines, "i2"), "ib=%q: i2 empty", ib)

		gen := awgDecoyFunc(o, nil)
		require.NotNil(t, gen, "ib=%q: dynamic decoy generator", ib)
		a, b := gen(), gen()
		require.Len(t, a, 1, "ib=%q: one Initial per handshake", ib)
		require.Len(t, b, 1)
		da, db := decryptInitial(t, a[0]), decryptInitial(t, b[0])
		require.Equal(t, sni, extractSNI(t, da.clientHello), "ib=%q: decoy carries the SNI", ib)
		require.NotEqual(t, da.dcid, db.dcid, "ib=%q: fresh DCID per handshake", ib)
		require.NotEqual(t, a[0], b[0], "ib=%q: fresh ciphertext per handshake", ib)
		switch ib {
		case "chrome", "chrome-full":
			assertChaosLayout(t, da)
		default:
			assertPlainLayout(t, da)
		}
	}

	// Static profiles and plain configs have no dynamic decoy.
	require.Nil(t, awgDecoyFunc(option.AmneziaWGOptions{Id: sni, Ip: "dns"}, nil))
	require.Nil(t, awgDecoyFunc(option.AmneziaWGOptions{Jc: 4, Jmin: 40, Jmax: 70}, nil))
	lines, err := awgIpcLines(option.AmneziaWGOptions{Id: sni, Ip: "dns"})
	require.NoError(t, err)
	require.NotEmpty(t, ipcValue(t, lines, "i1"), "dns stays a static i1")
}

// dns/stun/sip are single-packet static decoys: they fill i1 only, leaving i2
// empty. (quic is generated per handshake and emits no static slot — see
// TestAwgIpcLinesQUICDynamicDecoy.)
func TestAwgIpcLinesNonSIPNoI2(t *testing.T) {
	t.Parallel()
	for _, o := range []option.AmneziaWGOptions{
		{Id: "a.com", Ip: "dns"},
		{Ip: "stun"},
		{Id: "a.com", Ip: "sip"},
	} {
		lines, err := awgIpcLines(o)
		require.NoError(t, err)
		require.NotEmpty(t, ipcValue(t, lines, "i1"), "i1 present for ip=%s", o.Ip)
		require.Empty(t, ipcValue(t, lines, "i2"), "i2 empty for ip=%s", o.Ip)
	}
}

// ip=sip wires a complete INVITE into i1 and leaves i2 to the user: an explicit
// i2 next to the sugar is passed through unchanged.
func TestAwgIpcLinesSIPFillsI1(t *testing.T) {
	t.Parallel()
	const host = "pbx.example.com"
	lines, err := awgIpcLines(option.AmneziaWGOptions{Id: host, Ip: "sip", I2: "<b 0x0844>"})
	require.NoError(t, err)

	invite := string(obfuscateCPS(t, ipcValue(t, lines, "i1")))
	assertSIPInvite(t, invite, host)
	require.Equal(t, "<b 0x0844>", ipcValue(t, lines, "i2"), "user i2 passes through")
}

// ipcValue extracts the value of a "\nkey=value" line from awgIpcLines output,
// or "" if absent.
func ipcValue(t *testing.T, lines, key string) string {
	t.Helper()
	for _, line := range strings.Split(lines, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}
