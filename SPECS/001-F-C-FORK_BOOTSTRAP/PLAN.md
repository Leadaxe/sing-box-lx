# PLAN: 001 — FORK_BOOTSTRAP

## 1. Канонический набор build-тегов lx

Единый источник истины (использовать в Makefile, CI, DoD):

```
with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_xhttp,with_awg
```

Хранить в `Makefile` (переменная `LX_TAGS`) и продублировать в `SPECS/CONSTITUTION.md` при изменениях.

## 2. Изменяемые / новые файлы

| Файл | Тип | Изменения |
|------|-----|-----------|
| `Makefile` | new (или дополнение) | Цель `lx-build`: `go build -tags "$(LX_TAGS)" -ldflags "$(LX_LDFLAGS)" -o sing-box ./cmd/sing-box`; переменные `LX_TAGS`, `VERSION=…-lx.$(LX_BUILD)` |
| `.github/workflows/lx-ci.yml` | new | Скелет: checkout → setup-go → `make lx-build` → `go vet` → `./sing-box check -c lx-test/config/xhttp_smoke.json` (заглушка появится в 002) |
| `lx-test/config/*.json` | new | Sample-конфиги для `sing-box check` (минимальный валидный, без фич — для 001) |
| `SPECS/001-.../IMPLEMENTATION_REPORT.md` | new | Отчёт |

> Версия: upstream хранит строку версии в `constant/version.go` (или собирается через ldflags в `cmd/sing-box`). Проверить фактический механизм и **задавать `-lx` суффикс через `-ldflags -X`**, не правя `constant/version.go` напрямую (иначе лишний `// lx:` дифф на каждый ребейз). Если upstream не поддерживает ldflags-override — тогда минимальная `// lx:` правка в `constant/version.go`.

## 3. Зона касания upstream

- В идеале **ноль** правок upstream-файлов (всё через новые файлы + ldflags).
- Допустимый минимум: одна `// lx:` строка в `constant/version.go`, если ldflags-override невозможен.

## 4. Порядок работ

1. Проверить механизм версии upstream (`constant/version.go`, `cmd/sing-box`).
2. `Makefile` с `LX_TAGS`/`LX_LDFLAGS`/`lx-build`.
3. Sample-конфиг + CI-скелет.
4. Прогнать DoD, заполнить отчёт.

## 5. Риски

- Версионный механизм upstream может не принимать ldflags-override — fallback на `// lx:` правку.
- `with_xhttp`/`with_awg` как несуществующие теги не ломают сборку (Go игнорирует неизвестные build-теги) — но файлов с этими тегами пока нет, это нормально.
