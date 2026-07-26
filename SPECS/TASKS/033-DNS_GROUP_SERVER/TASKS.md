# TASKS 033

- [ ] Пакет `dns/transport/group`: адаптер, конструктор, валидации конфига
- [ ] Start: резолв участников, проверка типов, ошибка на неизвестный тег
- [ ] Failover-Exchange: классификатор сбоя, down_time, ротация при полном исчерпании
- [ ] Юнит-тесты (см. критерии приёмки SPEC §5)
- [ ] Интеграционный тест цикла через TransportManager
- [ ] Живой box.New тест `servers: []`
- [ ] Маркированные швы: constant/dns.go, option/dns.go, include/registry.go
- [ ] DoD: build ± теги, vet, gofmt, `sing-box check` с группой в final и правиле
- [ ] Коммиты по зонам (новый пакет / lx-швы)
- [ ] IMPLEMENTATION_REPORT.md, статус C, Roadmap
