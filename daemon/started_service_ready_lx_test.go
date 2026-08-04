package daemon

import (
	"sync"
	"testing"
)

// lx: SPECS/TASKS/047-EARLY_RPC_NIL_ROUTER_CRASH
//
// Ready — предикат готовности для гейтов CommandServer. Ключевой случай —
// STARTING: instance уже опубликован (s.instance присвоен до instance.Start()),
// но поля NetworkManager ещё nil, поэтому RPC обслуживать нельзя.
func TestStartedServiceReady_LX(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		status   ServiceStatus_Type
		expected bool
	}{
		{"idle", ServiceStatus_IDLE, false},
		{"starting", ServiceStatus_STARTING, false},
		{"started", ServiceStatus_STARTED, true},
		{"stopping", ServiceStatus_STOPPING, false},
		{"fatal", ServiceStatus_FATAL, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service := &StartedService{serviceStatus: &ServiceStatus{Status: testCase.status}}
			if service.Ready() != testCase.expected {
				t.Fatalf("Ready() = %v for status %v, want %v",
					service.Ready(), testCase.status, testCase.expected)
			}
		})
	}
}

// Публикация instance сама по себе не делает сервис готовым — именно на этом
// ломался прежний гейт `Box() != nil`.
func TestStartedServiceNotReadyWhileStarting_LX(t *testing.T) {
	service := &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTING},
		instance:      &Instance{},
	}
	if service.Instance() == nil {
		t.Fatal("instance must be published during STARTING — the premise of the bug")
	}
	if service.Ready() {
		t.Fatal("Ready() must be false while the box is still starting")
	}
}

// Ready читается из горутины command-протокола, пока стартующая горутина
// переписывает serviceStatus — под -race доступ должен быть чистым.
func TestStartedServiceReadyConcurrent_LX(t *testing.T) {
	service := &StartedService{serviceStatus: &ServiceStatus{Status: ServiceStatus_IDLE}}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for _, status := range []ServiceStatus_Type{
			ServiceStatus_STARTING, ServiceStatus_STARTED,
			ServiceStatus_STOPPING, ServiceStatus_IDLE,
		} {
			for range 250 {
				service.serviceAccess.Lock()
				service.serviceStatus = &ServiceStatus{Status: status}
				service.serviceAccess.Unlock()
			}
		}
	}()

	go func() {
		defer wg.Done()
		for range 1000 {
			_ = service.Ready()
		}
	}()

	wg.Wait()
}
