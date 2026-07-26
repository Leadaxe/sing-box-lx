# TASKS 033

- [x] Пакет `dns/transport/group`: адаптер, конструктор, валидации конфига
- [x] Start: резолв участников, проверка типов, ошибка на неизвестный тег
- [x] Failover-Exchange: классификатор сбоя, down_time, ротация при полном исчерпании
- [x] Юнит-тесты (см. критерии приёмки SPEC §5)
- [x] Интеграционный тест цикла через TransportManager
- [x] Живой box.New тест `servers: []`
- [x] Маркированные швы: constant/dns.go, option/dns.go, include/registry.go
- [x] DoD: build ± теги, vet, gofmt, `sing-box check` с группой в final и правиле
- [x] Коммиты по зонам (новый пакет / lx-швы)
- [x] IMPLEMENTATION_REPORT.md, статус C, Roadmap
