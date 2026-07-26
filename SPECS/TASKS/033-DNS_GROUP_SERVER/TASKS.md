# TASKS 033 (v2)

- [x] Конфиг: mode stable|fastest|parallel, error_ttl, win_ttl; interval/down_time удалены
- [x] Записи TTL: ленивые срезы, успех↔ошибки / ошибка↔победы, Reset-амнистия
- [x] Выбор цели: stable (липкость→случайность), fastest (победы→выборы), parallel, выживание
- [x] Единый поток: под-дедлайн цели ½ остатка, веер по чистым, guard ошибок, победы/опоздавшие
- [x] Single-flight выборов (CAS + gen)
- [x] Лог решений (debug/info/warn)
- [x] Юниты §4 SPEC (-race)
- [x] Живые box-тесты: v2-конфиги, негативные на v1-поля
- [x] DoD: build ± теги, vet, gofmt, check ×3 режима
- [x] IMPLEMENTATION_REPORT.md, статус C, Roadmap
