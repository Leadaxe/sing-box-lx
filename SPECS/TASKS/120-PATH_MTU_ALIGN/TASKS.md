# TASKS: 120 — PATH_MTU_ALIGN

**Фича:** [015-CHAIN](../../FEATURES/015-CHAIN/FEATURE.md) · план — [PLAN.md](PLAN.md)

## 1. Спека

- [x] 1.1 SPEC.md, PLAN.md, TASKS.md; замер на GL (§1.1 SPEC), разбор 1242 (§1.3)
- [x] 1.2 Строка 120 в Roadmap `SPECS/README.md`; строки в FEATURE 015 и 009

## 2. `common/lxmtu`

- [x] 2.1 Таблицы: `IPUDP`, `TunnelOverhead` (WG 60/80, masque 79/99), `IsTunnelType`, `TunnelDefaultMTU`, QUIC-кламп [1200, 1452], `MASQUEOuterInitialPacketSize`
- [x] 2.2 Режимы `off`/`fill`/`clamp` + `except`; `apply(configured, limit)` → `Decision`
- [x] 2.3 Обход графа по опциям: ёмкость (туннели, tailscale, группы min, транзитивный detour, ∞ по умолчанию, tuic native / chain — предупреждение), мемоизация, защита от цикла
- [x] 2.4 Потребность по типу: wireguard (endpoint, legacy), masque, hysteria2/tuic/hysteria (`QUICOptions` + PMTUD off), openvpn-client/openconnect (предупреждение)
- [x] 2.5 `AlignMap` для `chain` (map-форма после strip/rewrite)
- [x] 2.6 Юниты §6.1 SPEC

## 3. masque

- [x] 3.1 `InitialPacketSize` = `mtu + 51` с клампом; юнит
- [x] 3.2 ICMP Packet Too Big — фактический бюджет (≥ 1280); юнит

## 4. `detour`

- [x] 4.1 `option/lx.go`: `mtu_align` строка-или-объект, валидация, дефолт `clamp`, `LXResolved.MTUAlign`; юниты
- [x] 4.2 `box_lx.go`: пре-пасс после `ResolveLX`, Info/Warn по решениям, ошибка на неизвестный тег в `except`
- [x] 4.3 `box_lx_test.go`: §6.2 SPEC

## 5. `chain`

- [x] 5.1 `mtu.go` на `lxmtu`; политика из контекста; QUIC-звенья получают `initial_packet_size`/PMTUD
- [x] 5.2 `cloneInfo`/`describe` с режимом; `TestChainMTU` зелёный; кейс hysteria2 над WG

## 6. Проверка

- [x] 6.1 `gofmt`, `go vet`, `go test` затронутых пакетов под тегами из PLAN
- [x] 6.2 `make -f Makefile.lx lx-build`; `sing-box check` конфига #37 и трёх форм ключа
- [x] 6.3 `lx-test/chain`
- [x] 6.4 GL: §6.5 (hy2 IPv4 через masque — работает, лог выравнивания 1252; QUIC-проба IPv6 — без изменений), §6.6 (первый пакет 1280 байт: 0/5 при 1242 → 5/6 при `mtu + 51`); сквозной hy2 по IPv6 — нет сервера, за репортёром
- [x] 6.5 IMPLEMENTATION_REPORT.md; статус I

## 7. Доки и issue

- [x] 7.1 `lx-config.ru.md`/`.md` §13 + §0 + §4 + §10; `protocols-transports.*`
- [x] 7.2 FEATURE 015 (контракт MTU → общий механизм), FEATURE 009 (границы WARP/IPv6-фрагменты)
- [x] 7.3 `lx-changelog.md`
- [ ] 7.4 Ответ в issue #37: причина, обход конфигом, фикс в релизе; предупреждение про опубликованный `private_key` — после коммита, по решению владельца
