# Протоколы и транспорты — полный справочник параметров

> 🌐 English version: **[lx-protocols-transports.md](lx-protocols-transports.md)**.

Исчерпывающий, по-полевой справочник по трём downstream-фичам протоколов/транспортов
`sing-box-lx`:

| Фича | Build tag | Куда крепится | Глава |
|------|-----------|---------------|-------|
| **XHTTP** транспорт (Xray "splithttp"/"xhttp") | `with_xhttp` | блок `transport` у VLESS / VMess / Trojan **outbound** | [§1](#1-xhttp-транспорт) |
| **AmneziaWG 2.0/3.x** (AWG2, AWG3) обфускация | `with_awg` | промо-поля на `wireguard` **endpoint** | [§2](#2-amneziawg-203x-awg2-awg3) |
| **MASQUE** outbound (CONNECT-IP / WARP) | `with_quic` + `with_gvisor` | `outbounds[].type: "masque"` | [§3](#3-masque-outbound-connect-ip--warp) |

Обзор всех downstream-фич целиком (idle-suspend, DNS-группа, VLESS `encryption`,
`lxd`, наблюдаемость) и короткие «getting started» примеры — в
**[lx-config.ru.md](lx-config.ru.md)**. Этот документ — тот глубокий справочник, на
который ссылается обзор: каждое поле, его тип, дефолт, валидация и точный текст
ошибки, когда значение неверно.

Всё здесь взято из option-структур (`option/v2ray_xhttp.go`,
`option/wireguard_awg.go`, `option/masque.go`) и реализаций протоколов, а не по
памяти — дефолты и строки ошибок ровно те, что выдаёт текущее ядро.

> ⚠️ Все ключи / UUID / адреса ниже — **плейсхолдеры**. Никогда не коммить в
> репозиторий реальные приватные ключи и pre-shared-ключи.

**Собери один раз, используй везде.** Desktop/CLI-бинарь несёт все три:

```bash
make -f Makefile.lx lx-build
```

Без тега фича отсутствует, и конфиг, который её использует, **отвергается при
загрузке** с явной ошибкой — никогда не тихий даунгрейд до простого транспорта (это
бы свело на нет обфускацию). Точные сообщения:

- XHTTP без `with_xhttp` → `XHTTP transport requires the with_xhttp build tag`
- AWG-поле без `with_awg` → `AmneziaWG (awg) support is not included in this build, rebuild with -tags with_awg`
- `masque` outbound без `with_quic`+`with_gvisor` → тип `masque` не зарегистрирован (`unknown outbound type: masque`)

---

## Оглавление

- [§1 XHTTP транспорт](#1-xhttp-транспорт)
  - [1.1 Режимы](#11-режимы)
  - [1.2 Основные поля (v1)](#12-основные-поля-v1)
  - [1.3 Размещение session / seq (v2)](#13-размещение-session--seq-v2)
  - [1.4 Размещение uplink-данных (v2, packet-up)](#14-размещение-uplink-данных-v2-packet-up)
  - [1.5 X-Padding обфускация (v2)](#15-x-padding-обфускация-v2)
  - [1.6 Тюнинг packet-up (v2)](#16-тюнинг-packet-up-v2)
  - [1.7 Переиспользование соединений — `xmux`](#17-переиспользование-соединений--xmux)
  - [1.8 Принятые-но-игнорируемые поля](#18-принятые-но-игнорируемые-поля)
  - [1.9 Формы записи диапазонов](#19-формы-записи-диапазонов)
  - [1.10 Примеры](#110-примеры)
  - [1.11 Диагностика](#111-диагностика)
- [§2 AmneziaWG 2.0/3.x (AWG2, AWG3)](#2-amneziawg-203x-awg2-awg3)
  - [2.1 Модель: AWG1 vs AWG2 vs AWG3](#21-модель-awg1-vs-awg2-vs-awg3)
  - [2.2 Junk- и signature-поля](#22-junk--и-signature-поля)
  - [2.3 Magic-заголовки `h1`–`h4`](#23-magic-заголовки-h1h4)
  - [2.4 CPS-декои `i1`–`i5` и формат тегов](#24-cps-декои-i1i5-и-формат-тегов)
  - [2.5 Сахар маскировки `id` / `ip` / `ib`](#25-сахар-маскировки-id--ip--ib)
  - [2.6 Бюджет MTU](#26-бюджет-mtu)
  - [2.7 Маппинг `awg.conf` 1:1](#27-маппинг-awgconf-11)
  - [2.8 Примеры](#28-примеры)
  - [2.9 Ошибки валидации (дословно)](#29-ошибки-валидации-дословно)
  - [2.10 AWG 3.x: защита заголовка, паддинг, хвосты, тайминги](#210-awg-3x-защита-заголовка-паддинг-хвосты-тайминги)
- [§3 MASQUE outbound (CONNECT-IP / WARP)](#3-masque-outbound-connect-ip--warp)
  - [3.1 Что это](#31-что-это)
  - [3.2 Поля, специфичные для MASQUE](#32-поля-специфичные-для-masque)
  - [3.3 Наследуемые DialerOptions](#33-наследуемые-dialeroptions)
  - [3.4 Профили — что переключает `profile`](#34-профили--что-переключает-profile)
  - [3.5 Ключевой материал и форматы значений](#35-ключевой-материал-и-форматы-значений)
  - [3.6 Стратегия SNI](#36-стратегия-sni)
  - [3.7 `vhttp`: auto / h3 / h2](#37-vhttp-auto--h3--h2)
  - [3.8 Idle-suspend и keepalive](#38-idle-suspend-и-keepalive)
  - [3.9 Валидация при старте (fail-fast)](#39-валидация-при-старте-fail-fast)
  - [3.10 Миграция со схемы до SPEC 062](#310-миграция-со-схемы-до-spec-062)
  - [3.11 Примеры](#311-примеры)
  - [3.12 Частые грабли](#312-частые-грабли)

---

# 1. XHTTP транспорт

XHTTP (Xray "splithttp"/"xhttp") — v2ray-транспорт, туннелирующий прокси поверх
обычных HTTP/2-запросов. Крепится к **VLESS / VMess / Trojan** через общий блок
`transport` и сочетается с TLS, включая **Reality**. (XHTTP несовместим с
XTLS-Vision — это ограничение протокола, не наше.)

JSON-ключи в snake_case — под stream settings Xray, sing-box-extended и остальной
sing-box. **Дефолтная форма на проводе (всё ниже на дефолтах) байт-в-байт совпадает с
live-проверенным v1-клиентом**, так что существующие конфиги не затронуты — каждое
v2-поле включается явно (opt-in).

## 1.1 Режимы

`mode` выбирает, как сформированы потоки запроса/ответа (Xray-совместимо):

| Режим | Форма |
|-------|-------|
| `auto` (деф) | резолвится в `stream-one` на Reality TLS, иначе в `packet-up` — то же правило, что применяет Xray |
| `packet-up` | отдельный GET download-поток + последовательные POST upload-пакеты |
| `stream-up` | один стримовый POST upload + отдельный GET download-поток |
| `stream-one` | один двунаправленный HTTP-поток (тело запроса вверх, тело ответа вниз) — ближайший аналог httpupgrade |

Оставляй `mode` на `auto`, пока сервер не документирует иное; задавай явно, только
если знаешь, чего сервер ждёт.

## 1.2 Основные поля (v1)

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `type` | string | — | селектор — должен быть `"xhttp"` |
| `mode` | string | `auto` | см. [§1.1](#11-режимы) |
| `host` | string | TLS SNI / адрес сервера | переопределяет HTTP-заголовок `Host` |
| `path` | string | `""` (корень) | префикс пути запроса; session id (и, для `packet-up`, upload sequence number) добавляются сегментами пути, когда их placement = `path` |
| `headers` | object | — | доп. заголовки запроса на каждом XHTTP-запросе |
| `x_padding_bytes` | string-диапазон | `"100-1000"` | включительный **диапазон** длины (в байтах) значения паддинга (`"min-max"` или одно int). Управляет и длиной legacy Referer `x_padding`, и длиной паддинга в obfs-режиме |
| `no_grpc_header` | bool | `false` | опустить заголовок `Content-Type: application/grpc`, который стримовые режимы (`stream-one`, `stream-up`) несут по умолчанию, зеркаля `NoGRPCHeader` Xray. Этот заголовок несущий перед reverse-прокси / CDN, которые включают стриминг ответа (без буферизации) по gRPC-типу — без него `stream-one` dial может висеть до таймаута. Не включай, если сервер не отвергает gRPC-тип |

## 1.3 Размещение session / seq (v2)

Где несётся **session id** каждого запроса и (packet-up) **upload sequence number**.
Session id опознаёт логическое соединение между расщеплёнными upload/download-потоками;
seq упорядочивает upload-POST-ы.

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `session_placement` | string | `path` | `path` \| `query` \| `header` \| `cookie` |
| `session_key` | string | `X-Session` (header) / `x_session` (query\|cookie) | имя, несущее session id при placement ≠ `path`; не используется для `path` |
| `seq_placement` | string | `path` | `path` \| `query` \| `header` \| `cookie`. Для `path` seq — **второй** добавленный сегмент |
| `seq_key` | string | `X-Seq` (header) / `x_seq` (query\|cookie) | имя, несущее seq при placement ≠ `path`; не используется для `path` |
| `session_table` | string | не задан | алфавит, из которого набирается случайный session id: имя предопределённого набора или свой ASCII-набор. Только клиент |
| `session_length` | string | не задан | длина session id, `"min-max"` или `"n"`. Действует только вместе с `session_table` |

> **Форма session id (`session_table` / `session_length`).** По умолчанию session id —
> дашед-UUID (`8f14e45f-ceea-4d31-9d4f-d0b8e5c1a2b7`), ровно то, что шлёт и
> ненастроенный Xray. Эти две опции заменяют его случайной строкой нужного вида:
> например `"session_table": "Base62"` с `"session_length": "16-32"` даёт
> `k7Qm2XpR9vLdA3wZ`. Смысл — убрать из URL узнаваемый отпечаток UUID, поэтому
> подбирай форму под сервис, который имитируешь.
>
> Имена предопределённых алфавитов (**регистрозависимы**, набор байт-в-байт как у Xray):
> `hex`, `HEX`, `number`, `alphabet`, `Alphabet`, `ALPHABET`, `base36`, `BASE36`,
> `Base62`. Всё остальное трактуется как свой ASCII-алфавит.
>
> Обе опции задаются только вместе — одна без другой отвергается, а не молча
> откатывается к UUID. Нижняя граница длины должна быть больше 0, а пространство
> id (`len(table) ^ min`) — превышать 2^31, чтобы два независимых клиента не
> вытянули одинаковый id и не склеились в одну сессию на сервере.
>
> **Серверу об этом не сообщается.** Для него session id — непрозрачный ключ
> группировки, так что это чисто клиентская ручка: сервер менять не нужно, Xray-сервер
> принимает любую форму. В Xray эти поля зовутся `sessionIDTable` / `sessionIDLength`.

> **Порядок сегментов пути несущий.** Когда оба на `path` (дефолт), session id
> добавляется **первым**, а seq **вторым** — сервер расщепляет именно в этом порядке.
> Не меняй их местами вручную.

## 1.4 Размещение uplink-данных (v2, packet-up)

Куда идёт upload-payload. `header`/`cookie` **валидны только в режиме `packet-up`** —
их использование в другом режиме — ошибка при загрузке.

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `uplink_data_placement` | string | `auto` | `body` \| `auto` (== body) \| `header` \| `cookie`. `header`/`cookie` несут payload как `base64.RawURLEncoding`, нарезанный на заголовки `<key>-<i>` / cookie `<key>_<i>` |
| `uplink_data_key` | string | `X-Data` (header/auto) / `x_data` (cookie) | базовое имя для нарезанного header/cookie-payload; `""` для body |
| `uplink_chunk_size` | string-диапазон | cookie `2048-3072`, header `3000-4000`, иначе `= sc_max_each_post_bytes` | `"min-max"` диапазон (в base64-символах) каждого чанка; минимум прижимается к 64 |
| `uplink_http_method` | string | `POST` | HTTP-метод для **upload**-запросов (download всегда GET); приводится к верхнему регистру. `GET` допустим **только** в `packet-up` |

## 1.5 X-Padding обфускация (v2)

Активна, только когда `x_padding_obfs_mode` = `true`; иначе используется legacy
Referer-паддинг (см. примечание в [§1.10](#110-примеры)). **Длина** паддинга всегда
управляется `x_padding_bytes` ([§1.2](#12-основные-поля-v1)); поля здесь выбирают,
*куда* и *как* кладутся байты паддинга.

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `x_padding_obfs_mode` | bool | `false` | главный переключатель. `false` → legacy Referer `x_padding`. `true` → настраиваемое семейство `x_padding_*` ниже |
| `x_padding_placement` | string | `queryInHeader` | `cookie` \| `header` \| `query` \| `queryInHeader` |
| `x_padding_key` | string | `x_padding` | имя cookie/query-параметра (не используется при placement `header`) |
| `x_padding_header` | string | `X-Padding` | имя заголовка (для placement `header` / `queryInHeader`) |
| `x_padding_method` | string | `repeat-x` | `repeat-x` (N литеральных байт `X`) \| `tokenish` (base62-токен, чья HPACK-Huffman-длина подогнана под ~N) |

## 1.6 Тюнинг packet-up (v2)

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `sc_max_each_post_bytes` | string-диапазон | `"1000000-1000000"` | `"min-max"` диапазон, ограничивающий один upload-POST (порог расщепления) |
| `sc_min_posts_interval_ms` | string-диапазон | `"30-30"` | `"min-max"` anti-burst задержка между последовательными POST-ами, в мс |

## 1.7 Переиспользование соединений — `xmux`

Без пула каждый XHTTP-поток платит полный TCP + TLS (+ REALITY) handshake. `xmux`
переиспользует HTTP-соединения — и, что не менее важно, это то, чего ждут Xray-сервера:
секцию `xmux`, пришедшую из подписки, раньше тихо игнорировали, так что клиент вёл себя
не так, как задумал автор сервера.

**`nil`/отсутствующая секция `xmux` всё равно включает XMUX с Xray-совместимыми
дефолтами** — пул всегда включён, как в Xray-core и sing-box-extended.

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `xmux.max_concurrency` | диапазон | `1-1` | сколько потоков могут делить одно HTTP-соединение. **Взаимоисключимо** с `max_connections` |
| `xmux.max_connections` | диапазон | unlimited | сколько соединений держит пул; ниже этого числа новое соединение открывается всегда. **Взаимоисключимо** с `max_concurrency` |
| `xmux.c_max_reuse_times` | диапазон | unlimited | сколько раз соединение может быть выдано под новый поток, прежде чем выйдет в отставку |
| `xmux.h_max_request_times` | диапазон | `600-900` | сколько **HTTP-запросов** может пройти через соединение до отставки. Считает запросы, не потоки — в `packet-up` один поток шлёт много upload-POST-ов |
| `xmux.h_max_reusable_secs` | диапазон | `1800-3000` | как долго соединение остаётся переиспользуемым, в секундах |
| `xmux.h_keep_alive_period` | int (секунды) | `0` = деф | период HTTP/2 keep-alive ping (`ReadIdleTimeout` транспорта). Отрицательное отключает пинги. Простое целое, **не** диапазон — как в референсной реализации |

**Каждый диапазон роллится один раз, не на запрос:** менеджер роллит `max_concurrency`
и `max_connections` при построении; каждое соединение роллит свои reuse-лимиты при
создании. Соединение на пределе выходит в отставку, но **не рвётся, пока его ещё
используют потоки** — закрытие откладывается до завершения последнего. Только клиент:
серверная половина и секция `download` вне области.

## 1.8 Принятые-но-игнорируемые поля

Присутствуют, чтобы inbound-образный конфиг или симметричная ссылка не падали — клиент
на них не реагирует:

| Ключ | Зачем существует |
|------|------------------|
| `sc_max_concurrent_posts` | legacy-ручка Xray (убрана из текущего Xray-core); текущий Xray сериализует до одного тела POST в полёте, как наш последовательный upload |
| `server_max_header_bytes` | только сервер (`http.Server.MaxHeaderBytes`) |
| `no_sse_header` | только сервер (опустить `Content-Type: text/event-stream` на stream-down ответе); клиент никогда не смотрит `Content-Type` |
| `sc_max_buffered_posts` | только сервер (глубина буфера переупорядочивания upload) |
| `sc_stream_up_server_secs` | только сервер ("min-max" интервал keepalive-паддинга на stream-up ответе). Клиент **не** снимает серверный keepalive-паддинг из stream-up download — проверяй против целевого сервера, если он его шлёт |

## 1.9 Формы записи диапазонов

Каждое поле, помеченное «диапазон» (`x_padding_bytes`, `uplink_chunk_size`, `sc_*`,
все `xmux.*` кроме `h_keep_alive_period`), принимает три формы:

- строка `"min-max"` — `"600-900"`
- одно целое — `4` (эквивалент `4-4`) или `"4000"`
- двухэлементный JSON-массив — `[600, 900]` (для конфигов под Xray / sing-box-extended)

Пустое значение выбирает документированный дефолт.

## 1.10 Примеры

### VLESS + XHTTP + Reality (`stream-one`)

```jsonc
{
  "type": "vless",
  "tag": "xhttp-out",
  "server": "example.com",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "tls": {
    "enabled": true,
    "server_name": "example.com",
    "utls": { "enabled": true, "fingerprint": "chrome" },
    "reality": { "enabled": true, "public_key": "<reality-public-key-base64>", "short_id": "0123abcd" }
  },
  "transport": {
    "type": "xhttp",
    "mode": "stream-one",
    "host": "example.com",
    "path": "/xhttp",
    "x_padding_bytes": "100-1000"
  }
}
```

### packet-up за CDN, с пулом xmux

```jsonc
{
  "type": "vless",
  "tag": "xhttp-cdn",
  "server": "cdn.example.com",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "tls": { "enabled": true, "server_name": "cdn.example.com" },
  "transport": {
    "type": "xhttp",
    "mode": "packet-up",
    "path": "/down",
    "sc_max_each_post_bytes": "800000-1000000",
    "sc_min_posts_interval_ms": "10-30",
    "xmux": {
      "max_concurrency": "4-8",
      "h_max_request_times": "600-900",
      "h_max_reusable_secs": "1800-3000",
      "h_keep_alive_period": 45
    }
  }
}
```

> **Примечание (дефолтный формат на проводе).** С выключенным `x_padding_obfs_mode`
> (дефолт) паддинг несётся как `x_padding=<zeros>` внутри заголовка `Referer`
> (дефолтное размещение Xray) — live-проверено против реального Xray (3x-ui) сервера.
> Сервер валидирует длину `x_padding` (деф 100–1000) и без неё отвечает `400`. Версии
> Xray клиента и сервера всё же должны совпадать (XHTTP эволюционирует быстро).

## 1.11 Диагностика

| Симптом | Вероятная причина |
|---------|-------------------|
| Сервер отвечает **`400`** на каждый запрос | отсутствует/короткий `x_padding` — сервер требует длину; проверь `x_padding_bytes` и что режим совпадает с сервером |
| Сервер отвечает **`404`** | несовпадение префикса `path` — срезанный хвостовой слэш был корнем реального провала `stream-one` (SPEC 043); подтверди точный `path`, который ждёт сервер |
| `stream-one` dial **висит до таймаута**, без ошибки | прокси/CDN забуферизовал ответ, т.к. gRPC content-type отсутствовал — оставь `no_grpc_header` **выключенным** (SPEC 042). Наоборот, если сервер отвергает gRPC-тип — включи |
| Работает с перебоями, ломается через время | рассинхрон версий Xray клиент/сервер — формат XHTTP на проводе меняется быстро; выровняй версии |
| Upload-payload отвергается | `uplink_data_placement: header`/`cookie` вне `packet-up`, или `uplink_http_method: GET` вне `packet-up` — обе ошибки при загрузке, так что видны на старте, а не в рантайме |

---

# 2. AmneziaWG 2.0/3.x (AWG2, AWG3)

AWG — это WireGuard + обфускация для обхода DPI: AWG2 меняет форму пакетов, AWG3
дополнительно шифрует их заголовки и рандомизирует размеры и тайминги
([§2.10](#210-awg-3x-защита-заголовка-паддинг-хвосты-тайминги)). Настраивается как обычный
sing-box **`wireguard` endpoint** с дополнительными промо-полями (все в **корне**
endpoint, ни одно не на peer — зеркаля секцию `[Interface]` из `awg-quick` `.conf`).
С `with_awg` они проталкиваются в device; конфиг без единого AWG-поля — обычный
WireGuard endpoint, **байт-в-байт как в апстриме**.

Рантайм — на `Leadaxe/wireguard-go` (sagernet/wireguard-go + AmneziaWG-обфускация,
подключён через сабмодуль `submodules/wireguard-go`).

## 2.1 Модель: AWG1 vs AWG2 vs AWG3

- **AWG1** = junk/signature/magic-header поля: `jc`, `jmin`, `jmax`, `s1`, `s2`,
  `h1`–`h4` (одиночные значения).
- **AWG2** = AWG1 **плюс** CPS-пакеты `i1`–`i5`, AWG-2.0 junk-size параметры `s3`/`s4`
  и **диапазонные** magic-заголовки (`"min-max"` форма `h1`–`h4`).
- **AWG3** (amneziawg-go v3.0 / v3.1, контейнер `amnezia-awg2` с
  `protocol_version` 3.x) = AWG2 **плюс** защита заголовка
  (`header_protection_key`), паддинг содержимого (`content_padding_addition`),
  случайные хвосты, отключённые cookie, диапазонные тайминги и диапазонный
  `persistent_keepalive_interval` — см. [§2.10](#210-awg-3x-защита-заголовка-паддинг-хвосты-тайминги).

И клиент, и сервер должны крутить AmneziaWG с **совпадающими** параметрами — junk и
I-пакеты это *конфигурация*, не согласовываются. Задавай их из одного `awg.conf` на
обоих концах.

## 2.2 Junk- и signature-поля

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `jc` | int | `0` (unset) | число junk-пакетов, отправляемых до handshake |
| `jmin` | int | `0` | минимальный размер этих junk-пакетов |
| `jmax` | int | `0` | максимальный размер этих junk-пакетов. Держи **ниже** реального path MTU (см. [§2.6](#26-бюджет-mtu)) |
| `s1` | int | `0` | junk, добавляемый перед handshake-сообщением **INIT** |
| `s2` | int | `0` | junk, добавляемый перед handshake-сообщением **RESPONSE** |
| `s3` | int | `0` | AWG 2.0 junk-size param; паддит только **cookie-reply** сообщения (без влияния на MTU) |
| `s4` | int | `0` | AWG 2.0 junk-size param; добавляется перед **каждым transport (data) сообщением** — именно это диктует пониженное требование к [MTU](#26-бюджет-mtu) |

## 2.3 Magic-заголовки `h1`–`h4`

`h1`–`h4` переопределяют четыре magic-значения типов сообщений WireGuard. Каждое —
либо одно `uint32`, **либо** включительная строка-диапазон `"min-max"`.

| Ключ | Тип | Смысл |
|------|-----|-------|
| `h1` | int \| `"min-max"` string | magic для типа сообщения 1 (деф `1`) |
| `h2` | int \| `"min-max"` string | magic для типа сообщения 2 (деф `2`) |
| `h3` | int \| `"min-max"` string | magic для типа сообщения 3 (деф `3`) |
| `h4` | int \| `"min-max"` string | magic для типа сообщения 4 (деф `4`) |

- **Одно** `uint32` (`1234567890`, стиль AWG 1.x) фиксирует значение.
- Строка-**диапазон** (`"43613244-384550127"`, диапазонные заголовки AWG 2.0)
  заставляет device выбирать случайное значение из диапазона на каждое сообщение.
- `0` **или** `""` = unset (считается WireGuard-дефолтом `1`/`2`/`3`/`4`). Простое
  число `N` эквивалентно диапазону `"N-N"`.
- На проводе: одиночное значение маршалится обратно в JSON-**число**; только диапазон
  становится JSON-**строкой** (сохранение типа со старым полем `uint32`).

> **Диапазонные заголовки не должны пересекаться.** Четыре диапазона (unset-заголовок
> считается своим WireGuard-дефолтом) **не должны перекрываться**, иначе device
> отвергает конфиг с `headers must not overlap`. Задавай все четыре вместе, как это
> делают AWG2-экспорты.

## 2.4 CPS-декои `i1`–`i5` и формат тегов

`i1`–`i5` — AWG 2.0 **Controlled Packet Sequence (CPS)** пакеты-приманки,
отправляемые по порядку *до* handshake. Это **регистрозависимые** строки формата
тегов, маппятся 1:1 на ключи `i1`..`i5` amneziawg-go.

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `i1` … `i5` | string | `""` | CPS-пакеты-приманки, шлются по порядку. `i1` обычно имитирует реальный протокол (например QUIC/STUN-заголовок) и **взаимоисключим с сахаром [`id`/`ip`/`ib`](#25-сахар-маскировки-id--ip--ib)** |

**Формат тегов** (UPPERCASE-ключевые слова, порядок важен):

| Тег | Выдаёт |
|-----|--------|
| `<b 0xHEX>` | статические байты (литеральный hex) |
| `<c>` | счётчик |
| `<t>` | timestamp |
| `<r N>` | N случайных байт |
| `<rc N>` | N случайных символов |
| `<rd N>` | N случайных цифр |

Пример: `"i1": "<b 0x000100002112a442><r 12>"` — статический 8-байтовый префикс, за
ним 12 случайных байт.

## 2.5 Сахар маскировки `id` / `ip` / `ib`

Писать `i1`-CPS-строку руками муторно. Как более дружелюбная альтернатива — то же
именование, что у [WireSock Secure Connect](https://www.wiresock.net/) — ты
объявляешь маскировку по **домену / протоколу / браузеру**, и device сам генерирует
`i1`-декой.

| Ключ | Тип | Смысл |
|------|-----|-------|
| `id` | string | **домен** маскировки (хост, выглядящий нормально для твоего региона, напр. `www.google.com`). Строгий LDH-хост. Вшивается в декой для `ip=quic` (как **SNI** в ClientHello), `ip=dns` (как **QNAME**) и `ip=sip` (как **host**); `ip=stun` некуда нести имя хоста и игнорирует его. **Обязателен только для `quic`**; для `dns`/`sip` при отсутствии генерируется псевдо-имя; `stun` игнорирует. Когда задан — всегда LDH-валидируется (инъекционные значения отвергаются) |
| `ip` | string | **протокол** маскировки: `quic` \| `dns` \| `stun` \| `sip` |
| `ib` | string | **браузер** маскировки: `chrome` \| `firefox` \| `curl`. Осмыслен только при `ip=quic`, и даже тогда эффект **минимален** (см. заметки ниже) |

Декой шлётся до handshake, ровно как написанный руками `i1`. Каждый профиль —
**клиент-инициируемый** пакет в форме этого протокола (формы вдохновлены
open-source-референсом WireSock, но эмитятся как клиентский запрос, который peer
реально шлёт первым, а не серверный ответ):

- **`quic`** — целевой **QUIC Initial (RFC 9001)**, несущий реалистичный
  браузер-образный ClientHello (с твоим `id` как SNI) **разбитый на несколько
  out-of-order CRYPTO-фреймов**: первый фрейм на проводе начинается в середине
  ClientHello (offset≠0), так что line-rate DPI, хватающий первый фрейм и
  считающий offset 0, парсит мусор и пропускает (fail-open), а реальный QUIC-сервер
  переупорядочивает фреймы нормально. Раскладка рандомизирована на каждый вызов (нет
  фиксированной кросс-юзерной сигнатуры). `ip=quic` эмитит **один** фрагментированный
  Initial в `i1` — это device-proven DPI-обход (простой QUIC short header
  эмпирически блокировался).
- **`dns`** — клиентский DNS **query** (QR=0, QTYPE HTTPS/тип 65), чей QNAME — твой
  `id`, несущий случайные cover-байты как opaque неизвестную EDNS-опцию.
- **`stun`** — WebRTC/ICE **STUN Binding Request** (magic cookie + USERNAME +
  ICE-CONTROLLING + PRIORITY + SOFTWARE + MESSAGE-INTEGRITY + FINGERPRINT). Это
  клиентский connectivity-check — пакет, который ICE-агент легитимно шлёт первым.
- **`sip`** — SIP **INVITE request** без тела (`i1`: request-line + Via /
  Max-Forwards / To / From / Call-ID / CSeq / Contact + `Content-Type:
  application/sdp` и `Content-Length: 0`, без SDP-тела) в паре с соответствующим
  провизорным ответом **`100 Trying`** того же диалога (`i2`), с твоим `id` (или
  сгенерированным псевдо-host) как host и произносимыми псевдо-именами пользователей.
  `ip=sip` поэтому заполняет **оба** `i1` и `i2`.

**Куда `id` попадает на провод:**

| `ip` | декой | `id` виден цензору? |
|------|-------|---------------------|
| `quic` | фрагментированный QUIC Initial, `id` = **SNI** | **да** — DPI, расшифровывающий Initial (ключи выводятся из DCID), прочтёт его, если соберёт фреймы по порядку |
| `dns` | EDNS query (QR=0), `id` = **QNAME** | **да**, открытым текстом |
| `sip` | SIP INVITE, `id` = **host** в URI | **да** (когда задан), открытым текстом |
| `stun` | STUN Binding Request | **нет** — в STUN нет поля под имя хоста |

**Какой профиль выбрать:**

- **Коннект к WARP под реальным DPI** → `ip=quic`, `id=<популярный домен>`,
  `ib=chrome`. Фрагментированный QUIC Initial с `id` как SNI; device-proven против
  реального LTE-DPI.
- **Нужно, чтобы DPI увидел «разрешённый» домен** → `ip=quic`/`dns`/`sip` с
  региональным популярным `id` (SNI / QNAME / SIP-host).
- **`stun`** нишевый (выглядит как ICE connectivity-check); домен не несёт.

### Заметки и ограничения

- `id`/`ip`/`ib` **взаимоисключимы** с явным `i1` — задавай одно или другое (конфиг с
  обоими отвергается).
- Это **декой** перед handshake, не полноценная протокол-сессия — `quic`-Initial
  никогда не завершает TLS-handshake (ему нужно лишь, чтобы первый пакет потока
  выглядел как легитимный старт QUIC). `id` **попадает** на провод как SNI, так что
  выбирай **правдоподобный, разрешённый** домен — никогда VPN/Cloudflare-маяк.
- DPI-обход держится в первую очередь на **фрагментации CRYPTO-фреймов**, не на
  TLS-fingerprint. `ib` всё же выбирает один: `chrome`/`firefox` эмитят настоящий
  браузерный ClientHello (реальный JA3/JA4) в сборках с поддержкой TLS-мимикрии, а
  `curl` и отсутствующий `ib` используют generic ClientHello. Без этой поддержки
  сборки браузерные профили откатываются к generic.
- Полевой статус: `ip=quic` — **device-proven против реального LTE-DPI**. Для
  `dns`/`stun`/`sip` подтверждены приём движком, структурная валидность и
  `sing-box check`, но систематический полевой A/B против конкретного DPI не
  проводился. На тестовом LTE/WARP DPI `dns`/`stun` упирались в таймаут (DPI режет
  DNS/STUN к дата-центровому IP как класс протокола) — для WARP используй `ip=quic`.
- Мотивирующий кейс — облегчение подключений к **Cloudflare WARP**.

**📖 [Подробные примеры →](../SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md)** —
полные конфиги по каждому профилю (включая Cloudflare WARP), генерируемый CPS для
каждого и дословные ошибки валидации.

## 2.6 Бюджет MTU

`s4` у AmneziaWG добавляет junk-байты перед **каждым transport (data) сообщением**,
поэтому AWG-endpoint нуждается в **более низком `mtu`, чем обычный WireGuard**. (`s3`
паддит только cookie-reply сообщения, так что на бюджет MTU не влияет.) Если
обфусцированный пакет превысит path MTU, ОС его отвергает, и туннель завершает
handshake, но **не может слать данные**:

```
peer(…) - received handshake response
peer(…) - failed to send data packets: write udp4 …: sendmsg: message too long
```

Считай накладные против 1500-байтового пути:

```
mtu ≤ 1500 − 28 (UDP/IP) − 32 (WireGuard) − S4 junk-байт
```

Для `S4 = 60` это `mtu ≤ 1380`. **Используй `1280`** (рекомендованный AmneziaWG
клиентский MTU) для запаса на меньших path MTU (PPPoE, вложенные туннели). Это не
связано с handshake — слишком высокий `mtu` позволяет handshake пройти, но тихо
ломает передачу данных.

**Что sing-box-lx делает за тебя:**

- Если ты опустил `mtu` на endpoint, задающем `s4`, ядро дефолтит на **`1280`**
  (вместо plain-WireGuard `1408`).
- Если ты задал `mtu` явно, и он слишком высок для junk-накладных, ядро логирует
  startup-warning — против консервативного **1492**-байтового (PPPoE) бюджета,
  `mtu ≤ 1492 − 28 − 32 − S4`, так что может отметить значение на несколько байт ниже
  1500-байтового Ethernet-потолка. Предупреждение рекомендательное; туннель всё равно
  грузится.

**Внешний сокет больше не форсит DF (SPEC 028).** По умолчанию sing-box-lx теперь
даёт ОС IP-фрагментировать негабаритную внешнюю датаграмму на `wireguard` endpoint (и
`masque` outbound), а не дропать её — старый дефолт ставил
`IP_MTU_DISCOVER=IP_PMTUDISC_DO` (Linux/Android) / `IP_DONTFRAG` (macOS), что как раз
и производило `sendmsg: message too long` выше. Именно это даёт работать **вложенным
туннелям**: `masque`/`wireguard`/AWG в цепочке через `detour` в любой комбинации, где
внешняя датаграмма рутинно негабаритна и должна фрагментироваться. Чтобы вернуть
старое поведение на конкретном endpoint, задай на нём `"udp_fragment": false`. Подбор
корректного `mtu` (выше) всё равно избегает фрагментации целиком и предпочтителен —
фрагментация это страховка, а не цель.

Также держи `jmax` **ниже** реального path MTU: amneziawg-go предупреждает, что если
размер junk-пакета достигнет системного MTU, он IP-фрагментируется, что те же узкие
пути потом дропают.

## 2.7 Маппинг `awg.conf` 1:1

Маппь файл `awg.conf` / awg-quick напрямую:

| Строка `awg.conf` | JSON |
|-------------------|------|
| `[Interface] PrivateKey / Address / MTU` | корень endpoint `private_key` / `address` / `mtu` |
| `[Interface] Jc / Jmin / Jmax / S1–S4 / I1–I5` | корень endpoint `jc` / `jmin` / `jmax` / `s1`–`s4` / `i1`–`i5` |
| `[Interface] H1 = N` | корень endpoint `"h1": N` (JSON-**число**) |
| `[Interface] H1 = N-M` (AWG2-экспорт) | корень endpoint `"h1": "N-M"` (JSON-**строка**, дословно) |
| `[Interface] HeaderProtectionKey = <base64>` (AWG3) | корень endpoint `"header_protection_key": "<base64>"` (дословно) |
| `[Interface] ContentPaddingAddition / RekeyAfterTime / RekeyTimeout / RejectAfterTime / KeepaliveTimeout / MaxHandshakeAttempts = N-M` (AWG3) | корень endpoint `content_padding_addition` / `rekey_after_time` / `rekey_timeout` / `reject_after_time` / `keepalive_timeout` / `max_handshake_attempts` — строка `"N-M"` (или число `N`) |
| `[Interface] RandomTrailers / DisableCookies = on` (AWG3) | корень endpoint `"random_trailers": true` / `"disable_cookies": true` |
| `[Peer] PublicKey / PresharedKey` | `peers[0].public_key` / `pre_shared_key` |
| `[Peer] Endpoint host:port` | `peers[0].address` + `peers[0].port` |
| `[Peer] AllowedIPs / PersistentKeepalive` | `peers[0].allowed_ips` / `persistent_keepalive_interval` (`N` или `"N-M"` для AWG3-диапазона) |

Если `awg.conf` опускает `MTU` или ставит WireGuard-дефолт `1420`, понизь его для AWG2
(см. [§2.6](#26-бюджет-mtu)).

Экспорт `vpn://` приложения Amnezia несёт те же ключи (`awg` → `last_config` →
`config` — это текст `.conf` выше); `protocol_version: "3.1"` там означает AWG3-сервер.

## 2.8 Примеры

### AmneziaWG 2.0 endpoint (I-пакеты руками)

```jsonc
{
  "type": "wireguard",
  "tag": "awg-out",
  "system": false,
  "mtu": 1280,
  "address": ["10.0.0.2/32"],
  "private_key": "<client-private-key-base64>",

  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  // одиночные значения (AWG 1.x) — либо диапазонные заголовки AWG 2.0, напр.
  // "h1": "43613244-384550127", "h2": "826869626-2105069164",
  // "h3": "2124774725-2141151992", "h4": "2144594503-2146278491",
  "h1": 1234567890, "h2": 1234567891, "h3": 1234567892, "h4": 1234567893,
  "i1": "<b 0x000100002112a442><r 12>",
  "i2": "<b 0x010100002112a442><r 12>",
  "i3": "<r 24>",

  "peers": [
    {
      "address": "server.example.com",
      "port": 51821,
      "public_key": "<server-public-key-base64>",
      "pre_shared_key": "<preshared-key-base64>",
      "allowed_ips": ["0.0.0.0/0", "::/0"],
      "persistent_keepalive_interval": 25
    }
  ]
}
```

### AmneziaWG 3.1 endpoint (экспорт `amnezia-awg2`, `protocol_version` 3.1)

Полный набор параметров живого AWG 3.1-сервера, скопированный 1:1 из его `.conf`
(`H1`–`H4` остаются дефолтами WireGuard — при защите заголовка слово типа всё равно
замаскировано). Проверено end-to-end против этого сервера (SPEC 080).

```jsonc
{
  "type": "wireguard",
  "tag": "awg3-out",
  "system": false,
  "mtu": 1376,
  "address": ["10.8.1.7/32"],
  "private_key": "<client-private-key-base64>",

  "jc": 4, "jmin": 10, "jmax": 50,
  "s1": 55, "s2": 42, "s3": 40, "s4": 12,          // каждый >= 12: в них nonce шифра заголовка
  "h1": 1, "h2": 2, "h3": 3, "h4": 4,

  "header_protection_key": "<HeaderProtectionKey-base64>",   // серверный: обязан совпасть с сервером
  "content_padding_addition": "10-100",
  "rekey_after_time": "100-120",
  "rekey_timeout": "3-7",
  "reject_after_time": "150-180",
  "keepalive_timeout": "5-15",
  "max_handshake_attempts": "15-20",
  "random_trailers": true,
  "disable_cookies": true,

  "peers": [
    {
      "address": "77.239.123.44",
      "port": 30565,
      "public_key": "<server-public-key-base64>",
      "pre_shared_key": "<preshared-key-base64>",
      "allowed_ips": ["0.0.0.0/0", "::/0"],
      "persistent_keepalive_interval": "25-35"    // AWG3-диапазон; обычное число тоже работает
    }
  ]
}
```

### Сахар маскировки (Cloudflare WARP, профиль QUIC)

```jsonc
{
  "type": "wireguard", "tag": "awg-out", "mtu": 1280,
  "address": ["172.16.0.2/32", "2606:4700:110:8000::2/128"],
  "private_key": "<client-private-key-base64>",
  "jc": 4, "jmin": 40, "jmax": 70,
  "id": "www.google.com", "ip": "quic", "ib": "chrome",
  "peers": [ { "address": "engage.cloudflareclient.com", "port": 2408,
    "public_key": "<server-public-key-base64>", "allowed_ips": ["0.0.0.0/0", "::/0"],
    "persistent_keepalive_interval": 25 } ]
}
```

## 2.9 Ошибки валидации (дословно)

| Конфиг | Ошибка |
|--------|--------|
| любое AWG-поле, собрано без `with_awg` | `AmneziaWG (awg) support is not included in this build, rebuild with -tags with_awg` |
| диапазонные `h1`–`h4`, которые пересекаются | `headers must not overlap` |
| `i1` **и** `id`/`ip`/`ib` вместе | `amneziawg: id/ip/ib masquerade conflicts with an explicit i1; use one or the other` |
| `id`/`ib` заданы, `ip` нет | `amneziawg: ip (masquerade protocol) is required when id/ib is set; one of quic\|dns\|stun\|sip` |
| `ip` не из набора | `amneziawg: unknown masquerade protocol "ftp"; one of quic\|dns\|stun\|sip` |
| `ip=quic` без `id` | `amneziawg: id (masquerade domain) is required for ip=quic (it becomes the ClientHello SNI)` |
| `ip=dns` / `ip=sip` / `ip=stun` без `id` | **не ошибка** — генерируется псевдо-имя (`dns`/`sip`); `stun` не нуждается |
| домен с `\r\n` / `;` / `@` / пробелом | `amneziawg: invalid masquerade domain "...": illegal character (only a-z A-Z 0-9 - _ allowed)` |
| `ib` не из набора | `amneziawg: unknown masquerade browser "safari"; one of chrome\|firefox\|curl` |
| `ib` с `ip≠quic` | `amneziawg: ib (browser) is only meaningful with ip=quic, got ip="dns"` |
| `header_protection_key` при любом из `s1`–`s4` меньше 12 | `amneziawg: s4=8 is too short for header_protection_key: each of s1-s4 must be at least 12 bytes (the padding carries the header cipher nonce)` |
| `header_protection_key` не base64 / не 32 байта / все нули | `amneziawg: decode header_protection_key (expected base64, as printed by \`awg genkey\`)` / `… must decode to 32 bytes, got N` / `… is all zeros (the device treats that as "off"); omit the field instead` |
| диапазонное поле с началом > конца или мусор | `invalid range "180-150": range start > end` (ключ называется: `reject_after_time: …`) |
| диапазонный `persistent_keepalive_interval` в сборке без `with_awg` | `AmneziaWG (awg) support is not included in this build (persistent_keepalive_interval range), rebuild with -tags with_awg` |

Домен проходит **строгую LDH-валидацию** (как SNI у WireSock): метки из
`a-z A-Z 0-9 - _`, ≤63 байт, без дефиса по краям; всё имя ≤253, без точки в начале;
одна точка в конце допускается. Это security-граница — домен идёт в текст SIP и в DNS
QNAME, поэтому control-байты и метасимволы отвергаются (защита от инъекции).

## 2.10 AWG 3.x: защита заголовка, паддинг, хвосты, тайминги

AmneziaWG 3.0 (amneziawg-go v3.0.0, tools v3.0.20260730) и 3.1 (v3.1.2026081x)
добавили второй слой поверх AWG2-трюков с формой пакетов. Всё это задаётся на корне
endpoint, как и поля AWG2, и требует `with_awg`. Контейнер Amnezia `amnezia-awg2` с
`protocol_version: "3.1"` экспортирует ровно этот набор.

**Какая сторона должна совпадать.** AmneziaWG различает *серверные* параметры
(значение обязано быть одинаковым на обоих концах — при расхождении хендшейка молча не
будет) и *клиентские* (локальное поведение, серверу безразлично). Серверный здесь
только `header_protection_key`; остальное клиентское — но всё равно копируй значения
сервера, они подобраны вместе.

| Ключ | Тип | Дефолт | Сторона | Смысл |
|------|-----|--------|---------|-------|
| `header_protection_key` | base64-строка (32 байта, `awg genkey`) | `""` (выкл) | **сервер** | Шифрует низкоэнтропийный заголовок каждого пакета: у handshake-сообщения — всё сообщение, у data-пакета — 16-байтный заголовок (тип, receiver index, счётчик). ChaCha20 с этим ключом и nonce на датаграмму = первые 12 байт случайного паддинга сообщения — поэтому **каждый из `s1`–`s4` должен быть ≥ 12**. При включённой защите magic-значения `h1`–`h4` теряют смысл (слово типа на проводе случайно), и AWG3-экспорты оставляют их `1`–`4`. |
| `content_padding_addition` | int \| `"min-max"` | `""` (выкл) | клиент | Дополнительные нулевые байты в plaintext **каждого data-пакета** (внутри AEAD, на проводе случайны), выбираются из диапазона на каждый пакет **вместо** выравнивания WireGuard по 16. Ограничены так, чтобы датаграмма не превысила самую большую уже виденную на пути этого пира («UDP-окно», старт 500 Б) — full-MTU пакет добавки не получает. |
| `random_trailers` | bool | `false` | клиент | Случайный по длине случайный хвост после каждого **handshake**-сообщения (init/response/cookie) и, по тому же правилу UDP-окна, внутри data-пакетов, если не задан `content_padding_addition` — ни у одного вида сообщения нет фиксированного размера. Приёмник принимает handshake-датаграммы *длиннее* ожидаемого и отбрасывает хвост. |
| `disable_cookies` | bool | `false` | клиент | Никогда не слать cookie reply и пропускать under-load-гейт mac2, который бы его потребовал (у cookie-обмена узнаваемая форма). |
| `rekey_after_time` | int \| `"min-max"` секунд | `""` (WireGuard 120) | клиент | Когда инициатор начинает свежий хендшейк. Выбирается из диапазона при каждой проверке. |
| `rekey_timeout` | int \| `"min-max"` секунд | `""` (WireGuard 5) | клиент | Интервал между повторами хендшейка (джиттер по-прежнему добавляется). **Минимум** диапазона — пол между двумя инициациями. |
| `reject_after_time` | int \| `"min-max"` секунд | `""` (WireGuard 180) | клиент | После этого ключевая пара отвергается. Там, где протокол не должен рано выбрасывать ключи, берётся **максимум** диапазона. |
| `keepalive_timeout` | int \| `"min-max"` секунд | `""` (WireGuard 10) | клиент | Пассивный keepalive после принятых данных. |
| `max_handshake_attempts` | int \| `"min-max"` | `""` (WireGuard 18) | клиент | Повторы до отказа от цикла хендшейка (дальше, как и раньше, срабатывает self-heal rebind из SPEC 041). Перевыбирается на каждый цикл. |
| `peers[].persistent_keepalive_interval` | int \| `"min-max"` секунд | `0` (выкл) | клиент | Число WireGuard или AWG3-диапазон, перевыбираемый при каждом взводе. |

Замечания:

- Nonce защиты заголовка берётся из **паддинга**, поэтому на handshake обычного
  размера (`s1`=0) ключ применить нельзя вообще — проверка конфига это отвергает
  ([§2.9](#29-ошибки-валидации-дословно)), устройство тоже.
- `content_padding_addition` и `random_trailers` растят пакеты: бюджет MTU из
  [§2.6](#26-бюджет-mtu) не меняется (кламп по UDP-окну держит добавки внутри
  размеров, которые путь уже проносил), но держи `mtu` на рекомендованном сервером
  значении (`1376` в экспорте Amnezia).
- `random_trailers` расширяет классификацию на приёме: любая датаграмма **длиннее**
  `s1`+148 / `s2`+92 / `s3`+64 *тоже* пробуется как handshake-сообщение по слову типа.
  С одиночными `h1`–`h4` (дефолт AWG3) это ложное совпадение с вероятностью 2⁻³²; с
  широкими AWG2-**диапазонами** `h1`–`h4` вероятность становится ширина/2³² на каждый
  data-пакет, который затем не проходит MAC и отбрасывается. **Наш приёмник к этому
  невосприимчив** (SPEC 081): датаграмма с одним из наших живых receiver index за
  словом типа transport классифицируется как данные до handshake-кандидатов, поэтому
  downlink пакеты так не теряет — и в более узком AWG2-варианте (data-датаграмма ровно
  `s1`+148 байт) тоже. Приёмник сервера — референсная реализация, поэтому **uplink**
  по-прежнему уязвим: не сочетай `random_trailers` с широкими диапазонами `h1`–`h4`.
- Тайминги не обязаны совпадать с сервером, но бессмыслица (например
  `rekey_after_time` выше `reject_after_time`) заставит туннель дёргаться. Копируй
  экспорт сервера.
- Всё это байт-совместимо с amneziawg-go v3.1 (коммит `b5928ef`, 2026-08-28): та же
  раскладка nonce/keystream, тот же порядок классификации, те же правила
  паддинга/хвостов. Проверено против живого сервера `protocol_version 3.1` —
  [SPEC 080](../SPECS/TASKS/080-AWG3_HEADER_PROTECTION_TIMINGS/SPEC.md).

---

# 3. MASQUE outbound (CONNECT-IP / WARP)

## 3.1 Что это

`masque` outbound туннелирует **целые IP-пакеты** поверх HTTP/3- или
HTTP/2-соединения через **CONNECT-IP (RFC 9484)**, в первую очередь для подключения к
**Cloudflare WARP**. (Это CONNECT-IP, не CONNECT-UDP / RFC 9298; и не связано с AWG
`id/ip/ib` *маскировкой* из [§2.5](#25-сахар-маскировки-id--ip--ib) — то же слово,
другая фича.) Ядро строит userspace gVisor-стек на каждый туннель и гонит через него
трафик как через WireGuard endpoint. Гейтится на `with_quic` + `with_gvisor` (оба в
дефолтных `LX_TAGS`). Ключевой материал берётся готовым из конфига — регистрация
устройства (ECDSA-ключи, WARP-enroll) делается клиентом, не ядром.

> Блок `dns` верхнего уровня **обязателен** — userspace-стек работает на L3 и сам
> домены не резолвит; outbound резолвит их через DNS-роутер перед dial. Без него
> доменный трафик через туннель не резолвится (`masque: no DNS router available to
> resolve …`).

> ⚠️ **HTTP-версия это `vhttp`, не `network`** (SPEC 062). Старые конфиги
> использовали `network` для `h3`/`h2` — противоположно тому, что `network` значит на
> любом другом outbound. Старая форма ещё работает и репортит deprecation; см.
> [таблицу миграции](#310-миграция-со-схемы-до-spec-062).

## 3.2 Поля, специфичные для MASQUE

| Ключ | Тип | Обязат. | Дефолт | Смысл |
|------|-----|---------|--------|-------|
| `type` | string | ✅ | — | всегда `"masque"` |
| `tag` | string | ✅ | — | имя outbound (для route/групп) |
| `server` | string | ✅ | — | IP/хост WARP endpoint |
| `server_port` | uint16 | ✅ | — | порт (обычно 443) |
| `profile` | string | — | `cloudflare` | `cloudflare` \| `standard` (см. [§3.4](#34-профили--что-переключает-profile)) |
| `vhttp` | string | — | `auto` | **HTTP-версия, несущая CONNECT-IP**: `auto` (сначала h3, при незавершённом за 3 с QUIC-хендшейке — фолбэк на h2, SPEC 074) \| `h3` (QUIC) \| `h2` (HTTP/2, TCP:443). На `standard` дефолт тихо означает `h3` (h2-ноги там нет). Список tcp/udp — это `network_list`, как везде |
| `private_key` | string (base64) | ✅¹ | — | клиентский EC private key, DER (`x509.ParseECPrivateKey`) |
| `public_key` | string (base64) | ✅¹ | — | PKIX public key endpoint, DER (`x509.ParsePKIXPublicKey`, ECDSA) |
| `ip` | string (CIDR) | ✅² | — | локальный IPv4 внутри туннеля; голый адрес → `/32` |
| `ipv6` | string (CIDR) | ✅² | — | локальный IPv6 внутри туннеля; голый адрес → `/128` |
| `tls` | object | — | — | **стандартный** outbound-блок TLS — `server_name`, `insecure`, `disable_sni`, `fragment`, `record_fragment`, `fragment_fallback_delay`, … Тот же контейнер, что у любого TLS-outbound |
| `uri` | string | — | по профилю³ | CONNECT-IP request URI |
| `mtu` | int | — | `1280` | MTU userspace-стека. На `h2` максимум `16000` (один IP-пакет = один HTTP/2 DATA frame) |
| `idle_timeout` | duration | — | выкл | suspend туннеля после простоя (освобождает gVisor-стек, насосы и QUIC keepalive); следующий dial его пересобирает. **По умолчанию выключен**: отсутствие ключа, `0` и отрицательное держат туннель; включает только положительное значение |
| `keep_alive_period` | duration | — | `30s` | QUIC keepalive (только h3). **Отрицательное отключает** |
| `network_list` | list | — | tcp+udp | L4-протоколы, идущие через туннель |

¹ Обязательны для `profile=cloudflare`; опциональны для `standard`.
² Обязателен **хотя бы один** из `ip`/`ipv6`.
³ Дефолты по профилю — см. [§3.4](#34-профили--что-переключает-profile).

## 3.3 Наследуемые DialerOptions

MASQUE встраивает стандартные sing-box `DialerOptions`, применяемые к исходящему dial
к WARP endpoint. Все опциональны:

| Ключ | Тип | Смысл |
|------|-----|-------|
| `detour` | string | пустить dial к endpoint через другой outbound (вложенные туннели) |
| `bind_interface` | string | привязать к сетевому интерфейсу |
| `inet4_bind_address` / `inet6_bind_address` | addr | исходящий bind-адрес |
| `routing_mark` | int | fwmark (Linux) |
| `reuse_addr` | bool | SO_REUSEADDR |
| `connect_timeout` | duration | таймаут установки соединения |
| `tcp_fast_open` | bool | TFO (актуально на `h2`) |
| `domain_resolver` | object | какой DNS резолвит `server`, если он домен |
| `domain_strategy` | string | `prefer_ipv4` \| `prefer_ipv6` \| `ipv4_only` \| `ipv6_only` |
| `fallback_delay` | duration | задержка happy-eyeballs |
| `network_strategy` / `network_type` / `fallback_network_type` | | мульти-сетевые стратегии |

(Полный перечень — общий `DialerOptions` в `option/outbound.go`.)

## 3.4 Профили — что переключает `profile`

Профиль варьирует четыре вещи; всё остальное (QUIC/HTTP2, capsule/datagram,
userspace-стек, насосы) — профиль-независимо.

| Аспект | `cloudflare` (деф) | `standard` (RFC 9484) |
|--------|--------------------|-----------------------|
| `:protocol` (h3) / `cf-connect-proto` (h2) | `cf-connect-ip` | `connect-ip` |
| Extended CONNECT settings | терпеть отсутствие (WARP их не шлёт) | требовать |
| дефолт `tls.server_name` | `www.cloudflare.com` | = `server` |
| дефолт `uri` | `https://cloudflareaccess.com` | нет (обязателен) |
| TLS-проверка | pinning на `public_key` (ECDSA) | обычная цепочка по `server_name` |
| `private_key`/`public_key` | обязательны | опциональны |

`standard` нацелен на собственный RFC-совместимый CONNECT-IP сервер; к Cloudflare WARP
он **не подключится**. Для WARP всегда используй `cloudflare` (это дефолт). Учти:
`vhttp: h2` **не реализован для `standard`** — ошибка при загрузке.

## 3.5 Ключевой материал и форматы значений

**Ключи** (`private_key` / `public_key`): base64 от DER. `private_key` =
`x509.MarshalECPrivateKey` (SEC1 EC), `public_key` = `x509.MarshalPKIXPublicKey` от
`*ecdsa.PublicKey` (P-256) — ровно тот формат, что отдаёт WARP-регистрация на
клиент-стороне, парсится ядром без преобразований.

**`ip` / `ipv6`**: адрес или CIDR. `"172.16.0.2"` → `172.16.0.2/32`, `"2606:…::"` →
`/128`. Это **локальные** адреса **внутри** туннеля (твой адрес в сети WARP), **не**
выходной IP.

**Duration** (`idle_timeout`, `keep_alive_period`, `connect_timeout`, …): строка
Go-duration — `"30s"`, `"5m"`, `"1h30m"`. Пустая = дефолт. Отрицательная (`"-1s"`) =
«выключить».

## 3.6 Стратегия SNI

> **Дефолтный SNI — `www.cloudflare.com`, не хостнейм endpoint.** Назвать
> MASQUE-endpoint в ClientHello — ровно то, на что фильтрует DPI; нейтральный
> высокотрафиковый хост — нет. Endpoint аутентифицируется pinning-ом `public_key`, так
> что SNI волен отличаться. `tls.disable_sni: true` не шлёт вообще ничего — некоторые
> endpoint-ы предъявляют свой реальный сертификат только на ClientHello без SNI.

Переопределяй `tls.server_name` по узлу (LxBox ротирует пул). Собственное имя
endpoint (`consumer-masque.cloudflareclient.com`) намеренно **не** дефолт — его
отправка измеримо ловила тихий CONNECT-IP таймаут на российских аплинках, тогда как
`www.cloudflare.com`, `yandex.ru`, `www.google.com` и другие нейтральные имена все
коннектятся к тому же endpoint.

## 3.7 `vhttp`: auto / h3 / h2

`auto` — дефолт (SPEC 074): сначала пробует `h3` (QUIC) и падает на `h2`, если
QUIC-хендшейк не завершился за 3 с; победившая нога запоминается до конца процесса.
Отказ, ради которого он существует, **не даёт ошибки** — endpoint (или TCP-only-хоп
перед ним: HTTP CONNECT в `detour`, звено VLESS/Trojan в цепочке) молча глотает QUIC,
и каждый dial просто виснет. Фиксированный `h3` — самый быстрый на заведомо чистом
пути; на сетях, фильтрующих входящий UDP:443, пришпиль узел к `h2` (TCP:443),
device-проверено, что там работает.

- На `h2` один IP-пакет = один HTTP/2 DATA frame, так что `mtu` можно поднять до
  **16000**.
- Путь `h2` гонит свой TLS через общий слой `common/tls`, так что получает
  ClientHello-фрагментацию как любой другой TLS-outbound — включая автоматическую под
  `detour` (SPEC 060). `h3` этим не затронут: QUIC вообще не несёт TLS поверх TCP.
- **Первый** `h3`-dial медленный (холодная настройка CONNECT-IP: QUIC-handshake +
  Extended CONNECT + route advertise + стек), так что короткий urltest-таймаут может
  отметить свежий h3-узел `-1` на первой пробе, хотя после он работает.

Чтобы пришпилить конфиг к одной ноге, задай единственное поле: `"vhttp": "h3"` или `"vhttp": "h2"`.

## 3.8 Idle-suspend и keepalive

- `idle_timeout` suspend-ит туннель после такого простоя без трафика, освобождая
  gVisor-стек, насосы и QUIC keepalive; следующий dial его пересобирает.
  **По умолчанию выключен** (с lx.31): отсутствие ключа, `0` и отрицательное значение
  держат туннель до закрытия outbound'а; включает только положительное (например
  `"5m"`). Пробуждение после сна стоит полного QUIC-хендшейка + CONNECT-IP + нового
  gVisor-стека на первом запросе после тишины — на роутерах и десктопах это хуже, чем
  ~6 МБ RSS и один keepalive-пакет в 30 с, которые сон экономит. Включай явно на
  хостах с батареей, где размен обратный.
- `keep_alive_period` (деф `30s`, только h3) — интервал QUIC keepalive. Отрицательное
  значение отключает.
- **Взаимодействие:** при выключенном suspend именно keepalive держит туннель сквозь
  idle-таймаут сервера и UDP NAT-мэппинг провайдера — **не** выключай его (`-1s`),
  если только `idle_timeout` не настолько короткий, что туннель сносится раньше, чем
  keepalive становится нужен; иначе сервер сбросит туннель по своему idle, а
  следующий dial молча заплатит пересборку.
- Выходной IP **меняется** после idle-suspend/reconnect — WARP anycast раздаёт разный
  edge-адрес на каждое новое подключение. Внутренний `ip`/`ipv6` при этом стабилен.

## 3.9 Валидация при старте (fail-fast)

Конфиг отвергается при загрузке, если:

- `profile=cloudflare`, но `private_key`/`public_key` отсутствует/не парсится →
  `masque: private_key and public_key are required for the cloudflare profile` /
  `parse private_key` / `parse public_key`;
- не задано ни `ip`, ни `ipv6` → `masque: at least one of ip/ipv6 is required`;
- `vhttp` ∉ {`h3`, `h2`, `auto`} (напр. привычный `"tcp"`) →
  `masque: invalid vhttp: … (expected h3, h2 or auto)`;
- `vhttp=h2` и `mtu > 16000` → `masque: mtu … too large for h2 (max 16000)`;
- `vhttp=h2` и `profile=standard` →
  `masque: vhttp h2 is not implemented for the standard profile`;
- `profile=standard` без `uri` → `masque: uri is required for the standard profile`;
- `public_key` не ECDSA-ключ → `public_key is not an ECDSA key`.

## 3.10 Миграция со схемы до SPEC 062

Обе формы принимаются до **v1.14.0-lx.30**; использование legacy-поля логирует одну
строку deprecation на outbound. Legacy-поле, которое *расходится* со своей заменой, —
жёсткая ошибка, а не тихий выбор.

| Legacy (устарело) | Актуальное |
|---|---|
| `network: "h3"` / `"h2"` | `vhttp: "h3"` / `"h2"` |
| `sni` | `tls.server_name` |
| `skip_cert_verify: true` | `tls.insecure: true` |
| `fragment: true` | `tls.fragment: true` |
| `fragment_fallback_delay` | `tls.fragment_fallback_delay` |
| `record_fragment: true` | `tls.record_fragment: true` |

> Legacy-булевы не отличают «отсутствует» от явного `false`, так что переносится
> только legacy `true` — пиши `tls`-форму, если нужно что-то выключить.

## 3.11 Примеры

### WARP поверх h3 (QUIC)

```jsonc
{
  "type": "masque",
  "tag": "warp",
  "server": "162.159.198.2",
  "server_port": 443,
  "profile": "cloudflare",
  "vhttp": "h3",
  "tls": {
    "server_name": "www.microsoft.com"   // любой нейтральный высокотрафиковый хост (domain-fronting)
  },
  "private_key": "<base64 DER EC private key>",
  "public_key":  "<base64 DER PKIX public key>",
  "ip":   "172.16.0.2/32",
  "ipv6": "2606:4700:110:...::/128",
  "mtu":  1280
}
```

### WARP поверх h2 (CONNECT-IP over TCP:443)

То же, что выше, с одним изменённым полем:

```jsonc
{ /* … */ "vhttp": "h2", "mtu": 1280 }
```

### Минимальный конфиг (только обязательное)

```jsonc
{
  "type": "masque",
  "tag": "warp",
  "server": "162.159.198.2",
  "server_port": 443,
  "private_key": "<base64 DER EC private key>",
  "public_key":  "<base64 DER PKIX public key>",
  "ip":   "172.16.0.2",
  "ipv6": "2606:4700:110:...::"
}
```

(profile=cloudflare, vhttp=h3, `tls.server_name`=`www.cloudflare.com`,
uri=`https://cloudflareaccess.com`, mtu=1280, idle_timeout=выкл, keep_alive_period=30s,
network_list=tcp+udp — всё по умолчанию. Не забудь блок `dns` верхнего уровня.)

## 3.12 Частые грабли

1. **`vhttp` ≠ tcp/udp.** У `masque` `vhttp` — это HTTP-версия (h3/h2). Список tcp/udp
   — это `network_list`. `"vhttp": "tcp"` даст fail-fast. У всех прочих outbound
   `network` наоборот — здесь `vhttp` by design.
2. **Блок `dns` верхнего уровня обязателен** — иначе доменный трафик через туннель не
   резолвится.
3. **Выходной IP меняется** после idle-suspend/reconnect — WARP anycast раздаёт разные
   edge-адреса на каждое новое подключение. Внутренний `ip`/`ipv6` стабилен.
4. **`tls.insecure: true`** снимает pubkey-pinning целиком — единственную защиту, когда
   WARP SNI замаскирован. Только для отладки.
5. **`keep_alive_period` vs `idle_timeout`** — см. [§3.8](#38-idle-suspend-и-keepalive).

**📖 Статус.** Device-verified end-to-end на реальном Wi-Fi и LTE — `warp=on`,
реальный трафик на обоих `h3` и `h2`, idle-suspend + самовосстанавливающийся reconnect
подтверждены на устройстве.

---

## См. также

- **[lx-config.ru.md](lx-config.ru.md)** — обзор downstream-фич, который эти главы
  разворачивают (таблица фич, build-теги, плюс idle-suspend, DNS-группа, VLESS
  `encryption`, `lxd`, наблюдаемость).
- **[lx-energy.ru.md](lx-energy.ru.md)** — энергомодель, тайминги idle-suspend и
  рекомендованная мобильная конфигурация (актуально для suspend AWG- и MASQUE-endpoint).
- Feature-спеки: [XHTTP](../SPECS/FEATURES/002-XHTTP/), [AWG2](../SPECS/FEATURES/003-AWG2/),
  [MASQUE/WARP](../SPECS/FEATURES/009-MASQUE_WARP/).
