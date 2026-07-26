# PLAN 036 — трасса проб, state-RPC, лог решений

## Файлы

### lx-owned (правки без новых upstream-касаний)

| Файл | Правка |
|------|--------|
| `common/dnstrack/effective_server.go` → `query_trace.go` | Холдер → `QueryTrace`: write-once effective, `groupPath` (prepend), `attempts`, `racer`; `WithQueryTraceIfTracking` (гейт по HasSubscribers); снимок под мьютексом |
| `common/dnstrack/manager.go` | `QueryEvent` += `GroupPath []string`, `Attempts []Attempt`, `Racer bool` |
| `dns/client_log.go` | Эмиты читают снимок трассы; effective — как раньше |
| `dns/transport/group/group.go` | `memberState{lastFail, consecFails, lastRTT}` вместо голой карты; `probeMember()` — замер rtt + классификация исхода + RecordAttempt + учёт; `PushGroup` на входе Exchange; `GroupState()` для RPC; warn на исчерпание |
| `dns/transport/group/race.go` | Коллектор пишет attempts; `SetRacer`; лог итога гонки/смены победителя |
| `daemon/started_service_command_lx.go` | Handler `GetDNSGroups` (образец GetPool; менеджер — `service.FromContext[adapter.DNSTransportManager]`, участники через type-assertion `dnsGroupStateProvider`) |
| `daemon/started_service_command_lx_stub.go` | `GetDNSGroups` → `Unimplemented` |
| `experimental/libbox/command_client_command_lx.go` | `DnsGroup`/`DnsGroupMember` + итераторы (gomobile), `Ranking()` через `StringIterator`; `DnsQuery` += `GroupPath() StringIterator`, `Attempts() DnsAttemptIterator`, `Racer` |
| Тесты | `dns/transport/group/trace_test.go`, `state_test.go`; `dns/client_log_effective_lx_test.go` (расширить); `daemon/…_lx_test.go` при наличии инфры; `test/dns_group_trace_lx_test.go` (живой) |

### Upstream-шов (существующая маркированная зона)

| Файл | Правка |
|------|--------|
| `daemon/started_service.proto` | Внутри `lx:begin lx_command`: +3 поля в `DnsQueryEvent` (13–15), +`DnsGroupAttempt`, +RPC `GetDNSGroups` + 3 message |
| `daemon/started_service*.pb.go` | Регенерация `make -f Makefile.lx lx-proto` (артефакт, не правится руками) |
| `dns/client.go` | Та же 3-строчная зона SPEC 035: вызов меняется на `WithQueryTraceIfTracking` |

⚠️ После `lx-proto`: `git checkout -- .` внутри `submodules/wireguard-go`
(gofumpt пачкает сабмодуль) и откат не-нашего шума регенерации.

## Ключевые решения

- Исход пробы: `errors.Is(err, context.DeadlineExceeded)` → `timeout`;
  SERVFAIL (ответом или RcodeError) → `servfail`; прочие err →
  `network_error`; успех (вкл. NXDOMAIN/RcodeError(NXDOMAIN)/пустой) →
  `answered`.
- Проба участника-группы в `attempts` НЕ пишется (листья расскажут);
  определяется по `member.transport.Type() == C.DNSTypeGroup`.
- `PushGroup` — prepend: внешняя группа входит первой, итоговый порядок
  «изнутри наружу» без разворота на эмите.
- Гонка: коллектор держит ctx гонщика (values живут после отмены), пишет
  attempts по мере прихода; поздние — за снимком эмита, теряются штатно.
- `downRemainingMs` считается на момент снимка RPC (`downTime - since(lastFail)`,
  кламп в 0) — не храним абсолютный дедлайн.

## Порядок коммитов

1. `lx(dns-group): query trace holder — write-once effective, group path, attempts (SPEC 036)` — dnstrack + group + client_log + юниты.
2. `lx(dns-group): SubscribeDNSQueries trace fields + GetDNSGroups RPC (proto seam, SPEC 036)` — `.proto` шов.
3. `lx(dns-group): regenerate pb for SPEC 036` — артефакт регенерации отдельно.
4. `lx(dns-group): GetDNSGroups handler + libbox client wrappers (SPEC 036)` — daemon/libbox + тесты.
5. `lx(dns-group): decision log (SPEC 036)` — если не размажется по 1/4 естественно.

## Грабли

- gomobile: никаких срезов/вложенных срезов в экспортируемых типах — только
  итераторы и скаляры.
- `AttemptOutcome`-строки — единый словарь в dnstrack, daemon только
  пробрасывает (никакого маппинга enum→string в двух местах).
- Снимок ctx-холдера обязан копировать срезы — иначе фоновый коллектор
  гонки мутирует память уже отданного события.
