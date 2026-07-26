# IMPLEMENTATION_REPORT 035 — наблюдаемость DNS-группы

## Файлы

| Зона | Файлы |
|------|-------|
| lx-код | `common/dnstrack/query_trace.go` (+поля `QueryEvent` в `manager.go`), `dns/client_log.go`, `dns/transport/group/{group,race}.go`, `daemon/started_service_command_lx.go` (`GetDNSGroups`, маппинг трассы), `daemon/started_service_command_lx_stub.go`, `experimental/libbox/command_client_command_lx.go` |
| Upstream-шов | `daemon/started_service.proto` — всё внутри существующей `lx:begin lx_command`-зоны; `dns/client.go` — 3-строчная маркированная зона (холдер только для типа `group` и только при подписке) |
| Регенерация | `started_service{,_grpc}.pb.go` через `make -f Makefile.lx lx-proto` (pinned); шум gofumpt по сабмодулю/чужим файлам откачен |
| Тесты | `dns/transport/group/observability_test.go` (трасса+state), `dns/client_log_effective_lx_test.go` (emit-уровень), `daemon/started_service_dnsgroup_stub_lx_test.go` (`!with_lx_command` → Unimplemented), `test/dns_group_trace_lx_test.go` (живой) |

## Контракт для LxBox (готов к внедрению)

- Атрибуция: `dnsServer`/`dnsServerType` = фактически ответивший участник
  (лист при вложенности); кеш-попадание и полный сбой — тег группы.
- Поток: `DnsQueryEvent` 13–15 (`dnsGroupPath` изнутри наружу, `attempts`
  со словарём `answered|timeout|network_error|servfail` + `rttMs`, `racer`);
  клиентские геттеры `DnsQuery.GroupPath()/Attempts()/Racer`.
- Снимок: `CommandClient.GetDNSGroups() DnsGroupIterator` →
  `DnsGroup{Tag, Mode, Winner, RaceAgeMs(-1 = гонки не было), Ranking(),
  Members()}`, `DnsGroupMember{Tag, ServerType, Up, DownRemainingMs,
  ConsecutiveFailures, LastRTTMs}`.
- Совместимость двусторонняя: поля аддитивны, старый клиент их игнорирует,
  пустые значения = валидное состояние.

## Ключевые решения

- Write-once `effective` починил атрибуцию вложенных групп (лист, не тег
  внутренней группы) — закреплено юнитом; подробности смены архитектуры
  против первой редакции — [HISTORY.md](HISTORY.md).
- `group_path` строится prepend'ом на входе группы — порядок «изнутри
  наружу» без разворота на эмите.
- В `attempts` — только листовые пробы (участник-группа не пишется).
- Эмит читает снимок под мьютексом; опоздавшие ответы гонки дописываются
  в холдер и штатно теряются — их место в `GetDNSGroups` (живой тест
  подтверждает на реальном ядре с медленным loopback-DNS).
- Холдер аллоцируется только при активной подписке.
- Лог решений: down-пометка/итог гонки/смена победителя — debug;
  полное исчерпание — warn.

## DoD

- `go test -race` — dnstrack, dns/..., daemon, group: зелёные; живой
  `box.New`-тест (PlatformLogWriter включает observable-режим ядра —
  как в мобильном клиенте).
- Сборки: `go build ./...` и `-tags with_lx_command ./...` — обе зелёные;
  stub отвечает `Unimplemented`.
- gofmt чист; vet — только два «possible misuse of unsafe.Pointer»
  в upstream-наследии (`daemon/managed_service.go`, `libbox/debug.go`),
  существовали до задачи.

## Вне скоупа

Полевой device-verify (runbook требует перед промоутом rc→final);
push-подписка на состояние групп (пока pull-RPC — UI опрашивает).
