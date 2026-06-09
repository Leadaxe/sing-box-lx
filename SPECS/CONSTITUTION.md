# CONSTITUTION — sing-box-lx

Неизменяемые принципы проекта. При конфликте с любым SPEC/PLAN — приоритет у этого документа.

---

## 1. Миссия

`sing-box-lx` — **тонкий downstream** [SagerNet/sing-box](https://github.com/SagerNet/sing-box). Это **upstream + ровно две фичи**:

1. **XHTTP** — клиентский v2ray-транспорт, совместимый с Xray XHTTP.
2. **AmneziaWG 2.0 (AWG2)** — клиентский endpoint поверх WireGuard, с обфускацией (Jc/Jmin/Jmax, S1/S2, H1–H4, I1–I5).

Обе фичи **отклонены upstream** ([XHTTP — not planned](https://github.com/SagerNet/sing-box/issues/3550), [AmneziaWG — closed not-planned](https://github.com/SagerNet/sing-box/issues/4045)), поэтому форк постоянный, а «согласованность с upstream» достигается не вливанием в него, а **дешёвым ребейзом на каждый новый тег**.

---

## 2. Приоритеты (в порядке убывания)

1. **Согласованность с upstream / минимальный дифф.** Любое решение выбирается так, чтобы ребейз на следующий тег был максимально дешёвым.
2. **Корректность и совместимость** с реальными серверами Xray-XHTTP и AmneziaWG 2.0.
3. **Ребейзопригодность** изменений (изоляция, атомарность, маркеры).
4. **Сами фичи** (функциональность XHTTP/AWG2).

Если фича требует жертвовать пунктом 1 — она переосмысливается, а не пункт 1.

---

## 3. Жёсткие правила (запреты и инварианты)

### 3.1 Объём
- **Только две фичи.** Любой код вне XHTTP и AWG2 — вне скоупа. Багфиксы upstream не патчим у себя — ждём апстрим-тег.
- **Scope — client-only.** Реализуем outbound/endpoint и клиентскую сторону транспорта. Server/inbound для XHTTP и AWG — **отложены** (отдельные будущие задачи), в текущих спеках не реализуются.

### 3.2 Изоляция изменений
- **Go module path остаётся `github.com/sagernet/sing-box`.** Не переименовывать — это ломает все внутренние импорты и каждый ребейз.
- **Новый код — в новых файлах/пакетах.** XHTTP-транспорт — пакет `transport/v2rayxhttp`. AWG — в выделенных файлах рядом с `protocol/wireguard` / `transport/wireguard`.
- **Каждая фича — за build-tag:** `with_xhttp`, `with_awg`. Без тега сборка обязана быть **байт-в-байт эквивалентна upstream по поведению** (фича отсутствует).
- **Шаблон гейтинга — `include/*_stub.go`** (как у upstream `include/wireguard.go` + `wireguard_stub.go`): реальная регистрация под тегом, заглушка с понятной ошибкой без тега.

### 3.3 Правки upstream-файлов
- Допускаются **только** там, где иначе нельзя (диспетчеры, struct опций, списки констант, `go.mod`).
- Каждая такая правка **обёрнута маркером**:
  ```go
  // lx:begin xhttp
  ...
  // lx:end xhttp
  ```
- Правки upstream-файлов выносятся в **отдельные атомарные коммиты** (см. IMPLEMENTATION_PROMPT). Один коммит = одна логическая правка одной зоны.

### 3.4 Синхронизация
- **Только rebase, никогда merge.** Ветка `lx` всегда ребейзится поверх тега upstream (`v1.13.13`, затем следующий стабильный).
- `origin` = `Leadaxe/sing-box-lx`, `upstream` = `SagerNet/sing-box`. Теги тянем из `upstream`.

### 3.5 Дистрибуция
- **Имя бинаря — `sing-box`** (drop-in для лаунчера `singbox-launcher`, который ищет `LookPath("sing-box")` → `bin/sing-box`).
- Идентичность сборки — **в версии**: `sing-box version` → `1.13.13-lx.N` (см. задачу BUILD_CI_RELEASE).

---

## 4. Архитектурные ориентиры (факты upstream v1.13.13)

- **v2ray-транспорты** диспатчатся `switch` по `options.Type` в `transport/v2ray/transport.go` (`NewClientTransport`/`NewServerTransport`). Константы — `constant/v2ray.go`. Опции — `option/v2ray_transport.go` (`_V2RayTransportOptions`). VLESS/VMess/Trojan ходят через общий транспорт — пер-протокольных правок не требуется.
- **WireGuard** — это **endpoint**: `protocol/wireguard/endpoint.go`, регистрация `endpoint.Register[option.WireGuardEndpointOptions](registry, C.TypeWireGuard, NewEndpoint)`, проводка в `include/wireguard.go` (+ `wireguard_stub.go`). Девайс — через `transport/wireguard`, зависимость `github.com/sagernet/wireguard-go` в `go.mod`.

---

## 5. Референсы (только как образец, код не тянуть «как есть»)

- **AWG2** — [`hoaxisr/amnezia-box`](https://github.com/hoaxisr/amnezia-box) (submodule + `patches/amneziawg-go`, тег `with_awg`).
- **XHTTP** — [`hiddify/hiddify-sing-box`](https://github.com/hiddify/hiddify-sing-box), пакет `transport/v2rayxhttp`.
- **Спецификация XHTTP** — Xray-core (актуальная версия параметров `mode`/`path`/`host`/`extra`).

---

## 6. Лицензия

Upstream — GPLv3. Портируемый код из сторонних проектов держать в отдельных файлах с сохранением исходных лицензионных заголовков и указанием происхождения.
