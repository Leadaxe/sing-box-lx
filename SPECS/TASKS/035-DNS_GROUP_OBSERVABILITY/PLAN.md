# PLAN 035 — наблюдаемость DNS-группы

## Файлы

### lx-owned

| Файл | Правка |
|------|--------|
| `common/dnstrack/query_trace.go` | Холдер `QueryTrace`: write-once effective, `groupPath` (prepend), `attempts`, `racer`; `WithQueryTraceIfTracking` (гейт по HasSubscribers); снимок под мьютексом |
| `common/dnstrack/manager.go` | `QueryEvent` += `GroupPath`, `Attempts`, `Racer` |
| `dns/client_log.go` | Эмиты читают снимок трассы, подменяют effective |
| `dns/transport/group/group.go` | `memberState{lastFail, consecFails, lastRTT}`; `probeMember`/`finishProbe` — rtt + классификация исхода + RecordAttempt; `PushGroup` на входе Exchange; `GroupState()`; warn на исчерпание |
| `dns/transport/group/race.go` | Коллектор пишет attempts; `MarkRacer`; лог итога гонки/смены победителя |
| `daemon/started_service_command_lx.go` | Handler `GetDNSGroups` (образец GetPool; type-assertion `dnsGroupStateProvider`), маппинг трассы в `dnsQueryEventToProto` |
| `daemon/started_service_command_lx_stub.go` | `GetDNSGroups` → `Unimplemented` |
| `experimental/libbox/command_client_command_lx.go` | `DnsGroup`/`DnsGroupMember` + итераторы; `DnsQuery.GroupPath()/Attempts()/Racer` |
| Тесты | `dns/transport/group/observability_test.go`, `dns/client_log_effective_lx_test.go`, `daemon/started_service_dnsgroup_stub_lx_test.go` (`!with_lx_command`), `test/dns_group_trace_lx_test.go` (живой, PlatformLogWriter включает observable-режим) |

### Upstream-шов (существующие маркированные зоны)

| Файл | Правка |
|------|--------|
| `daemon/started_service.proto` | Внутри `lx:begin lx_command`: поля 13–15 `DnsQueryEvent`, `DnsGroupAttempt`, RPC `GetDNSGroups` + 3 message |
| `daemon/started_service*.pb.go` | Регенерация `make -f Makefile.lx lx-proto` (артефакт) |
| `dns/client.go` | Та же 3-строчная зона: `WithQueryTraceIfTracking` |

⚠️ После `lx-proto`: `git checkout -- .` внутри `submodules/wireguard-go`
(gofumpt пачкает сабмодуль) и откат не-нашего шума регенерации.

## Ключевые решения

- Исход пробы: `DeadlineExceeded` → `timeout`; SERVFAIL (ответом или
  `RcodeError`) → `servfail`; прочие err → `network_error`; успех
  (вкл. NXDOMAIN/пустой) → `answered`.
- Проба участника-группы в `attempts` не пишется (листья расскажут).
- `PushGroup` — prepend: итоговый порядок «изнутри наружу» без разворота.
- Гонка: коллектор держит ctx гонщика (values живут после отмены), пишет
  attempts по мере прихода; поздние — за снимком эмита, теряются штатно.
- `downRemainingMs` считается на момент снимка RPC.
- Снимок копирует срезы — фоновый коллектор не мутирует отданное событие.

## Грабли

- gomobile: только итераторы и скаляры в экспортируемых типах.
- Словарь исходов — один, в dnstrack; daemon пробрасывает строки verbatim.
- `observable.Observer.UnSubscribe` закрывает done-канал, а не канал
  подписки — потребитель обязан селектить оба (см. память
  `observable-unsubscribe-range-hang`).
