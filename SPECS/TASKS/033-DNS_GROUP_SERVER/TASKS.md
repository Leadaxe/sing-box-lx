# TASKS 033 (v2)

- [ ] Конфиг: mode stable|fastest|parallel, error_ttl, win_ttl; interval/down_time удалены
- [ ] Записи TTL: ленивые срезы, успех↔ошибки / ошибка↔победы, Reset-амнистия
- [ ] Выбор цели: stable (липкость→случайность), fastest (победы→выборы), parallel, выживание
- [ ] Единый поток: под-дедлайн цели ½ остатка, веер по чистым, guard ошибок, победы/опоздавшие
- [ ] Single-flight выборов (CAS + gen)
- [ ] Лог решений (debug/info/warn)
- [ ] Юниты §4 SPEC (-race)
- [ ] Живые box-тесты: v2-конфиги, негативные на v1-поля
- [ ] DoD: build ± теги, vet, gofmt, check ×3 режима
- [ ] IMPLEMENTATION_REPORT.md, статус C, Roadmap
