# SPEC 139 — StartConnectionTest: активная проверка соединения с трассой стадий

> 🌐 English version: **[SPEC.md](SPEC.md)** — основная спека; этот файл — русская копия для ознакомления.

**Фича:** [OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature). RPC — мост §3.6; трасса (`common/lxtrace` и её вставки) — lx-логика, изолированная по CONSTITUTION §3.2–3.3, цена мержа зафиксирована в §10 |
| Статус | N (new) |
| Ветка | `lx` |
| Связанные | [#59](https://github.com/Leadaxe/sing-box-lx/issues/59) (запрос: DNS через роутер и матч правила — закрывается этой задачей) · [Leadaxe/LxBox#176](https://github.com/Leadaxe/LxBox/issues/176) (экран «Connection Check») · [[SPECS/TASKS/058-GET_URL_VIA_OUTBOUND]] (HTTP-пробник узла — образец модели ошибок и лимитов тела) · [[SPECS/TASKS/018-DNS_QUERY_STREAM]] · [[SPECS/TASKS/035-DNS_GROUP_OBSERVABILITY]] (атрибуция DNS и пробы группы) · [[SPECS/TASKS/017-CONNECTION_DETOUR_CHAIN]] · [[SPECS/TASKS/038-GOMOBILE_STRING_RETURN_FRAME_KILL]] (форма возврата libbox) |

## 1. Проблема

На жалобу «не открывается» ядро сегодня отвечает кусками, и ни один кусок
не говорит, **каким путём пойдёт запрос и где он ломается**:

- `URLTestOutbound` и `GetURLViaOutbound` проверяют отдельный узел, минуя
  правила, sniff и выбор групп.
- DNS-поток показывает запросы, которые уже были, но не связывает их
  с соединением, ради которого они шли.
- Ошибка соединения приходит одной склеенной строкой. Корень часто лежит
  **вне маршрута**: адрес узла резолвит DNS-сервер, который сам ходит через
  другой узел, а тот мёртв. Пользователь видит «узел A не работает» и чинит
  не тот узел.
- Соединения, отклонённые правилом или упавшие до учёта, в списке
  соединений не появляются вовсе.

Запрос #59 просил два вызова: DNS через роутер и матч правила без dial.
Матч на синтетических данных неточен: без sniff, приложения, `network`
и порта правила дают другой ответ, чем для настоящего соединения. Эта
задача заменяет оба вызова одной проверкой с выбором глубины:
`DEPTH_DNS` отвечает на первый вызов из #59 (адреса, задержка,
ответивший сервер и сработавшее DNS-правило, по типу запроса);
`DEPTH_ROUTE` — на второй (правило, действие, outbound и цепочка
с развёрнутыми группами и детурами — стадии `group`, `chain`
и `detour`).

## 2. Суть

`StartConnectionTest` — **активная** проверка: ядро выполняет настоящий
запрос через весь свой конвейер (DNS → правило → группа → узел → его
detour'ы → туннель → HTTP) от имени заданного источника и записывает
каждую стадию: что делалось, внутри какой стадии, сколько заняло, чем
кончилось. Стадии отдаются потоком по мере выполнения. Последнее сообщение
потока — итог: HTTP-результат, корневая причина и выводы.

Ретроспектива (разбор прошлых соединений) — не эта задача.

## 3. Контракт

### 3.1 Протокол

```proto
// lx:begin lx_command
rpc StartConnectionTest(ConnectionTestRequest) returns (stream ConnectionTestProgress) {}

// От чьего имени идёт проверка. Все поля необязательны; заполняются те,
// по которым правила различают источник на данной платформе.
message ConnectionTestSource {
  string inbound = 1;      // тег inbound; пусто — проверка без inbound
  string packageName = 2;  // Android
  int32 uid = 3;           // 0 — не задан; на Android выводится из packageName, если платформа даёт package manager (§7)
  string processPath = 4;  // десктоп
  string sourceIP = 5;     // роутер: адрес устройства в локальной сети
  string sourceMAC = 6;
  string authUser = 7;
}

message ConnectionTestRequest {
  string link = 1;                      // http/https URL
  string outboundTag = 2;               // пусто — через роутер; тег — напрямую через узел или endpoint
  ConnectionTestSource source = 3;
  bool appDNS = 4;                      // сначала DNS-запросы, как их задало бы приложение
  string connectIP = 5;                 // подключаться к этому адресу вместо результата DNS
  bool fresh = 6;                       // false — как в работе (кэш, живые соединения); true — всё с нуля
  ConnectionTestDepth depth = 7;
  string network = 8;                   // "tcp" (по умолчанию) или "udp"
  uint32 timeout = 9;                   // мс; 0 — дефолт 15000; потолок 60000
  uint32 maxBytes = 10;                 // как в GetURLViaOutbound: дефолт 256 KiB, потолок 1 MiB
  repeated HttpHeaderPair headers = 11;
}

enum ConnectionTestDepth {
  DEPTH_HTTP = 0;      // полный путь до HTTP-ответа
  DEPTH_DNS = 1;       // только DNS-запросы приложения (подразумевает appDNS)
  DEPTH_ROUTE = 2;     // DNS (если appDNS) + выбор правила, группы и узла; наружу не соединяется
  DEPTH_CONNECT = 3;   // до установленного туннеля, без запроса к цели
}

message ConnectionTestAttr {
  string key = 1;
  string value = 2;
}

enum ConnectionTestStageResult {
  STAGE_RUNNING = 0;
  STAGE_OK = 1;
  STAGE_FAILED = 2;
  STAGE_SKIPPED = 3;        // не выполнялась: упала стадия раньше или не позволила глубина
  STAGE_REUSED = 4;         // не выполнялась: использовано готовое (кэш, живое соединение, поднятый туннель)
  STAGE_NO_ACK = 5;         // выполнена, но протокол не подтверждает её успех
  STAGE_NOT_EVALUATED = 6;  // не вычислена: не хватило данных (например, адрес неизвестен)
}

message ConnectionTestStage {
  uint32 id = 1;            // уникален в пределах проверки, растёт
  uint32 parent = 2;        // 0 — стадия верхнего уровня
  string kind = 3;          // словарь — §4
  string subject = 4;       // над чем стадия: тег, домен, адрес
  uint32 startMs = 5;       // от начала проверки
  uint32 durationMs = 6;
  ConnectionTestStageResult result = 7;
  string error = 8;         // причина как есть; пусто, если не FAILED
  repeated ConnectionTestAttr attrs = 9;
}

enum ConnectionTestFindingLevel {
  FINDING_INFO = 0;
  FINDING_WARNING = 1;
  FINDING_PROBLEM = 2;
}

message ConnectionTestFinding {
  string code = 1;                      // словарь — §6
  ConnectionTestFindingLevel level = 2;
  repeated uint32 stages = 3;           // к каким стадиям относится
  repeated ConnectionTestAttr attrs = 4;
}

message ConnectionTestResult {
  string error = 1;         // ошибка самой проверки (неверный запрос); пусто — проверка состоялась
  uint32 rootCause = 2;     // id стадии; 0 — путь пройден
  repeated ConnectionTestFinding findings = 3;
  uint32 httpStatus = 4;
  bytes body = 5;
  bool truncated = 6;
  string contentType = 7;
  uint32 elapsedMs = 8;
  bool stagesTruncated = 9; // достигнут потолок стадий (§5.15); дерево неполное
}

message ConnectionTestProgress {
  oneof event {
    ConnectionTestStage stage = 1;    // новая стадия (RUNNING) или её завершение — тот же id
    ConnectionTestResult result = 2;  // последнее сообщение потока
  }
}
// lx:end lx_command
```

### 3.2 Модель ошибок

| Исход | Ответ |
|-------|-------|
| Путь пройден, сервер цели ответил любым статусом | `result.error == ""`, `rootCause == 0`, HTTP-поля заполнены. Не-2xx — данные, не ошибка (как в `GetURLViaOutbound`) |
| Путь оборвался (DNS, правило `reject`, dial, TLS, таймаут…) | `result.error == ""`, `rootCause` указывает на стадию, HTTP-поля пусты. Сбой соединения — результат проверки, а не ошибка вызова |
| Неверный запрос: битый URL, неизвестный тег узла или inbound, `network = "udp"` глубже `DEPTH_ROUTE` | Поток из одного `result` с `error`; стадий нет |
| Превышен лимит одновременных проверок | `result.error = "busy"` |
| Сервис не STARTED | transport-ошибка (`os.ErrInvalid`), как у `URLTestOutbound` |
| Сборка без `with_lx_command` | `Unimplemented` (stub-двойник) |

Отмена — закрытием потока клиентом. Все соединения проверки закрываются,
итог не отправляется.

### 3.3 Форма в libbox

```go
func NewConnectionTestRequest() *ConnectionTestRequest
func (r *ConnectionTestRequest) SetLink(link string)
func (r *ConnectionTestRequest) SetOutboundTag(tag string)
func (r *ConnectionTestRequest) SetInbound(tag string)
func (r *ConnectionTestRequest) SetPackageName(name string)
func (r *ConnectionTestRequest) SetUID(uid int32)
func (r *ConnectionTestRequest) SetProcessPath(path string)
func (r *ConnectionTestRequest) SetSourceIP(ip string)
func (r *ConnectionTestRequest) SetSourceMAC(mac string)
func (r *ConnectionTestRequest) SetAuthUser(user string)
func (r *ConnectionTestRequest) SetAppDNS(enabled bool)
func (r *ConnectionTestRequest) SetConnectIP(ip string)
func (r *ConnectionTestRequest) SetFresh(fresh bool)
func (r *ConnectionTestRequest) SetDepth(depth int32)
func (r *ConnectionTestRequest) SetNetwork(network string)
func (r *ConnectionTestRequest) SetTimeout(ms int32)
func (r *ConnectionTestRequest) SetMaxBytes(n int32)
func (r *ConnectionTestRequest) SetHeaders(headers *HTTPHeaders)

type ConnectionTestHandler interface {
	OnStage(stage *ConnectionTestStage)
	OnResult(result *ConnectionTestResult)
	OnError(message string)
}

func (c *CommandClient) StartConnectionTest(request *ConnectionTestRequest, handler ConnectionTestHandler) (*ConnectionTestSession, error)
func (s *ConnectionTestSession) Cancel()
```

- Запрос — объект с сеттерами: параметров много, и gomobile не умеет ни
  необязательных аргументов, ни вложенных структур. `HTTPHeaders` —
  билдер из `GetURLViaOutbound`.
- Стадия, итог и вывод — объекты с геттерами; атрибуты и списки — через
  итераторы. Голых строк в возврате методов `CommandClient` нет (038).
- Форма handler'а и сессии — как у апстримных `StartNetworkQualityTest`
  и `StartSTUNTest`.
- `OnError` срабатывает только на транспортный сбой потока. Ошибка самой
  проверки (`result.error`: неверный запрос, `busy`) приходит в
  `OnResult`, и клиент не должен ждать на неё `OnError`.

### 3.4 CLI

`sing-box api connection-test <url>` с флагами по полям запроса.
Печатает дерево стадий по мере прихода и итог. Нужен для lxd-машин,
десктопа и стендов.

## 4. Стадии

Каждая стадия — один шаг конвейера. Стадия, порождённая внутри другой,
— её потомок на любой глубине: резолв адреса узла — потомок dial узла,
DNS-сервер этого резолва — потомок резолва, detour DNS-сервера — потомок
DNS-сервера, и так далее.

**Словарь открыт.** Ядро может добавлять виды стадий и атрибуты без смены
протокола. Клиент рисует неизвестный вид как `kind` + `subject` + атрибуты.

| `kind` | Что | Основные атрибуты |
|--------|-----|-------------------|
| `app_dns` | DNS-запрос приложения (при `appDNS`), по одному на тип: A, AAAA, HTTPS | тип, правило (`dns[i]` или `final`), сервер, ответы, rcode, источник ответа (сеть / кэш с возрастом) |
| `route` | Выбор правила маршрута | назначение, откуда взят домен (`request` / `fakeip` / `reverse_mapping` / `sniff` / `none`), результат sniff, применённые не финальные действия по порядку, правило (`route[i]` + описание или `final`), действие, outbound, правила, которые не удалось вычислить, и почему |
| `group` | Выбор в группе | тип и режим группы, выбранный узел; для выбора на каждое соединение — признак и слот |
| `chain` | Развёртка `chain` | позиции, разрешённые узлы |
| `dial` | Соединение через узел | тег, тип, адрес сервера |
| `detour` | Передача соединения детуру | тег детура, что передано: домен или адрес |
| `lookup` | Резолв адреса сервера узла или DNS-сервера | домен, кто резолвит (`domain_resolver` / `default_domain_resolver` / `dns_rules` с правилом), сервер, ответ |
| `dns` | Запрос к DNS-серверу | тег, тип, путь групп, пробы группы (как в 035), соединение (новое / переиспользовано, возраст), rcode |
| `wake` | Подъём усыплённого endpoint'а | сколько спал |
| `tcp_connect` | TCP-соединение | адрес, локальный адрес |
| `quic` | QUIC-соединение | адрес, обфускация |
| `tls` | TLS-рукопожатие | SNI, отпечаток, ALPN, версия, key share |
| `reality` | Вердикт REALITY | `accepted` / `dest_cert` (сервер не признал клиента и отдал сертификат dest) / `invalid_cert` |
| `transport` | Транспорт поверх TLS (XHTTP, gRPC, WS, HTTPUpgrade, H2) | тип, режим, метод, путь, HTTP-статус |
| `auth` | Аутентификация там, где протокол её подтверждает (Hysteria2, TUIC, naive, TrustTunnel, MASQUE, SOCKS, HTTP CONNECT) | статус ответа |
| `wg_handshake` | Хендшейк WG/AWG | новый (время) или существующий (возраст последнего) |
| `proxy_header` | Заголовок протокола без подтверждения (VLESS, VMess, Trojan, Shadowsocks) | цель; итог всегда `NO_ACK` |
| `target_tls` | TLS проверки с целью через туннель | SNI, ALPN |
| `first_byte` | Первые байты ответа цели | — |
| `http` | HTTP-ответ | статус, Content-Type, байты |

Стадии проверки — те, которые реально выполнил конвейер. Если узел
направил соединение не туда, куда ожидал пользователь, трасса покажет
фактический путь, а не ожидаемый.

## 5. Правила и гарантии

1. **Настоящий путь.** Через роутер проверка входит как обычное входящее
   соединение с метаданными источника: правила, sniff, выбор групп
   и detour'ы — боевые. Через узел (`outboundTag`) — тот же путь, что
   у `GetURLViaOutbound`, плюс трасса.
2. **Корневая причина.** Среди `FAILED`-стадий, без проигравших гонку
   сиблингов (§5.13), берётся завершившаяся первой и без упавших
   потомков: самый глубокий сбой, остановивший конвейер. Стадии,
   не выполненные из-за него, — `SKIPPED`. PLAN §4 реализует это правило
   и никакое другое.
3. **Пропуск не маскируется.** Стадия, не выполненная из-за кэша, живого
   соединения или поднятого туннеля, — `REUSED` с возрастом, а не `OK`
   с нулём миллисекунд.
4. **Нет подтверждения — не успех.** Шаг, успех которого протокол не
   подтверждает, — `NO_ACK`. Его подтверждает только последующая стадия
   (`first_byte`).
5. **Цикл резолва** ловится сразу, а не по таймауту: повторный вход в ту же
   пару «резолвер, домен» среди предков даёт `FAILED` с атрибутом,
   указывающим на стадию-предка.
6. **`fresh = true`:**
   - кэш DNS обходится на всех уровнях, и ответы проверки в кэш не пишутся;
   - DNS-серверы открывают новые соединения;
   - транспорты узлов открывают новые соединения там, где протокол это
     допускает.

   Endpoint'ы WG/AWG и мультиплексы внешних модулей проверка не
   разбирает и не перезапускает: их стадии — `REUSED` с возрастом
   соединения или последнего хендшейка.
7. **Проверка не меняет состояние групп:** выбор selector'а, историю
   urltest, штрафы и деградацию failover, закрепления `round_robin`.
8. **Через роутер соединение проверки видно** в списке соединений
   и отличимо от боевых по типу входа. Через узел (`outboundTag`) оно
   не трекается, как и у `GetURLViaOutbound`.
9. **Глубина соблюдается:** `DEPTH_DNS` и `DEPTH_ROUTE` не открывают
   соединений наружу, кроме DNS-запросов, которые требует сама глубина.
10. **Только по явному действию.** Проверка — реальный трафик: на лимитных
    узлах это расход, спящие WG/AWG просыпаются. Одновременно — не больше
    4 проверок.
11. **Таймаут общий.** По истечении незавершённые стадии — `FAILED`
    с причиной `timeout`, корень — самая глубокая из них.
12. **Без проверки боевой путь не платит ничего,** кроме проверки наличия
    трассы в контексте.
13. **Параллельные dial.** Когда dialer гонит несколько адресов или
    интерфейсов, проигравшие сиблинги — `SKIPPED` с атрибутом
    `lost_race=<id победителя>`, не `FAILED`, и корнем не бывают.
14. **Проверка владеет своими соединениями.** Каждое соединение,
    открытое проверкой, включая вынужденные `fresh`, закрывается по её
    концу и не попадает в боевые пулы (пул транспорта XHTTP, пул
    соединений DNS).
15. **Потолок стадий.** Проверка записывает не больше 512 стадий. Сверх
    потолка стадии считаются, но не записываются, итог несёт
    `stagesTruncated`.

## 6. Выводы

Выводы ядро вычисляет по трассе и состоянию ядра и отдаёт кодами со
ссылками на стадии и атрибутами. Текст пишет клиент. Словарь открыт,
как и у стадий.

| Код | Когда |
|-----|-------|
| `excluded_from_vpn` | Источник не попадает в туннель (`include_package` / `exclude_package` у TUN). Путь в трассе гипотетический |
| `resolve_via_rules_in_source_context` | Адрес сервера узла резолвится по DNS-правилам в контексте источника (у узла есть detour и нет `domain_resolver`). Для другого источника путь резолва может быть другим |
| `dependency_outside_route` | Путь зависит от узла, который в маршруте не участвует (например, detour DNS-сервера). Атрибуты: узел и что от него зависит |
| `resolve_cycle` | Найден цикл резолва (§5.5) |
| `ip_rules_not_evaluated` | Правила по адресу не вычислены: адрес неизвестен (fakeip, домен без `resolve`) |
| `fakeip_not_persisted` | fakeip не сохраняется между перезапусками: после рестарта приложения с закэшированными адресами получат отказ |
| `fakeip_pool_wrapped` | Пул fakeip обернулся быстрее TTL ответа: старый адрес мог достаться новому домену. Атрибуты: размер диапазона, время оборота, TTL |
| `fakeip_unknown_address` | `connectIP` из диапазона fakeip, записи нет |
| `fakeip_address_reassigned` | `connectIP` сейчас принадлежит другому домену |
| `fakeip_ipv6_without_tun_ipv6` | AAAA отдаёт fakeip IPv6, а у TUN нет IPv6-адреса |
| `https_query_rejected` | DNS-запрос HTTPS отклонён правилом: клиент не получит ECH и ALPN-подсказки |
| `quic_rejected` | `udp/443` отклоняется правилом: браузер откатится на TCP |
| `reality_not_recognized` | Вердикт `dest_cert`. Возможные причины (не различимы): `public_key`, `short_id`, SNI вне `server_names`, версия клиента |
| `failed_after_no_ack` | Обрыв после стадии `NO_ACK`. Возможные причины: неверный UUID/пароль, сервер не достучался до цели, потеря больших пакетов (MTU) |
| `partially_reused` | Путь пройден, но часть стадий `REUSED`: результат не доказывает, что путь поднимется с нуля |

## 7. Границы

- **Только GET по http/https.** Произвольные методы и тела — нет (как
  у `GetURLViaOutbound`). `network = "udp"` — только до выбора узла:
  HTTP/3-запроса проверка не делает.
- **Поведение приложения не воспроизводится.** Свой DNS приложения,
  ECH, собственный ClientHello, соединения, открытые до VPN, проверка
  не повторяет: она повторяет правила для источника, а не его сетевое
  поведение.
- **Не ретроспектива.** Прошлые отказы, включая отклонённые до учёта
  соединения, — отдельная задача.
- **Конфиг не правится,** и правок проверка не предлагает. Выводы
  описывают состояние, решение за пользователем.
- **За сервером узла** проверка видит только сквозной HTTP-ответ.
- **Качество канала не меряется:** скорость и тип NAT — апстримные
  `StartNetworkQualityTest` и `StartSTUNTest`.
- **Неразличимое не различается.** Причины внутри одного класса
  (`reality_not_recognized`, тишина после `NO_ACK`) перечисляются,
  но не выбираются.
- **Гранулярность стадий зависит от протокола.** Где реализация протокола
  во внешнем модуле не сообщает промежуточных шагов (например, QUIC
  и аутентификация Hysteria2 — одним вызовом), стадии объединены,
  а итог классифицируется по ошибке.
- **UID по пакету** требует package manager платформы, который есть
  только при inbound `tun` в платформенной сборке (Android). Без него
  `uid` остаётся 0 и правила `user_id` не матчатся.
- **Правила `source_port` не покрываются:** порт источника у проверки
  эфемерный.
- **Протоколы с ранним handshake при `DEPTH_CONNECT`.** VLESS, Trojan
  и Shadowsocks шлют заголовок вместе с первой полезной нагрузкой. Без
  запроса нагрузки нет, и стадии `proxy_header` на этой глубине в трассе
  не будет; туннель поднят до транспорта узла.

## 8. Критерии приёмки

1. Через роутер: правило, sniff, выбор группы и цепочка проверки
   совпадают с боевым соединением с теми же метаданными источника.
   Измеряется на стенде сравнением стадий `route` и `group` с записью
   списка соединений (`rule`, `outbound`, `chain`, sniff-`protocol`
   и `domain`) настоящего соединения того же источника к тому же URL.
2. Узел с двумя detour'ами разных протоколов и резолвом адреса через
   DNS-сервер, который ходит через узел вне маршрута: дерево с правильной
   вложенностью; при выключенном узле DNS-детура корень — его
   `tcp_connect`, стадии узлов маршрута — `SKIPPED`, есть вывод
   `dependency_outside_route`.
3. Цикл резолва даёт `resolve_cycle` раньше таймаута.
4. Прогретый путь при `fresh = false` даёт стадии `REUSED` с возрастом;
   тот же путь при `fresh = true` выполняет их, а кэш DNS после
   проверки не меняется.
5. REALITY: неверный `public_key` — `dest_cert` и
   `reality_not_recognized`; верный — `accepted`.
6. Пакет, исключённый в TUN, — `excluded_from_vpn`.
7. `connectIP` из диапазона fakeip без записи — `route` `FAILED`
   и `fakeip_unknown_address`; с записью другого домена —
   `fakeip_address_reassigned`.
8. `DEPTH_ROUTE` не открывает соединений наружу.
9. После проверки через группу не изменились выбор selector'а, история
   urltest, штрафы и закрепления.
10. Отмена закрывает все соединения проверки; утечек горутин нет
    (`-race`).
11. Без `with_lx_command` — `Unimplemented`; страж 038 по поверхности
    `CommandClient` проходит.

## 9. Поставка двумя релизами

Задача выходит двумя релизами, чтобы первый результат дошёл
до пользователя, не дожидаясь всего словаря стадий.

| Релиз | Содержание | Критерии приёмки |
|------|------------|------------------|
| 1 | Протокол, держатель трассы, оба пути (роутер и узел), все глубины, `appDNS`, `connectIP` без выводов по fakeip; стадии `app_dns`, `route`, `group`, `chain`, `dial`, `detour`, `lookup`, `dns`, `tcp_connect`, `tls`, `transport` (XHTTP), `proxy_header`, `target_tls`, `first_byte`, `http`; корень, ловля цикла резолва, libbox, CLI. Без выводов | 1, 3, 8, 9, 10, 11; критерий 2 без вывода |
| 2 | `fresh` на вложенных уровнях с возрастом `REUSED`, вердикт `reality`, стадии `wake`, `wg_handshake`, `auth` и `quic`, состояние fakeip, все выводы §6 | 2 (вывод), 4, 5, 6, 7 |

Релиз 1 закрывает #59 и даёт экрану LxBox данные. Состав релиза 2 определяется
по полевому опыту релиза 1 и может быть пересмотрен. Протокол §3.1 полон
уже в релизе 1, включая виды стадий и коды выводов, которые даёт только
релиз 2: релиз 2 не меняет `.proto`, и LxBox может собираться против
протокола релиза 1.

## 10. Цена мержа (CONSTITUTION §3.1 а3)

Сам RPC — мост §3.6: шов в `daemon/started_service.proto`, регенерация,
handler и stub в `daemon/*_lx.go`, клиент в `experimental/libbox/*_lx.go`.
Трасса — lx-логика в новом пакете `common/lxtrace` плюс однострочные
вставки `// lx:` в апстрим-файлы, каждая — nil-safe вызов, который без
трассы стоит одну проверку `ctx.Value`:

| Файл | Релиз | Вставок |
|------|------|---------|
| `route/route.go` | 1 | 1 (`lxtrace.Routed` до трекеров: стадия `route` и останов `DEPTH_ROUTE`) |
| `route/conn.go` | 1 | 1–2 (`dial` первого хопа вокруг `DialContext` / `DialSerialNetwork`, TCP и UDP) |
| `common/dialer/default.go`, `detour.go`, `resolve.go` | 1 | по 1–2 |
| `common/tls/client.go` | 1 | 1 |
| `dns/router.go` | 1 | 1–3 (сработавшее DNS-правило) |
| `protocol/wireguard/endpoint.go` | 1 | 2 (`lookup` по правилам и `tcp_connect` внутри туннеля, обе в `DialContext`, рядом с lx-блоками SPEC 020) |
| `common/tls/reality_client.go` | 2 | 1 (уже несёт lx-правки) |
| `dns/client.go`, `dns/transport/conn_pool.go` | 2 | по 1–2 |
| `dns/transport/fakeip/store.go` | 2 | 1 |

Альтернативы на стороне потребителя нет: решение маршрута, sniff, выбор
группы и вложенные резолвы видны только внутри ядра, а апстрим не даёт
ни трассы соединения, ни вызова матча правил (#59). Самые конфликтные
места — `exchangeWithRules` в `dns/router.go` и `NewConnection`
в `route/conn.go`; их вставки идут отдельным коммитом, чтобы при
конфликте мержа их можно было снять и вернуть без остального.

## Приложение А. Пример трассы

Конфиг: `proxy` (selector) → `vless-nl` (VLESS + XHTTP + REALITY,
`nl.example.net`, detour `awg-de`, без `domain_resolver`) → `awg-de`
(AWG, `de.example.net`, detour `hy2-fi`, `domain_resolver: dns-remote`)
→ `hy2-fi` (Hysteria2, salamander, `domain_resolver: dns-boot`).
`dns-remote` — DoH через `ss-pl` (Shadowsocks 2022, в маршруте не
участвует). DNS-правило `dns[2] domain_suffix example.net → dns-remote`.

Запрос: `https://www.youtube.com/generate_204`, через роутер, источник
`com.android.chrome` на `tun-in`, `appDNS`, `fresh`. Так клиент может
нарисовать трассу (левая колонка — `startMs`, правая — `durationMs`):

```
   0  ✓ app_dns   A     www.youtube.com → 198.18.0.42      dns[3] → dns-fake
   0  ✗ app_dns   HTTPS www.youtube.com → reject           dns[0]
   1  ✓ route     198.18.0.42:443 tcp · fakeip ↦ www.youtube.com
                  sniff tls www.youtube.com · route[5] domain_suffix=youtube.com → proxy
   1  ✓ group     proxy (selector) → vless-nl
   2  ✓ dial      vless-nl
   2    ✓ detour  awg-de ← nl.example.net:443 (домен)
   2      ✓ lookup  nl.example.net → 198.51.100.20   dns_rules: dns[2] → dns-remote
   2        ✓ dns   dns-remote (DoH), новое соединение
   2          ✓ lookup       dns.example.org → 198.51.100.9   domain_resolver dns-boot     22
  24          ✓ dial         ss-pl
  24            ✓ tcp_connect  203.0.113.50:8388                                         37
  61            · proxy_header (NO_ACK)
  61          ✓ tls          dns.example.org · h2                                        79
 140          ✓ transport    POST /dns-query → 200                                       38
 178      ✓ wake    awg-de, спал 12 мин
 178        ✓ lookup  de.example.net → 192.0.2.77   domain_resolver dns-remote           33
 211        ✓ dial    hy2-fi
 211          ✓ lookup  fi.example.net → 203.0.113.9   domain_resolver dns-boot          19
 230          ✓ quic + auth   203.0.113.9:443 · salamander · 233                        109
 340        ✓ wg_handshake  новый                                                       81
 421      ✓ tcp_connect  198.51.100.20:443 внутри awg-de                                66
 487    ✓ tls          uTLS chrome · SNI www.microsoft.com · X25519MLKEM768             74
 561    ✓ reality      accepted
 561    ✓ transport    XHTTP stream-one · POST /xh/ → 200                               67
 628    · proxy_header VLESS → www.youtube.com:443 (NO_ACK)
 628  ✓ target_tls    www.youtube.com                                                  147
 775  ✓ first_byte                                                                      67
 842  ✓ http          204

выводы: resolve_via_rules_in_source_context (стадия lookup nl.example.net),
        https_query_rejected, dependency_outside_route (ss-pl → адреса vless-nl, awg-de)
```
