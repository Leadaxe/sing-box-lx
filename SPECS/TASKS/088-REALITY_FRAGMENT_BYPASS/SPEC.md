# SPEC: 088 — REALITY_FRAGMENT_BYPASS

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md) · хотфикс в [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — апстримный: `RealityClientConfig.ClientHandshake` строит `utls.UClient` на голом соединении, минуя обёртку `tlsfragment`, которую получают STD- и uTLS-клиенты; `fragment` / `record_fragment` на REALITY-узлах пишутся в конфиг и молча не действуют |
| Статус | D (done, device-verified) — выпущено в `v1.14.1-lx.4`; полевой прогон репортёра LxBox #142 2026-09-18 (Android, мобильная сеть, серверные pcap): с `record_fragment` на сервер приходит целая TLS-запись 131 байт + начало следующей, с `fragment` — первые сегменты 133/164 байт; REALITY-рукопожатие проходит во всех режимах. ⚠️ Лечит ли фрагментация исходную жалобу — не доказано: причина зависаний оказалась состоянием сети, не формой ClientHello |
| Ветка | `lx` |
| Связанные | [060](../060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md) — авто-`record_fragment` под `detour`, чей дефолт до 088 до REALITY не доходил; [083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md) — гибридный key share, из-за которого ClientHello вырос до двух TCP-сегментов; [089](../089-REALITY_KEY_SHARE_OPTION/SPEC.md) — вторая половина ответа на ту же жалобу (`reality.key_share`); LxBox [#142](https://github.com/Leadaxe/LxBox/issues/142) (репортёр нашёл обход сам, по коду) |

**Touches:** `common/tls/utls_client.go` (helper `wrapClientConn`, `Client()` через него), `common/tls/reality_client.go` (`prepareClientHello` — обёртка перед `utls.UClient`; `ClientHandshake` = prepare + handshake), `common/tls/reality_client_lx_test.go` (стражи, общий файл с 089), реестр HOTFIXES, `SPECS/README.md`, [060 §3](../060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md) (примечание), `docs-lx/lx-config.{ru.,}md` §9, `docs-lx/lx-changelog.md`.

## Why

Три клиента в `common/tls` умеют оборачивать соединение в `tlsfragment` перед рукопожатием: STD (`std_client.go`), uTLS (`utls_client.go`) — и всё. REALITY-клиент собирает `utls.UConn` сам, чтобы подменить `SessionId` до отправки, и делает это на голом `conn`:

```go
uConn := utls.UClient(conn, uConfig, e.uClient.id)   // reality_client.go, до 088
```

Поля `fragment` / `recordFragment` / `fragmentFallbackDelay` при этом лежат в `e.uClient` — заполнены из конфига и никем не прочитаны. Следствия:

1. **Явные `"fragment": true` / `"record_fragment": true` на REALITY-узле не работают.** Конфиг принимается, ошибки нет, ClientHello уходит одной записью. Репортёр #142 проверил обе опции на живой сети и увидел в коде, почему они пусты.
2. **Дефолт [060](../060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md) до REALITY не доходил.** `applyDetourFragmentDefault` ставит `RecordFragment = true` до диспетчеризации по движкам (и в 060 записано «STD, uTLS и REALITY получают одинаковый дефолт»), но REALITY-путь флаг не применяет. То есть REALITY-узел за `detour` — самый частый случай «VLESS через плечо» — оставался с целым ClientHello, и порог «1488 проходит, 1502 исчезает» из 060 к нему применим в полный рост.
3. **После [083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md) это стало заметнее.** Гибридный key share `X25519MLKEM768` добавляет 1216 байт: ClientHello `chrome` = 1720 байт, `firefox` = 1885, `safari` = 1533 (замер в тесте, без record-заголовка) — всегда два TCP-сегмента при MSS 1448. До 083 (и у апстрима сегодня) он был 594 байта и умещался в один.

`spoof` REALITY-путь не касается: он отвергается ещё в `newRealityClient` («spoof is unsupported in reality»), поэтому единственное, что теряется, — фрагментация.

## Что делает фикс

Одна точка вместо двух копий: `UTLSClientConfig.wrapClientConn(conn)` — обёртка `tlsfragment` по флагам плюс `applyTLSSpoof`. `UTLSClientConfig.Client()` вызывает её (поведение без изменений), `RealityClientConfig.prepareClientHello` вызывает её перед `utls.UClient`. Никаких новых опций: работают те, что уже в конфиге, и дефолт 060.

Ради тестируемости `ClientHandshake` разделён: `prepareClientHello(conn)` собирает UConn, применяет политику key share ([089](../089-REALITY_KEY_SHARE_OPTION/SPEC.md)) и запечатывает `SessionId` — ничего не отправляя; `ClientHandshake` = prepare + `HandshakeContext` + проверка `verified`. Логика рукопожатия и контракт `AuthKey` 083 не менялись.

## Требования

- **R1.** При `fragment` или `record_fragment` REALITY-клиент пишет ClientHello через `tf.Conn`; без флагов — на голое соединение (лишней обёртки нет).
- **R2.** Дефолт 060 (`DialedThroughDetour` → `record_fragment`) доходит до REALITY без отдельного кода: обёртка читает те же поля `uClient`, которые 060 уже заполняет.
- **R3.** Стражи в `common/tls`: структурный (тип `UConn.NetConn()` по матрице флагов), проводной (первый пакет по `net.Pipe`: без флагов — ровно одна TLS-запись, с `record_fragment` — две и больше) и проводной для дефолта 060 (`NewClientWithOptions` с `DialedThroughDetour` и без флагов → REALITY-hello уходит несколькими записями; идея теста — Alex01d, [PR #23](https://github.com/Leadaxe/sing-box-lx/pull/23), пришедший с тем же фиксом за несколько часов до 088). Все должны падать, если мерж вернёт `utls.UClient(conn, …)` на голый conn.
- **R4.** Существующие тесты `TestUTLSClient_Client_*` (гейт обёртки uTLS-клиента) зелёные — helper не изменил их поведение.

## Критерии приёмки

1. `go test -tags with_utls -ldflags "-checklinkname=0" ./common/tls/` зелёный, включая новые стражи. ✅ 2026-09-17
2. Red-check: с убранным вызовом `wrapClientConn` в REALITY-пути оба стража 088 падают. ✅ (`TestLxRealityClientHelloGoesThroughFragmentConn` — три подтеста из четырёх, `TestLxRealityRecordFragmentSplitsFirstFlight`)
3. `make -f Makefile.lx lx-build` полным `LX_TAGS`. ✅
4. Полевой прогон на сети, где REALITY-хендшейк с гибридом теряется: `record_fragment` на узле — проходит или нет. ⏳ см. «Остаток»

## Реализация — 2026-09-17

Замер на `net.Pipe` (тест, `chrome`, SNI `www.example.com`):

| Режим | Первый Write на проводе | TLS-записей |
|---|---|---|
| без флагов | 1757 байт | 1 |
| `record_fragment` | 1762 байт (+5 на второй заголовок) | 2 |
| `fragment` | 1418 байт первым куском, остальное после паузы | packet-split, как у uTLS-клиента |

`tlsfragment` режет первую запись вокруг SNI (`IndexTLSServerName`), поэтому число записей = число меток SNI до публичного суффикса + 1; с одной меткой — две записи. Экстеншены Chrome перемешаны (`shuffle`), SNI может стоять в любом месте — парсер `tlsfragment` находит его по структуре, а не по смещению, поэтому перемешивание ему не мешает.

## Границы

- Никаких новых ключей конфига. Что и когда фрагментировать — по-прежнему решают `fragment` / `record_fragment` и дефолт 060.
- **Это не обещание обхода DPI.** Если middlebox не собирает сегменты и режет всё, чего не видит целиком, дробление сделает хуже; если он ищет группу или размер в первом сегменте — поможет. Различить может только прогон на конкретной сети (критерий 4). Для сети репортёра #142 ответ пока неизвестен: он тестировал опции, когда они ещё не действовали.
- Серверная сторона REALITY не трогается.

## Остаток

- Полевой прогон `record_fragment` на REALITY-узле в сети, где гибридный ClientHello теряется (репортёр #142, мобильный оператор). Сравнить с [089](../089-REALITY_KEY_SHARE_OPTION/SPEC.md) `classical` на том же узле.
- LxBox: TLS-настройки фрагментации у REALITY-узлов теперь действуют — если UI прятал их для REALITY как бесполезные, показать.

## Условие снятия

Апстрим проведёт `fragment` / `record_fragment` в `RealityClientConfig` сам (сейчас в `upstream/stable` обёртки там нет) — тогда наш helper и вызов в `prepareClientHello` снимаются в пользу апстримной формы. Проверять на каждом мерже: `utls.UClient(` в `reality_client.go` должен получать результат `wrapClientConn`, а не входной `conn`; страж `TestLxRealityClientHelloGoesThroughFragmentConn` падает в CI, если это не так.
