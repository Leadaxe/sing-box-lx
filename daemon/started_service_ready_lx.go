package daemon

// lx: early-rpc-guard — SPECS/TASKS/047-EARLY_RPC_NIL_ROUTER_CRASH
//
// Instance() публикуется до instance.Start(), поэтому проверка "Box() != nil"
// на стороне CommandServer не отражает готовность: между присваиванием
// s.instance и статусом STARTED лежит вся стадийная инициализация box,
// в ходе которой поля NetworkManager ещё nil.
//
// Ready — тот же предикат, который upstream держит внутри пакета для
// URLTest/SelectOutbound, вынесенный наружу для experimental/libbox.

// Ready сообщает, что сервис достартовал и его RPC можно обслуживать.
func (s *StartedService) Ready() bool {
	s.serviceAccess.RLock()
	defer s.serviceAccess.RUnlock()
	return s.serviceStatus.Status == ServiceStatus_STARTED
}
