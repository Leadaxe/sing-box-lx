# SPEC: 055 — LXD_DAEMON_SKELETON

**Фича:** [LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — скелет headless-демона `lxd`; первая задача фичи 014 |
| Статус | C (complete) — в дереве, юниты зелёные, сквозное демо на macOS живьём |
| Ветка | `lx` |
| Base | `4676c31ea` (fix GetRules attached-паники — прямой пререквизит канала) |
| Связанные | [FEATURE 014](../../FEATURES/014-LXD_DAEMON/FEATURE.md) (полный скоуп и дорожная карта), [FEATURE 006](../../FEATURES/006-OBSERVABILITY/FEATURE.md) (lx-RPC, которые канал отдаёт бесплатно), SPEC 019 (пул, виден через GetPool), SPEC 037 (running-config snapshot) |

**Touches:** только новые файлы — [lxd/daemon.go](../../../lxd/daemon.go),
[lxd/stub.go](../../../lxd/stub.go), [lxd/daemon_test.go](../../../lxd/daemon_test.go),
[cmd/sing-box/cmd_lxd_lx.go](../../../cmd/sing-box/cmd_lxd_lx.go). Ни одной
правки в существующих апстримных файлах: сабкоманда самонавешивается
через `init()` (паттерн остальных `cmd_*.go`).

## Why

Решение владельца (диалог 2026-08-07, два исследовательских прогона: ядро +
лаунчер): удалённое управление серверным ядром строится на демоне, где ядро
живёт in-process за долгоживущим управляющим каналом — по модели апстримного
десктопного демона (experimental/boxdd), но headless и в форме **сабкоманды**
существующего бинаря (один артефакт, extractor лаунчера не трогается, version
skew невозможен). Скелет фиксирует несущее свойство всей архитектуры — канал
переживает reload и битый конфиг — до того, как поверх появится admin-плоскость.

## Design

`sing-box lxd run -c config.json --listen 127.0.0.1:9091 --secret …`
(тег `with_lxd`; без тега сабкоманды нет, пакет — пустой стаб).

> ⚠️ **Синтаксис этой задачи устарел** (историческая фиксация). Актуально:
> демон — голая команда `sing-box lxd`, а connection-флагов нет вовсе —
> настройки живут в `<state-dir>/daemon.json`. См.
> [SPEC 057](../057-LXD_MTLS_SERVICE/SPEC.md) и руководство
> [docs-lx/lxd-daemon.md](../../../docs-lx/lxd-daemon.md).

Порядок внутри: чтение конфига → `daemon.NewStartedService` (владеющий режим,
тот же кодопуть, что libbox/boxdd — НЕ attached) → `daemon.NewServer` (gRPC,
Bearer-интерцепторы, health, reflection) → listener → **и только потом**
`StartOrReloadService(конфиг строкой)`. Провал старта или reload'а логируется,
демон остаётся слушать в FATAL. SIGHUP: перечитать файл → применить (со времён
056 — через общий пайплайн apply: битый файл отбивается валидацией, инстанс не
трогается; в 055-скелете это был голый `StartOrReloadService` с FATAL). Подмена
инстанса всегда идёт под живым сервером. SIGINT/SIGTERM: `CloseService` →
`grpcServer.Stop` → `Close`.

Сознательно вне скоупа (дорожная карта фичи): валидация до убийства старого
инстанса, автооткат, версии конфигов, admin-RPC, SRS, TLS, `service install`.

## Verification

- `go build` с тегом и без (стаб) — обе стороны зелёные; `gofmt -l` пусто;
  `go test ./lxd/` — ok.
- Сквозное демо (macOS, darwin/amd64, сборка `1.14.0-lx.22-lxd-demo`,
  поведение 055-скелета — до пайплайна 056, см. Design):
  `GetVersion`/`GetPool` отвечают; **один** `SubscribeServiceStatus`-стрим без
  переподключения увидел полный цикл: `STARTED → (SIGHUP, +нода) STOPPING →
  STARTING → STARTED → (SIGHUP, битый конфиг) FATAL{"unknown outbound type"}
  → (SIGHUP, починенный) STARTING → STARTED`; в FATAL-состоянии канал отвечал
  `GetVersion`; `GetPool` после reload'ов отражал новый состав пула (2→3→1
  слота); SIGTERM завершил процесс чисто. После 056 битый SIGHUP-конфиг даёт
  не FATAL, а reject валидацией (инстанс живёт) — несущее свойство «канал
  переживает reload и битый конфиг» закреплено юнитом
  `TestControlPlaneSurvivesBrokenApply` на реальном ядре.
