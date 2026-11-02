# SPEC: 149 — TAILSCALE_LOGIN_TOGGLE

**Фича:** [006-OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — вход Tailscale-узла с новым ключом и его выключение/включение без перезапуска ядра; выключатель Taildrop узла и отправитель входящего файла |
| Статус | D (done, device-verified) — пререлиз `v1.14.4-lx.30-rc.1` 2026-11-02; LxBox на arm64 AVD (Android 14) против login.tailscale.com: Logout → `TailscaleLogin` → Running ≈2 с без reload; узел с отвергнутым ключом → `TailscaleLogin` с действующим → Running ≈2 с; выключение → `disabled`/Stopped, включение → `up`/Running. §4 (Taildrop, 2026-11-02) — I (implemented), юниты; на устройстве не проверен |
| Ветка | `lx` |
| Build-tag | ядро — без тега (методы без вызова ведут себя как апстрим); RPC — `with_lx_command` |
| Связано | [106](../106-WG_ENDPOINT_TOGGLE/SPEC.md) (переключатель WG/AWG, тот же RPC), [115](../115-TAILSCALE_PEER_PATH_STATUS/SPEC.md) (статус Tailscale-узла) |

## 1. Назначение

Запрос LxBox 2026-11-02, решение владельца 2026-11-02. Узел на телефоне владельца выпал из tailnet: control отверг ключ («invalid key: API key does not exist»), узел висит в `NeedsLogin`. Сменить ключ сейчас можно только записью `auth_key` в конфиг и перезапуском сервиса, а это рвёт весь трафик. Нужны две ручки:

1. **Вход** с переданным auth key, без перезапуска box. Пустой ключ = интерактивный вход (AuthURL в статусе).
2. **Выключение/включение** узла, как у WG/AWG в SPEC 106: узел уходит из tailnet с сохранением личности, дайлы через него отвергаются, остальной box работает. Выключение и включение подряд = «перезапустить один залипший узел».

Второй запрос LxBox, решение владельца 2026-11-02 — §4:

3. **Выключатель Taildrop** узла. Сейчас узел принимает файлы от своих устройств всегда, если file sharing включён в админке; клиент не может отказаться от файла, только удалить его после приёма.
4. **Отправитель входящего файла.** Разрешения на служебные файлы у LxBox и лаунчера привязаны к устройству-отправителю, а имя пира можно сменить; нужен stable node ID.

## 2. Выключение и включение

Переиспользует `adapter.EndpointToggle` и RPC `SetEndpointEnabled` из SPEC 106; новых вызовов нет.

| Состояние до | `SetEnabled(false)` | `SetEnabled(true)` |
|---|---|---|
| запущен, включён | флаг `disabled`, затем `EditPrefs(WantRunning=false)` | no-op |
| запущен, выключен | no-op | `EditPrefs(WantRunning=true)`, затем флаг снят |
| не запущен / закрывается | `os.ErrClosed` | `os.ErrClosed` |

- `WantRunning=false` переводит бэкенд в `Stopped`: соединение с control и пиры magicsock закрыты, в админке узел offline. Ключи и state в `state_directory` не трогаются. Включение — обычное переподключение без повторной авторизации, пока ключ узла не истёк.
- Флаг ставится **до** `EditPrefs` при выключении и снимается **после** при включении: на всём переходе дайлы получают отказ. Ошибка `EditPrefs` возвращается; флаг остаётся выключенным в обоих случаях (узел не работает — и дайлы не ждут).
- Дайл по выключенному узлу (`DialContext`, `ListenPacket`) сразу получает `Tailscale endpoint is disabled`. Без этой проверки дайл ушёл бы в gVisor без пиров и ждал бы до `C.TCPTimeout` (TCP) или молча терялся (UDP). Проверка стоит до резолва домена: выключенный узел не делает DNS.
- DNS-транспорт `tailscale`: запросы к резолверам tailnet идут дайлом через endpoint и отвергаются той же проверкой. Ответы MagicDNS из последней карты сети отдаются как прежде, дайл на полученный адрес отвергается.
- Установленные потоки не рвутся: после `Stopped` они стоят и либо оживают после включения (те же адреса, ретрансмиты TCP), либо умирают по своим таймаутам. Входящее (пиры к нам, exit node, приём Taildrop) при `Stopped` не работает.
- `IdleState()`: `disabled` у выключенного, `up` у запущенного включённого, `down` у незапущенного. Реальное состояние бэкенда (`NeedsLogin`, `Running`, `Stopped`) — в `GetTailscaleStatus` (SPEC 115).
- **Не сохраняется.** `EditPrefs` пишет `WantRunning=false` в state, но tsnet при каждом старте передаёт `UpdatePrefs` с `WantRunning=true`, и сохранённое перекрывается; флаг отказа живёт только в памяти. Reload и перезапуск сервиса поднимают узел включённым, как в SPEC 106.
- Для групп выключенный узел = мёртвый узел, как в SPEC 106 §5.

## 3. Вход

`Login(ctx, authKey)` повторяет путь `tsnet.Server.start`, не пересоздавая `tsnet.Server`:

1. `localBackend.Start(ipn.Options{UpdatePrefs: prefs, AuthKey: authKey})` с тем же набором prefs, что собирают tsnet при старте и апстримный `Logout` (`Hostname`, `WantRunning=true`, `ControlURL`, `AdvertiseTags`), затем `editPrefs` endpoint'а (маршруты, exit node, SSH, relay).
2. Если после этого бэкенд в `NeedsLogin` — `StartLoginInteractive`. С ключом control регистрирует узел по ключу; без ключа появляется AuthURL, как при старте без `auth_key`.

| Условие | Результат |
|---|---|
| бэкенд `Running` или `Starting` | отказ `Tailscale is already logged in, log out first`; ключ не применяется |
| узел выключен (§2) | отказ `Tailscale endpoint is disabled` |
| endpoint не запущен | `os.ErrClosed` |
| иначе (`NeedsLogin`, `NeedsMachineAuth`, `NoState`) | вход по §3.1–2 |

- Отказ для залогиненного узла — потому что tsnet применяет ключ только вне `Running` («Ignoring authkey»): молчаливый Logout внутри стёр бы личность узла без явного действия. Смена ключа у живого узла = `TailscaleLogout`, затем `TailscaleLogin`.
- Ошибка регистрации (неверный или истёкший ключ) приходит асинхронно, как при старте: в состоянии бэкенда и `health` статуса. Вызов возвращает ошибку только если не удалось запустить бэкенд.
- После входа личность живёт в `state_directory`; при следующем старте `auth_key` из конфига игнорируется, пока узел залогинен. Клиенту стоит записать новый ключ и в конфиг — на случай потери state.
- Ключ не пишется в лог и не возвращается ни в каком ответе. `KeyAuth` статуса (SPEC 115) по-прежнему отражает только конфиг.
- Апстримный `Logout` выключенного узла отвергается той же ошибкой: он перезапускает бэкенд с `WantRunning=true` и включил бы узел в обход флага.

## 4. Taildrop

### 4.1 `disable_taildrop`

```json
{ "type": "tailscale", "tag": "ts", "disable_taildrop": true }
```

Поле отсутствует или `false` — как апстрим. `true`:

| Путь | Результат |
|---|---|
| peer API `/v0/put/` (PUT файла и GET докачки) | 403 `Taildrop is disabled on this node`; у отправителя — ошибка с этим текстом |
| `SendTaildropFiles` с этого узла | `FailedPrecondition` `Taildrop is disabled on this node`, до проверки состояния узла |
| статус узла (`GetTailscaleStatus`, поток статуса) | `canShareFiles = false`, у всех пиров `canReceiveFiles = false` |
| файлы, уже лежащие в `taildrop_directory` | остаются во входящих; открытие и удаление работают |

- Проверка стоит перед апстримными проверками прав: при выключенном Taildrop узел не смотрит ни права пира, ни capability админки.
- Другие устройства tailnet по-прежнему видят узел получателем: это решает control (`CapabilityFileSharing`, `PeerCapabilityFileSharingTarget`), а не узел. Отказ отправитель узнаёт ответом 403.
- Ключ конфига: меняется reload'ом, переключения на лету нет.

### 4.2 `senderID` у полученного файла

`TaildropFile` (адаптер, proto, libbox) получает `senderID` — stable node ID отправителя, тот же, что у `TaildropReceivingFile.senderID` во время приёма.

```proto
message TaildropFile {
  ...
  string senderID = 5; // empty for files received before the core restarted
}
```

- Хранится в памяти рядом с `senderName` и с ним же чистится (удаление файла, файл пропал с диска). После перезапуска ядра у файлов, лежавших на диске, пустые оба поля.
- libbox: `TaildropFile.SenderID` (`getSenderID()` в javap).

## 5. RPC

```proto
rpc TailscaleLogin(TailscaleLoginRequest) returns (google.protobuf.Empty) {}

message TailscaleLoginRequest {
  string endpointTag = 1;
  string authKey = 2; // empty: interactive login, AuthURL in the status
}
```

| Код | Когда |
|---|---|
| `FailedPrecondition` | сервис не в `STARTED`; endpoint не запущен; узел выключен; узел уже залогинен |
| `NotFound` | нет endpoint'а с таким тегом |
| `InvalidArgument` | endpoint не Tailscale |
| `Unavailable` | бэкенд не запустился или не начал вход |
| `Unimplemented` | сборка без `with_lx_command` |

`SetEndpointEnabled` с тегом Tailscale-узла работает без изменений в обработчике; в ответе `state` по §2. `GetOutbounds` отдаёт тот же `endpointState` у Tailscale-узлов (раньше поле было пустым).

libbox для LxBox: `CommandClient.TailscaleLogin(endpointTag, authKey string) error` → в javap `public native void tailscaleLogin(java.lang.String, java.lang.String) throws java.lang.Exception`. Выключение — существующий `setEndpointEnabled`.

## 6. Карта реализации

| Файл | Что |
|---|---|
| `adapter/tailscale_login_lx.go` | `TailscaleLoginProvider`, ошибки `ErrTailscaleDisabled`, `ErrTailscaleLoggedIn` |
| `protocol/tailscale/toggle_lx.go` | `SetEnabled`, `Enabled`, `IdleState`, `TailscaleLogin`, `checkEnabled` |
| `protocol/tailscale/endpoint.go` | `// lx:` поля `disabled`, `toggleAccess`; проверка в `DialContext` и `ListenPacketWithDestination`; проверка в `Logout` |
| `daemon/started_service.proto` + регенерация `*.pb.go` | RPC и сообщение в блоке `lx_command` |
| `daemon/started_service_tailscale_lx.go` / `_stub.go` | обработчик `TailscaleLogin` и близнец `Unimplemented` |
| `experimental/libbox/command_client_tailscale_lx.go` | `CommandClient.TailscaleLogin` |
| `option/tailscale.go` | `// lx:` поле `DisableTaildrop` |
| `protocol/tailscale/endpoint.go` | `// lx:` поле `taildropDisabled` из опции |
| `protocol/tailscale/taildrop.go` | `// lx:` отказ 403 в `handleTaildropRequest`; карта `senderIDs` рядом с `senderNames` |
| `protocol/tailscale/taildrop_send.go` | `// lx:` отказ в `SendTaildropFile`, `false` в `taildropTargets` |
| `adapter/tailscale.go`, `adapter/tailscale_login_lx.go` | `// lx:` поле `TaildropFile.SenderID`; ошибка `ErrTaildropDisabled` |
| `daemon/started_service.proto`, `started_service_taildrop.go` | `senderID = 5` в блоке `lx_command`; `ErrTaildropDisabled` → `FailedPrecondition`, перенос `SenderID` |
| `experimental/libbox/command_types_taildrop.go` | `// lx:` поле `TaildropFile.SenderID` |

## 7. Границы

- CLI-команды нет: управление — из LxBox и через `lxd` (тот же обработчик).
- Ядро не следит за ключом и не меняет его само; когда звать вход — решает клиент по `GetTailscaleStatus`.
- Режим `system_interface` (системный TUN Tailscale): выключение опускает бэкенд так же; трафик ОС в интерфейс без пиров никуда не уходит.

## 8. За чем следить на мерже

- `protocol/tailscale/endpoint.go`: проверки в `DialContext` (сразу после `started`) и в начале `ListenPacketWithDestination`; `Logout` — порядок `Start` → `editPrefs` → `StartLoginInteractive`, его повторяет `TailscaleLogin`.
- `tsnet.Server.start` в `sagernet/tailscale`: состав prefs и `UpdatePrefs` с `WantRunning=true` при старте — на нём держится «не сохраняется» §2.
- `daemon/started_service.proto`: блок `lx_command` переносится руками, `*.pb.go` — только регенерацией.
- `TaildropFile` в proto — апстримное сообщение: если апстрим займёт номер 5, наш `senderID` уходит на следующий свободный, клиенты пересобираются.
- `protocol/tailscale/taildrop.go`: каждое изменение `senderNames` сопровождается тем же для `senderIDs`; новая апстримная ветка, чистящая `senderNames`, без пары оставит лишнюю запись в памяти.

## 9. Тесты

- `protocol/tailscale/toggle_lx_test.go`: отказ дайла и listen у выключенного до резолва домена; `IdleState` по трём состояниям; `SetEnabled` и `TailscaleLogin` у незапущенного → `os.ErrClosed`; вход у выключенного → `ErrTailscaleDisabled`; `Logout` выключенного отвергается.
- `daemon/started_service_tailscale_lx_test.go`: коды `TailscaleLogin` — `FailedPrecondition` (сервис не запущен, `ErrTailscaleDisabled`, `ErrTailscaleLoggedIn`, `os.ErrClosed`), `NotFound`, `InvalidArgument`, `Unavailable`; ключ доходит до endpoint'а; `_stub_lx_test.go`: `Unimplemented`.
- `experimental/libbox`: запрос через подменённый `StartedServiceClient`, gRPC-код доходит до вызывающего.
- `protocol/tailscale/taildrop_lx_test.go`: 403 на PUT и GET peer API при `disable_taildrop`; отказ `SendTaildropFile` и `canShareFiles = false` у запущенного и незапущенного; `SenderID` во входящих, очистка при удалении, пустой после перезапуска менеджера.
- `daemon/started_service_taildrop_lx_test.go`: `ErrTaildropDisabled` → `FailedPrecondition`; `SenderID` в proto.
- Вход по ключу и `WantRunning` против живого control юнитами не покрываются: нужен tailnet или Headscale. Taildrop между двумя узлами — тоже.
