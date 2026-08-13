package daemon

import "time"

// lx: observability-plane — SPECS/TASKS/065-LXD_OBSERVABILITY_PLANE
//
// GetStartedAt отдаёт то же значение по gRPC, но lxd обслуживает REST-ручку
// GET /admin/stats и обращается к сервису напрямую, без прохода через
// протокол. Отдельный аксессор вместо экспорта поля: чтение обязано идти под
// serviceAccess (StartOrReloadService и CloseService пишут startedAt).
//
// Нулевое время = ядра нет. Это не "начало эпохи": CloseService выставляет
// time.Time{} явно, и /admin/stats обязан отличить остановленное ядро от
// работающего с 1970 года — отсюда возврат самого time.Time, а не Duration.

// StartedAt возвращает момент старта текущего инстанса; нулевое значение
// означает, что ядро не запущено.
func (s *StartedService) StartedAt() time.Time {
	s.serviceAccess.RLock()
	defer s.serviceAccess.RUnlock()
	return s.startedAt
}
