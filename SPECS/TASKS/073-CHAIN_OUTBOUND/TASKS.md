# TASKS 073 — чеклист

Этапы по [PLAN.md](PLAN.md) §4; критерии — [SPEC.md](SPEC.md) §5.

## Этап 1 — каркас, форма «все узлы»
- [x] `option/chain_lx.go`, `constant/chain_lx.go`, `include/lx_chain{,_stub}.go`, тег в `Makefile.lx` и `build_libbox`
- [x] `Chain` + `chainHop`: порядок пакета, `DialContext/ListenPacket → хоп n−1`, хоп 0 как есть
- [x] юнит: `[node,node,node]` строит путь; стенд: трафик TCP+UDP

## Этап 2 — звенья через реестр
- [x] `OptionsOf` в обоих менеджерах (маркер `chain`), внутренний реестр `AddInternal/RemoveInternal`, `Outbound(tag)` падает в него
- [x] фабрика: копия опций → `detour=<chain>#(i−1)` → реестр (outbound/endpoint) → стадии; singleflight; `Tag()` = оригинал; логгер с префиксом
- [x] валидация: ≥2, теги, коллизия `<tag>#i`, типы позиций ≥1
- [x] закрытие от старшего к младшему; снятие внутренних тегов

## Этап 3 — группы
- [x] `adapter/chain_lx.go`: привязка, `ResolveChainLeaf`, `ChainLeafResolver`
- [x] хук в `selector.go` ×2, `urltest.go` ×2 (маркеры), `urltest_penalty_lx.go` ×1
- [x] тесты: три формы, вложенные группы, fallback SPEC 054 уходит в звено, хук no-op без привязки

## Этап 4 — direct/block
- [x] passthrough для `direct` на позициях ≥1; все-direct ≡ позиция 0; `block` терминален
- [x] отказ на `dns`/вложенный `chain` на позициях ≥1

## Этап 5 — жизненный цикл
- [x] счётная обёртка conn, `lastPicked`, тикер эвикшна, `idle_timeout=0`
- [x] прогрев детерминированных позиций (селекторы да, urltest нет); ошибка прогрева = ошибка старта
- [x] `EndpointCloneHolder` в idle-тике, паузе сети, quiesce (SPEC 020/030)
- [x] тесты: удержание при живом потоке, удаление при нуле+T, interrupt через группу

## Этап 6 — MTU
- [x] `capacity/overhead`, min-по-группе, применение к WG/MASQUE звеньям; предупреждение tuic-native
- [x] юниты таблицы; ~~стенд WG над WG~~ — WG-пары на стенде нет, MTU-правила покрыты юнитами на настоящих `WireGuardEndpointOptions`

## Этап 7 — strip/rewrite
- [x] каталог как merge-patch'и; карта `strip`; `rewrite` по типу; порядок strip→rewrite→MTU
- [x] сухой прогон на старте; `tls.utls`×`reality` — ошибка; неизвестный ключ — ошибка
- [x] проверка: `fragment` снят, `record_fragment` SPEC 060 активен

## Этап 8 — наблюдаемость
- [x] `PathProvider` + ветка в tracker (`detourList`)
- [x] `ChainInfo`: proto (маркер `lx_command`), регенерация фикс-таргетом, handler `_lx.go` + stub, Clash API поля
- [x] ошибки с позициями; логи create/evict/path-changed; pprof-метки при старте звена
- [x] URLTest по `<tag>#i` (проверить, что `#i` не в `Outbounds()`)

## Этап 9 — приёмка
- [x] стенд `lx-test/chain/` по матрице SPEC §5
- [x] `go vet`, gofmt lx-файлов, `make -f Makefile.lx lx-build`, сборка без тега
- [x] `docs-lx/lx-config.md` §9 (+ `.ru.md`); changelog; IMPLEMENTATION_REPORT.md; статус → I
