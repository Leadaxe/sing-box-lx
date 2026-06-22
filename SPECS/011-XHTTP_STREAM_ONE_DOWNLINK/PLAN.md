# PLAN: 011 — XHTTP_STREAM_ONE_DOWNLINK

## 1. Суть фикса (по разведке, построчно сверено с Xray `main` + hiddify)

Два изменения в пакете `transport/v2rayxhttp` (новый код — нулевая ребейз-зона):

1. **stream-one → голый путь без sessionId** (главный баг).
2. **`mode=auto` → stream-one при Reality** (иначе packet-up).

ALPN и padding — **не трогаем** (доказано benign / совпадает; см. SPEC §3.4).

---

## 2. Изменяемые файлы (все — новые/lx-файлы; upstream не трогаем)

| Файл | Тип | Изменение |
|------|-----|-----------|
| `transport/v2rayxhttp/client.go` | lx (new pkg) | `requestURL` bare-path ветка; `Client` +поле reality; `NewClient` детект reality; `DialContext` auto-резолв |
| `transport/v2rayxhttp/conn.go` | lx (new pkg) | `dialStreamOne`: `requestURL(sessionID)` → `requestURL()` (голый путь) |
| `option/v2ray_xhttp.go` | lx (new file) | doc-комментарий про auto/stream-one (косметика, не обязательно) |
| `lx-test/config/xhttp_reality.json` + (новый) auto-конфиг | lx (test) | проверка `check` для stream-one и auto |

**Upstream-файлов не касаемся.** Ребейз-зона 011 = ∅ (всё в новом пакете).

---

## 3. Детали изменений

### 3.1 `requestURL` — bare-path ветка (client.go:179-192)
Сейчас:
```go
fullPath := c.path + "/" + strings.Join(elem, "/")
```
При `len(elem)==0` → `strings.Join` = `""` → `c.path + "/"` = `<path>/` (trailing slash — отличается от Xray/hiddify `<path>`).
**Правка:** если `len(elem)==0` → `fullPath = c.path` (голый путь, c.path уже без trailing slash, client.go:129). Иначе — как было.

### 3.2 `dialStreamOne` (conn.go:21)
```go
u, err := c.requestURL(sessionID)   // было: <path>/<sessionId>
→
u, err := c.requestURL()            // стало: <path>
```
sessionID в stream-one больше нигде не используется (можно убрать его генерацию для этой ветки, но проще оставить — он безвреден, в URL не идёт). Остальное (POST, pipe-body, streamConn late-binding) — без изменений: разведка подтвердила, что late-binding корректен, баг был только в URL.

### 3.3 `mode=auto` резолв (client.go DialContext:152-167)
Сейчас `case modeAuto, modePacketUp:` → `dialPacketUp`.
**Правка:** выделить `modeAuto` в отдельную ветку:
```go
case modeAuto:
    if c.realityEnabled {
        return c.dialStreamOne(ctx, sessionID)
    }
    return c.dialPacketUp(ctx, sessionID)
case modePacketUp:
    return c.dialPacketUp(ctx, sessionID)
```
`c.realityEnabled` — новое bool-поле на `Client`, проставляется один раз в `NewClient`.

### 3.4 REALITY-детект в `NewClient` — БЕЗ межтеговой зависимости (РАЗВИЛКА)

`*tls.RealityClientConfig` / `*tls.KTLSClientConfig` — под `//go:build with_utls`; `v2rayxhttp` — под `with_xhttp`. Прямой `tlsConfig.(*tls.RealityClientConfig)` введёт жёсткую связь `with_xhttp → with_utls` и сломает сборку `with_xhttp` без `with_utls` (нарушение CONSTITUTION §3.2). Варианты:

- **(A) Матч по имени типа (рекомендуется).** В `NewClient`:
  ```go
  realityEnabled := tlsConfigIsReality(tlsConfig)
  // helper: reflect.TypeOf(unwrap(tlsConfig)).String() содержит "RealityClientConfig"
  ```
  Разворачивать KTLS-обёртку: у `*KTLSClientConfig` встроено поле `Config Config` → если имя типа = KTLS, взять inner и проверить снова. Делать через рефлексию по имени поля/типа, **без** импорта with_utls-типов. Плюс: нулевая межтеговая связь, работает и для kTLS. Минус: матч по строке имени типа — хрупковато к переименованию upstream (митигируется тестом).
- **(B) Проброс флага из вызывающего слоя.** Добавить признак reality в `option.V2RayXHTTPOptions` или в сигнатуру конструктора. Минус: правка upstream-сигнатуры `ClientConstructor`/диспетчера — расширяет ребейз-зону, противоречит «новый код в новых файлах». Отклонено.
- **(C) Эвристика по ServerName/NextProtos.** Ненадёжно (reality неотличим от обычного uTLS по этим полям). Отклонено.

**Решение: (A)** — изолированный helper в `client.go` (или соседнем lx-файле пакета), детект по имени типа с разворачиванием KTLS, покрытый юнит-тестом. Если по ходу выяснится, что рефлексия по приватному полю KTLS недоступна — fallback: матчить и `RealityClientConfig`, и `KTLSClientConfig` по суффиксу имени (для не-Linux kTLS не используется, риск низкий).

---

## 4. Порядок работ

1. Ветка `lx/xhttp` от `lx` (по git-дисциплине; сейчас HEAD на `lx-gro-probe-010`).
2. `requestURL` bare-path ветка + `dialStreamOne` голый путь (фикс 3.1 главный — проверяем stream-one лайв сразу).
3. auto-резолв + reality-детект helper (3.3, 3.4) + юнит-тест детекта.
4. Тест-конфиги, `sing-box check`, лайв-проверка (stream-one + auto на reality-ноде; packet-up регрессия).
5. DoD, IMPLEMENTATION_REPORT, статус (шапка SPEC.md + Roadmap) → C.

---

## 5. Риски

- **Лайв-сервер обязателен.** Синтетического `check` мало — XHTTP под активной разработкой, нужна reality-нода Xray. stream-one лайв-валидируем явно; auto — на той же ноде убеждаемся, что резолвится в stream-one.
- **Матч по имени типа (3.4-A)** хрупок к переименованию `RealityClientConfig` в upstream/sing. Митигировать юнит-тестом, который при ребейзе сразу покраснеет.
- **h2 для stream-one обязателен** — наш h2-only транспорт это обеспечивает; не регрессируем packet-up (он тоже h2 поверх reality).
- Не сломать stream-up/packet-up URL (оставляют sessionId) — правим только пустую-elem ветку `requestURL` и только `dialStreamOne`.
