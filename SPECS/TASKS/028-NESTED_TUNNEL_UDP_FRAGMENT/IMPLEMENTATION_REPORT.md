# IMPLEMENTATION REPORT: 028 — NESTED_TUNNEL_UDP_FRAGMENT

## Итог

Задача «masque/awg/wg в любых комбинациях через `detour`» сведена к одному
дефекту: нижнее (реальное) UDP-плечо цепочки открывалось с принудительным DF
(`common/dialer` default → `DisableUDPFragment()`), и оверсайз внешней
датаграммы — норма для вложенных туннелей — молча дропался вместо
фрагментации. Фикс: `wireguard`-endpoint и `masque`-outbound отказываются от
DF-дефолта (`options.UDPFragmentDefault = true`), как это уже делают
direct/hysteria/hysteria2/tuic. Явный `udp_fragment` в конфиге сильнее.

## Изменения кода

| Файл | Что |
|---|---|
| `protocol/wireguard/endpoint.go` | `UDPFragmentDefault = true` в начале `NewEndpoint`; `UDPFragmentDefault: true` в per-interface `CreateDialer` (system-режим) |
| `protocol/masque/outbound.go` | `UDPFragmentDefault = true` перед `dialer.New` |
| `common/dialer/udp_fragment_lx_test.go` (+`_df_darwin/_df_linux/_df_other`) | юнит: DF-флаг реального сокета через getsockopt, оба пути (dial + listener-control), 3 кейса |
| `test/wireguard_detour_lx_test.go` | e2e-стенд AWG-over-AWG через detour, 2 MTU-режима |
| `test/go.mod` | `replace sagernet/wireguard-go => ../submodules/wireguard-go` (replace главного модуля не наследуется; без него AWG-конфиг в тестах падает `invalid UAPI device key: jc`) |
| `docs-lx/lx-config.md` / `.ru.md` | §MTU: поведение при превышении бюджета теперь фрагментация, не `message too long` |
| `docs-lx/lx-changelog.md` | секция `#### v1.14.0-lx.12` |
| `SPECS/README.md` | строка 028 в Roadmap |

## Верификация

- `go test ./common/dialer/ -run TestUDPFragment` — 3/3 PASS (darwin;
  linux-хелпер собирается, гоняется в lx-ci).
- `cd test && go test -tags with_gvisor,with_wireguard,with_awg -run
  TestAWGOverAWGDetour_LX` — PASS: fits (1420/1280) 2.7s, fragments
  (1280/1280, IP-фрагментация на detour-стеке в обе стороны) 4.1s;
  TCP+UDP ping-pong + large-data через двойной AWG-туннель.
- `gofmt -l` по затронутым lx-файлам — чисто; `go vet` затронутых пакетов —
  чисто; полная сборка с lx-тегами (минус `badlinkname`, go1.25-хост) — ок.

## Инварианты (соблюдены)

- Явный `udp_fragment` бьёт протокольный default в обе стороны (юнит-кейс). ✔
- Прочие протоколы и listener-инбаунды: дефолты не тронуты. ✔
- Detour-плечи (gVisor) и AWG-datapath (s4/padding/magic) не изменены. ✔
- MASQUE direct: размер датаграмм по-прежнему ограничен QUIC (≤1452). ✔

## Остаток (owed)

- **Field-тест CPH2411** (SPEC §6): подтвердить на реальном пути телефон→дом
  MASQUE-over-AWG и AWG-over-AWG + отсутствие регресса плоских нод.
- Наблюдать DPLPMTUD MASQUE через detour (SPEC §5) — фикс отдельной задачей,
  только если field покажет деградацию.
