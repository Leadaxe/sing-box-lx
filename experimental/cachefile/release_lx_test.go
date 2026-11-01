package cachefile

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/bbolt"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"

	"github.com/stretchr/testify/require"
)

type recordingLogger struct {
	logger.ContextLogger
	access sync.Mutex
	warns  []string
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{ContextLogger: logger.NOP()}
}

func (l *recordingLogger) Warn(args ...any) {
	l.access.Lock()
	defer l.access.Unlock()
	l.warns = append(l.warns, fmt.Sprint(args...))
}

func (l *recordingLogger) warnCount() int {
	l.access.Lock()
	defer l.access.Unlock()
	return len(l.warns)
}

func newTestCacheFile(t *testing.T, path string, log logger.Logger) *CacheFile {
	t.Helper()
	if log == nil {
		log = logger.NOP()
	}
	return New(context.Background(), log, option.CacheFileOptions{Path: path, StoreFakeIP: true})
}

func startTestCacheFile(t *testing.T, path string, log logger.Logger) *CacheFile {
	t.Helper()
	c := newTestCacheFile(t, path, log)
	require.NoError(t, c.start())
	t.Cleanup(func() { _ = c.Release() })
	return c
}

func registeredInstances(path string) int {
	cacheFileRegistryAccess.Lock()
	defer cacheFileRegistryAccess.Unlock()
	return len(cacheFileRegistry[path])
}

func testFakeIPMetadata() *adapter.FakeIPMetadata {
	return &adapter.FakeIPMetadata{
		Inet4Range:   netip.MustParsePrefix("198.18.0.0/15"),
		Inet6Range:   netip.MustParsePrefix("fc00::/18"),
		Inet4Current: netip.MustParseAddr("198.18.3.7"),
		Inet6Current: netip.MustParseAddr("fc00::3:7"),
	}
}

func TestTakeoverFromUnreleasedInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	a := startTestCacheFile(t, path, nil)
	require.NoError(t, a.StoreSelected("g", "node1"))

	log := newRecordingLogger()
	b := newTestCacheFile(t, path, log)
	startAt := time.Now()
	require.NoError(t, b.start())
	elapsed := time.Since(startAt)
	defer b.Release()
	require.Less(t, elapsed, 900*time.Millisecond)
	require.Equal(t, 1, log.warnCount())

	require.ErrorIs(t, a.StoreSelected("g", "node2"), os.ErrClosed)
	require.Empty(t, a.LoadSelected("g"))
	require.Equal(t, "node1", b.LoadSelected("g"))
	require.NoError(t, checkDatabase(b.DB))
	require.Equal(t, 1, registeredInstances(path))
	require.NoError(t, b.Release())
	require.Zero(t, registeredInstances(path))
}

func TestReleaseFlushesPendingFakeIPMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	a := startTestCacheFile(t, path, nil)
	a.FakeIPSaveMetadataAsync(testFakeIPMetadata())
	require.NoError(t, a.Release())

	b := startTestCacheFile(t, path, nil)
	require.Equal(t, testFakeIPMetadata(), b.FakeIPMetadata())
}

func TestFakeIPMetadataSurvivesSessionWithoutAllocations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	a := startTestCacheFile(t, path, nil)
	require.NoError(t, a.FakeIPSaveMetadata(testFakeIPMetadata()))
	require.NoError(t, a.Release())

	b := startTestCacheFile(t, path, nil)
	require.Equal(t, testFakeIPMetadata(), b.FakeIPMetadata())
	require.NoError(t, b.Release())

	c := startTestCacheFile(t, path, nil)
	require.Equal(t, testFakeIPMetadata(), c.FakeIPMetadata())
	require.NoError(t, c.FakeIPReset())
	require.NoError(t, c.Release())

	d := startTestCacheFile(t, path, nil)
	require.Nil(t, d.FakeIPMetadata())
}

func TestReleaseIdempotentAndResetDBNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	c := startTestCacheFile(t, path, nil)
	require.NoError(t, c.Release())
	require.NoError(t, c.Release())

	noop := func(*bbolt.Tx) error { return nil }
	require.ErrorIs(t, c.view(noop), os.ErrClosed)
	require.ErrorIs(t, c.batch(noop), os.ErrClosed)
	require.ErrorIs(t, c.update(noop), os.ErrClosed)

	c.resetDB(c.DB, "boom")
	db, err := bbolt.Open(path, 0o666, &bbolt.Options{Timeout: 200 * time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func TestNormalCloseUnregisters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	ctx := context.Background()
	a := newTestCacheFile(t, path, nil)
	scope := adapter.NewScope(ctx, logger.NOP())
	require.NoError(t, a.Start(adapter.StartStateInitialize, scope))
	require.Equal(t, 1, registeredInstances(path))
	require.NoError(t, scope.Close())
	require.Zero(t, registeredInstances(path))

	log := newRecordingLogger()
	b := startTestCacheFile(t, path, log)
	require.Zero(t, log.warnCount())
	require.NoError(t, checkDatabase(b.DB))
}
