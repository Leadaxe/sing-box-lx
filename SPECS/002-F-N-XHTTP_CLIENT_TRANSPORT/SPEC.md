# SPEC: 002 — XHTTP_CLIENT_TRANSPORT

Добавить **клиентский XHTTP-транспорт** (совместимость с Xray XHTTP) для VLESS/VMess/Trojan, встроив его через **registry-рефактор** диспетчера v2ray-транспортов, за build-тегом `with_xhttp`.

---

## 1. Проблема / контекст

- Upstream sing-box XHTTP не поддерживает и не планирует ([#3550](https://github.com/SagerNet/sing-box/issues/3550)). Сервера на Xray всё чаще только XHTTP (после депрекации части транспортов в Xray).
- В sing-box диспетчер v2ray-транспортов — **хардкод-`switch`** по `options.Type` в `transport/v2ray/transport.go`. Добавлять `case` на каждый ребейз — точка постоянных конфликтов.

## 2. Цель

VLESS/VMess/Trojan outbound с `transport.type = "xhttp"` поднимают рабочее соединение к XHTTP-серверу Xray, в т.ч. поверх **TLS/Reality**. Без тега `with_xhttp` тип `xhttp` отвергается с понятной ошибкой.

## 3. Требования

### 3.1 Registry-рефактор диспетчера (точка касания upstream)
- Превратить выбор клиентского транспорта в **реестр**: `transport.RegisterClient(type, ClientConstructor)` + `map[string]ClientConstructor`, заполняемый при `init()`.
- Встроенные транспорты (`http`, `ws`, `quic`, `grpc`, `httpupgrade`) регистрируются как раньше (поведение идентично upstream).
- `NewClientTransport` ищет конструктор в реестре вместо `switch` (поведение для известных типов — без изменений; для неизвестных — та же ошибка `unknown transport type`).
- **Серверный** диспетчер (`NewServerTransport`) — **не трогаем** (scope client-only); xhttp-сервер отложен.

### 3.2 Пакет `transport/v2rayxhttp` (новый код)
- Клиентская реализация XHTTP (референс — [`hiddify/hiddify-sing-box`](https://github.com/hiddify/hiddify-sing-box) `transport/v2rayxhttp`, сверка параметров с Xray-core).
- Поддержать режимы Xray: `auto`, `packet-up`, `stream-up`, `stream-one`; параметры `path`, `host`, `headers`, и padding-расширения (`x_padding_bytes` и т.п.) — в объёме, нужном для совместимости.
- Регистрация конструктора через `init()` в файле за `//go:build with_xhttp`.

### 3.3 Опции и константа
- `constant/v2ray.go`: `V2RayTransportTypeXHTTP = "xhttp"` (внутри `// lx:` маркера).
- `option/v2ray_transport.go`: поле `XHTTPOptions XHTTPOptions` в `_V2RayTransportOptions` + тип `XHTTPOptions` (в новом файле `option/v2ray_xhttp.go`, чтобы минимизировать дифф основного файла; в `_V2RayTransportOptions` — одна `// lx:` строка).

### 3.4 TLS/Reality
- `tlsConfig` прокидывается в конструктор как у прочих транспортов → связка **XHTTP + Reality** работает без доп. кода. (XHTTP + XTLS-Vision несовместимы — ограничение протокола, не наше.)

## 4. Критерии приёмки

- `sing-box check -c` принимает VLESS + `transport.type=xhttp` + `tls.reality`.
- Реальный коннект к XHTTP-серверу Xray (ручная проверка), хотя бы `mode=stream-one` и `packet-up`.
- Сборка **без** `with_xhttp`: конфиг с `xhttp` → ошибка `unknown transport type: xhttp` (или эквивалент реестра).
- `go test ./transport/...`, `go vet ./...` зелёные.
- Ребейз-проверка: при следующем upstream-теге конфликты возможны **только** в `transport/v2ray/transport.go`, `constant/v2ray.go`, `option/v2ray_transport.go`.

## 5. Вне скоупа

- **XHTTP server/inbound** (отдельная будущая задача).
- Маппинг `vless://…type=xhttp` в самом лаунчере (репозиторий `singbox-launcher`, follow-up к его задаче 023).
- 100% паритет всех Xray-расширений XHTTP — только то, что нужно для рабочего коннекта.

## 6. Ссылки

- [V2Ray Transport — sing-box](https://sing-box.sagernet.org/configuration/shared/v2ray-transport/)
- [hiddify-sing-box (референс XHTTP)](https://github.com/hiddify/hiddify-sing-box)
- [XHTTP overview (Habr)](https://habr.com/en/articles/990208/)
