# PLAN 073 — как строим `chain`

Спека: [SPEC.md](SPEC.md). Здесь — файлы, типы, швы, порядок этапов.

## 1. Новые файлы

| Файл | Содержимое |
|------|------------|
| `option/chain_lx.go` | `ChainOutboundOptions{Outbounds []string \`json:"outbounds" reference:"outbound"\`; IdleTimeout badoption.Duration; StripEvasion *bool; Strip map[string]bool; Rewrite map[string]json.RawMessage}` |
| `constant/chain_lx.go` | `TypeChain = "chain"` |
| `include/lx_chain.go` / `lx_chain_stub.go` | регистрация `group.RegisterChain(registry)` под `with_lx_chain`; стаб отдаёт понятную ошибку на `type: chain` |
| `adapter/chain_lx.go` | ctx-ключ привязки; `ChainLeafResolver{ResolveLeaf(ctx, leaf) (adapter.Outbound, error)}`; `ResolveChainLeaf(ctx, picked)` (no-op без привязки / для групп); `PathProvider{Path() []string}`; `EndpointCloneHolder{CloneEndpoints() []adapter.Endpoint}` |
| `protocol/group/chain.go` | `Chain` (Adapter, hops, clones, eviction ticker, `Path()`, `Close`), `RegisterChain`, валидация старта, прогрев |
| `protocol/group/chain_hop.go` | `chainHop` (Outbound с тегом `<chain>#i`; DialContext/ListenPacket по §3.2; `ResolveLeaf`; passthrough для `direct`) |
| `protocol/group/chain_clone.go` | фабрика звеньев: опции из менеджера → копия → strip → rewrite → mtu → detour → реестр → стадии; singleflight; счётная обёртка conn; ключи |
| `protocol/group/chain_transform_lx.go` | каталог `strip` как предопределённые merge-patch'и по типу; применение `rewrite`; сухой прогон; проверка `tls.utls`×`reality` |
| `protocol/group/chain_mtu_lx.go` | `capacity(i−1)`, накладные по типу звена, min-по-группе |
| `protocol/group/chain_test.go`, `chain_transform_lx_test.go`, `chain_mtu_lx_test.go` | юниты |
| `lx-test/chain/` | стенд: 3 локальных ss-сервера + WG-пара (если доступна) — три формы, длина 5, interrupt, eviction, SPEC 054 fallback, UDP-over-TCP ошибка |
| `daemon/started_service_chain_lx.go` (+ `_stub.go`) | заполнение `ChainInfo` в `OutboundInfo` (§3.6) |
| `experimental/clashapi/chain_lx.go` | поля `/proxies/<tag>` для типа `chain` |
| `docs-lx/lx-config.md` §10 | пользовательская дока |

## 2. Правки upstream-файлов (все за маркером `lx:begin chain … lx:end chain`)

1. `adapter/outbound.go` — в `OutboundManager`: `AddInternal(Outbound) error`, `RemoveInternal(tag string)`, `OptionsOf(tag string) (typ string, options any, ok bool)`.
2. `adapter/endpoint.go` — в `EndpointManager`: `OptionsOf(tag)`.
3. `adapter/outbound/manager.go` — `optionsByTag`, `internalByTag`; `Create` сохраняет `{typ, options}`; `Outbound(tag)`: `outboundByTag` → `internalByTag` → `endpoint.Get`.
4. `adapter/endpoint/manager.go` — `optionsByTag`, `Create` сохраняет.
5. `protocol/group/selector.go:150,158` — `picked, err := adapter.ResolveChainLeaf(ctx, s.selected.Load())`.
6. `protocol/group/urltest.go:267,308` — то же для `outbound`.
7. `protocol/group/urltest_penalty_lx.go:232` — то же для `fallback` (lx-файл).
8. `common/trafficcontrol/tracker.go` — в обходе detour-хвоста: если `finalOutbound` реализует `PathProvider` → `detourChain = reverse(Path())`, обход по `Dependencies()` не делается.
9. `daemon/started_service.proto` — `message ChainInfo {...}` + поле в `OutboundInfo`, маркер `lx_command`; регенерация по фиксированному proto-таргету `Makefile.lx`.
10. `Makefile.lx` (`LX_TAGS` += `with_lx_chain`), `cmd/internal/build_libbox/main.go` (`sharedTags`).

Отдельные атомарные коммиты на каждую зону (IMPLEMENTATION_PROMPT).

## 3. Ключевые решения реализации

- **Звено создаётся без контекстных трюков**: `detour` — штатное поле опций
  (`option.DialerOptionsWrapper.TakeDialerOptions/ReplaceDialerOptions`),
  резолв тега — штатный `dialer.DetourDialer` через `OutboundManager.Outbound`.
  Именно поэтому хопы должны быть видны `Outbound(tag)` → внутренний реестр.
- **Копия опций**: `reflect.New(elem).Elem().Set(*orig)` (поверхностно;
  `DialerOptions` встроена по значению). `strip`/`rewrite` — через JSON
  round-trip (marshal → merge-patch → unmarshal в `registry.CreateOptions(type)`),
  так валидация типов/неизвестных полей достаётся от декодера.
- **TLS-контейнер**: `option.OutboundTLSOptionsContainer`
  (`TakeOutboundTLSOptions/ReplaceOutboundTLSOptions`) — для проверки
  `reality`×`utls`; сами патчи — JSON.
- **Endpoint-звенья**: реестр endpoint'ов (`EndpointRegistry.Create`),
  стадии `adapter.ListStartStages` через `LegacyStart`; `InitializeDetour`
  в `Start` (SPEC 029) резолвит `<chain>#(i−1)` — хоп зарегистрирован
  раньше, в `Chain.Start` до прогрева.
- **Обход ENERGY/закрытия**: места, где идёт `r.endpoint.Endpoints()`
  (idle-тик `route/reachability_lx.go`, пауза `route/network.go`, quiesce
  SPEC 030) дополняются `CloneEndpoints()` всех outbound'ов-`EndpointCloneHolder`.
  Это lx-файлы/зоны — без новых маркеров, кроме `network.go`, если там
  маркера нет.
- **Счётчик соединений**: обёртка над `net.Conn`/`net.PacketConn` со
  `sync/atomic`, `Close` декрементит; `lastPicked` — atomic time.
- **Эвикшн**: один тикер на цепочку (период = `idle_timeout/4`, min 15s);
  под мьютексом карты: `active==0 && idle` → `Close` вне мьютекса после
  удаления из карты; singleflight ключа исключает гонку с созданием.
- **Прогрев**: `RealTag`-подобный обход с остановкой на `URLTest` (тип) —
  «детерминирован» = на пути только `Selector`/узлы.
- **Ошибки**: `E.Cause(err, "chain[", tag, "] #", i, " (", leaf, ") via #", i−1, " (", lowerLeaf, ")")` в хопе.
- **pprof-метки**: `pprof.Do(ctx, pprof.Labels(...), func(ctx){ стадии старта })`.
- **Логгер звена**: `logger.With("chain[" + tag + "]#" + i + "/" + leaf)` — по
  доступному API лог-фабрики.

## 4. Этапы

| # | Этап | Выход |
|---|------|-------|
| 1 | Опции, константа, include-гейт, `Chain`/`chainHop` без групп: формы «все узлы» | `[node,node,node]` несёт трафик; юнит + стенд |
| 2 | Менеджер: `OptionsOf`, внутренний реестр; фабрика звеньев через реестр с `detour` | звенья создаются легально; тег-коллизии отвергаются |
| 3 | Привязка + хук в 5 точках; формы с группами и вложенностью | три формы; SPEC 054 fallback в звено (тест) |
| 4 | `direct` passthrough, `block`, валидация типов | укорачивание пути, все-direct ≡ позиция 0 |
| 5 | Счётчик/эвикшн, прогрев, `EndpointCloneHolder` в обходах ENERGY/quiesce | критерии 2, 5, 9 |
| 6 | MTU | критерий 6 |
| 7 | `strip`/`rewrite` + сухой прогон | критерии 7, 8 |
| 8 | Наблюдаемость: `PathProvider`+tracker, `ChainInfo` (proto §3.6), Clash API, ошибки/логи/pprof | критерии 1, 10, 11 |
| 9 | Стенд `lx-test/chain`, дока §10, changelog, DoD | статус I |

## 5. Тесты

- Юниты: копия опций не мутирует оригинал; ключи звеньев; singleflight;
  каталог `strip` и карта; `rewrite` сухой прогон (ошибка на неизвестном
  поле); MTU-таблица (WG/WG v4/v6, WG/vless, WG/селектор, MASQUE/WG);
  прогрев-детерминизм (селектор да, urltest нет); эвикшн (fake clock);
  passthrough; валидация типов/тегов; хук no-op без привязки и для групп.
- Стенд: три формы × TCP/UDP; длина 5; interrupt через группу в середине;
  SPEC 054 fallback; ошибка UDP-над-TCP с позициями; URLTest по `#i`;
  `detourList` = путь; без тега `type: chain` отвергается.
- CI: обе сборки (с тегом и без) — `lx-ci`.

## 6. Риски реализации

- `Manager.Create` из `Start` другого outbound не используется — хопы идут
  через `AddInternal`, без стадий менеджера: исключает двойной PostStart.
- Обход `Endpoints()` в upstream-`route/network.go` — проверить наличие
  lx-зоны; если нет — маркер `chain`.
- Регенерация `.pb.go` — только через фиксированный proto-таргет `Makefile.lx`
  (§3.6 п.5); шум protogen по другим файлам откатывать
  ([[lx-proto-regen-network-and-noise]]).
