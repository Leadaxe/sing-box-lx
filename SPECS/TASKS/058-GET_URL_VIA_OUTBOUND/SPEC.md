# SPEC 058 — GetURLViaOutbound: HTTP-пробник узла с возвратом тела ответа

**Фича:** [OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — расширение libbox command-протокола (CONSTITUTION §3.6) |
| Статус | C (complete) — код + тесты + сборки; отгружено в `v1.14.0-lx.25-rc.1`; ждёт полевой проверки |
| Ветка | `lx` |
| Связанные | [[SPECS/TASKS/015-COMMAND_PROTOCOL_RPC_EXTENSIONS]] (`URLTestOutbound` — донор резолва тега и модели отмены) · [[SPECS/TASKS/037-RUNNING_CONFIG_RPC]] (образец объекта-возврата) · [[SPECS/TASKS/038-GOMOBILE_STRING_RETURN_FRAME_KILL]] (форма возврата libbox) |

## 1. Проблема

Вся диагностика узла сегодня — одно число. `URLTestOutbound` отвечает
«жив, N мс», но реальные жалобы — следующего класса: «пинг есть, а сайты
не открываются», «узел работает, но сервисы считают меня другой страной»,
«WARP подключён, а `warp=off`». Это вопросы про **содержимое** ответа
(exit-IP, гео, заголовки), и из клиента (CC) на них ответить нечем:

- `URLTestOutbound` тело выбрасывает — возвращает только delay/error.
- `libbox.NewHTTPClient()` ходит **мимо ядра** (голый `net.Dialer`,
  максимум socks-обход) — узел по тегу им не проверить.
- Снаружи проверка конкретного узла выражается только временным
  переключением активного selector'а на него — ломает живые соединения
  пользователя и гоняется при возврате. Диагностика, меняющая состояние
  системы, — плохая диагностика.

У апстрима такого вызова нет, потому что нет сценария «per-node
диагностика из хост-приложения»: официальный GUI показывает только
задержки. У нас сценарий есть — значит, шов наш.

## 2. Контракт

Синхронный **unary** RPC — один вызов, один ответ, без стрима.
Отмена — закрытием вызова (модель `URLTestOutbound`).

```proto
// lx:begin lx_command
rpc GetURLViaOutbound(GetURLViaOutboundRequest) returns (GetURLViaOutboundResponse) {}

message HttpHeaderPair {
  string key = 1;
  string value = 2;
}
message GetURLViaOutboundRequest {
  string outboundTag = 1;              // outbound ИЛИ endpoint (WG/AWG/…)
  string link = 2;                     // полный URL, http/https
  uint32 timeout = 3;                  // мс; 0 → ограничен только ctx вызова
  uint32 maxBytes = 4;                 // 0 → дефолт 256 KiB; клампится к потолку 1 MiB
  repeated HttpHeaderPair headers = 5; // опционально; пустой список = без заголовков
}
message GetURLViaOutboundResponse {
  uint32 httpStatus = 1;   // 0 ⇔ error != ""
  bytes body = 2;          // обрезано по maxBytes
  bool truncated = 3;      // тело было длиннее лимита
  string contentType = 4;  // Content-Type ответа as-is
  string remoteAddr = 5;   // адрес соединения изнутри туннеля (см. §2.2)
  uint32 elapsedMs = 6;    // от начала dial до конца чтения тела
  string error = 7;        // Variant B, см. ниже
}
// lx:end lx_command
```

### 2.1 Модель ошибок — Variant B (как `URLTestOutbound`)

Каждый прикладной исход — в payload, не в transport-ошибке gRPC;
handler всегда возвращает `(resp, nil)`. Источник правды — поле `error`:

| Исход | Ответ |
|-------|-------|
| Сервер ответил (любым статусом: 200, 403, 500…) | `error == ""`, `httpStatus`/`body`/… заполнены. **Не-2xx — результат, а не ошибка**: 403 от Cloudflare — говорящие данные |
| Тег не найден ни в outbound, ни в endpoint | `error = "outbound or endpoint not found: <tag>"` |
| dial/TLS/таймаут/обрыв чтения | `error = <причина>` |
| Сервис не STARTED | transport-ошибка (`os.ErrInvalid`) — как у `URLTestOutbound` |
| Без `with_lx_command` | `Unimplemented` (stub-двойник) |

### 2.2 Семантика полей

- **`httpStatus`** — код финального ответа. Редиректы следуются
  (лимит Go по умолчанию, 10), каждый хоп — через тот же узел;
  возвращается статус конечной точки.
- **`body`** — `bytes`, не `string`: тело произвольно и не обязано быть
  валидным UTF-8 (proto3-`string` этого требует). Обрезка — ровно по
  клампнутому `maxBytes`, признак — `truncated`, молчаливого усечения нет.
- **`remoteAddr`** — адрес, с которым фактически установлено соединение
  **изнутри туннеля** (результат резолва цели через узел). Это НЕ exit-IP
  узла — exit-IP приносит тело (`cdn-cgi/trace`). Поле отвечает на
  «куда резолвился домен через этот узел».
- **`elapsedMs`** — полное время обмена, включая чтение тела; это не
  замер задержки и **в историю urltest не пишется** (см. §3).
- **`headers`** запроса: применяются as-is; пара с ключом `Host`
  переопределяет `Host` запроса (в Go это отдельное поле, не заголовок).
  `User-Agent` по умолчанию ядро не подменяет; нужен свой — клиент
  передаёт парой.

### 2.3 Форма в libbox (контракт с клиентом)

```go
type HTTPHeaders struct{ ... }              // минимальный билдер
func NewHTTPHeaders() *HTTPHeaders
func (h *HTTPHeaders) Add(key string, value string)

func (c *CommandClient) GetURLViaOutbound(
    outboundTag string, link string,
    timeout int32, maxBytes int32,
    headers *HTTPHeaders,                   // nil = без заголовков
) (*GetURLResult, error)

func (r *GetURLResult) Status() int32       // 0 при неудаче обмена
func (r *GetURLResult) Content() string     // тело; бинарное → лоссиконверсия UTF-8
func (r *GetURLResult) Truncated() bool
func (r *GetURLResult) ContentType() string
func (r *GetURLResult) RemoteAddr() string
func (r *GetURLResult) ElapsedMs() int32
```

- **Опциональность `headers`** выражается nullable-объектом — в gomobile
  нет ни overload'ов, ни variadic; `nil` — легальный вызов. Коллекции
  кроме `[]byte` мост не умеет, поэтому билдер, не map/slice пар.
- **Возврат — объект с геттерами**, не голая строка: требование биндинга
  ([[SPECS/TASKS/038-GOMOBILE_STRING_RETURN_FRAME_KILL]]), та же форма,
  что `RunningConfig`.
- `Content()` отдаёт `string`: инструмент рассчитан на текстовые
  диагностические эндпоинты; бинарное тело через мост доедет с потерями —
  это граница, а не баг (см. §5).
- Прикладная неудача (`error != ""` в payload) мапится в Go-`error`
  вызова — симметрично `URLTestOutboundResult`.

## 3. Механизм

- **Handler** — сосед `URLTestOutbound` в build-tag-паре
  `started_service_command_lx.go` / `_stub.go`. От донора берётся
  дословно: проверка STARTED, **резолв тега в обоих менеджерах**
  (outbound → endpoint; endpoint встраивает `adapter.Outbound`, значит
  `N.Dialer` есть у обоих), модель отмены — тест дочерен per-call ctx
  gRPC, `timeout > 0` наслаивает дочерний deadline в мс.
- **HTTP-клиент на вызов**: `http.Transport{DialContext: detour.DialContext}`,
  `ForceAttemptHTTP2`, `TLSHandshakeTimeout = C.TCPTimeout`,
  `DisableKeepAlives` — одноразовый, соединение не кешируется, чтобы
  не держать сокет через узел после ответа.
- **Корни TLS на Android**: системный пул `crypto/x509` в mobile-процессе
  пуст — HTTPS обязан работать тем же механизмом certificate-store,
  которым уже пользуется `libbox.NewHTTPClient` (`C.IsAndroid`-ветка).
  Store создаётся на вызов и закрывается с ним либо переиспользуется
  инстансом — решение за PLAN, требование — «https://1.1.1.1/… работает
  на девайсе без кастомных корней в конфиге».
- **Чтение тела**: `io.LimitReader(maxBytes+1)`; прочитано больше лимита →
  усечь до лимита, `truncated = true`. Кламп: `0 → 256 KiB`,
  запрошенное выше потолка `1 MiB` — прижимается к потолку.
- **`remoteAddr`** — `httptrace.GotConn → conn.RemoteAddr()`.
- **История urltest НЕ трогается**: фетч — не замер (время включает тело,
  URL произвольный); писать его в `urlTestHistoryStorage` значило бы
  портить показания задержек в UI. Это осознанное отличие от донора.
- Регенерация proto — пиненым тулчейном, идемпотентно.

## 4. Безопасность

- **SSRF-поверхность.** Вызов позволяет управляющей стороне ходить по
  произвольным URL через туннели пользователя и читать ответ. Смягчение —
  контекстом, не кодом: форк client-focused, libbox линкуется в наше же
  приложение, вызов за `with_lx_command`, канал управления и так владеет
  ядром целиком (включая полную подмену конфига). GET-only и кламп тела —
  границы против превращения пробника в универсальный HTTP-клиент.
- **Тело не логируется.** Ответ идёт только вызывающему; в лог ядра
  попадает не более чем факт/ошибка обмена — тело может содержать
  что угодно.
- **Секретов в запросе нет** сверх того, что клиент сам положил
  в headers; канал локальный (unix socket / in-process) — новой
  поверхности утечки нет.

## 5. Границы

- Только GET; произвольные методы/тела запроса — вне скоупа навсегда
  для этого вызова (расширение = отдельный вызов и отдельное решение).
- Ядро не парсит тело — форматы внешних сервисов живут на стороне CC.
- Бинарные тела доезжают с потерями (`Content()` — UTF-8); инструмент —
  для текстовых эндпоинтов.
- Не заменяет проверку «через что я хожу сейчас» — состояние туннеля
  целиком проверяется обычным запросом с устройства.
- Проба — реальный трафик и пробуждение спящих WG-узлов: клиент вызывает
  её только по явному действию пользователя, без фоновых обходов.

## 6. Критерии приёмки

- [x] Резолв тега в **обоих** менеджерах: outbound и endpoint проверяются
  одним и тем же вызовом (`TestGetURLViaOutbound_ResolvesOutboundAndEndpoint_LX`).
- [x] Не-2xx → `error == ""`, статус/тело/Content-Type заполнены
  (`TestGetURLViaOutbound_NonOKIsResult_LX`).
- [x] Кламп `maxBytes`: 0 → дефолт; выше потолка → потолок; тело длиннее
  лимита → усечено ровно по лимиту, `truncated = true`
  (`TestGetURLViaOutbound_ClampsAndTruncates_LX`); тело ровно в лимит
  не помечается обрезанным (`…_ExactLimitIsNotTruncated_LX`).
- [x] Variant B: несуществующий тег / отказ dial / таймаут → ошибка
  в payload, `(resp, nil)` от handler'а (`…_VariantB_LX`); не-STARTED →
  transport-ошибка (`…_NotStarted_LX`).
- [x] Отмена: закрытие вызова обрывает висящий фетч на dial
  (`…_CancelAbortsFetch_LX`).
- [x] История urltest после вызова не изменилась
  (`…_LeavesURLTestHistoryIntact_LX`).
- [x] `Host` из заголовков перекладывается в поле запроса
  (`…_HostHeaderOverridesHost_LX`).
- [x] Stub-эквивалентность: без `with_lx_command` → `Unimplemented`
  (`TestGetURLViaOutboundStub_LX`); обе сборки (`daemon`+`libbox`,
  с тегом и без) компилируются.
- [x] Регенерация proto идемпотентна, non-SPEC-шум отревёрчен
  (чужие `pb.go` и сабмодуль `gvisor` возвращены в исходное состояние).
- [x] Полный бинарь с LX-тегами собирается, `check -c lx-test/config/minimal.json`
  зелёный; прогон пакета чистый под `-race`.
- [ ] Полевая проверка с девайса: `https://1.1.1.1/cdn-cgi/trace` через
  WG-endpoint и через vless-outbound возвращает тело с `ip=`/`warp=`;
  HTTPS работает на Android без кастомных корней.
