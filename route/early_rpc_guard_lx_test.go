package route

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/adapter"

	"github.com/stretchr/testify/require"
)

// countingConnectionManager считает CloseAll; остальные методы не вызываются
// на проверяемом пути, поэтому интерфейс встроен как nil.
type countingConnectionManager struct {
	adapter.ConnectionManager
	closeAllCalls int
}

func (m *countingConnectionManager) CloseAll() {
	m.closeAllCalls++
}

// lx: SPECS/TASKS/047-EARLY_RPC_NIL_ROUTER_CRASH
//
// ResetNetwork приходит по command-протоколу (смена WiFi↔LTE) в окно между
// созданием box и стадией StartStateInitialize, на которой присваиваются
// router/endpoint/inbound/outbound. До фикса это SIGSEGV на r.router.
func TestResetNetworkBeforeInitializeDoesNotPanic(t *testing.T) {
	t.Parallel()

	// Ровно то состояние, в котором NetworkManager находится сразу после
	// NewNetworkManager и до Start(StartStateInitialize).
	nm := &NetworkManager{}

	require.NotPanics(t, func() { nm.ResetNetwork(context.Background()) })
}

// Гейт должен срабатывать до connectionManager.CloseAll(): полуинициализированный
// менеджер не должен получать вызовов, даже если сам по себе не nil.
func TestResetNetworkBeforeInitializeSkipsConnectionManager(t *testing.T) {
	t.Parallel()

	connectionManager := &countingConnectionManager{}
	nm := &NetworkManager{connectionManager: connectionManager}

	require.NotPanics(t, func() { nm.ResetNetwork(context.Background()) })
	require.Zero(t, connectionManager.closeAllCalls, "CloseAll must not run before initialize")
}
