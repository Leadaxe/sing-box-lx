# SPEC: 123 — UTLS_RANDOMIZED_CONSISTENT

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — `fingerprint: "randomized"` раз на процесс выбрасывает ClientHello без гибридного шара или без ALPN; узел мёртв до рестарта |
| Статус | N (new) — 2026-10-10: спека, код не начат |
| Ветка | `lx` |
| Build-tag | нет (`with_utls`, как весь `common/tls/utls_*`) |
| Источник | [shtorm-7/sing-box-extended#162](https://github.com/shtorm-7/sing-box-extended/issues/162) п.2 — патч podkop-engine `0010-utls-keep-the-randomized-fingerprint-consistent`; разбор по нашему дереву ниже. Патч не скачался (сеть), спека написана по нашему коду |
| Связанные | [083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md) (Xray ≥ v26.9.8 требует гибрид), [089](../089-REALITY_KEY_SHARE_OPTION/SPEC.md) (`key_share`), [086](../086-UTLS_FORK_FIREFOX148/SPEC.md) (форк utls) |

**Touches:** `common/tls/utls_client.go` (одна `// lx:`-строка в `init`), новый `common/tls/utls_randomized_lx.go`, страж в `common/tls/utls_firefox148_lx_test.go` (или новый `utls_randomized_lx_test.go`), строка 123 в реестре `SPECS/README.md`, строка `randomized` в таблицах `SPECS/FEATURES/017-REALITY/FEATURE.md` и `docs-lx/protocols-transports.{ru.,}md` §5.2. Форк `submodules/utls` не трогается.

## 1. Проблема

`randomized` = `utls.HelloRandomized` с весами из `init` (`common/tls/utls_client.go:372-377`): `DefaultWeights`, TLS 1.3 принудительно, P-256 первым шаром выключен. Seed — один на процесс (`NewPRNGSeed` в `init`), поэтому выпавший спек один для всех соединений всех узлов с `randomized` до рестарта.

Спек строит `generateRandomizedSpec` (`submodules/utls/u_parrots.go:3320-3528`). Три независимые монеты решают то, что должно решаться вместе:

| Что | Где | Вес | Монета |
|---|---|---|---|
| `X25519MLKEM768` в `supported_groups` | `u_parrots.go:3426` | `CurveIDs_Append_X25519` = 0.71 | своя |
| шар `X25519MLKEM768` в `key_share` | `u_parrots.go:3486` | `KeyShare_Append_RandomGroups` = 0.50 | своя (та же монета весом 0.5 перед ней решает шар P-256) |
| расширение ALPN | `u_parrots.go:3350` | `Extensions_Append_ALPN` = 0.70 | своя, только у `helloRandomized`; `helloRandomizedALPN` ставит ALPN всегда |

Распределение по процессам (TLS 1.3 принудительно, `X25519` в группах и шарах всегда):

| Гибрид в группах | Гибридный шар | Доля | Обычный TLS | REALITY → Xray ≥ v26.9.8 |
|---|---|---|---|---|
| да | да | 35,5 % | ок | ок |
| да | нет | 35,5 % | ок (сервер выберет X25519 или пошлёт HelloRetryRequest) | `reality verification failed` — REALITY не умеет HRR, сервер молча проксирует на dest |
| нет | нет | 14,5 % | ок | `reality verification failed` |
| нет | да | 14,5 % | нарушение RFC 8446 §4.2.8 (шар для группы, которой нет в `supported_groups`); реакция строгого сервера не проверялась | не проверялось |

Независимо от групп, в 30 % процессов нет ALPN. Транспорты поверх HTTP/2 — gRPC, XHTTP (h2), `http` — без ALPN не поднимаются: сервер с `alpn: [h2]` выбирает http/1.1 или рвёт рукопожатие. Это касается и обычного TLS, и REALITY: `prepareClientHello` в `common/tls/reality_client.go:231-238` и `utlsALPNWrapper` переписывают ALPN, только если расширение в спеке есть; добавить отсутствующее они не умеют.

Итого `randomized` на REALITY к Xray ≥ v26.9.8 с gRPC/XHTTP жив в ~25 % процессов (0,355 × 0,7), если исход «шар без группы» сервер тоже отвергает. `key_share: "hybrid"` (089) превращает молчание в явную ошибку, но вероятность не меняет.

Xray для `randomized` берёт `HelloRandomizedALPN` (сверка 2026-10-09), ALPN у него есть всегда. Монеты групп и шара в refraction/utls с нашим кодом не сверялись.

## 2. Решение

Строить `randomized` в нашем lx-файле с тремя отличиями от апстрима, форк utls не трогать:

1. `utls.HelloRandomizedALPN` вместо `utls.HelloRandomized` — ALPN всегда. Без `config.NextProtos` спек ставит `h2, http/1.1` (`u_parrots.go:3450-3456`), с ними — их; ALPS (вес 0.33, `h2`) остаётся случайным.
2. `CurveIDs_Append_X25519 = 1` — гибрид всегда в `supported_groups`.
3. `KeyShare_Append_RandomGroups = 1` — гибридный шар всегда. Побочно шар P-256 тоже всегда: ClientHello получает три шара (`X25519MLKEM768`, `X25519`, `P-256`), как у Firefox. Порядок «гибрид перед X25519» уже даёт сам генератор (`u_parrots.go:3487` ставит гибрид в начало списка).

`FlipWeightedCoin(1.0)` возвращает `true` для любого выхода PRNG, кроме нуля `Int63()` (`u_prng.go:145-151`), то есть всегда.

Остальное остаётся случайным, как раньше: набор и порядок шифров, `signature_algorithms`, P-521, padding, status_request, SCT, renegotiation, EMS, ALPS, порядок расширений, минимальная версия TLS.

`AuthKey` REALITY не меняется: чистый `X25519` в шарах есть всегда, `reality_client.go` берёт `Ecdhe` (083). Ключи гибрида и чистого X25519 у `randomized` независимые (маркеров повторного использования, как у Firefox 148, в спеке нет; `u_parrots.go:3228-3285`), так же как у `chrome` — тот проходит на Xray ≥ v26.9.8 (083, device-verified). `Ecdhe` достаётся первому классическому шару, а это `X25519`: P-256 добавляется после него.

### Почему не в форке utls

Связать монеты внутри `generateRandomizedSpec` (одна монета на «гибрид или нет», как делает podkop) — восьмой коммит в форке в файле, куда приходят cherry-pick'и refraction, и ещё одна строка в сверке раннбука §1.1. При этом исход «гибрида нет» REALITY к новому Xray всё равно убивает, так что для нашего клиента монета здесь не нужна. Веса — публичный API utls (`Weights`), правка в ядре переживает бамп библиотеки без переноса.

## 3. Требования

- **R1.** `common/tls/utls_randomized_lx.go` (`with_utls`): `func lxRandomizedFingerprint(seed *utls.PRNGSeed) utls.ClientHelloID` — `HelloRandomizedALPN`, копия `DefaultWeights` с `TLSVersMax_Set_VersionTLS13 = 1`, `FirstKeyShare_Set_CurveP256 = 0` (как сейчас) и п. 2–3 выше. Seed параметром — чтобы страж перебирал семена.
- **R2.** `common/tls/utls_client.go` `init`: три строки построения `randomizedFingerprint` заменяются одной `// lx: SPEC 123`-строкой `randomizedFingerprint = lxRandomizedFingerprint(seed)`; генерация seed остаётся на месте. Ветка `case "randomized"` в `uTLSClientHelloID` не меняется.
- **R3.** Страж (`with_utls`), 500 семян: в каждом ClientHello гибрид есть в `supported_groups` и в `key_share` ровно по разу и стоит перед `X25519`; расширение ALPN есть; при `NextProtos = ["h2"]` ALPN = `["h2"]`, без `NextProtos` — `["h2","http/1.1"]`. Ещё одна проверка: при одном и том же seed спеки двух `UClient` совпадают (seed на процесс остаётся детерминированным).
- **R4.** `TestLxRealityKeyShareClassicalStripsHybrid` и `TestLxRealityKeyShareHybridRequiresShare` (`reality_client_lx_test.go`) дополнить случаем `randomized`: `classical` вырезает гибрид и из групп, и из шаров; `hybrid` проходит.
- **R5.** Доки: строка `randomized` в таблице отпечатков FEATURE 017 и `docs-lx/protocols-transports.{ru.,}md` §5.2 — «X25519MLKEM768, X25519, P-256; ALPN всегда | ходит».

## 4. Критерии приёмки

1. `go test -tags with_utls -ldflags=-checklinkname=0 ./common/tls/` зелёный, страж R3 падает на старом `init` (проверить откатом одной строки).
2. `make -f Makefile.lx lx-build`, `sing-box check` на VLESS+REALITY с `fingerprint: "randomized"`.
3. Стенд 083/086 (Xray на loopback, dest `www.apple.com`): `randomized` × VLESS-TCP-REALITY и × gRPC-REALITY против Xray v26.9.9 → 204 в 10 перезапусках ядра подряд (каждый перезапуск — новый seed). До фикса тот же прогон даёт отказы.
4. `chrome`, `firefox`, `safari`, `random` на проводе без изменений (существующие стражи).

## 5. Риски

- **ClientHello всегда длинный.** Гибридный шар — +1216 байт, P-256 — +65: ~1,9 КБ, два TCP-сегмента. Раньше в половине процессов `randomized` слал короткий ClientHello. Сети, теряющие длинный первый пакет ([089](../089-REALITY_KEY_SHARE_OPTION/SPEC.md), LxBox #142), теперь видят длинный всегда; ручки прежние — `key_share: classical`, фрагментация (088).
- **Меньше разнообразия в одной точке.** Группы и шары у `randomized` теперь постоянны; разброс по остальным полям прежний. Отпечаток «три шара + случайные шифры» не совпадает ни с одним браузером — так было и до правки, `randomized` не мимикрия по определению.
- **ALPS `h2` при ALPN без `h2`.** Если транспорт выставил `NextProtos = ["http/1.1"]` (WS), ALPS с `h2` может выпасть рядом. Было и раньше в 70 % процессов; сервер расширение игнорирует.

## 6. Открытые вопросы к владельцу

- **`random`.** Соседний баг той же природы: пул `random` (`utls_client.go:363-369`) — chrome/firefox/edge/safari/ios, у `edge` и `ios` гибрида нет, REALITY к Xray ≥ v26.9.8 мёртв в 2/5 процессов (FEATURE 017, сверка 2026-10-09). Предлагалось `// lx:`-строкой убрать `edge`/`ios` из пула; решения не было. В эту задачу не входит без решения.
- **Имена Xray.** `randomizednoalpn` и `hello*`-имена у нас не принимаются (`unknown uTLS fingerprint`). Добавлять не предлагается — только фиксирую.

## Условие снятия

Апстрим sing-box сам перейдёт на `HelloRandomizedALPN` и согласует гибрид в группах и шарах (или utls свяжет монеты в `generateRandomizedSpec`) → lx-файл удалить, `init` вернуть к апстриму; страж R3 оставить, если он проходит на апстримном коде.
