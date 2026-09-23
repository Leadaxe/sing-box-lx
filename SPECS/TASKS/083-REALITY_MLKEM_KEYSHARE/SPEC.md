# SPEC: 083 — REALITY_MLKEM_KEYSHARE

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md) · хотфикс в [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — апстримный костыль, протухший вместе с зависимостью; жив в upstream (issue [SagerNet/sing-box#4520](https://github.com/SagerNet/sing-box/issues/4520) без реакции) |
| Статус | D (done, device-verified) — стенд на Mac 2026-09-13: Xray v26.9.9 (после отсечки), v26.7.28 и v26.7.11 (до); выпущено в v1.14.0-lx.36. Фикс покрывает **только chrome-семейство**: у остальных отпечатков гибридного шара нет в самой utls — разбор и следствия в §5a; `firefox` закрыт продолжением [086](../086-UTLS_FORK_FIREFOX148/SPEC.md) (форк utls с Firefox 148, стенд 2026-09-16) |
| Связанные | [053](../053-REALITY_MIN_CLIENT_VER/SPEC.md) — предыдущая тихая отсечка REALITY, закрыта полевым прогоном на этом же стенде; [086](../086-UTLS_FORK_FIREFOX148/SPEC.md) — продолжение: Firefox 148 через форк utls (§5a); issue [#22](https://github.com/Leadaxe/sing-box-lx/issues/22) |

Xray-сервер начиная с v26.9.8 отвергает ClientHello, в котором нет key_share
`X25519MLKEM768`. Апстримный sing-box сам вырезает этот шар из приветствия перед
рукопожатием и потому **тихо** отбрасывается — подменой на камуфляжный сайт,
на клиенте это `reality verification failed`. Фикс — перестать вырезать шар и
считать `AuthKey` по тому же ключу, что выбирает сервер.

Build-tag: нет (правка внутри `with_utls`). Scope: **client-only**.

---

## 1. Проблема

### 1.1 Что изменилось на стороне Xray

Коммит `XTLS/REALITY@8cdf7bf` (RPRX, 2026-09-08, «Reject outdated/strange Client
Hello that doesn't have X25519MLKEM768 before optional X25519») переписал выбор
пирового ключа в `tls.go` (`Server()`):

```go
var peerPub, peerPub2 []byte
for _, keyShare := range hs.clientHello.keyShares {
    if keyShare.group == X25519MLKEM768 && len(keyShare.data) == mlkem.EncapsulationKeySize768+32 {
        if peerPub2 != nil { peerPub2 = nil; break } // ensure once
        peerPub2 = keyShare.data[mlkem.EncapsulationKeySize768:]
        continue
    }
    if keyShare.group == X25519 && len(keyShare.data) == 32 {
        if peerPub != nil { peerPub2 = nil; break } // ensure once
        peerPub = keyShare.data
        break // ensure order
    }
}
if peerPub2 == nil {
    break // reject outdated/strange Client Hello that doesn't have X25519MLKEM768 before optional X25519
}
if peerPub == nil {
    peerPub = peerPub2 // secondary choice: X25519 in X25519MLKEM768
}
```

Три следствия:

- гибридный шар **обязателен**, и он должен стоять **раньше** чистого X25519
  (`break // ensure order` после X25519 не даёт увидеть гибрид, идущий следом);
- каждый из двух шаров допустим **не более одного раза**;
- `AuthKey` считается по чистому X25519, если он есть, иначе — по X25519-части
  гибрида (`peerPub2`). До коммита порядок был тот же, но без обязательности.

Xray-core пинит `github.com/xtls/reality v0.0.0-20260908062103-8cdf7bf9c7f0`
в **v26.9.8** и **v26.9.9**; v26.7.28 и раньше — `…20260322125925-9234c772ba8f`
(до отсечки). Обновлённый сервер получает отсечку без настройки и без ручки.

### 1.2 Почему у нас нет гибридного шара

`common/tls/reality_client.go`, `ClientHandshake`: после `BuildHandshakeState`
апстрим проходит по расширениям и **вырезает** `X25519MLKEM768` из
`SupportedCurvesExtension` и `KeyShareExtension`, затем собирает состояние
заново. Это костыль апстримного коммита `dbdcce20a` «Update utls to v1.7.2»:
та utls не различала X25519-ключ гибрида и чистого шара в `KeyShareKeys`, и
`AuthKey`, посчитанный по неверному ключу, ломал верификацию. Проще было
убрать гибрид совсем — REALITY-серверы того времени его не требовали.

Апстрим sing-box сломан ровно так же (issue #4520, 2026-09-11). Именно про этот
фильтр RPRX пишет как про «обрезку» ClientHello у клиентов sing-box.

### 1.3 Почему теперь безопасно снять

Наш пин — `github.com/metacubex/utls v1.8.7`. Там ключи разложены по
раздельным полям (`u_public.go`):

```go
type KeySharePrivateKeys struct {
    CurveID    CurveID
    Ecdhe      *ecdh.PrivateKey        // чистый X25519 (первый не-GREASE, не-гибрид)
    Mlkem      *mlkem.DecapsulationKey768
    MlkemEcdhe *ecdh.PrivateKey        // X25519-часть X25519MLKEM768
}
```

`u_parrots.go` при сборке `KeyShareExtension` кладёт ключ гибрида в
`Mlkem`/`MlkemEcdhe`, а первый обычный шар — в `Ecdhe`; путаницы utls 1.7.2
больше нет. Спека `HelloChrome_133` (= `HelloChrome_Auto`, все `chrome*`
имена ядра) шлёт шары в порядке `GREASE, X25519MLKEM768, X25519` — ровно то,
что требует сервер, каждый по одному разу.

Выбор ключа для `AuthKey` зеркалит серверный порядок: `Ecdhe`, а при `nil` —
`MlkemEcdhe`. Так же считают Xray-клиент и mihomo. Для Chrome в ClientHello
есть чистый X25519 → обе стороны берут его; для отпечатка только с гибридом
обе стороны взяли бы X25519-часть гибрида. Совпадение по построению.

Старые серверы регрессии не получают: клиент шлёт оба шара, REALITY до
`8cdf7bf` берёт первый чистый X25519 — тот же ключ. Подтверждено прогоном (§2).

### 1.4 Симптом

Тот же класс, что в SPEC 053 §1.3: провал условия не рвёт соединение, сервер
прозрачно проксирует его на `dest` с настоящим сертификатом, клиент видит
`uConn.Verified == false` → `reality verification failed`. На стороне Xray
(`show: true`) — только `forwarded SNI` без строк `AuthKey`/`ClientVer`, затем
`processed invalid connection … authentication failed or validation criteria
not met`. Неотличимо от чужого ключа или неверного shortId.

### 1.5 Зона поражения

Все REALITY-клиенты форка (VLESS, trojan, vmess, XHTTP-поверх-REALITY): фильтр
живёт в одном месте. После снятия фильтра судьбу узла решает **отпечаток**:
гибридный шар несут только спеки, где он заложен.

| `utls.fingerprint` ядра | Спека utls v1.8.7 | key_share | Xray ≥ v26.9.8 |
|---|---|---|---|
| `chrome`, `""`, `chrome_psk`, `chrome_psk_shuffle`, `chrome_padding_psk_shuffle`, `chrome_pq`, `chrome_pq_psk` | `HelloChrome_133` | GREASE, **X25519MLKEM768**, X25519 | ходит |
| `firefox` | `HelloFirefox_120` | X25519, P-256 | отвергается |
| `edge` | `HelloEdge_85` | X25519 | отвергается |
| `safari` | `HelloSafari_16_0` | X25519 | отвергается |
| `ios` | `HelloIOS_14` | X25519 | отвергается |
| `android` | `HelloAndroid_11_OkHttp` | X25519 | отвергается |
| `360` | `Hello360_7_5` | X25519 | отвергается |
| `qq` | `HelloQQ_11_1` | X25519 | отвергается |
| `random` | один из Chrome/Firefox/Edge/Safari/iOS на процесс | как у выпавшего | ходит в 1 случае из 5 |
| `randomized` | генератор `HelloRandomized` | гибрид добавляется монетой `KeyShare_Append_RandomGroups` = 0.5 | ~половина процессов |

Гибрид есть только у `HelloChrome_131`/`HelloChrome_133`; `HelloChrome_115_PQ`/
`120_PQ` несут `X25519Kyber768Draft00`, который сервер не считает за гибрид.
У Xray `fp=firefox` при этом работает: его uTLS — другая ветка, где
`HelloFirefox_Auto = HelloFirefox_148` с гибридом (§5a, продолжение — [086](../086-UTLS_FORK_FIREFOX148/SPEC.md)). Ядро отпечаток **не подменяет** (см. Границы); приложение
подменяет на своём уровне — LxBox §281.

## 2. Доказательство

Стенд 2026-09-13, macOS arm64, всё на loopback: Xray-сервер (VLESS+REALITY,
`xtls-rprx-vision`, `dest`/`serverNames` = `www.apple.com`, `show: true`),
ядро `go build -tags "with_utls with_quic with_gvisor" ./cmd/sing-box` до и
после патча, `mixed`-inbound, `curl -x socks5h://… http://www.gstatic.com/generate_204`
×3 на каждый отпечаток.

| Ядро | Xray | chrome | firefox | safari | ios | random |
|---|---|---|---|---|---|---|
| до патча | v26.9.9 | ✗ verification failed | ✗ | ✗ | ✗ | ✗ |
| **после патча** | **v26.9.9** | **204 204 204** | ✗ (нет гибрида) | ✗ | ✗ | ✗ |
| до патча | v26.7.28 | 204 | 204 | 204 | 204 | 204 |
| после патча | v26.7.28 | 204 | 204 | 204 | 204 | 204 |
| до патча | v26.7.11 | 204 | 204 | 204 | 204 | 204 |
| после патча | v26.7.11 | 204 | 204 | 204 | 204 | 204 |

- В логе Xray v26.9.9 до патча: `forwarded SNI: www.apple.com` без `AuthKey`,
  далее `authentication failed or validation criteria not met` — отсечка по
  ключу, не по версии/shortId. После патча для chrome — `hs.c.conn == conn: true`.
- Контроль Xray v26.9.9 «сам с собой» (клиент Xray, `fingerprint: chrome`) — 204.
- **SPEC 053 закрыта тем же стендом**: v26.7.11 — первый релиз с дефолтом
  `minClientVer = 26.3.27`. Наше ядро (`26.3.27`) — 204 на всех отпечатках;
  контрольная сборка с апстримными `1.8.1` — `hs.c.ClientVer: [1 8 1]`,
  `hs.c.conn == conn: false`, `reality verification failed`.
- Ловушка стенда: с `dest = www.microsoft.com:443` серверы REALITY **до**
  `8cdf7bf` (v26.3.27, v26.7.11, v26.7.28) не завершают рукопожатие даже с
  Xray-клиентом той же версии (`handshake did not complete successfully` сразу
  после `Certificate: 8273`). В ленте REALITY тех же суток есть #33 «Increase
  target TLS record buffer to 17 KiB» — вероятная причина; к нашему клиенту
  отношения не имеет, `dest` для стенда брать с коротким сертификатом.

Unit: `go test -tags with_utls ./common/tls/` зелёный; `go vet` чистый.

## 3. Требования

- `ClientHandshake` не трогает `SupportedCurvesExtension`/`KeyShareExtension`:
  отпечаток шлёт то, что заложено в его спеке utls. Второй
  `BuildHandshakeState` не нужен.
- `AuthKey` считается по `keyShareKeys.Ecdhe`, при `nil` — по
  `keyShareKeys.MlkemEcdhe`; `nil` обоих — прежняя ошибка `nil ecdheKey`.
- Совместимость со старыми серверами сохраняется без ручек в конфиге.
- Маркер `// lx: SPEC 083` на обеих правках — файл апстримный и активно
  правится, встречный мерж не должен вернуть фильтр молча.

## 4. Критерии приёмки

- Сборка и тесты `common/tls` под `with_utls` зелёные. ✅
- Против Xray ≥ v26.9.8 с `fingerprint: chrome` — HTTP через туннель. ✅ (§2)
- Против Xray до `8cdf7bf` (v26.7.11, v26.7.28) — все отпечатки без регрессии. ✅ (§2)
- SPEC 053: полевой критерий «Xray ≥ v26.7.11 с незаданным `minClientVer`
  ходит» — ✅ (§2), статус 053 переведён.

## 5. Границы

- Серверная сторона REALITY (`reality_server.go`) вне scope — форк client-focused.
- Ядро **не подменяет** отпечаток при REALITY: `fingerprint` — явный выбор
  пользователя. Не-chrome отпечатки мертвы на Xray ≥ v26.9.8 из-за самой
  библиотеки utls, а не нашего кода (разбор — §5a; ⚠️ прежняя формулировка
  «Xray сам ведёт себя так же» **неверна**: у Xray другая ветка utls, где
  `HelloFirefox_Auto = HelloFirefox_148` с гибридным шаром, и там `fp=firefox`
  работает). Подмена и предупреждение — уровень приложения:
  LxBox §281 (при REALITY отпечаток не из chrome-семейства → `chrome` +
  предупреждение на ноде). Ручку «принудительно chrome при reality» в ядро
  не вводим — у выбора нет второго осмысленного значения.
- ML-DSA-65 (`mldsa65Seed` на сервере / `mldsa65Verify` у клиента,
  Xray ≥ v25.x) — необязательная постквантовая подпись сертификата поверх
  ed25519-HMAC; без неё верификация идёт как раньше. К этой поломке отношения
  не имеет, в scope не входит.
- `X25519Kyber768Draft00` (спеки `chrome_pq` старых utls) сервером за гибрид не
  считается — ничего не делаем, у нас эти имена и так схлопнуты в `HelloChrome_133`.

## 5a. Граница фикса: не-chrome отпечатки (исследование 2026-09-16)

Полевой отчёт (issue лаунчера [#124](https://github.com/Leadaxe/singbox-launcher/issues/124)):
узел провайдера отдаёт `fp=firefox` прямо в ссылке, и на Xray ≥ v26.9.8 такой
узел не поднимается ни на одной версии ядра, хотя `fp=chrome` после этой SPEC
даёт 204. Причина — вне нашего кода, и снять её в нашем слое нечем.

**Гибридный шар `X25519MLKEM768` есть только у двух пресетов `metacubex/utls` v1.8.7:**
`HelloChrome_133` (= `HelloChrome_Auto`) и `HelloChrome_131`; `HelloChrome_120_PQ` несёт
`X25519Kyber768Draft00`, который сервер за гибрид не считает (§1.5). У
`HelloFirefox_Auto = HelloFirefox_120` его нет; `safari`/`ios`/`android`/`edge`/
`360`/`qq` — тоже без него. Поэтому эта SPEC починила chrome и **не могла**
починить остальных: у них шара нет в спецификации ClientHello, вырезать или
сохранять нечего.

**Почему ядро на `metacubex/utls`.** Это не наш выбор: строка
`github.com/metacubex/utls v1.8.7` в `go.mod` идентична у нас и у
`upstream/stable`, `replace` на неё нет — зависимость наследуется от апстрима.

**Чем `metacubex/utls` отличается от первоисточника
`refraction-networking/utls`.** Это не «форк ради пары патчей»: ветки разошлись
в апреле 2025, у metacubex 60+ собственных коммитов трёх родов:

1. **Целый REALITY-сервер, которого у refraction нет вообще** — `RealityConfig`,
   `RealityServer()`, `Clone()`, rate limiting фолбэка, `X25519MLKEM768 for
   reality server`. Проверено: у refraction `RealityConfig` не существует.
2. **Экспорт внутренностей под `//go:linkname`** — `export hkdf`, `export mlkem`,
   `export cpu`, `export HasAESGCMHardwareSupport`, `fix more linkname`. На этом
   стоят `common/badtls/registry_utls.go` (семь linkname в `utls.(*Conn)` /
   `utls.(*halfConn)`) и `common/ktls/ktls_prf.go`.
3. **Портируемость** — loong64, riscv64, s390x, `replace internal/cpu`,
   `remove fipsonly`, `change back to go1.20`.

Обратная сторона этой ветки — свежие профили браузеров в неё не попадают:
metacubex отстал от refraction на **34 коммита**, и ровно в них лежит нужное —
`fc716b2` (Firefox 148 + keyshare reuse), `6d6e1c6` (ML-KEM в supported groups),
`aa6edf4` (Safari 26.3).

**Почему у Xray это работает.** Xray сидит на `refraction-networking/utls`, причём
пином на коммит мимо релизов: `v1.8.3-0.20260301010127-aa6edf4b11af` при последнем
теге `v1.8.2` — то есть на полтора месяца свежее любого тега (`fc716b2` является
предком этого пина, проверено `git merge-base --is-ancestor`). Он может позволить
себе первоисточник, потому что REALITY у него **свой** (`xtls/reality`), а не
библиотечный. Нам туда нельзя: без метакубикового `utls.RealityConfig` /
`utls.RealityServer` не соберётся `common/tls/reality_server.go`, и развалятся
linkname в `badtls`/`ktls`. **Это не «Xray решил лучше» — это разные библиотеки
под разные архитектурные решения.**

**Ждать нечего.** У metacubex `v1.8.7` — последняя версия, а на `master`
`HelloFirefox_Auto` по-прежнему `HelloFirefox_120`: ни пресета Firefox 148, ни
reuse-механики там нет вовсе. Пин на коммит (приём Xray) нам ничего не даёт —
тянуть неоткуда.

**Путь «попросить апстрим библиотеки» закрыт эмпирически.** В `metacubex/utls`
**issues отключены** (`has_issues: false`), а из внешних PR за всю историю
репозитория **не влит ни один** — 6 из 6 закрыты, включая ровно наши сценарии:
#5 «Merge refraction-networking/utls/master» (синк с апстримом), #3 «Updated
fingerprints», #2 «added HelloChrome_147» (новый профиль). В #4 автор пинговал
мейнтейнера и ответа не получил.

**Апстрим sing-box знает и не чинит.** [SagerNet/sing-box#4520](https://github.com/SagerNet/sing-box/issues/4520)
открыт с 2026-09-11 без реакции мейнтейнера. Там же — подтверждение стороннего
репортёра, что **mihomo v1.19.30 проходит на том же сервере, пиная тот же
`metacubex/utls v1.8.7`**: значит дело не в библиотеке целиком, а в том, как
строится приветствие, — что эта SPEC для chrome и закрыла.

**Следствие — выполнено в [086](../086-UTLS_FORK_FIREFOX148/SPEC.md) 2026-09-16.** Единственный путь к `fp=firefox` — форк `metacubex/utls` сабмодулем с переносом
Firefox 148 из первоисточника: задача [086](../086-UTLS_FORK_FIREFOX148/SPEC.md). Переносится не
только декларативный пресет, но и reuse одного классического X25519-ключа между гибридной и
классической записями key share — это логика генерации ключей внутри библиотеки, в нашем слое её
не собрать; без неё отпечаток не совпадёт с настоящим Firefox, а мимикрия и есть смысл `fp=firefox`.

## 6. Условие снятия

Апстрим sing-box уберёт фильтр в `common/tls/reality_client.go` и добавит
fallback на `MlkemEcdhe` (issue #4520) — тогда наши два маркера снимаются
в пользу апстримной формы. Пока висит: проверять на каждом мерже, что **безусловный**
фильтр не вернулся — с [089](../089-REALITY_KEY_SHARE_OPTION/SPEC.md) (2026-09-17) фильтр
в файле снова есть, но только под `case C.RealityKeyShareClassical` (`reality.key_share`);
`grep X25519MLKEM768` по файлу больше не ноль, вместо него страж
`TestLxRealityKeyShareDefaultKeepsHybrid` (`common/tls/reality_client_lx_test.go`) — пустой
`key_share` обязан слать гибрид, — и что
`HelloChrome_Auto` в новой utls по-прежнему несёт гибрид **перед** X25519 — смена
порядка в спеке utls сломает узлы так же тихо. Вторую проверку с 086 делает тест
`TestLxRealityFingerprintsCarryHybridShareFirst` (`common/tls/utls_firefox148_lx_test.go`)
для `chrome` и `firefox` — он падает в CI, а не в поле.
