# SPEC: 102 — UPSTREAM_SYNC_1_14_2

**Фича:** [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | R (sync) — мерж `upstream/stable` v1.14.2 в `lx` по раннбуку; форк-сабмодуль `sing-tun` синхронизирован первым |
| Статус | I (implemented) — мерж `bfb3a73c9` в дереве, дрейф 0, `upstream.version` → 1.14.2 (`8d2d71de6`); сборка без тегов и с полным `LX_TAGS`+`with_lx_idle_suspend`, тесты затронутых пакетов и стенды `lx-test`, `lx-check` по всем конфигам; выпуск в `v1.14.2-lx.1`; прогон на устройстве (§1.4) — при перепине LxBox |
| Ветка | `lx` |
| База | до: `1c27561a6` (v1.14.1-lx.13, merge-base `0d91c4547` = v1.14.1 + 34); после: `upstream/stable` = `af6e64c3b` = тег **v1.14.2** |
| Связано | [095](../095-UPSTREAM_SYNC_1_14_1_PLUS_34/SPEC.md) (предыдущий синк, тот же ритуал), [040](../040-SINGTUN_ACCEPTLOOP_SELFHEAL/SPEC.md) (дельта форка sing-tun), [047](../047-EARLY_RPC_NIL_ROUTER_CRASH/SPEC.md) (шов в `route/network.go`) |

Решение владельца 2026-09-24 (после среза lx.13): «дрейф устранить». На срезе lx.13 `upstream/stable`
был впереди на 7 коммитов и тег v1.14.2.

## 1. Что принёс апстрим

| Коммит | Суть | Наши файлы |
|---|---|---|
| `4abede8ce`, `111ecb348` | Network reset: сброс сети не на каждом старте, а по фактической смене интерфейса (`networkResetPending`, диспетчер под `interfaceUpdateAccess`); `sing-tun` ddaa4ca25e3b убирает purge TCP-NAT из `ResetNetwork` | `route/network.go` — автослияние, швы SPEC 047 и chain (SPEC 073) на месте |
| `6b6c20a24` | `service/resolved`: `interfaceName` вместо `getLink` в резолв-методах, `mDNS.ReverseAddr`, `Exchange` без inbound-метаданных, `deleteCallback` в `RevertLink` | конфликт ×2 — взята форма апстрима (нашей дельты в пакете нет; наша сторона была testing-формой тех же фиксов из 095) |
| `17c52d03b`, `e6a1a3073` | hysteria2: realm STUN через default domain resolver; port hopping не уходит на другой резолвнутый адрес; `sing-quic` 6a3a24d65b99 | `option/hysteria2.go`, `protocol/hysteria2/*`, `go.mod` |
| `83aec4222` | hijack-dns: `ReportConnHandshakeSuccess` / `ReportPacketConnHandshakeSuccess` до обработки | `route/dns.go` — автослияние, шов SPEC 046 не задет |
| — | `adapter.DNSQueryOptionsFrom` удалён | у нас не используется |
| `af6e64c3b` | Bump version 1.14.2; пины `clients/*` | `upstream.version` → 1.14.2 |

`common/dialer/dialer.go` переписан апстримом (126 строк), нашей дельты там нет — автослияние.
`box.go`: перенос регистрации `ConnectionManager` перед `NetworkManager` у нас уже был (095 взял testing-форму), автослияние пустое.

## 2. Ритуал

1. **Сабмодуль первым** (раннбук §1): `submodules/sing-tun` — merge `ddaa4ca25e3b` (3 коммита: auto route IPv6 rules, default interface monitor, system stack network reset) в ветку `lx` форка → `59bb103`; один конфликт `monitor_shared.go` (`interfaceNetworks`: наша сторона = форма 3e03774, апстрим переписал) — взята форма апстрима. Дельта форка после мержа = только `stack_system.go` + `stack_system_selfheal_test.go` (SPEC 040). `go build`/`go test` сабмодуля зелёные; запушен в fork-remote ДО ядра.
2. **Ядро**: `git merge upstream/stable` → 4 конфликта: `go.mod` (наша сторона + два бампа `require`: `sing-quic`, `sing-tun` = строка пина сабмодуля; четыре `replace` на месте), `go.sum` (сторона апстрима + `go mod tidy`; дифф к нашему — только пара строк `sing-quic`), `service/resolved/resolve1.go`, `transport.go` (форма апстрима).
3. **Сверка `go list -m`**: все четыре модуля резолвятся в `./submodules/<name>`.
4. **Сборка/тесты**: `go build ./...`; `go build -tags LX_TAGS,with_lx_idle_suspend ./...`; `go test` `.`, `route`, `service/resolved`, `protocol/hysteria2`, `common/dialer`, `adapter`, `dns`, `option`, `protocol/wireguard`, `transport/wireguard` без тегов и `.`, `route`, `daemon`, `protocol/wireguard`, `experimental/libbox`, `common/tls` с полными тегами (страж utls-форка зелёный); `make -f Makefile.lx lx-check` (9 конфигов); стенды `lx-test/{chain,initerr,startclose,zombie}`; `gofmt -l` пуст.
5. **§2a**: Go-тулчейн и CI-скрипты апстрим не менял (диффа в `.github`/скриптах нет) → dry run не требуется.

## 3. За чем следить

- `route/network.go`: апстрим перенёс регистрацию колбэка монитора в `StartStateInitialize` и первый диспатч под `interfaceUpdateAccess` в `StartStateStart`; наш `started`-флаг (0df95da5a) и nil-guard SPEC 047 в `InterfaceUpdated`-пути остались. Прогон смены сети на устройстве — при перепине LxBox.
- `service/resolved` — линия «stable бэкпортит то, что в testing» (память): дельты форка в пакете нет, при следующем синке брать сторону апстрима не глядя.

## 4. Критерии приёмки

1. Дрейф от `upstream/stable` = 0 (merge-base == tip). ✅
2. Сборка и тесты по §2 п. 4. ✅
3. `upstream.version` = 1.14.2; линия релизов — `v1.14.2-lx.N` с N=1. ✅
4. Выпущено в `v1.14.2-lx.1` с релиз-нотами; прогон §1.4 на устройстве — за приложениями.
