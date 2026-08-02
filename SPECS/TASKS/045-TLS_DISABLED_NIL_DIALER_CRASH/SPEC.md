# SPEC: 045 — TLS_DISABLED_NIL_DIALER_CRASH

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — регрессия апстрима, жива в upstream `testing` на момент фикса |
| Статус | C (complete) — корень доказан крашдампом с устройства + red/green юнитами; отгружен в `v1.14.0-lx.19-rc.3` |

Trojan- или VLESS-нода с `"tls": {"enabled": false}` роняет **весь процесс** ядра
nil-pointer паникой при первом же дозвоне (включая URL-тест). Фикс — не создавать
TLS-dialer, когда TLS выключен: plain-TCP путь и так существует рядом.

Build-tag: нет (фикс безусловный, поведение = задуманное апстримом). Scope: **client-only**.

---

## 1. Проблема

Жалоба (4PDA, 2026-08-02): краш при URL-тесте. Крашбандл с устройства
(ядро `1.14.0-lx.18`):

```
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x28 ...]

sing/common/tls.ClientHandshake(..., {0x0, 0x0})        ← config = nil
sing-box/common/tls.(*defaultDialer).dialContext(...)
sing-box/protocol/trojan.(*trojanDialer).DialContext(...)
sing-box/common/urltest.urlTest(...)
```

Порт назначения в кадре dialContext (`0xa1b1` = 41393) указал на единственную
ноду конфига с непустым tls-блоком и `enabled: false` — plain-trojan из публичной
подписки. Такой узел — легальный конфиг: trojan без TLS ядро принимает
(`check` зелёный), нода выглядит живой до первого дозвона.

### 1.1 Корень: регрессия апстрима в ECH-коммите

Апстримный коммит `1f0308054` «Add support for ech retry configs» перестроил
дозвон с TLS:

- **было:** plain-дозвон, затем `if h.tlsConfig != nil { ClientHandshake(...) }` —
  nil-конфиг легально означал «без TLS»;
- **стало:** в конструкторе под гейтом `options.TLS != nil` создаётся
  `tlsDialer = tls.NewDialer(dialer, tlsConfig)`, а дозвон выбирает ветку по
  `h.tlsDialer != nil`.

При этом `tls.NewClientWithOptions` для `enabled: false` возвращает `(nil, nil)` —
это контракт, на который опираются другие вызыватели. Итог: живой dialer с nil-конфигом
внутри → `aTLS.ClientHandshake(ctx, conn, nil)` → SIGSEGV. Паника рождается в
горутине ядра (gRPC-хендлер URL-теста) — гибнет весь процесс, VPN падает.

### 1.2 Зона поражения

| Протокол | Гейт при создании dialer | Уязвим |
|---|---|---|
| trojan | `options.TLS != nil` — без проверки конфига | **да** |
| vless | `options.TLS != nil` — без проверки конфига | **да** |
| vmess | `if outbound.tlsConfig != nil` | нет |
| anytls | `tls.enabled` обязателен (`ErrTLSRequired`) | нет |

Условие срабатывания: TCP-коннект до сервера **удался** (паника — на этапе
рукопожатия). Отсюда полевой паттерн жалобы: нода-мина месяцами лежит в подписке
незамеченной, пока ТСПУ дропает её TCP, и стреляет, когда путь внезапно проходим —
у автора жалобы LxBox работал в обход туннеля другого VPN-клиента, и коннект
до ноды пробился.

## 2. Доказательство

- Крашдамп с устройства: nil-конфиг третьим аргументом `ClientHandshake`,
  порт кадра = порт единственной `enabled:false`-ноды конфига.
- Red/green юниты: на коде до фикса `TestNewOutboundTLSDisabledNoDialer`
  падает в обоих пакетах (trojan, vless), после фикса — зелёный.

## 3. Требования

- `"tls": {"enabled": false}` (и отсутствие tls-блока) у trojan/vless дают
  plain-TCP дозвон — как до ECH-коммита апстрима, без паники.
- `"tls": {"enabled": true}` продолжает давать TLS-dialer (ECH-retry-путь
  апстрима не трогаем).
- Минимальный дифф: тот же гейт, что апстрим сам держит в vmess.

## 4. Критерии приёмки

- Юниты: `tlsDialer == nil` при выключенном TLS, `!= nil` при включённом — оба пакета.
- `sing-box check` принимает конфиг с plain-trojan/plain-vless нодами.
- URL-тест ноды `65.109.219.124:41393` из жалобы не роняет процесс (field, после AAR).

## 5. Границы

- Серверная сторона (inbound) вне scope форка — не трогаем.
- Контракт `NewClientWithOptions → (nil, nil)` не меняем: на нём другие вызыватели.
- Транспортный путь (`v2ray.NewClientTransport` с nil tlsConfig) корректен и не менялся.
