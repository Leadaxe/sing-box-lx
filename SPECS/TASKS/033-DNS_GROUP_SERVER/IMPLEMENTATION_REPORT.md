# IMPLEMENTATION_REPORT 033 — DNS-группа: тип `group`, failover

## Файлы

| Зона | Файлы |
|------|-------|
| Новый пакет | `dns/transport/group/group.go`, `group_test.go` |
| Live-тесты | `test/dns_group_lx_test.go` (+ `test/go.mod`/`go.sum` tidy) |
| `// lx:` upstream | `constant/dns.go` (+1 константа), `option/dns.go` (+1 структура), `include/registry.go` (импорт-алиас `dnsgroup` + 1 строка регистрации) |
| Deps / wiring | нет (build-tag не вводился — обоснование в FEATURE.md) |

## DoD

- `go build ./...` без тегов — OK; с lx-тегами (минус `badlinkname`/naive на go1.25-хосте) — OK.
- `go vet`, `gofmt -l` — чисто.
- Юниты: порядок failover, SERVFAIL ответом и `RcodeError`, NXDOMAIN/пустой — не сбой, down_time (пометка/пропуск/возврат), all-down ротация «самый давний, одна попытка», восстановление снимает метку, Reset, валидации конструктора.
- Интеграционные через `TransportManager`: цикл групп → `circular server dependency`; отсутствующий участник; `fakeip`/`hosts` в составе → ошибка старта; вложенная группа стартует и отвечает.
- Live `box.New`: группа объявлена раньше участников (порядок старта из зависимостей), группа в `final`; `servers: []` → ошибка конструктора (badjson-коллапс покрыт); цикл на живом ядре.
- `sing-box check` с группой в `final` и в правиле — OK.

## Зона ребейз-конфликтов

`constant/dns.go`, `option/dns.go`, `include/registry.go` — по одному маркированному блоку; конфликты возможны только контекстные.

## Вне скоупа

Режим `race` (034), атрибуция потока (035), RPC состояния группы (отложен, см. 035 §4), `mode: parallel`.
