//go:build with_awg

package wireguard

import (
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
