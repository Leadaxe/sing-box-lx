package rule

import (
	"testing"

	"github.com/sagernet/sing-box/adapter"

	"github.com/stretchr/testify/require"
)

func TestPackageNameRegexItem(t *testing.T) {
	t.Parallel()

	item, err := NewPackageNameRegexItem([]string{`^com\.termux.*`, `^org\.mozilla\.firefox$`})
	require.NoError(t, err)

	matchWith := func(packageNames ...string) bool {
		return item.Match(&adapter.InboundContext{
			ProcessInfo: &adapter.ConnectionOwner{AndroidPackageNames: packageNames},
		})
	}

	require.True(t, matchWith("com.termux"))
	require.True(t, matchWith("com.termux.api"))
	require.True(t, matchWith("org.mozilla.firefox"))
	// regex matches one of several reported package names
	require.True(t, matchWith("com.android.chrome", "com.termux"))

	require.False(t, matchWith("org.mozilla.firefox.beta")) // anchored $ rejects suffix
	require.False(t, matchWith("com.android.chrome"))
	require.False(t, matchWith())

	// nil ProcessInfo must not panic and must not match
	require.False(t, item.Match(&adapter.InboundContext{}))
}

func TestPackageNameRegexItemInvalid(t *testing.T) {
	t.Parallel()
	_, err := NewPackageNameRegexItem([]string{"("})
	require.Error(t, err)
}
