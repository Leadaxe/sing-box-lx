# Протоколы и транспорты — полный справочник параметров

> 🌐 English version: **[protocols-transports.md](protocols-transports.md)**.

> 🧭 **Где что искать.** Документация форка — три уровня, по вопросу читателя:
> [lx-config](lx-config.ru.md) — что есть в форке и как включить;
> [protocols-transports](protocols-transports.ru.md) — каждое поле, тип, дефолт, текст ошибки;
> [xray-protocols-explained](xray-protocols-explained.ru.md), [amneziawg-explained](amneziawg-explained.ru.md) и [masque-explained](masque-explained.ru.md) —
> как устроено, почему, как сделано у нас и чем отличается от ванили.

Справочник по каждому полю downstream-фич протоколов и транспортов
`sing-box-lx`:

| Фича | Build tag | Куда крепится | Глава |
|------|-----------|---------------|-------|
| **XHTTP** транспорт (Xray "splithttp"/"xhttp") | `with_xhttp` | блок `transport` у VLESS / VMess / Trojan outbound | [§1](#1-xhttp-транспорт) |
| **AmneziaWG 2.0/3.x** (AWG2, AWG3) обфускация | `with_awg` | промо-поля на `wireguard` endpoint | [§2](#2-amneziawg-203x-awg2-awg3) |
| **MASQUE** outbound (CONNECT-IP / WARP) | `with_quic` + `with_gvisor` | `outbounds[].type: "masque"` | [§3](#3-masque-outbound-connect-ip--warp) |
| **REALITY**-клиент (гибридный key share, `key_share`, фрагментация) и **VLESS `encryption`** | — (REALITY внутри `with_utls`) | блок `tls` у TLS-over-TCP outbound; плоское поле `encryption` у `vless` | [§5](#5-reality-и-vless-encryption) |
| **TrustTunnel** outbound и inbound (протокол AdGuard VPN) | `with_trusttunnel` (+ `with_quic` для HTTP/3), в `LX_TAGS` с lx.21, не в AAR | `outbounds[].type` / `inbounds[].type: "trusttunnel"` | [§6](#6-trusttunnel-outbound-и-inbound) |

[§4](#4-grpc-транспорт) описывает gRPC-транспорт. Он апстримный, а в справочник
попал из-за одного места с downstream-заметками по совместимости — форм
`service_name`.

Обзор всех downstream-фич (idle-suspend, DNS-группа, VLESS `encryption`, `lxd`,
наблюдаемость) и короткие примеры «getting started» — в
[lx-config.ru.md](lx-config.ru.md). Этот документ — справочник, на который
ссылается обзор: каждое поле, его тип, дефолт, валидация и точный текст ошибки
на неверное значение.

Всё здесь взято из option-структур (`option/v2ray_xhttp.go`,
`option/wireguard_awg.go`, `option/masque_lx.go`) и реализаций протоколов.
Дефолты и строки ошибок те же, что выдаёт текущее ядро.

> ⚠️ Все ключи / UUID / адреса ниже — плейсхолдеры. Никогда не коммить в
> репозиторий реальные приватные ключи и pre-shared-ключи.

Desktop/CLI-бинарь несёт все четыре и собирается одной командой:

```bash
make -f Makefile.lx lx-build
```

Без тега фичи в сборке нет, и конфиг, который её использует, отвергается при
загрузке с явной ошибкой. Тихого даунгрейда до простого транспорта нет: он свёл бы
на нет обфускацию. Тексты ошибок:

- XHTTP без `with_xhttp` → `XHTTP transport requires the with_xhttp build tag`
- AWG-поле без `with_awg` → `AmneziaWG (awg) support is not included in this build, rebuild with -tags with_awg`
- `masque` outbound без `with_quic`+`with_gvisor` → тип `masque` не зарегистрирован (`unknown outbound type: masque`)
- `trusttunnel` без `with_trusttunnel` → `trusttunnel is not included in this build, rebuild with -tags with_trusttunnel` (только в своей сборке без тега: `lx-build` несёт его с lx.21, Android AAR — нет; какие сборки несут TrustTunnel — в [§6](#6-trusttunnel-outbound-и-inbound))

---

## Оглавление

- [1. XHTTP транспорт](#1-xhttp-транспорт)
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
- [2. AmneziaWG 2.0/3.x (AWG2, AWG3)](#2-amneziawg-203x-awg2-awg3)
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
- [3. MASQUE outbound (CONNECT-IP / WARP)](#3-masque-outbound-connect-ip--warp)
  - [3.1 Что это](#31-что-это)
  - [3.2 Поля, специфичные для MASQUE](#32-поля-специфичные-для-masque)
  - [3.3 Наследуемые DialerOptions](#33-наследуемые-dialeroptions)
  - [3.4 Профили — что переключает `profile`](#34-профили--что-переключает-profile)
  - [3.5 Ключевой материал и форматы значений](#35-ключевой-материал-и-форматы-значений)
  - [3.6 Стратегия SNI](#36-стратегия-sni)
  - [3.7 `vhttp`: auto / h3 / h2](#37-vhttp-auto--h3--h2)
  - [3.8 Idle-suspend и keepalive](#38-idle-suspend-и-keepalive)
  - [3.9 Валидация при старте (fail-fast)](#39-валидация-при-старте-fail-fast)
  - [3.10 Миграция со старой схемы](#310-миграция-со-старой-схемы)
  - [3.11 Примеры](#311-примеры)
  - [3.12 Частые грабли](#312-частые-грабли)
- [4. gRPC-транспорт](#4-grpc-транспорт)
  - [4.1 `service_name`: формы Xray](#41-service_name-формы-xray)
- [5. REALITY и VLESS `encryption`](#5-reality-и-vless-encryption)
  - [5.1 Поля `tls.reality`](#51-поля-tlsreality)
  - [5.2 Отпечатки и гибридный key share](#52-отпечатки-и-гибридный-key-share)
  - [5.3 Фрагментация ClientHello](#53-фрагментация-clienthello)
  - [5.4 Ошибки валидации REALITY (дословно)](#54-ошибки-валидации-reality-дословно)
  - [5.5 Поле `encryption` у `vless`](#55-поле-encryption-у-vless)
  - [5.6 Ошибки валидации `encryption` (дословно)](#56-ошибки-валидации-encryption-дословно)
  - [5.7 Примеры](#57-примеры)
- [6. TrustTunnel outbound и inbound](#6-trusttunnel-outbound-и-inbound)
  - [6.1 Что это](#61-что-это)
  - [6.2 Поля outbound](#62-поля-outbound)
  - [6.3 Проверки при старте (дословно)](#63-проверки-при-старте-дословно)
  - [6.4 Поведение сессии](#64-поведение-сессии)
  - [6.5 Соответствие ключей outbound](#65-соответствие-ключей-outbound)
  - [6.6 Поля inbound](#66-поля-inbound)
  - [6.7 Поведение inbound](#67-поведение-inbound)
  - [6.8 Соответствие ключей inbound](#68-соответствие-ключей-inbound)
  - [6.9 Примеры](#69-примеры)

---

# 1. XHTTP транспорт

> 🧭 Как XHTTP устроен, почему так и как сделан в форке — [xray-protocols-explained §4](xray-protocols-explained.ru.md#4-xhttp); диагностика по симптому — там же, [§4.6](xray-protocols-explained.ru.md#46-расхождение-с-сервером-проявляется-тишиной-а-не-ошибкой). Здесь — только поля, дефолты и ошибки.

XHTTP (Xray "splithttp"/"xhttp") — v2ray-транспорт, который туннелирует прокси
поверх обычных HTTP-запросов. По умолчанию это HTTP/2; HTTP/1.1 или HTTP/3 — когда
их запрашивает TLS-блок ([версия HTTP](#версия-http)). Транспорт крепится к
VLESS / VMess / Trojan через общий блок `transport` и сочетается с TLS, включая
Reality. XTLS-Vision поверх XHTTP работает только вместе с `encryption` у
VLESS-outbound'а: тогда Vision опирается на слой шифрования, а не на TLS
транспорта, как в Xray.

JSON-ключи в snake_case — как в stream settings Xray, sing-box-extended и остальном
sing-box. Когда все поля ниже на дефолтах, форма на проводе байт-в-байт совпадает с
v1-клиентом, проверенным вживую. Существующие конфиги это не затрагивает: каждое
v2-поле включается явно (opt-in).

## 1.1 Режимы

`mode` выбирает, как сформированы потоки запроса/ответа (Xray-совместимо):

| Режим | Форма |
|-------|-------|
| `auto` (деф) | резолвится в `stream-one` на Reality TLS, иначе в `packet-up` — то же правило, что применяет Xray |
| `packet-up` | отдельный GET download-поток + последовательные POST upload-пакеты |
| `stream-up` | один стримовый POST upload + отдельный GET download-поток |
| `stream-one` | один двунаправленный HTTP-поток (тело запроса вверх, тело ответа вниз) — ближайший аналог httpupgrade |

Оставляй `mode` на `auto`, пока сервер не документирует иное. Задавай явно, только
если знаешь, чего сервер ждёт.

### Версия HTTP

Отдельного ключа нет: версия HTTP выводится из блока `tls` outbound'а по правилу
Xray (`decideHTTPVersion`). Все режимы работают на всех версиях.

| TLS | Reality | `tls.alpn` | Версия | Провод |
|-----|---------|-----------|--------|--------|
| выкл | — | — | HTTP/1.1 | TCP |
| вкл | вкл | любой | HTTP/2 | TCP + Reality |
| вкл | выкл | пусто | HTTP/2 | TCP + TLS, ALPN `h2` |
| вкл | выкл | два и больше элементов | HTTP/2 | TCP + TLS, ALPN как в конфиге |
| вкл | выкл | `["http/1.1"]` | HTTP/1.1 | TCP + TLS |
| вкл | выкл | `["h3"]` | HTTP/3 | UDP + QUIC |
| вкл | выкл | любой другой один элемент | HTTP/2 | TCP + TLS, ALPN как в конфиге |

- Элемент сравнивается точно (`h3`, `http/1.1`).
- **HTTP/3** требует сборки с `with_quic` (у всех поставляемых сборок он есть);
  без него конфиг с `alpn: ["h3"]` не грузится. QUIC-рукопожатие идёт с профилем
  Chrome. `utls.fingerprint` на HTTP/3 не применяется: при загрузке выходит
  предупреждение, конфиг грузится, Xray ведёт себя так же. С `disable_sni` профиль
  Chrome для этого узла выключается, тоже с предупреждением. ECH на HTTP/3 не
  поддержан: конфиг грузится, dial узла отвечает ошибкой. Трафик HTTP/3 идёт по
  UDP через тот же `detour`.
- **HTTP/1.1**: долгие запросы (download-GET, потоковый POST) идут каждый по своему
  соединению с `Connection: close`. Upload-POST'ы `packet-up` переиспользуют
  keep-alive-соединения.
- **Reality — всегда HTTP/2.** `tls.alpn` без `h2` заменяется на `["h2"]` с
  предупреждением: иначе Reality-сервер с `alpn: ["h3"]` по TCP не поднимется.
- **Без TLS — HTTP/1.1, как у Xray.** Сервер Xray принимает и HTTP/1.1, и h2c,
  поэтому работают и прямые узлы без TLS, и узлы за обратным прокси или CDN,
  который на незашифрованном порту говорит только HTTP/1.1. Мультиплексирования
  нет: каждый поток даунлинка и каждый потоковый запрос идут отдельным
  TCP-соединением.
- Не поддержаны `downloadSettings` Xray и `finalmask.quicParams`. QUIC работает
  с дефолтами Xray; контроль перегрузки — Cubic, у Xray — BBR.

## 1.2 Основные поля (v1)

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `type` | string | — | селектор — должен быть `"xhttp"` |
| `mode` | string | `auto` | см. [§1.1](#11-режимы) |
| `host` | string | TLS SNI / адрес сервера | переопределяет HTTP-заголовок `Host` |
| `path` | string | `""` (корень) | префикс пути запроса; session id (и, для `packet-up`, upload sequence number) добавляются сегментами пути, когда их placement = `path` |
| `headers` | object | — | доп. заголовки запроса на каждом XHTTP-запросе |
| `x_padding_bytes` | string-диапазон | `"100-1000"` | включительный диапазон длины значения паддинга в байтах (`"min-max"` или одно int). Задаёт и длину legacy Referer `x_padding`, и длину паддинга в obfs-режиме |
| `no_grpc_header` | bool | `false` | не слать заголовок `Content-Type: application/grpc`, который стримовые режимы (`stream-one`, `stream-up`) шлют по умолчанию; аналог `NoGRPCHeader` Xray. Reverse-прокси и CDN включают стриминг ответа без буферизации по gRPC-типу, и без этого заголовка dial `stream-one` может висеть до таймаута. Не включай, если сервер не отвергает gRPC-тип |

## 1.3 Размещение session / seq (v2)

Где передаются session id каждого запроса и, в `packet-up`, upload sequence number.
Session id опознаёт логическое соединение между расщеплёнными upload- и
download-потоками. Seq упорядочивает upload-POST-ы.

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `session_placement` | string | `path` | `path` \| `query` \| `header` \| `cookie` |
| `session_key` | string | `X-Session` (header) / `x_session` (query\|cookie) | имя, несущее session id при placement ≠ `path`; не используется для `path` |
| `seq_placement` | string | `path` | `path` \| `query` \| `header` \| `cookie`. Для `path` seq — второй добавленный сегмент |
| `seq_key` | string | `X-Seq` (header) / `x_seq` (query\|cookie) | имя, несущее seq при placement ≠ `path`; не используется для `path` |
| `session_table` | string | не задан | алфавит, из которого набирается случайный session id: имя предопределённого набора или свой ASCII-набор. Только клиент |
| `session_length` | string | не задан | длина session id, `"min-max"` или `"n"`. Действует только вместе с `session_table` |

По умолчанию session id — UUID с дефисами (`8f14e45f-ceea-4d31-9d4f-d0b8e5c1a2b7`),
его же шлёт ненастроенный Xray. `session_table` и `session_length` заменяют его
случайной строкой нужного вида: `"session_table": "Base62"` с
`"session_length": "16-32"` даёт `k7Qm2XpR9vLdA3wZ`. Так из URL уходит узнаваемый
отпечаток UUID, поэтому подбирай форму под сервис, который имитируешь.

Имена предопределённых алфавитов регистрозависимы, наборы байт-в-байт как у Xray:
`hex`, `HEX`, `number`, `alphabet`, `Alphabet`, `ALPHABET`, `base36`, `BASE36`,
`Base62`. Любое другое значение трактуется как свой ASCII-алфавит.

Обе опции задаются только вместе: одна без другой отвергается, отката к UUID нет.
Нижняя граница длины должна быть больше 0, а пространство id (`len(table) ^ min`) —
больше 2^31. Иначе два независимых клиента могут вытянуть одинаковый id и
склеиться в одну сессию на сервере.

Серверу форма не сообщается: для него session id — непрозрачный ключ группировки.
Ручка чисто клиентская: сервер менять не нужно, Xray-сервер принимает любую форму.
В Xray эти поля зовутся `sessionIDTable` / `sessionIDLength`.

> ⚠️ Когда session id и seq оба на `path` (дефолт), session id добавляется первым
> сегментом, а seq вторым, и сервер разбирает путь в этом порядке. Не меняй их
> местами вручную.

## 1.4 Размещение uplink-данных (v2, packet-up)

Куда идёт upload-payload. Значения `header` и `cookie` допустимы только в режиме
`packet-up`, в другом режиме они дают ошибку при загрузке.

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `uplink_data_placement` | string | `auto` | `body` \| `auto` (== body) \| `header` \| `cookie`. `header`/`cookie` несут payload как `base64.RawURLEncoding`, нарезанный на заголовки `<key>-<i>` / cookie `<key>_<i>` |
| `uplink_data_key` | string | `X-Data` (header/auto) / `x_data` (cookie) | базовое имя для нарезанного header/cookie-payload; `""` для body |
| `uplink_chunk_size` | string-диапазон | cookie `2048-3072`, header `3000-4000`, иначе `= sc_max_each_post_bytes` | `"min-max"` диапазон (в base64-символах) каждого чанка; минимум прижимается к 64 |
| `uplink_http_method` | string | `POST` | HTTP-метод для upload-запросов (download всегда GET); приводится к верхнему регистру. `GET` допустим только в `packet-up` |

## 1.5 X-Padding обфускация (v2)

Обфускация работает, только когда `x_padding_obfs_mode` = `true`. Иначе клиент
шлёт legacy-паддинг в Referer; его формат описан абзацем после примеров
[§1.10](#110-примеры). Длину паддинга всегда задаёт `x_padding_bytes`
([§1.2](#12-основные-поля-v1)), а поля ниже выбирают, куда и как кладутся байты
паддинга.

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

Без пула каждый XHTTP-поток проходит полный handshake TCP + TLS (+ REALITY). `xmux`
переиспользует HTTP-соединения, и этого же ждут Xray-серверы: клиент применяет
секцию `xmux` из подписки так, как задумал автор сервера.

Пул включён всегда, как в Xray-core: секция `xmux`, которой нет или которая `nil`,
включает XMUX с Xray-совместимыми дефолтами. Дефолты действуют, только если секция
отсутствует или полностью пуста. Секция хотя бы с одним полем берётся как есть.

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `xmux.max_concurrency` | диапазон | unlimited | сколько потоков могут делить одно HTTP-соединение. Взаимоисключимо с `max_connections` |
| `xmux.max_connections` | диапазон | `3-3` | сколько соединений держит пул; ниже этого числа новое соединение открывается всегда. Взаимоисключимо с `max_concurrency` |
| `xmux.c_max_reuse_times` | диапазон | unlimited | сколько раз соединение может быть выдано под новый поток, прежде чем выйдет в отставку |
| `xmux.h_max_request_times` | диапазон | `600-900` | сколько HTTP-запросов может пройти через соединение до отставки. Считает запросы, не потоки — в `packet-up` один поток шлёт много upload-POST-ов |
| `xmux.h_max_reusable_secs` | диапазон | `1800-3000` | как долго соединение остаётся переиспользуемым, в секундах |
| `xmux.h_keep_alive_period` | int (секунды) | `0` = деф | Период keep-alive. HTTP/2: период ping (`ReadIdleTimeout` транспорта), `0` = без пингов. HTTP/3: QUIC keep-alive, `0` = 10 с. На HTTP/1.1 не действует. Отрицательное отключает keep-alive. Простое целое, не диапазон, как в референсной реализации |

Каждый диапазон роллится один раз, а не на каждый запрос. Менеджер роллит
`max_concurrency` и `max_connections` при построении, каждое соединение роллит свои
reuse-лимиты при создании. Соединение на пределе выходит в отставку, но не рвётся,
пока его используют потоки: закрытие ждёт завершения последнего. Реализован только
клиент: серверная половина и секция `download` вне области.

## 1.8 Принятые-но-игнорируемые поля

Клиент принимает эти поля, чтобы конфиг в форме inbound или симметричная ссылка не
давали ошибку, но на них не реагирует:

| Ключ | Зачем существует |
|------|------------------|
| `sc_max_concurrent_posts` | legacy-ручка Xray (убрана из текущего Xray-core); текущий Xray сериализует до одного тела POST в полёте, как наш последовательный upload |
| `server_max_header_bytes` | только сервер (`http.Server.MaxHeaderBytes`) |
| `no_sse_header` | только сервер (опустить `Content-Type: text/event-stream` на stream-down ответе); клиент никогда не смотрит `Content-Type` |
| `sc_max_buffered_posts` | только сервер (глубина буфера переупорядочивания upload) |
| `sc_stream_up_server_secs` | только сервер ("min-max" интервал keepalive-паддинга на stream-up ответе). Клиент не снимает серверный keepalive-паддинг из stream-up download — проверяй против целевого сервера, если он его шлёт |

## 1.9 Формы записи диапазонов

Каждое поле, помеченное «диапазон» (`x_padding_bytes`, `uplink_chunk_size`, `sc_*`,
все `xmux.*` кроме `h_keep_alive_period`), принимает три формы:

- строка `"min-max"` — `"600-900"`
- одно целое — `4` (эквивалент `4-4`) или `"4000"`
- двухэлементный JSON-массив — `[600, 900]` (для конфигов под Xray / sing-box-extended)

Пустое значение означает дефолт из таблицы поля.

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

### HTTP/3 (`alpn: ["h3"]`)

Сервер слушает XHTTP только по QUIC (Xray `tlsSettings.alpn: ["h3"]`).

```jsonc
{
  "type": "vless",
  "tag": "xhttp-h3",
  "server": "example.com",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "tls": {
    "enabled": true,
    "server_name": "example.com",
    "alpn": ["h3"],
    "certificate_public_key_sha256": ["<base64-sha256-ключа-сервера>"]
  },
  "transport": {
    "type": "xhttp",
    "mode": "stream-up",
    "path": "/xhttp"
  }
}
```

С выключенным `x_padding_obfs_mode` (дефолт) паддинг идёт как `x_padding=<zeros>`
в заголовке `Referer` — это дефолтное размещение Xray. Формат проверен вживую на
сервере Xray (3x-ui). Сервер проверяет длину `x_padding` (дефолт 100–1000) и без
неё отвечает `400`. Версии Xray клиента и сервера всё равно должны совпадать: XHTTP
быстро меняется.

## 1.11 Диагностика

Диагностика по симптому — в объясняющем документе:
[xray-protocols-explained §4.6 «Грабли, на которых соединение молчит»](xray-protocols-explained.ru.md#46-расхождение-с-сервером-проявляется-тишиной-а-не-ошибкой).
Здесь — только поля: таблицы выше и принятые-но-игнорируемые поля
([§1.8](#18-принятые-но-игнорируемые-поля)).

---

# 2. AmneziaWG 2.0/3.x (AWG2, AWG3)

> 🧭 Как AmneziaWG устроен, бюджет MTU, графт и валидация — [amneziawg-explained](amneziawg-explained.ru.md); типичные отказы — там же, [§10](amneziawg-explained.ru.md#10-типичные-отказы). Здесь — только поля, дефолты и ошибки.

AWG — это WireGuard с обфускацией против DPI: AWG2 меняет форму пакетов, AWG3
вдобавок шифрует их заголовки и рандомизирует размеры и тайминги
([§2.10](#210-awg-3x-защита-заголовка-паддинг-хвосты-тайминги)). AWG настраивается
как обычный `wireguard` endpoint sing-box с дополнительными промо-полями. Все они
лежат в корне endpoint, ни одно не на peer — как секция `[Interface]` в `.conf`
для `awg-quick`. С `with_awg` поля передаются в device. Конфиг без единого
AWG-поля — обычный WireGuard endpoint, байт-в-байт как в апстриме.

Рантайм — на `Leadaxe/wireguard-go` (sagernet/wireguard-go + AmneziaWG-обфускация,
подключён через сабмодуль `submodules/wireguard-go`).

## 2.1 Модель: AWG1 vs AWG2 vs AWG3

| Слой | Ключи |
|------|-------|
| **AWG1** | `jc`, `jmin`, `jmax`, `s1`, `s2`, `h1`–`h4` одиночными значениями |
| **AWG2** | AWG1 плюс `i1`–`i5`, `s3`/`s4`, `h1`–`h4` диапазоном `"min-max"`, сахар `id`/`ip`/`ib` |
| **AWG3** | AWG2 плюс `header_protection_key`, `content_padding_addition`, `random_trailers`, `disable_cookies`, диапазонные тайминги и диапазонный `persistent_keepalive_interval` ([§2.10](#210-awg-3x-защита-заголовка-паддинг-хвосты-тайминги)) |

Что каждый слой делает с пакетами и зачем — [amneziawg-explained §0–§4](amneziawg-explained.ru.md#0-awg-добавляет-к-wireguard-три-слоя-обфускации).

Параметры AmneziaWG на клиенте и сервере должны совпадать: junk и I-пакеты задаёт
конфигурация, стороны их не согласовывают. Задавай их из одного `awg.conf` на обоих
концах.

## 2.2 Junk- и signature-поля

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `jc` | int | `0` (unset) | число junk-пакетов, отправляемых до handshake |
| `jmin` | int | `0` | минимальный размер этих junk-пакетов |
| `jmax` | int | `0` | максимальный размер этих junk-пакетов. Держи ниже реального path MTU, расчёт — в [§2.6](#26-бюджет-mtu) |
| `s1` | int | `0` | junk перед handshake-сообщением INIT |
| `s2` | int | `0` | junk перед handshake-сообщением RESPONSE |
| `s3` | int | `0` | параметр размера junk из AWG 2.0; паддит только сообщения cookie-reply и на MTU не влияет |
| `s4` | int | `0` | параметр размера junk из AWG 2.0; добавляется перед каждым transport (data) сообщением, поэтому требует пониженного [MTU](#26-бюджет-mtu) |

## 2.3 Magic-заголовки `h1`–`h4`

`h1`–`h4` переопределяют четыре magic-значения типов сообщений WireGuard. Каждое —
либо одно `uint32`, либо включительная строка-диапазон `"min-max"`.

| Ключ | Тип | Смысл |
|------|-----|-------|
| `h1` | int \| `"min-max"` string | magic для типа сообщения 1 (деф `1`) |
| `h2` | int \| `"min-max"` string | magic для типа сообщения 2 (деф `2`) |
| `h3` | int \| `"min-max"` string | magic для типа сообщения 3 (деф `3`) |
| `h4` | int \| `"min-max"` string | magic для типа сообщения 4 (деф `4`) |

- Одно `uint32` (`1234567890`, стиль AWG 1.x) фиксирует значение.
- Строка-диапазон (`"43613244-384550127"`, диапазонные заголовки AWG 2.0)
  заставляет device выбирать случайное значение из диапазона на каждое сообщение.
- `0` или `""` = unset, считается WireGuard-дефолтом `1`/`2`/`3`/`4`. Простое
  число `N` эквивалентно диапазону `"N-N"`.
- При сериализации одиночное значение маршалится обратно в JSON-число, и только
  диапазон становится JSON-строкой. Так тип совпадает со старым полем `uint32`.

> ⚠️ Четыре диапазона не должны перекрываться; unset-заголовок считается своим
> WireGuard-дефолтом. Иначе device отвергает конфиг с `headers must not overlap`.
> Задавай все четыре вместе, как это делают AWG2-экспорты.

## 2.4 CPS-декои `i1`–`i5` и формат тегов

`i1`–`i5` — пакеты-приманки Controlled Packet Sequence (CPS) из AWG 2.0; они
уходят по порядку до handshake. Это регистрозависимые строки в формате тегов, они
соответствуют 1:1 ключам `i1`..`i5` amneziawg-go.

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `i1` … `i5` | string | `""` | CPS-пакеты-приманки, шлются по порядку. `i1` обычно имитирует реальный протокол (например QUIC/STUN-заголовок) и взаимоисключим с сахаром [`id`/`ip`/`ib`](#25-сахар-маскировки-id--ip--ib) |

**Формат тегов** (ключевые слова строчными, регистр и порядок важны):

| Тег | Выдаёт |
|-----|--------|
| `<b 0xHEX>` | статические байты (литеральный hex) |
| `<t>` | timestamp |
| `<r N>` | N случайных байт |
| `<rc N>` | N случайных символов |
| `<rd N>` | N случайных цифр |

Пример: `"i1": "<b 0x000100002112a442><r 12>"` — статический 8-байтовый префикс, за
ним 12 случайных байт.

## 2.5 Сахар маскировки `id` / `ip` / `ib`

Вместо CPS-строки `i1`, написанной руками, маскировку можно объявить тремя ключами:
домен, протокол и браузер. Имена ключей те же, что у
[WireSock Secure Connect](https://www.wiresock.net/). Устройство само генерирует из
них декой `i1`.

| Ключ | Тип | Смысл |
|------|-----|-------|
| `id` | string | Домен маскировки: хост, который выглядит обычным для твоего региона, напр. `www.google.com`. Строгий LDH-хост. Вшивается в декой для `ip=quic` (как SNI в ClientHello), `ip=dns` (как QNAME) и `ip=sip` (как host); `ip=stun` некуда нести имя хоста, и он его игнорирует. Обязателен только для `quic`; для `dns`/`sip` без `id` генерируется псевдо-имя. Заданное значение всегда проходит LDH-проверку: значения с инъекцией отвергаются |
| `ip` | string | Протокол маскировки: `quic` \| `dns` \| `stun` \| `sip` |
| `ib` | string | Браузер маскировки: `chrome` \| `firefox` \| `curl`; `chrome-full` не для клиентских интерфейсов. Только при `ip=quic`. Задаёт TLS-отпечаток ClientHello и раскладку фреймов Initial (подробности — в описании `quic` ниже) |

Декой уходит до handshake, как написанный руками `i1`. Каждый профиль — пакет в
форме своего протокола, который клиент шлёт первым. Формы взяты из
open-source-референса WireSock, но вместо серверного ответа устройство шлёт
клиентский запрос — тот, что peer отправляет первым:

- **`quic`** — QUIC Initial ([RFC 9001](https://www.rfc-editor.org/rfc/rfc9001)) с целым ClientHello: SNI — твой `id`,
  отпечаток — браузера из `ib`, фреймы разложены как у его стека:
  - `chrome` — Chrome 155 без PQ key_share, 1250 байт, ClientHello порезан на 2–11
    CRYPTO с 2–10 PING и PADDING вразброс (`QuicChaosProtector`);
  - `chrome-full` — Chrome 155 с `X25519MLKEM768`, один Initial ~2 КБ больше MTU
    (два IP-фрагмента);
  - `firefox` и `curl`/пусто — один CRYPTO плюс PADDING.

  Раскладка и DCID/random новые на каждый вызов, поэтому общей для пользователей
  сигнатуры нет. `ip=quic` эмитит один Initial в `i1`. По полевым прогонам
  ClientHello, размазанный по нескольким Initial, на пути к WARP отбрасывается, а
  порядок фреймов внутри одного пакета значения не имеет. Декой из простого QUIC
  short header не проходил.
- **`dns`** — клиентский DNS query (QR=0, QTYPE HTTPS/тип 65) с QNAME = твой `id` и
  обычной EDNS OPT-записью без опций. Так выглядит запрос stub-резолвера.
- **`stun`** — WebRTC/ICE STUN Binding Request (magic cookie + USERNAME +
  ICE-CONTROLLING + PRIORITY + SOFTWARE + MESSAGE-INTEGRITY + FINGERPRINT). Это
  клиентский connectivity-check: ICE-агент легитимно шлёт его первым.
- **`sip`** — SIP INVITE request без тела (`i1`: request-line + Via /
  Max-Forwards / To / From / Call-ID / CSeq / Contact + `Content-Type:
  application/sdp` и `Content-Length: 0`, без SDP-тела). Host — твой `id` или
  сгенерированный псевдо-host, имена пользователей — произносимые псевдо-имена.
  `i2` остаётся пустым: `100 Trying` — ответ сервера, а не пакет клиента.

**Куда `id` попадает на провод:**

| `ip` | декой | `id` виден посреднику? |
|------|-------|---------------------|
| `quic` | фрагментированный QUIC Initial, `id` = SNI | да: DPI, расшифровывающий Initial (ключи выводятся из DCID), прочтёт `id`, если соберёт фреймы по порядку |
| `dns` | EDNS query (QR=0), `id` = QNAME | да, открытым текстом |
| `sip` | SIP INVITE, `id` = host в URI | да (когда задан), открытым текстом |
| `stun` | STUN Binding Request | нет: в STUN нет поля под имя хоста |

Какой профиль выбирать и почему на устройстве прошёл только `quic` —
[amneziawg-explained §3.3–§3.4](amneziawg-explained.ru.md#33-сахар-маскировки-id--ip--ib).

### Заметки и ограничения

- `id`/`ip`/`ib` и явный `i1` взаимоисключимы: конфиг с обоими отвергается.
- `ib` выбирает TLS-отпечаток декоя. `chrome`/`firefox` эмитят браузерный
  ClientHello только в сборках с поддержкой TLS-мимикрии; `curl` и отсутствующий
  `ib` дают generic ClientHello. Без этой поддержки браузерные профили откатываются
  к generic.
- Полевой статус: `ip=quic` проверен на устройстве против LTE-DPI. Для
  `dns`/`stun`/`sip` подтверждены приём движком, структурная валидность и
  `sing-box check`; полевой A/B не проводился.

📖 [Подробные примеры](../SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md) —
полные конфиги по каждому профилю (включая Cloudflare WARP), генерируемый CPS для
каждого и дословные ошибки валидации.

## 2.6 Бюджет MTU

`s4` добавляет junk перед каждым transport-пакетом, поэтому AWG-endpoint требует
`mtu` ниже, чем обычный WireGuard. `s3` паддит только cookie-reply и на бюджет не
влияет. Слишком большой `mtu` не мешает хендшейку, но ломает передачу данных:

```
peer(…) - received handshake response
peer(…) - failed to send data packets: write udp4 …: sendmsg: message too long
```

Бюджет против 1500-байтового пути:

```
mtu ≤ 1500 − 28 (UDP/IP) − 32 (WireGuard) − S4 junk-байт
```

Для `S4 = 60` это `mtu ≤ 1380`. AmneziaWG рекомендует клиентский MTU 1280 — с
запасом на PPPoE и вложенные туннели.

**Что ядро делает само:**

- `mtu` не задан, `s4` задан → дефолт `1280` (у plain WireGuard — `1408`).
- `mtu` задан явно и не влезает в бюджет → предупреждение при старте. Бюджет
  считается от консервативных 1492 (PPPoE): `mtu ≤ 1492 − 28 − 32 − S4`. Туннель всё
  равно грузится.
- Внешний сокет не форсит DF: негабаритная внешняя датаграмма IP-фрагментируется, а
  не дропается. На этом работают вложенные туннели (`masque`/`wireguard`/AWG через
  `detour`). DF на конкретном endpoint включает `"udp_fragment": false`. Корректный
  `mtu` всё равно лучше: фрагментация — страховка.
- `jmax` держи ниже path MTU: junk-пакет длиннее MTU фрагментируется, и узкие пути
  роняют фрагменты.

Почему так и как это выглядит для пользователя —
[amneziawg-explained §5](amneziawg-explained.ru.md#5-s4-съедает-mtu-на-каждом-пакете-данных).

## 2.7 Маппинг `awg.conf` 1:1

Строки файла `awg.conf` / awg-quick переносятся в JSON один к одному:

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
по бюджету из [§2.6](#26-бюджет-mtu).

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

Полный набор параметров живого AWG 3.1-сервера, скопированный 1:1 из его `.conf`.
`H1`–`H4` остаются дефолтами WireGuard: при защите заголовка слово типа и так
замаскировано. Конфиг проверен end-to-end против этого сервера.

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
| `i1` вместе с `id`/`ip`/`ib` | `amneziawg: id/ip/ib masquerade conflicts with an explicit i1; use one or the other` |
| `id`/`ib` заданы, `ip` нет | `amneziawg: ip (masquerade protocol) is required when id/ib is set; one of quic\|dns\|stun\|sip` |
| `ip` не из набора | `amneziawg: unknown masquerade protocol "ftp"; one of quic\|dns\|stun\|sip` |
| `ip=quic` без `id` | `amneziawg: id (masquerade domain) is required for ip=quic (it becomes the ClientHello SNI)` |
| `ip=dns` / `ip=sip` / `ip=stun` без `id` | не ошибка: для `dns`/`sip` генерируется псевдо-имя, `stun` имя не нужно |
| домен с `\r\n` / `;` / `@` / пробелом | `amneziawg: invalid masquerade domain "...": illegal character (only a-z A-Z 0-9 - _ allowed)` |
| `ib` не из набора | `amneziawg: unknown masquerade browser "safari"; one of chrome\|firefox\|curl` |
| `ib` с `ip≠quic` | `amneziawg: ib (browser) is only meaningful with ip=quic, got ip="dns"` |
| `header_protection_key` при любом из `s1`–`s4` меньше 12 | `amneziawg: s4=8 is too short for header_protection_key: each of s1-s4 must be at least 12 bytes (the padding carries the header cipher nonce)` |
| `header_protection_key` не base64 / не 32 байта / все нули | `amneziawg: decode header_protection_key (expected base64, as printed by \`awg genkey\`)` / `… must decode to 32 bytes, got N` / `… is all zeros (the device treats that as "off"); omit the field instead` |
| диапазонное поле с началом > конца или мусор | `invalid range "180-150": range start > end` (ключ называется: `reject_after_time: …`) |
| диапазонный `persistent_keepalive_interval` в сборке без `with_awg` | `AmneziaWG (awg) support is not included in this build (persistent_keepalive_interval range), rebuild with -tags with_awg` |

Домен проходит строгую LDH-проверку, как SNI у WireSock: метки из
`a-z A-Z 0-9 - _`, ≤63 байт, без дефиса по краям; всё имя ≤253, без точки в начале;
одна точка в конце допускается. Домен попадает в текст SIP и в DNS QNAME, поэтому
управляющие байты и метасимволы отвергаются: это защита от инъекции.

## 2.10 AWG 3.x: защита заголовка, паддинг, хвосты, тайминги

AmneziaWG 3.0 (amneziawg-go v3.0.0, tools v3.0.20260730) и 3.1 (v3.1.2026081x)
добавляют второй слой поверх приёмов AWG2 с формой пакетов. Все эти поля задаются на
корне endpoint, как и поля AWG2, и требуют `with_awg`. Контейнер Amnezia
`amnezia-awg2` с `protocol_version: "3.1"` экспортирует этот набор.

AmneziaWG делит параметры на серверные и клиентские. Серверный параметр обязан
совпадать на обоих концах: при расхождении хендшейк молча не проходит. Клиентский
задаёт локальное поведение, серверу он безразличен. Серверный здесь только
`header_protection_key`, остальные клиентские. Значения клиентских всё равно копируй
с сервера: они подобраны вместе.

| Ключ | Тип | Дефолт | Сторона | Смысл |
|------|-----|--------|---------|-------|
| `header_protection_key` | base64-строка (32 байта, `awg genkey`) | `""` (выкл) | **сервер** | Шифрует низкоэнтропийный заголовок каждого пакета: у handshake-сообщения — всё сообщение, у data-пакета — 16-байтный заголовок (тип, receiver index, счётчик). Шифр — ChaCha20 с этим ключом; nonce на датаграмму — первые 12 байт случайного паддинга сообщения, поэтому каждый из `s1`–`s4` должен быть ≥ 12. При включённой защите magic-значения `h1`–`h4` теряют смысл (слово типа на проводе случайно), и AWG3-экспорты оставляют их `1`–`4`. |
| `content_padding_addition` | int \| `"min-max"` | `""` (выкл) | клиент | Дополнительные нулевые байты в plaintext каждого data-пакета (внутри AEAD, на проводе случайны). Число выбирается из диапазона на каждый пакет вместо выравнивания WireGuard по 16. Добавка ограничена так, чтобы датаграмма не превысила самую большую уже виденную на пути этого пира («UDP-окно», старт 500 Б). Пакет на полный MTU добавки не получает. |
| `random_trailers` | bool | `false` | клиент | Хвост случайной длины из случайных байтов после каждого handshake-сообщения (init/response/cookie). Если не задан `content_padding_addition`, такой же хвост по правилу UDP-окна идёт внутри data-пакетов. Так ни у одного вида сообщения нет фиксированного размера. Приёмник принимает handshake-датаграммы длиннее ожидаемого и отбрасывает хвост. |
| `disable_cookies` | bool | `false` | клиент | Никогда не слать cookie reply и пропускать under-load-гейт mac2, который бы его потребовал (у cookie-обмена узнаваемая форма). |
| `rekey_after_time` | int \| `"min-max"` секунд | `""` (WireGuard 120) | клиент | Когда инициатор начинает свежий хендшейк. Выбирается из диапазона при каждой проверке. |
| `rekey_timeout` | int \| `"min-max"` секунд | `""` (WireGuard 5) | клиент | Интервал между повторами хендшейка; джиттер к нему добавляется. Минимум диапазона — пол между двумя инициациями. |
| `reject_after_time` | int \| `"min-max"` секунд | `""` (WireGuard 180) | клиент | После этого ключевая пара отвергается. Там, где протокол не должен рано выбрасывать ключи, берётся максимум диапазона. |
| `keepalive_timeout` | int \| `"min-max"` секунд | `""` (WireGuard 10) | клиент | Пассивный keepalive после принятых данных. |
| `max_handshake_attempts` | int \| `"min-max"` | `""` (WireGuard 18) | клиент | Повторы до отказа от цикла хендшейка; после отказа срабатывает self-heal rebind. Перевыбирается на каждый цикл. |
| `peers[].persistent_keepalive_interval` | int \| `"min-max"` секунд | `0` (выкл) | клиент | Число WireGuard или AWG3-диапазон, перевыбираемый при каждом взводе. |

Замечания:

- Nonce защиты заголовка берётся из паддинга, поэтому на handshake обычного размера
  (`s1`=0) ключ применить нельзя. Такой конфиг отвергают и проверка конфига
  ([§2.9](#29-ошибки-валидации-дословно)), и устройство.
- `content_padding_addition` и `random_trailers` увеличивают пакеты, но бюджет MTU из
  [§2.6](#26-бюджет-mtu) от этого не меняется: кламп по UDP-окну держит добавки в
  размерах, которые путь уже пропускал. `mtu` держи на значении, рекомендованном
  сервером (`1376` в экспорте Amnezia).
- `random_trailers` расширяет классификацию на приёме: датаграммы длиннее
  `s1`+148 / `s2`+92 / `s3`+64 тоже пробуются как handshake по слову типа. Наш
  приёмник к ложным совпадениям невосприимчив, а приёмник сервера — референсный.
  Поэтому не сочетай `random_trailers` с широкими диапазонами `h1`–`h4`: uplink будет
  терять пакеты. Механика —
  [amneziawg-explained §6.3](amneziawg-explained.ru.md#63-приём-сначала-ищет-живой-receiver-index).
- Тайминги не обязаны совпадать с сервером. Несогласованные значения, например
  `rekey_after_time` выше `reject_after_time`, заставят туннель дёргаться. Копируй
  экспорт сервера.
- Всё это байт-совместимо с amneziawg-go v3.1 (коммит `b5928ef`, 2026-08-28): та же
  раскладка nonce/keystream, тот же порядок классификации, те же правила
  паддинга/хвостов. Проверено против живого сервера `protocol_version 3.1` —
  [SPEC 080](../SPECS/TASKS/080-AWG3_HEADER_PROTECTION_TIMINGS/SPEC.md).

---

# 3. MASQUE outbound (CONNECT-IP / WARP)

> 📖 Как устроен MASQUE и почему значения по умолчанию такие — [masque-explained.ru.md](masque-explained.ru.md). Здесь — только поля, дефолты и ошибки.

## 3.1 Что это

`masque` outbound переносит целые IP-пакеты поверх HTTP/3 или HTTP/2 через CONNECT-IP ([RFC 9484](https://www.rfc-editor.org/rfc/rfc9484)),
в первую очередь для Cloudflare WARP. Сборка требует `with_quic` и `with_gvisor`, оба есть в
дефолтных `LX_TAGS`. Ключи и адреса ядро берёт готовыми из конфига: устройство регистрирует
клиент.

> ⚠️ Блок `dns` верхнего уровня обязателен: outbound разрешает домены через DNS-роутер. Без
> блока доменный трафик через туннель не идёт (`masque: no DNS router available to resolve …`).

> ⚠️ HTTP-версию задаёт `vhttp`; ключ `network` снят в lx.18 (`unknown field "network"`).
> Плоские TLS-поля принимаются с предупреждением — [таблица миграции](#310-миграция-со-старой-схемы).

## 3.2 Поля, специфичные для MASQUE

| Ключ | Тип | Обязат. | Дефолт | Смысл |
|------|-----|---------|--------|-------|
| `type` | string | ✅ | — | всегда `"masque"` |
| `tag` | string | ✅ | — | имя outbound (для route/групп) |
| `server` | string | ✅ | — | IP/хост WARP endpoint |
| `server_port` | uint16 | ✅ | — | порт (обычно 443) |
| `profile` | string | — | `cloudflare` | `cloudflare` \| `standard` (см. [§3.4](#34-профили--что-переключает-profile)) |
| `vhttp` | string | — | `auto` | HTTP-версия, которая несёт CONNECT-IP: `auto` (сначала h3; если QUIC-хендшейк не завершился за 3 с — фолбэк на h2) \| `h3` (QUIC) \| `h2` (HTTP/2, TCP:443). На `standard` дефолт молча означает `h3`: h2-ноги там нет. Список tcp/udp — это `network_list`, как везде |
| `private_key` | string (base64) | ✅¹ | — | клиентский EC private key, DER (`x509.ParseECPrivateKey`) |
| `public_key` | string (base64) | ✅¹ | — | PKIX public key endpoint, DER (`x509.ParsePKIXPublicKey`, ECDSA) |
| `ip` | string (CIDR) | ✅² | — | локальный IPv4 внутри туннеля; голый адрес → `/32` |
| `ipv6` | string (CIDR) | ✅² | — | локальный IPv6 внутри туннеля; голый адрес → `/128` |
| `tls` | object | — | — | стандартный outbound-блок TLS — `server_name`, `insecure`, `disable_sni`, `fragment`, `record_fragment`, `fragment_fallback_delay`, … Тот же контейнер, что у любого TLS-outbound |
| `uri` | string | — | по профилю³ | CONNECT-IP request URI |
| `mtu` | int | — | `1280` | MTU userspace-стека. На `h2` максимум `16000` (один IP-пакет = один HTTP/2 DATA frame). Узлы над masque (через `detour`, группу или на позиции `chain`) получают размер пакета под этот `mtu` автоматически: у туннелей — `mtu`, у hysteria2/tuic/hysteria — `initial_packet_size` и выключенный PMTUD (при 1280: 1232 для IPv6-сервера, 1252 для IPv4; [lx-block.ru.md](lx-block.ru.md#lxdetourmtu_align--выравнивание-mtu-и-размера-пакета-по-пути)). Внешний QUIC к серверу стартует с `mtu + 51` (кламп 1200…1452) |
| `idle_timeout` | duration | — | наследует `lx.masque.idle_timeout`, иначе выкл | suspend туннеля после простоя (освобождает gVisor-стек, насосы и QUIC keepalive); следующий dial его пересобирает. Отсутствие ключа → глобальный `lx.masque.idle_timeout` ([lx-block.ru.md](lx-block.ru.md)); явный `0` или отрицательное держат узел поднятым при любом глобальном значении; включает только положительное значение |
| `keep_alive_period` | duration | — | `30s` | QUIC keepalive (только h3). Отрицательное значение отключает |
| `network_list` | list | — | tcp+udp | L4-протоколы, идущие через туннель |

¹ Обязательны для `profile=cloudflare`; опциональны для `standard`.
² Обязателен хотя бы один из `ip`/`ipv6`.
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

Полный перечень — в общем `DialerOptions`, файл `option/outbound.go`.

## 3.4 Профили — что переключает `profile`

Профиль меняет шесть вещей; остальное (QUIC/HTTP2, capsule/datagram,
userspace-стек, насосы) от профиля не зависит.

| Аспект | `cloudflare` (деф) | `standard` ([RFC 9484](https://www.rfc-editor.org/rfc/rfc9484)) |
|--------|--------------------|-----------------------|
| `:protocol` (h3) / `cf-connect-proto` (h2) | `cf-connect-ip` | `connect-ip` |
| Extended CONNECT settings | терпеть отсутствие (WARP их не шлёт) | требовать |
| дефолт `tls.server_name` | `www.cloudflare.com` | = `server` |
| дефолт `uri` | `https://cloudflareaccess.com` | нет (обязателен) |
| TLS-проверка | pinning на `public_key` (ECDSA) | обычная цепочка по `server_name` |
| `private_key`/`public_key` | обязательны | опциональны |

`standard` рассчитан на собственный RFC-совместимый CONNECT-IP сервер и к Cloudflare
WARP не подключается. Для WARP используй `cloudflare`, это дефолт. `vhttp: h2` для
`standard` не реализован: такой конфиг даёт ошибку при загрузке.

## 3.5 Ключевой материал и форматы значений

**Ключи** (`private_key` / `public_key`): base64 от DER. `private_key` =
`x509.MarshalECPrivateKey` (SEC1 EC), `public_key` = `x509.MarshalPKIXPublicKey` от
`*ecdsa.PublicKey` (P-256). В этом формате ключи отдаёт WARP-регистрация на стороне
клиента, и ядро разбирает их без преобразований.

**`ip` / `ipv6`**: адрес или CIDR. `"172.16.0.2"` → `172.16.0.2/32`, `"2606:…::"` →
`/128`. Это локальные адреса внутри туннеля (твой адрес в сети WARP), а не выходной
IP.

**Duration** (`idle_timeout`, `keep_alive_period`, `connect_timeout`, …): строка
Go-duration — `"30s"`, `"5m"`, `"1h30m"`. Пустая = дефолт. Отрицательная (`"-1s"`) =
«выключить».

## 3.6 Стратегия SNI

Дефолтный SNI — `www.cloudflare.com`, а не имя эндпоинта; эндпоинт проверяется по закреплённому
`public_key`. `tls.server_name` переопределяется на каждом узле. `tls.disable_sni: true` не шлёт
SNI вообще. Почему так — [masque-explained §2.1](masque-explained.ru.md#21-sni--нейтральный-популярный-хост).

## 3.7 `vhttp`: auto / h3 / h2

- `auto` (по умолчанию) — сначала `h3`; если QUIC-рукопожатие не завершилось за 3 с, `h2`.
  Победивший вариант запоминается ([masque-explained §3](masque-explained.ru.md#3-http3-или-http2)).
- `h3` — только QUIC.
- `h2` — только HTTP/2 поверх TCP:443; один IP-пакет = один DATA-кадр, `mtu` до 16000. ClientHello
  на `h2` защищают узловые `fragment` / `record_fragment` и секция `tls` блока `lx.sniff_guard`,
  на `h3` — секция `quic` ([lx-block](lx-block.ru.md#lxsniff_guard--защита-от-перенаправления-по-sni)).

## 3.8 Idle-suspend и keepalive

- `idle_timeout` — простой без трафика, после которого туннель закрывается; следующий dial его
  пересобирает. По умолчанию выключен: отсутствие ключа, `0` и отрицательное значение держат
  туннель; включает только положительное (например `"5m"`).
- `keep_alive_period` (по умолчанию `30s`, только h3) — интервал QUIC keepalive; отрицательное
  значение отключает.

Цена сна и связь с keepalive — в [masque-explained §5](masque-explained.ru.md#5-простой-и-пробуждение).

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
- `profile=standard` без `uri` → `masque: uri is required for the standard profile — set it to the server's CONNECT-IP request URI, e.g. https://<host>/.well-known/masque/ip/*/*/` (проверяется раньше `vhttp`, поэтому текст один для h2/h3/auto);
- `public_key` не ECDSA-ключ → `public_key is not an ECDSA key`.

## 3.10 Миграция со старой схемы

Плоские TLS-поля из таблицы ниже принимаются; каждое такое поле пишет в лог одну
строку deprecation на outbound. Если legacy-поле расходится со своей заменой, это
жёсткая ошибка: молча одно из двух ядро не выбирает.

Legacy-ключ `network` для версии HTTP снят в `v1.14.3-lx.18`: masque-outbound с ним не
разбирается, ошибка `unknown field "network"`. Переименуй ключ в `vhttp`, значения те
же.

| Legacy | Актуальное |
|---|---|
| `network: "h3"` / `"h2"` — снят | `vhttp: "h3"` / `"h2"` |
| `sni` | `tls.server_name` |
| `skip_cert_verify: true` | `tls.insecure: true` |
| `fragment: true` | `tls.fragment: true` |
| `fragment_fallback_delay` | `tls.fragment_fallback_delay` |
| `record_fragment: true` | `tls.record_fragment: true` |

> ⚠️ Legacy-булевы не отличают «отсутствует» от явного `false`, поэтому переносится
> только legacy `true`. Чтобы что-то выключить, пиши форму `tls`.

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
   — это `network_list`. `"vhttp": "tcp"` даст fail-fast. У остальных outbound список
   tcp/udp называется `network`.
2. **Блок `dns` верхнего уровня обязателен.** Без него доменный трафик через туннель не
   резолвится.
3. **Выходной IP меняется** после idle-suspend/reconnect: WARP anycast раздаёт разные
   edge-адреса на каждое новое подключение. Внутренний `ip`/`ipv6` стабилен.
4. **`tls.insecure: true`** снимает pubkey-pinning целиком. Когда WARP SNI замаскирован,
   pinning — единственная защита, поэтому `insecure` — только для отладки.
5. **`keep_alive_period` vs `idle_timeout`.** Чем они отличаются — в
   [§3.8](#38-idle-suspend-и-keepalive).

**📖 Статус.** Проверено на устройстве end-to-end в Wi-Fi и LTE: `warp=on`, трафик идёт
и по `h3`, и по `h2`, idle-suspend и самовосстанавливающийся reconnect работают.

---

# 4. gRPC-транспорт

> 🧭 Транспорт апстримный, объясняющего раздела нет; здесь только формы `service_name`, где наше поведение расходится с апстримом.

gRPC-транспорту (`"transport": { "type": "grpc" }`) build-тег не нужен: в наших
сборках идёт lite-реализация (`v2raygrpclite`). Полная, на `grpc-go`, собирается
под `with_grpc` и в desktop/CLI-бинарь не входит.

## 4.1 `service_name`: формы Xray

У `service_name` две формы, как у Xray. Форму выбирает один символ — ведущий `/`.

- **Без ведущего `/`** — всё значение это один сегмент сервиса. Он экранируется
  целиком (`/` внутри становится `%2F`), имя стрима всегда `Tun`. Эта форма
  совпадает с апстримом байт в байт.
- **С ведущим `/`** — значение это custom path в конвенции Xray. Каждый сегмент
  экранируется по отдельности, и `/` остаётся разделителем пути. Последний
  сегмент — имя стрима; хвост `|…` у имени стрима (имя multi-стрима Xray)
  отбрасывается.

| `service_name` | Путь на проводе | Сервис / стрим | Примечание |
|---|---|---|---|
| `TunService` | `/TunService/Tun` | `TunService` / `Tun` | без ведущего `/` |
| `a/b` | `/a%2Fb/Tun` | `a%2Fb` / `Tun` | без ведущего `/`, = старая форма Xray |
| `/a/b/Tun` | `/a/b/Tun` | `a/b` / `Tun` | custom path, = Xray |
| `/a/b/Stream` | `/a/b/Stream` | `a/b` / `Stream` | имя стрима берётся из конфига |
| `/a b/Tun` | `/a%20b/Tun` | `a%20b` / `Tun` | посегментный `PathEscape` |
| `/a/b/Tun\|TunMulti` | `/a/b/Tun` | `a/b` / `Tun` | хвост multi отброшен, как у Xray |

Новой опции нет, валидации нет: форму задаёт содержимое `service_name` — так же,
как у Xray.

**Пометки.**

1. Клиент под тегом `with_grpc` для формы без ведущего `/` путь не экранирует: шлёт
   `"/" + name + "/Tun"` как есть, и `a/b` уходит как `/a/b/Tun`. Это апстримное
   поведение, мы его не меняли. Разница видна только для такого имени, внутри
   которого есть `/`.
2. Наш lite-сервер сравнивает декодированный путь, поэтому он мягче Xray: сервер с
   `service_name: "a/b"` принимает и `/a%2Fb/Tun`, и `/a/b/Tun`. Xray сравнивает
   строго, поэтому выбирай одну и ту же форму с обеих сторон.
3. **Симптом несовпадения форм** с сервером Xray:
   `v2ray-grpc: unexpected status: 404 Not Found`. Dial до сервера дошёл, и сервер
   отверг его по пути.

**📖 Статус.** Реализовано, покрыто юнитами и тестами «клиент↔сервер». Против живого
Xray пока не прогонялось: путь на проводе сверен с исходниками Xray и grpc-go.
Доступно с `v1.14.1-lx.8`.

---

# 5. REALITY и VLESS `encryption`

> 🧭 Как REALITY узнаёт своих, что такое гибридный key share, как устроен слой `mlkem768x25519plus` и почему — [xray-protocols-explained §1–§2](xray-protocols-explained.ru.md#1-фундамент-tls-clienthello-и-отпечаток) и [§5](xray-protocols-explained.ru.md#5-vless-encryption-постквантовый-слой); типичные отказы REALITY — там же, [§2.7](xray-protocols-explained.ru.md#27-типичные-отказы). Здесь — только поля, дефолты и ошибки.

Два независимых слоя. REALITY живёт в блоке `tls` любого TLS-over-TCP outbound
(VLESS, trojan, vmess, XHTTP-транспорт) и требует `with_utls`. `encryption` — плоское
поле у `vless`-outbound, рядом с `uuid`, без build-тега. Апстримные поля блока `tls`
(`server_name`, `alpn`, `insecure`, `ech`, …) описаны в
[апстримной доке TLS](../docs/configuration/shared/tls.md); ниже — только то, что
форк добавил или изменил.

## 5.1 Поля `tls.reality`

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `enabled` | bool | `false` | Включить REALITY вместо обычной проверки сертификата ([апстрим](../docs/configuration/shared/tls.md#reality-fields)) |
| `public_key` | base64url, 32 байта | — | Публичный ключ X25519 сервера (Xray `publicKey`) |
| `short_id` | hex, до 8 байт | — | Метка клиента (Xray `shortId`) |
| `key_share` | `""` \| `"hybrid"` \| `"classical"` | `""` | Форма key share в ClientHello ([§5.2](#52-отпечатки-и-гибридный-key-share)) |

| `key_share` | ClientHello | Работает против |
|---|---|---|
| `""` | как несёт отпечаток ([§5.2](#52-отпечатки-и-гибридный-key-share)) | зависит от отпечатка ([§5.2](#52-отпечатки-и-гибридный-key-share)) |
| `"classical"` | `X25519MLKEM768` вырезан из `key_share` и `supported_groups`; `chrome`: 594 байта вместо 1720, один TCP-сегмент вместо двух | только Xray < v26.9.8; новые серверы отвергают молча (`reality verification failed`) |
| `"hybrid"` | гибрид обязателен; на отпечатке без него — ошибка при рукопожатии с текстом ([§5.4](#54-ошибки-валидации-reality-дословно)) | как `""` |

Автофолбэка между `hybrid` и `classical` нет. Поля Xray `spiderX`, `mldsa65Verify`
и `realitySettings.password` ядро не принимает.

## 5.2 Отпечатки и гибридный key share

`tls.utls.fingerprint` — апстримный ключ (enum: `chrome`, `chrome_psk`,
`chrome_psk_shuffle`, `chrome_padding_psk_shuffle`, `chrome_pq`, `chrome_pq_psk`,
`firefox`, `edge`, `safari`, `360`, `qq`, `ios`, `android`, `random`, `randomized`;
плюс наш `chrome_155`). Под REALITY от отпечатка зависит, пустит ли сервер узел:
Xray ≥ v26.9.8 принимает только ClientHello с гибридным key share `X25519MLKEM768`
перед `X25519`. Ядро отпечаток не подменяет.

| `fingerprint` | Key share в ClientHello | Xray ≥ v26.9.8 | Откуда пресет |
|---|---|---|---|
| `chrome` и апстримные `chrome_*` | GREASE, **X25519MLKEM768**, X25519 | ходит | metacubex/utls, Chrome 133 |
| `chrome_155` | GREASE, **X25519MLKEM768**, X25519 | ходит | форк utls-lx, Chrome 155: только явно, добавляет подписи ML-DSA и `trust_anchors`; `chrome` остаётся 133 |
| `firefox` | **X25519MLKEM768**, X25519 | ходит | форк utls-lx, Firefox 148 |
| `safari` | **X25519MLKEM768**, X25519 | ходит | форк utls-lx, Safari 26.3 |
| `edge`, `ios`, `android`, `360`, `qq` | только X25519 | отвергается | пресетов с гибридом нет |
| `random` | как у выпавшего: `chrome`, `firefox` или `safari` | ходит | пул без `edge`/`ios` |
| `randomized` | **X25519MLKEM768**, X25519, P-256; ALPN всегда | ходит | `HelloRandomizedALPN` с гибридом всегда; остальное случайно |

`AuthKey` считается по тому ключу, который выберет сервер: чистый X25519, если он
есть в `key_share`, иначе X25519-часть гибрида. Версия клиента в session id —
требуемый минимум.

Выбор задаётся на узле, потому что ядро не может выбрать между «сервер отвергнет» и «сеть
потеряет»: в некоторых сетях двухсегментное гибридное приветствие теряется, а односегментное
классическое проходит (LxBox #142), клиент самого Xray упирается в то же (XTLS#6256), а новые
серверы Xray гибрид требуют. `classical` снижает постквантовую защиту и годится только для старых
серверов.

## 5.3 Фрагментация ClientHello

Апстримные ключи блока `tls`; в форке они действуют и на REALITY.

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `fragment` | bool | `false` | Разрез первого пакета по SNI на TCP-сегменты с ожиданием ACK ([апстрим](../docs/configuration/shared/tls.md#fragment)) |
| `record_fragment` | bool | `false` | Те же точки разреза, каждая — своя TLS-запись, один пакет ([апстрим](../docs/configuration/shared/tls.md#record_fragment)) |
| `fragment_fallback_delay` | duration | `500ms` | Пауза, когда время ожидания ACK вычислить нельзя ([апстрим](../docs/configuration/shared/tls.md#fragment_fallback_delay)) |

Дефолта под `detour` нет: без блока `lx.sniff_guard` ClientHello режется только там, где
у узла задан `fragment` или `record_fragment`, через `detour` тоже. Защиту от релеев,
которые перенаправляют по SNI, настраивает блок `lx.sniff_guard`
([lx-block](lx-block.ru.md#lxsniff_guard--защита-от-перенаправления-по-sni)). Явное
значение узла, включая `"record_fragment": false`, сильнее блока, если в блоке не задан
`"override": true`. Фрагментируется только рукопожатие. Как режется ClientHello под
`detour` в целом — [lx-config §9](lx-config.ru.md#9-clienthello-под-detour--без-дефолта).

## 5.4 Ошибки валидации REALITY (дословно)

| Конфиг | Ошибка |
|--------|--------|
| `public_key` не base64url | `decode public_key: …` |
| `public_key` не 32 байта | `invalid public_key` |
| `short_id` длиннее 8 байт | `invalid short_id` |
| `short_id` не hex | `decode short_id: …` |
| `key_share` не из набора | `unknown reality key_share: X (expected "hybrid" or "classical")` |
| `key_share: "hybrid"` на отпечатке без гибрида (при рукопожатии) | `reality key_share "hybrid": fingerprint Edge 85 carries no X25519MLKEM768 key share` |
| сервер отверг клиента (любая причина) | `reality verification failed` — это не ошибка конфига; разбор причин — [xray-protocols-explained §2.7](xray-protocols-explained.ru.md#27-типичные-отказы) |

## 5.5 Поле `encryption` у `vless`

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `encryption` | string | `""` | `""` / `"none"` — слой выключен, поведение апстрима байт в байт. Иначе — spec-строка, валидируется на `check`/старте. В Xray — `users[0].encryption`, переносится дословно |

Грамматика (сегменты через точку):

```
mlkem768x25519plus.<native|xorpub|random>.<0rtt|1rtt>[.<padding>…].<key>[.<key>…]
```

| Сегмент | Значения | Смысл |
|---------|----------|-------|
| метод | `mlkem768x25519plus` | единственный; должен совпасть с сервером |
| вид | `native` \| `xorpub` \| `random` | как слой выглядит на проводе: записи в форме TLS 1.3 / плюс XOR публичных ключей / полностью случайный поток |
| rtt | `0rtt` \| `1rtt` | билет сервера с повторным использованием / полное рукопожатие на каждом соединении |
| padding | блоки `p-min-max`, через точку | сегменты короче 20 символов до первого ключа; чётные блоки — длины (вероятность %, от, до байт), нечётные — паузы (вероятность %, от, до мс); у первого блока `p ≥ 100`, `min`/`max ≥ 35`; сумма максимумов ≤ 65553; только для `1rtt` |
| key | base64url | публичный ключ сервера: X25519 (32 байта) или ML-KEM-768 (1184 байта, ~1579 символов); один или несколько |

Дефолт паддинга, если блоки не заданы: длины `100-111-1111.50-0-3333`, паузы `75-0-111`.
Серверная половина (`decryption`) не портирована.

Проверено на устройстве на подписке, из-за которой поле появилось (`v1.14.0-lx.18`): заработали
10 узлов, мёртвых без него (6 из 8 WS, 4 из 4 gRPC), в остальных группах транспортов ничего не
изменилось. В поле встречается форма `native.0rtt`; `1rtt` с padding и `xorpub`/`random` разбираются
и собираются, но живого сервера с ними ещё не встречали.

## 5.6 Ошибки валидации `encryption` (дословно)

| Конфиг | Ошибка |
|--------|--------|
| пустая строка после обрезки пробелов | `empty encryption string` |
| меньше четырёх сегментов | `invalid encryption string: expected at least method.appearance.rtt.key, got N segments` |
| метод не `mlkem768x25519plus` | `unsupported encryption method: X (only mlkem768x25519plus exists)` |
| вид не из набора | `unknown encryption appearance: X (expected native\|xorpub\|random)` |
| rtt не из набора | `unknown encryption RTT mode: X (expected 0rtt\|1rtt)` |
| пустой сегмент (две точки подряд) | `empty segment in encryption string` |
| ключ не base64url | `invalid encryption key (not base64url): X` |
| ключ не 32 и не 1184 байта | `invalid encryption key length: N (expected 32 or 1184)` |
| ни одного ключа | `no encryption keys in encryption string` |
| блок паддинга не `a-b-c` | `invalid padding lenth/gap parameter: X` (орфография референса) |
| первый блок паддинга меньше порога | `first padding length must not be smaller than 35` |
| сумма максимумов паддинга больше порога | `total padding length must not be larger than 65553` |

## 5.7 Примеры

VLESS + Vision + REALITY на голом TCP:

```jsonc
{
  "type": "vless",
  "tag": "reality-out",
  "server": "203.0.113.10",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "flow": "xtls-rprx-vision",
  "tls": {
    "enabled": true,
    "server_name": "www.microsoft.com",
    "utls": { "enabled": true, "fingerprint": "chrome" },
    "reality": { "enabled": true, "public_key": "<reality-public-key-base64url>", "short_id": "0123abcd" }
  }
}
```

VLESS + `encryption` поверх WebSocket без внешнего TLS:

```jsonc
{
  "type": "vless",
  "tag": "pq-ws",
  "server": "203.0.113.20",
  "server_port": 80,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "encryption": "mlkem768x25519plus.native.0rtt.<base64url ML-KEM-768 key>",
  "transport": { "type": "ws", "path": "/ws" }
}
```

Соответствие ключам Xray (`realitySettings.*`, `users[0].encryption`) с пояснениями —
[xray-protocols-explained §2.8](xray-protocols-explained.ru.md#28-пример-xray-и-sing-box-lx)
и [§5.8](xray-protocols-explained.ru.md#58-пример-xray-и-sing-box-lx).

---

# 6. TrustTunnel outbound и inbound

> Входит в релизные desktop- и роутерные бинари с lx.21: тег `with_trusttunnel` есть в
> `LX_TAGS`. HTTP/3 нужен ещё `with_quic`, он тоже в наборе. В Android AAR тега нет.
> Своя сборка без тега отвергает оба типа при загрузке конфига:
> `trusttunnel is not included in this build, rebuild with -tags with_trusttunnel`.

## 6.1 Что это

TrustTunnel — протокол AdGuard VPN под лицензией Apache-2.0. Спецификация провода —
[`PROTOCOL.md` v1.0](https://github.com/TrustTunnel/TrustTunnel/blob/master/PROTOCOL.md)
в официальном репозитории `TrustTunnel/TrustTunnel`. Протокол ходит по HTTP/2 поверх TLS
или HTTP/3 поверх QUIC к настоящему домену, внутри — прокси-`CONNECT` и Basic-авторизация.
Каждое TCP-соединение — стрим `CONNECT host:port`, каждая UDP-сессия — стрим
`CONNECT _udp2` с кадрами датаграмм; все стримы узла идут в одной сессии. Outbound
`trusttunnel` работает с официальным endpoint (`trusttunnel_endpoint`) и с нашим inbound;
inbound `trusttunnel` обслуживает официальных клиентов и наш outbound. Фича:
[018-TRUSTTUNNEL](../SPECS/FEATURES/018-TRUSTTUNNEL/FEATURE.ru.md).

## 6.2 Поля outbound

| Поле | Тип | По умолчанию | Смысл |
|------|-----|--------------|-------|
| `server`, `server_port` | string, number | обязательно | Адрес endpoint. IP-литерал требует `tls.server_name` (§6.3) |
| `username`, `password` | string | обязательно | Уходят как `proxy-authorization: Basic` (стандартный base64 с паддингом) на каждом `CONNECT` |
| `network` | `tcp` \| `udp` \| список | оба | Какие сети несёт узел |
| `quic` | bool | `false` | `false` — HTTP/2 поверх TLS/TCP, `true` — HTTP/3 поверх QUIC (нужен `with_quic`) |
| `quic_congestion_control` | `bbr` \| `cubic` \| `reno` | `bbr` | Без `quic` игнорируется |
| `health_check` | bool | `false` | `CONNECT _check` перед dial в сессии, молчавшей дольше 30 с (дедлайн 7 с); никогда не по таймеру |
| `idle_timeout` | duration | `lx.trusttunnel.idle_timeout`, иначе `5m` | Локально закрыть сессию после стольких без активности и без открытых стримов; `"0"` отключает закрытие |
| `tls` | блок `tls` sing-box | обязательно, `enabled: true` | `server_name`, `certificate`, `insecure`, `utls`, `fragment`, `ech`. ALPN: пусто → `h2` (`h3` при `quic`) |
| Dial Fields | — | — | `detour`, `bind_interface`, `domain_resolver`, … |

Глобальный дефолт задаётся в корневом блоке `lx`: нет ключа или `0` — 5 минут.
Собственный `idle_timeout` узла сильнее, включая явный `"0"`:

```json
{ "lx": { "trusttunnel": { "idle_timeout": "10m" } } }
```

Для HTTP/2 рекомендуется uTLS с отпечатком Chrome:

```json
"tls": { "enabled": true, "server_name": "vpn.example.com", "utls": { "enabled": true, "fingerprint": "chrome" } }
```

С `quic: true` uTLS недоступен: при старте ошибка `uTLS is not supported with quic`.
HTTP/3 остаётся с отпечатком quic-go: QUIC Initial, ClientHello и SETTINGS HTTP/3 — как в Go.
На HTTP/2 значения SETTINGS сессии и окно соединения — как у Chrome
(`HEADER_TABLE_SIZE 65536`, `ENABLE_PUSH 0`, `INITIAL_WINDOW_SIZE 6291456`,
`MAX_HEADER_LIST_SIZE 262144`, `WINDOW_UPDATE 15663105`), с uTLS и без него. Как в Go
остаются порядок SETTINGS, лишний `MAX_FRAME_SIZE`, порядок псевдозаголовков и HEADERS без
приоритета: `x/net/http2` взят без форка. Паддинга в протоколе нет, поэтому TLS-in-TLS
проксируемого HTTPS остаётся виден анализу трафика.

## 6.3 Проверки при старте (дословно)

| Условие | Ошибка |
|---------|--------|
| `tls` нет или `enabled: false` | `TLS required` |
| `server` — IP, `tls.server_name` пуст | `tls.server_name is required when server is an IP address: the server routes by SNI and drops a ClientHello without it` |
| пустой `username` / `password` | `missing username` / `missing password` |
| `icmp` в `network` | `outbounds[N].network: unknown network: icmp` (разбор конфига) |
| `quic: true` + `tls.utls.enabled` | `uTLS is not supported with quic` |
| неизвестный congestion control | `unknown quic_congestion_control: <значение>` |
| `tls.alpn` задан без `h2` (`h3` при `quic`) | `tls.alpn must contain h2` |
| `quic: true` в сборке без `with_quic` | `trusttunnel: HTTP/3 requires the with_quic build tag` |
| `lx.trusttunnel.idle_timeout` < 0 | `lx.trusttunnel.idle_timeout must be >= 0` |

## 6.4 Поведение сессии

- **Dial ждёт ответа сервера.** 200 — соединение; всё остальное — ошибка dial с кодом и
  `X-Warning`, например
  `trusttunnel: server responded 502 Bad Gateway: 300 - Connection failed for some reason`
  или `trusttunnel: server responded 407 Proxy Authentication Required`. 403/404/405/407
  вдобавок рвут сессию.
- **Ни keepalive, ни таймеров в простое.** Нет HTTP/2 PING, нет QUIC keepalive. QUIC
  idle timeout HTTP/3-сессии фиксирован: 600 с, это `client_listener_timeout_secs`
  endpoint; idle-окно ниже действует поверх него. Если сессия молчала дольше idle-окна,
  она считается устаревшей: следующий `CONNECT` в ней получает 3 с, затем сессия
  пересобирается и `CONNECT` повторяется один раз в пределах дедлайна вызывающего.
- **Одна сессия на узел.** На пределе стримов endpoint (1000 на HTTP/2, 4096 на HTTP/3)
  dial ждёт свободный стрим в пределах своего дедлайна; вторая сессия не открывается.
- **Смена сети** (`InterfaceUpdated`) сбрасывает сессию: миграция QUIC на endpoint
  выключена.
- **Асимметрия DNS.** TCP-домены уходят на endpoint без резолва и резолвятся там. UDP-кадры
  несут только IP, поэтому домен UDP-назначения резолвится локально `domain_resolver` узла
  (иначе `route.default_domain_resolver`, иначе DNS-правилами). Чтобы UDP-DNS не утекал,
  резолвер стоит пустить через сам узел (`detour`).
- **IPv6 внутри `::/96`** (включая `::1`) в UDP-кадре не передаётся: 12 нулевых байт в
  формате кадра означают IPv4, официальный endpoint декодирует так же.
- Узел представляется как `user-agent: <platform> sing-box-lx` (`Mac`, `Windows`, `Linux`,
  `Android`, `iOS`) и шлёт `sing-box-lx` именем приложения в каждом UDP-кадре.

## 6.5 Соответствие ключей outbound

Таблица сводит рядом три источника:

- официальный CLI-клиент `trusttunnel_client` 1.1.11 — таблица `[endpoint]` его TOML;
  экспорт endpoint, подписка и ссылка `tt://` несут те же ключи;
- форк sing-box-extended 2.7.2 — ключи его
  [`examples/trusttunnel/client.json`](https://github.com/shtorm-7/sing-box-extended/tree/v1.14.1-extended-2.7.2/examples/trusttunnel);
- этот форк.

sing-box отвергает незнакомые ключи при загрузке, поэтому конфиг extended перед стартом
здесь нужно переименовать по таблице.

| Официальный клиент (TOML) | sing-box-extended 2.7.2 | sing-box-lx | Примечание |
|---------------------------|-------------------------|-------------|------------|
| `addresses = ["ip:port"]` | `server`, `server_port` | `server`, `server_port` | Узел на адрес. IP в `server` требует `tls.server_name` |
| `hostname` (`custom_sni`, если задан) | `tls.server_name` | `tls.server_name` | Endpoint маршрутизирует по SNI |
| `username`, `password` | `username`, `password` | `username`, `password` | Пустой `password` здесь — ошибка старта |
| — | `network` | `network` | `tcp`, `udp` или обе (по умолчанию) |
| `upstream_protocol = "http2"` / `"http3"` | `quic: false` / `true` | `quic: false` / `true` | `auto` клиента (гонка H2/H3) здесь — `urltest`-группа из двух узлов |
| — | `congestion_controller` (`bbr`, `bbr_standard`, `bbr2`, `bbr2_variant`, `cubic`, `reno`) | `quic_congestion_control` (`bbr`, `cubic`, `reno`) | Имя как у апстримного outbound `naive`; ключ переименовать |
| — | `cwnd` | — | Не поддерживается; ключ убрать |
| — | `multiplex` (`max_connections`, `min_streams`, `max_streams`) | — | Одна сессия на узел (§6.4); ключ убрать |
| — (клиент проверяет по событиям сам) | `health_check` | `health_check` | Здесь: `CONNECT _check` только по событиям, никогда по таймеру |
| — | — | `idle_timeout` (по умолчанию `5m`, `lx.trusttunnel.idle_timeout`) | Только в этом форке |
| `tls_profile` (`chrome`, `safari`, `firefox`, …) | `tls.utls.fingerprint` | `tls.utls.fingerprint` | Только HTTP/2; рекомендуется `chrome` (§6.2); с `quic: true` — ошибка старта |
| `certificate` (PEM) | `tls.certificate` / `certificate_path` | `tls.certificate` / `certificate_path` | Ссылка `tt://` несёт DER-цепочку; импортёр конвертирует в PEM |
| `skip_verification` | `tls.insecure` | `tls.insecure` | |
| `anti_dpi` | `tls.fragment` | `tls.fragment` | Ближайший встроенный аналог, не та же нарезка |
| `has_ipv6 = false` | `domain_resolver.strategy: "ipv4_only"` | `domain_resolver.strategy: "ipv4_only"` | Endpoint отказывает IPv6-адресатам |
| `client_random` (в экспорте: `client_random_prefix`), `client_random_auth_key` | — | — | Не поддерживается: сервер с такими правилами роняет ClientHello |
| `dns_upstreams`, `vpn_mode`, `exclusions`, `killswitch_*`, `post_quantum_group_enabled`, `[listener.*]`, `name` | `dns`, `route`, `inbounds` | `dns`, `route`, `inbounds` | Не настройки узла: это DNS, маршрутизация и локальные inbound'ы sing-box |

## 6.6 Поля inbound

| Поле | Тип | По умолчанию | Смысл |
|------|-----|--------------|-------|
| Listen Fields | — | — | `listen`, `listen_port`, … |
| `users` | `[{username, password}]` | обязательно | Пустой список или пустое поле в записи — ошибка старта (`missing users`, `users[N]: missing password`) |
| `network` | `tcp` \| `udp` \| список | оба | Какие слушатели открыть, как у апстримного `naive` inbound: `tcp` — HTTP/2 поверх TLS, `udp` — HTTP/3 поверх QUIC. `udp` без `with_quic`: ошибка старта, если он один, предупреждение (`HTTP/3 disabled: …`), если оба. Туннелируемые сети — всегда TCP и UDP; ограничивать — route-правилами |
| `quic_congestion_control` | `bbr` \| `cubic` \| `reno` | `bbr` | Неизвестный — ошибка старта |
| `tls` | inbound-блок `tls` sing-box | обязательно, `enabled: true` | ALPN ставит inbound: `h2` впереди на TCP, `h3` в конце на UDP |

Каждый TCP `CONNECT` приходит в роутер с `inbound`, `user`, `source` (клиент),
`destination` (домен остаётся доменом) и `origin_destination` (адрес слушателя). Каждый
UDP-поток стрима `_udp2` приходит packet-соединением с источником из кадра. Если клиент
шлёт нулевой источник, источником становится адрес сессии.

## 6.7 Поведение inbound

- **Ответ на `CONNECT` идёт после dial.** 200 уходит на первом чтении соединения роутером,
  провал dial — 502 + `X-Warning` (`300 - Connection failed for some reason`, `301 - …` на
  недоступный хост, `302 - …` на таймаут), так же, как отвечает официальный endpoint. С
  route-действием `sniff` роутер читает до dial: 200 уходит раньше, и поздний провал dial
  выглядит как обычное закрытие стрима.
- **Неверные учётные данные** → 407 + `proxy-authenticate: Basic realm=Authorization Required`;
  в логе — адрес источника, учётные данные никогда.
- **Это не продукт AdGuard.** Нет правил по `client_random`, нет подписки, нет
  ping/speed-test-хостов, нет reverse proxy, нет ICMP (`CONNECT _icmp` → 404). Любой
  не-туннельный запрос (браузер, сканер) получает 404 с пустым телом и без
  `proxy-authenticate`. HTTP/1.1 не обслуживается.
- **`allow_private_network_connections = false` endpoint** здесь — route-правило:

```json
{ "route": { "rules": [ { "inbound": "tt-in", "ip_is_private": true, "action": "reject" } ] } }
```

## 6.8 Соответствие ключей inbound

Официальный endpoint (`trusttunnel_endpoint` 1.1.0: `vpn.toml`, `credentials.toml`,
`hosts.toml`), sing-box-extended 2.7.2 (ключи его
[`examples/trusttunnel/server.json`](https://github.com/shtorm-7/sing-box-extended/tree/v1.14.1-extended-2.7.2/examples/trusttunnel))
и этот форк.

| Официальный endpoint (TOML) | sing-box-extended 2.7.2 | sing-box-lx | Примечание |
|-----------------------------|-------------------------|-------------|------------|
| `listen_address = "0.0.0.0:443"` | `listen`, `listen_port` | `listen`, `listen_port` | |
| есть `[listen_protocols.http2]` / `[listen_protocols.quic]` | `network: tcp` / `udp` | `network: tcp` / `udp` | Какие слушатели открыть; по умолчанию оба. Туннелируемые сети — всегда TCP и UDP |
| `credentials.toml` `[[client]]` `username`, `password` | `users[].name`, `users[].password` | `users[].username`, `users[].password` | `username`, как у всех апстримных inbound'ов; `name` переименовать |
| `hosts.toml` `[[main_hosts]]` `hostname`, `cert_chain_path`, `private_key_path` | `tls.server_name`, `certificate_path`, `key_path` | `tls.server_name`, `certificate_path`, `key_path` | |
| — | `tls.alpn: ["h2", "h3"]` | ставит inbound | `h2` на TCP, `h3` на UDP; явный список сохраняется и дополняется |
| — | `congestion_controller` | `quic_congestion_control` (`bbr`, `cubic`, `reno`) | Ключ переименовать |
| — | `cwnd` | — | Не поддерживается; ключ убрать |
| `client_listener_timeout_secs` | — | фиксировано 600 с | Предел простоя сессии, как дефолт endpoint |
| `max_concurrent_streams`, `initial_max_streams_bidi` | — | фиксировано 1000 (H2) / 4096 (H3) | Дефолты endpoint |
| `allow_private_network_connections = false` | — | route-правило `ip_is_private` + `action: reject` | Пример в §6.7 |
| `auth_failure_status_code` | — | фиксировано: 407 на `CONNECT`, 404 на остальное | |
| `ipv6_available = false` | — | route-правила / `strategy` DNS | |
| `udp_connections_timeout_secs` | — | UDP-таймаут sing-box | `udp_timeout` правила `route-options`, по умолчанию 5 мин |
| `rules_file` (правила `client_random`), `[reverse_proxy]`, `[icmp]`, `[metrics]`, `[subscription]`, хосты ping / speed-test | — | — | Не поддерживается |

## 6.9 Примеры

### Outbound

```json
{
  "type": "trusttunnel",
  "tag": "tt-node",
  "server": "vpn.example.com",
  "server_port": 443,
  "username": "user",
  "password": "secret",
  "network": "",
  "quic": false,
  "quic_congestion_control": "bbr",
  "health_check": false,
  "idle_timeout": "5m",
  "tls": {
    "enabled": true,
    "server_name": "vpn.example.com",
    "utls": { "enabled": true, "fingerprint": "chrome" }
  }
}
```

### Inbound

```json
{
  "type": "trusttunnel",
  "tag": "tt-in",
  "listen": "::",
  "listen_port": 443,
  "users": [
    { "username": "user", "password": "secret" }
  ],
  "network": "",
  "quic_congestion_control": "bbr",
  "tls": {
    "enabled": true,
    "server_name": "vpn.example.com",
    "certificate_path": "/etc/sing-box/fullchain.pem",
    "key_path": "/etc/sing-box/key.pem"
  }
}
```

Loopback-стенд с официальными endpoint и клиентом —
[`lx-test/trusttunnel/`](../lx-test/trusttunnel/README.md). Официальный клиент соединяется
с loopback-назначениями напрямую, мимо туннеля, поэтому `check-in` обращается к origin по
имени `localhost` (`curl --socks5-hostname`): домен уходит в `CONNECT` и резолвится уже нашим
inbound.

---

## См. также

- **[lx-config.ru.md](lx-config.ru.md)** — обзор downstream-фич, который эти главы
  разворачивают (таблица фич, build-теги, плюс idle-suspend, DNS-группа, VLESS
  `encryption`, `lxd`, наблюдаемость).
- **[lx-energy.ru.md](lx-energy.ru.md)** — энергомодель, тайминги idle-suspend и
  рекомендованная мобильная конфигурация (актуально для suspend AWG- и MASQUE-endpoint).
- **[xray-protocols-explained.ru.md](xray-protocols-explained.ru.md)** — как устроены
  XHTTP, REALITY, Vision и VLESS `encryption` и как они поддержаны в форке;
  **[amneziawg-explained.ru.md](amneziawg-explained.ru.md)** — то же для AmneziaWG,
  включая бюджет MTU.
- Feature-спеки: [XHTTP](../SPECS/FEATURES/002-XHTTP/), [AWG](../SPECS/FEATURES/003-AWG/),
  [MASQUE/WARP](../SPECS/FEATURES/009-MASQUE_WARP/), [REALITY](../SPECS/FEATURES/017-REALITY/),
  [VLESS_ENCRYPTION](../SPECS/FEATURES/012-VLESS_ENCRYPTION/),
  [TRUSTTUNNEL](../SPECS/FEATURES/018-TRUSTTUNNEL/).
