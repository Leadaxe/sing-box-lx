//go:build !with_awg

package wireguard

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Without with_awg the plain-number keepalive still works (upstream
// behaviour), the AWG 3.x range form is rejected like every AWG field.
func TestAwgKeepaliveSpecStub(t *testing.T) {
	t.Parallel()
	got, err := awgKeepaliveSpec("25")
	require.NoError(t, err)
	require.Equal(t, "25", got)
	got, err = awgKeepaliveSpec("")
	require.NoError(t, err)
	require.Equal(t, "", got)
	_, err = awgKeepaliveSpec("25-35")
	require.Error(t, err)
	require.ErrorContains(t, err, "with_awg")
}
