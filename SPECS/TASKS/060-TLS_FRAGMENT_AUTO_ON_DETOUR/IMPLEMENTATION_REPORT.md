# IMPLEMENTATION_REPORT — SPEC 060 `lx.sniff_guard`

Состояние на 2026-11-13: код закоммичен в `lx` (`c2c0b5aff` lx-код, `9eea56364` вставки в
апстримные файлы, `a28594a58` docs-lx; правка таймера hysteria2/tuic — в первом), не запушен. Спека — [SPEC.md](SPEC.md), контракты —
[PLAN.md](PLAN.md), шаги — [TASKS.md](TASKS.md).

## 1. Файлы

**Новый пакет**

- `common/lxsniffguard/` — хранилище меток: `guard.go`, `node.go`, `attempt.go`, `sni.go` и тесты.

**Новые lx-файлы**

- `option/lx_sniff_guard.go` — разбор, проверка, канонизация, resolved-типы.
- `box_lx_sniff_guard.go` — факты об узлах и носителях из опций, проверки загрузки, создание
  хранилища, поздний подъём кеша.
- `adapter/outbound/sniff_guard_lx.go` — обёртка outbound'а.
- `common/tls/sniff_guard_lx.go`, `sniff_guard_utls_lx.go` — TLS-хук, писатель метода `delay`.
- `common/lxdatagram/delay.go` — задержка второго Initial.
- `experimental/cachefile/sniff_guard_lx.go` — бакет `persist`.
- `transport/v2rayxhttp/sniff_guard_lx.go` — перенос попытки в контекст запросов XHTTP.
- Тесты рядом с каждым.

**Переписанный lx-код**

- `common/lxdatagram/decoy.go`, `truncate.go` — политика и память приманки удалены, швы на
  хранилище.
- `protocol/trusttunnel/sniff_decoy_lx.go`, `transport/wireguard/handshake_burst_lx.go`,
  `adapter/carrier_lx.go`, `common/dialer/carrier_lx.go`, `common/dialer/conntest_lx.go`,
  `box_lx.go`.

**Вставки `// lx: SPEC 060` в апстримные файлы (зона мержа)**

| Файл | Что |
|---|---|
| `adapter/outbound/registry.go` | тег узла в контекст сборки; результат конструктора через обёртку |
| `box.go` | одна строка: подъём кеша меток после стадии Initialize внутренних сервисов |
| `route/network.go` | одна строка в `ResetNetwork` + импорт |
| `experimental/cachefile/cache.go` | имя бакета в списке известных |
| `common/dialer/detour.go` | две строки: шов носителя возвращает контекст без попытки узла |
| `common/tls/client.go` | хук в `ClientHandshake` и в `defaultDialer`; старый дефолт удалён, `ClientOptions` и `NewDialerFromOptions` — в апстримной форме |
| `common/tls/std_client.go`, `utls_client.go` | поле «узел сделал явный выбор», его копия в `Clone`, условие узлового разреза |
| `option/lx.go` | поле `sniff_guard` в `LXOptions` и `LXResolved`; сторож удалённого `quic_sniff_decoy` |
| `protocol/hysteria2/outbound.go`, `protocol/tuic/outbound.go` | аргумент существующей строки SPEC 150 |
| `protocol/masque/outbound.go` | `connectH3Once` / `connectH2Once` через хранилище |
| `protocol/trusttunnel/outbound.go` | хук узла |
| `transport/v2rayxhttp/client.go`, `conn.go`, `http3.go`, `http3_stub.go` | шов h3 и контекст запросов |
| `transport/wireguard/client_bind.go`, `endpoint.go` | путь с тегом носителя, решение раз на поколение сокета |

`transport/trusttunnel/client*.go` — файлы в апстримной форме без маркеров (FEATURE 018):
интерфейс `QUICDialHook` стал `Dial` + `Handshake`, h2 идёт через `common/tls.ClientHandshake`.

**Вернулись к апстриму:** `protocol/vmess`, `anytls`, `shadowtls`, `http` — `outbound.go`
побайтно как в `upstream/stable`; у `vless` и `trojan` снято поле `DialedThroughDetour`.

**Удалено:** `common/tls/detour_fragment_lx_test.go`; типы `LXQUICSniffDecoy*`;
`checkSniffDecoyDetourTypes`.

Зависимости (`go.mod`, сабмодули) и build-tag проводка не менялись.

## 2. Проверка

Go из `go.version`, `-ldflags "-checklinkname=0"`, теги — `make -f Makefile.lx lx-print-tags`.

| Что | Результат |
|---|---|
| `go build ./...` под `LX_TAGS` | чисто |
| `go build ./...` без тегов | чисто |
| `go vet` — `lxsniffguard`, `lxdatagram`, `adapter/outbound`, `common/tls` | чисто |
| Тесты: `common/lxsniffguard`, `common/lxdatagram`, `common/dialer`, `adapter/...`, `option`, `common/tls`, `common/tlsfragment`, `experimental/cachefile`, `protocol/masque`, `protocol/trusttunnel`, `protocol/tuic`, `transport/wireguard`, `transport/trusttunnel`, `transport/v2rayxhttp`, корневой `box` | зелёные |
| Путь `sing-box check` (разбор конфига + `box.New`) | тестом `box_lx_sniff_guard_check_test.go`: полный блок загружается, каждая ошибка загрузки срабатывает с именем ключа |
| `gofmt` изменённых файлов | чисто |

`TestDevicePauseLeavesSocketOpen` (`transport/wireguard`, файл задачей не тронут) один раз упал
в общем прогоне на предусловии «порт занят» и четыре раза подряд прошёл отдельно — зависит от
нагрузки машины.

Полный `go test ./...`, матрица тегов и линтеры не запускались.

## 3. Не сделано

- Стенды SPEC §6 пройдены на loopback 2026-11-13 ([TEST_PLAN.md](TEST_PLAN.md)); AmneziaWG — там же;
  не покрыты masque/WARP, личный отказ узла, смена сети (эти — юнит-тестами), настоящий роутер с
  прозрачным прокси.
- Полевые прогоны; приёмка; коммиты; запись в `docs-lx/lx-changelog.md` (нет версии релиза).
- P1 проверен по тождеству conn и форме на проводе, а не побайтным сравнением с апстримным
  ClientHello: случайные поля и ML-KEM не воспроизводятся.

## 4. Отступления от SPEC на момент написания кода

Все внесены в SPEC и FEATURE; список — чтобы было видно, что менялось по ходу.

- Отпечаток релея сверяется при подъёме кеша, а не при первом дайле: apply создаёт новый box.
- TLS-хук — не одна вставка: ещё по несколько строк в движках std и uTLS.
- hysteria2/tuic открывают соединение с контекстом сборки, а не дайла: шов берёт узел по тегу и
  отчитывается при следующем новом соединении.
- masque h2 судится своим `Node.Dial` вокруг подъёма сессии; trusttunnel h2 и XHTTP h2 — по всему
  дайлу обёртки (открыто в SPEC §7).
- AmneziaWG попадает под блок и при непустом `i1`.
- По итогам стенда: успех hysteria2/tuic засчитывается по таймеру в 30 с, а не при следующем
  новом соединении; `fragment` защищает и на прямом пути (FEATURE утверждала обратное).

## 5. Вне скоупа

Серверная сторона; линия `lx-1.15`; генераторы конфига LxBox и singbox-launcher (обязаны
добавить блок вместе с бампом ядра — SPEC §1 «Переход»).
