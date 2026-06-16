# PLAN: 009 — WIRESOCK_MASQUERADE_PROFILES

## Решённые развилки (НЕ передоговариваем)

- **Механизм — I1 CPS только.** S1–S4 padding отвергнут: он невозможен против
  Cloudflare WARP, ради упрощения коннекта к которому фича и существует. Сабмодуль
  `submodules/wireguard-go` **не трогаем** (и ребейз дешевле — CONSTITUTION §2).
- **QUIC — 1-RTT short header** (как WireSock `apply_quic_padding_short`), НЕ Initial.
  Нет SNI, нет ClientHello, нет JA3 — честно, без «byte-perfect».
- **Структура протоколов** — порт из WireSock `transform.rs` (MIT). DNS = EDNS OPT
  response, STUN = Binding Success Response, SIP = response-текст.
- **`Ib`** — валидируется (`chrome|firefox|curl`), но **не выдумывает fingerprint**:
  единственный эффект — выбор фикс-битов spin/key_phase первого QUIC-байта (косметика).

## Ключевое отличие модели от WireSock (определяет дизайн)

WireSock — **серверный** прокси, переписывает leading S1–S4 padding датаграммы, у
которой за padding идёт **реальный WG-ciphertext**. Его генераторы: (1) сидят PRNG
от этого хвоста, (2) считают длины так, чтобы покрыть хвост.

У нас — **standalone I1-приманка**: amneziawg-go `send.go` шлёт её
`Obfuscate(buf, nil)` (src=nil), хвоста **нет**. Поэтому:
- весь датаграм = вывод CPS (фикс-каркас `<b>` + энтропия `<r>/<rc>/<rd>`);
- длины (DNS RDLENGTH/OPTION-LENGTH, STUN message-length, SIP Content-Length=0)
  покрывают **только реально записанные байты**;
- энтропия — криптослучайная (`<r N>`), не payload-seeded LCG. Это честнее (выше
  энтропия, свежий пакет каждый раз), но это **не** byte-replay WireSock-трафика.

## Поток данных

```
option.AmneziaWGOptions{Id,Ip,Ib}
  → awgIpcLines (device_awg.go)
    → masqueI1(o)  // валидация + генерация CPS, "" если не задано
      → masque{QUIC,DNS,STUN,SIP}…CPS  // порт transform.rs
  → подстановка как i1=<CPS>
  → vendored newObfChain (obf.go) парсит
  → Obfuscate(buf, nil) шлёт приманку перед handshake (send.go:135)
```

## Изменённые / новые файлы

| Файл | Зона | Что |
|------|------|-----|
| `option/wireguard_awg.go` | lx | +`Id/Ip/Ib string` (json `id`/`ip`/`ib`); `IsSet()` учтёт автоматически |
| `transport/wireguard/masque_awg.go` | lx, `with_awg` | `masqueI1` диспетчер, `validateMasqueDomain` (LDH), `normalizeMasqueBrowser`, `cpsBuilder`, DNS/STUN/SIP генераторы |
| `transport/wireguard/masque_quic_awg.go` | lx, `with_awg` | QUIC 1-RTT short header (`masqueQUICShortHeaderCPS`, `quicFirstByte`) |
| `transport/wireguard/device_awg.go` | lx, `with_awg` | вызов `masqueI1(o)` в `awgIpcLines`, подстановка как `i1` |
| `transport/wireguard/masque_awg_test.go` | lx, `with_awg` | структурные тесты (обратный парсинг), KAT, валидация, инъекция |
| `transport/wireguard/masque_cps_test.go` | lx, `with_awg` | test-only верный реплей CPS-парсера (зеркало `newObfChain`) |
| `docs/lx-config.md` | lx | секция id/ip/ib |
| `SPECS/README.md` | lx | roadmap-строка 009 |

Новых upstream-швов нет, сабмодуль не трогаем (механизм I1).

## Валидация (fail-fast в `masqueI1`)

1. `Id/Ip/Ib` пусты → `""` (нет маскировки), конфиг = upstream.
2. Конфликт с явным `I1` → ошибка.
3. `Ip` пуст при заданном `Id/Ib` → ошибка; `Ip ∉ {quic,dns,stun,sip}` → ошибка.
4. `Id` обязателен **только для `dns`/`sip`** (там он идёт на провод как QNAME /
   SIP-host); для `quic`/`stun` опционален. Когда задан — **строгий LDH** (зеркало
   `is_valid_sni_hostname`): метки
   alnum+`-`+`_`, без edge-дефиса, ≤63, всего ≤253, трейлинг-дот ок. Это
   **security-граница** — домен идёт в SIP-текст и DNS QNAME.
5. `Ib ∉ {chrome,firefox,curl}` → ошибка; `Ib` с `Ip≠quic` → ошибка.

## DoD

- [ ] `go build ./...` без тегов — ок
- [ ] `go build -tags "with_wireguard with_gvisor with_awg" ./cmd/sing-box` — ок
- [ ] `go test -tags with_awg ./transport/wireguard/...` — зелёный
- [ ] каждый профиль принимается реальным `newObfChain` (transient submodule test)
- [ ] `sing-box check` на конфигах id/ip/ib всех профилей; конфликт с `i1` отвергнут
- [ ] `lx-test/config/awg2_*.json` — без регресса
- [ ] gating: id/ip/ib без `with_awg` → «awg support not built»
- [ ] `gofmt -l` lx-файлов — пусто
- [ ] тесты не тавтологичны (обратный парсинг структур, KAT, отличие по сущностным байтам)
- [ ] комментарии/доки честны (без «byte-perfect»/«fingerprint»)

## Зона ребейза

Все lx-файлы под `with_awg` (в upstream их нет) → конфликтов на ребейзе не дают.
`option/wireguard_awg.go` — lx-файл целиком. Сабмодуль не трогаем.
