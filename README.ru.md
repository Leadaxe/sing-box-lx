[English](README.md) · **Русский**

# sing-box-lx

> **Тонкий downstream-форк [SagerNet/sing-box](https://github.com/SagerNet/sing-box).**
> Ровно две клиентские фичи поверх upstream — **XHTTP** и **AmneziaWG 2.0** — и больше ничего.
> Цель: жить ребейзом на каждый upstream-тег, а не отдельной жизнью.

> 📄 README самого upstream sing-box — **[на GitHub](https://github.com/SagerNet/sing-box/blob/main/README.md)** (всегда актуальный).

Это не отдельный проект и не «улучшенный sing-box». Это upstream sing-box **плюс две вещи**, реализованные так, чтобы их можно было переносить на новые версии sing-box годами, почти без конфликтов.

---

## Уникальное позиционирование

В экосистеме sing-box форки, добавляющие XHTTP/AmneziaWG, делятся на два лагеря — и `sing-box-lx` не входит ни в один:

| Форк | Фичи | Подход | Синк с upstream |
|------|------|--------|-----------------|
| **SagerNet/sing-box** (upstream) | базовый | — | — |
| **shtorm-7/sing-box-extended** | десятки (WARP, MASQUE, MTProxy, XHTTP, AWG2, …) | «комбайн», правки повсюду | отдельная ветка, без ребейза на теги |
| **amnezia-vpn/amnezia-box**, **hoaxisr/amnezia-box** | только AWG | толстый форк, правки in-place | синк по веткам (`dev-next`/`stable-next`) |
| **➡ sing-box-lx** (этот репозиторий) | **только XHTTP + AWG2** | **тонкий: новые файлы за build-tag, минимум касаний upstream** | **ребейз атомарных `// lx`-коммитов на upstream-теги** |

**Чем мы отличаемся:**

- **Минимальная дивергенция.** Новый код живёт в новых файлах. Существующие upstream-файлы трогаются только в крошечных помеченных швах `// lx:begin … // lx:end`. → дешёвые ребейзы.
- **Изоляция за build-tag.** Фичи включаются тегами `with_xhttp` / `with_awg`. Сборка **без** них байт-в-байт повторяет поведение upstream — фичи ничего не ломают по умолчанию.
- **Идентичность сохранена.** Go-модуль остаётся `github.com/sagernet/sing-box`, бинарь называется `sing-box`. Суффикс `-lx` есть только в строке версии (`1.13.13-lx.N`).
- **Build-tag — родная конвенция sing-box**, а не наше изобретение (`with_quic`, `with_wireguard`, …). Мы просто применяем её с максимальной дисциплиной.

> Готовые форки-комбайны мы **не тянем как зависимость**, а используем только как референс wire-протокола.

---

## Фичи и статус

| # | Фича | Что это | Статус |
|---|------|---------|--------|
| **XHTTP** | клиентский транспорт | Xray-совместимый «splithttp» (режимы `auto`/`packet-up`/`stream-up`/`stream-one`) поверх Reality/TLS/h2c | ✅ **проверен живым Xray (3x-ui) сервером** (packet-up/auto): handshake + DNS + HTTPS + скачивание. `stream-one` — известный баг framing |
| **AmneziaWG 2.0** | клиентский endpoint | обфускация WireGuard: `Jc/Jmin/Jmax`, `S1–S4`, `H1–H4` + **2.0**: `I1–I5` (CPS — кастомные пакеты-приманки) | ✅ собирается, проходит `check`; зависимость **активирована** ([Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) — sagernet-база + обфускация); **проверено живым AWG2-сервером**: handshake + keepalive + трафик наружу |

Подробные отчёты — в [`SPECS/002-…`](SPECS/002-F-C-XHTTP_CLIENT_TRANSPORT/IMPLEMENTATION_REPORT.md) и [`SPECS/003-…`](SPECS/003-F-C-AWG2_CLIENT_ENDPOINT/IMPLEMENTATION_REPORT.md). Полный справочник конфига — **[docs/lx-config.md](docs/lx-config.md)**.

> **Не поддерживается (слой Reality, отложено):** post-quantum Reality (`pqv` / ML-DSA-65) и `spiderX` из Xray. Это Xray-специфичные фичи Reality, которых нет в sing-box, а Reality — upstream-слой TLS, который мы держим нетронутым (это не одна из наших двух фич). Классический X25519 Reality работает; сервер, который **требует** post-quantum Reality, не подключится. Это ограничение sing-box — правильнее решать в upstream (получим на ребейзе).

---

## Сборка

Сборка идёт через отдельный **`Makefile.lx`** (upstream `Makefile` не трогаем):

```bash
git clone --recurse-submodules https://github.com/Leadaxe/sing-box-lx
make -f Makefile.lx lx-build
# → бинарь ./sing-box с версией вида 1.13.13-lx.1
```

> `--recurse-submodules` обязателен для `with_awg`: рантайм AmneziaWG подключён submodule'ом `submodules/wireguard-go` → [Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx).

Под капотом — стандартный `go build` с набором тегов:

```
with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,with_naive_outbound,with_purego,badlinkname,tfogo_checklinkname0,with_xhttp,with_awg
```

Проверка конфигов:

```bash
./sing-box check -c lx-test/config/xhttp_reality.json
./sing-box check -c lx-test/config/awg2_basic.json
```

> `lx-test/config/` — наши примеры (upstream `test/` — отдельный Go-модуль, его не используем).

---

## Конфигурация фич

> Полные таблицы полей, дефолты и `awg-quick`→JSON маппинг — **[docs/lx-config.md](docs/lx-config.md)**. Здесь — кратко.

### XHTTP (outbound transport)

```jsonc
"transport": {
  "type": "xhttp",
  "host": "example.com",
  "path": "/xhttp",
  "mode": "auto"          // auto | packet-up | stream-up | stream-one
}
```

### AmneziaWG 2.0 (endpoint)

Поля AWG промотированы прямо в `WireGuardEndpointOptions`:

```jsonc
{
  "type": "wireguard",
  // … стандартные поля wireguard (private_key, address, peers, …) …
  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  "h1": 1, "h2": 2, "h3": 3, "h4": 4,
  "i1": "<b 0x...><r 12>", "i2": "", "i3": "", "i4": "", "i5": ""   // 2.0 CPS
}
```

> `I1–I5` — это конфиг (не согласуется по сети), значения должны **совпадать на клиенте и сервере**, регистрозависимы.

---

## Модель сопровождения

```
upstream tag (vX.Y.Z)
        │
        └─►  ветка lx = upstream + N атомарных // lx-коммитов
                 ├─ FORK_BOOTSTRAP (Makefile.lx, CI, версия)
                 ├─ XHTTP client transport
                 └─ AWG2 client endpoint
```

- **Только ребейз, никогда merge.** На новый upstream-тег ветка `lx` ребейзится поверх него.
- Каждая фича — атомарный коммит(ы), помеченный `// lx`. Новые файлы конфликтов не дают; швы в upstream-файлах малы и переносятся вручную.
- Разработка ведётся по **Spec Kit** (`SPECS/NNN-T-S-NAME/`: SPEC → PLAN → TASKS → IMPLEMENTATION_REPORT).

### Remotes

```bash
origin    git@github.com:Leadaxe/sing-box-lx.git   # ветка по умолчанию: lx
upstream  https://github.com/SagerNet/sing-box.git
```

---

## Структура lx-специфики

| Путь | Назначение |
|------|------------|
| `Makefile.lx` | сборка с lx-тегами и версией `-lx` |
| `.github/workflows/lx-ci.yml` | CI: матрица фич (baseline/xhttp/awg/full) + negative-check + кросс-платформа |
| `SPECS/` | Spec Kit (конституция, задачи, отчёты) |
| `lx-test/config/` | примеры конфигов для `sing-box check` |
| `transport/v2rayxhttp/` | XHTTP-клиент (новый пакет) |
| `transport/wireguard/device_awg.go` | AWG IpcSet-параметры (за `with_awg`) |
| `submodules/wireguard-go` | submodule: merged-форк AmneziaWG-рантайма ([Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx)) |
| `option/v2ray_xhttp.go`, `option/wireguard_awg.go` | опции фич |
| `include/v2rayxhttp.go` | регистрация транспорта за build-tag |

Поиск всех правок upstream-файлов: `grep -rn "// lx"`.

---

## Потребитель

Ядро собирается для десктоп-лаунчера **singbox-launcher** (бандлит `bin/sing-box`). Маппинг `type=xhttp` и AWG-полей в визарде — задачи на стороне лаунчера, не здесь.

---

## Ссылки

| | |
|---|---|
| Upstream | [SagerNet/sing-box](https://github.com/SagerNet/sing-box) · [документация](https://sing-box.sagernet.org/) |
| Этот форк | [Leadaxe/sing-box-lx](https://github.com/Leadaxe/sing-box-lx) |
| AmneziaWG-рантайм | [Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) — sagernet-база + обфускация (3-way merge) |
| AmneziaWG upstream | [amnezia-vpn/amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) · [docs.amnezia.org](https://docs.amnezia.org/documentation/amnezia-wg/) |
| XHTTP (исток) | [XTLS/Xray-core](https://github.com/XTLS/Xray-core) — `transport/internet/splithttp` |
| Конфиг фич | [docs/lx-config.md](docs/lx-config.md) |
| Spec Kit | [SPECS/](SPECS/) — [README](SPECS/README.md) · [CONSTITUTION](SPECS/CONSTITUTION.md) · [IMPLEMENTATION_PROMPT](SPECS/IMPLEMENTATION_PROMPT.md) |

---

## Лицензия

Наследует лицензию upstream sing-box (**GPL-3.0**). Все правки помечены `// lx` и распространяются под той же лицензией. Это неофициальный форк, не аффилирован с SagerNet.
