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

// ip=quic is a SINGLE Initial: i1 only, i2 empty. One Initial is what a real
// client sends to open one QUIC session; the realism is in the browser-accurate
// ClientHello (Ib), not in packet count. The i1 must be a valid fragmented
// Initial carrying the SNI.
func TestAwgIpcLinesQUICSingleInitial(t *testing.T) {
	t.Parallel()
	const sni = "www.google.com"
	lines, err := awgIpcLines(option.AmneziaWGOptions{Id: sni, Ip: "quic", Ib: "chrome"})
	require.NoError(t, err)

	i1 := ipcValue(t, lines, "i1")
	require.NotEmpty(t, i1, "i1 present")
	require.Empty(t, ipcValue(t, lines, "i2"), "quic is single-packet: i2 empty")

	pkt := obfuscateCPS(t, i1)
	require.Equal(t, 1250, len(pkt), "i1 is a 1250B Initial")
	d := decryptInitial(t, pkt)
	require.Equal(t, sni, extractSNI(t, d.clientHello), "i1 carries the SNI")
	require.NotEqual(t, uint64(0), d.cryptoFrames[0].offset, "first CRYPTO offset != 0 (I1)")
}

// dns/stun/quic are single-packet: they fill i1 only, leaving i2 empty. (sip is
// multi-packet — INVITE + 100 Trying — covered by its own test below.)
func TestAwgIpcLinesNonSIPNoI2(t *testing.T) {
	t.Parallel()
	for _, o := range []option.AmneziaWGOptions{
		{Id: "a.com", Ip: "dns"},
		{Ip: "stun"},
		{Id: "a.com", Ip: "quic"},
	} {
		lines, err := awgIpcLines(o)
		require.NoError(t, err)
		require.NotEmpty(t, ipcValue(t, lines, "i1"), "i1 present for ip=%s", o.Ip)
		require.Empty(t, ipcValue(t, lines, "i2"), "i2 empty for ip=%s", o.Ip)
	}
}

// ip=sip is multi-packet: i1 = a complete INVITE, i2 = the matching 100 Trying.
// Both must be whole valid SIP messages wired into the device, and they must
// share one dialog (Via branch / tag / Call-ID / CSeq) — the cross-slot check.
func TestAwgIpcLinesSIPFillsI1AndI2(t *testing.T) {
	t.Parallel()
	const host = "pbx.example.com"
	lines, err := awgIpcLines(option.AmneziaWGOptions{Id: host, Ip: "sip"})
	require.NoError(t, err)

	i1 := ipcValue(t, lines, "i1")
	i2 := ipcValue(t, lines, "i2")
	require.NotEmpty(t, i1, "i1 (INVITE) present")
	require.NotEmpty(t, i2, "i2 (100 Trying) present")

	invite := string(obfuscateCPS(t, i1))
	trying := string(obfuscateCPS(t, i2))
	assertSIPInvite(t, invite, host)
	assertSIPTrying(t, trying)
	assertSameSIPDialog(t, invite, trying)
}

// ip=sip is the only multi-packet sugar profile, so an explicit i2 alongside it
// is a conflict (the sugar fills i2), mirroring the i1 conflict guard. quic/dns/
// stun are single-packet and leave i2 free, so a user i2 there is not a conflict.
func TestAwgIpcLinesSIPExplicitI2Conflict(t *testing.T) {
	t.Parallel()
	_, err := awgIpcLines(option.AmneziaWGOptions{Id: "a.com", Ip: "sip", I2: "<b 0x0844>"})
	require.Error(t, err, "ip=sip + explicit i2 must conflict")
	require.Contains(t, err.Error(), "explicit i2 conflicts")
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
