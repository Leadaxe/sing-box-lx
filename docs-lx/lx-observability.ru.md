# sing-box-lx — наблюдаемость: расширения CommandClient

> EN: [lx-observability.md](lx-observability.md) · Фича: [OBSERVABILITY](../SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md)

Это дополнения клиентского API, а не конфига: методы на `CommandClient` libbox (нативный
gRPC-канал управления). Все они за тегом `with_lx_command`, их вызывает LxBox. В конфиг-файл
sing-box они ничего не добавляют: методы появляются в сборке с тегом, а вызывает их клиент. Без
тега методов нет, и демон обслуживает только апстримный набор команд. Разбор — в [фиче
OBSERVABILITY](../SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md).

Методы, которые форк добавляет в `CommandClient`:

- **`URLTestOutbound(tag, link, timeout)`** — синхронно измеряет задержку одного outbound
  или endpoint по требованию, вне периодического теста группы.
- **`GetRules()`** — отдаёт снимок таблицы маршрутных правил: route-правила и DNS-правила.
- **`GetGroups()`** — отдаёт снимок outbound-групп; те же данные пушит поток групп.
- **`GetOutbounds()`** — отдаёт плоский список outbound и endpoint. Он нужен рядом с
  `GetGroups`, потому что отдельно стоящие outbound не входят ни в одну группу. Элемент
  WG/AWG-endpoint'а ещё несёт `EndpointState` (`never_built` / `building` / `up` / `asleep`
  / `torn_down` / `down` / `disabled`) и `IdleSinceSeconds` — время с последнего дайла. У
  прочих outbound оба поля пусты или 0
  ([lx-energy.ru.md §11](lx-energy.ru.md#11-ленивая-сборка-и-бюджет-не-дают-собирать-устройства-впустую)).
  Состояние `disabled` выставляет `SetEndpointEnabled` (ниже;
  [lx-energy.ru.md §12](lx-energy.ru.md#12-ручной-переключатель-выключает-endpoint-без-правки-конфига)).
- **`GetPool(groupTag)`** — отдаёт текущий пул ротации round_robin группы `urltest`, слот
  за слотом ([lx-urltest.ru.md](lx-urltest.ru.md)). По каждому слоту ещё отдаёт
  `Quarantined`, `Connections` и `ConfirmedFailures`.
- **`GetPoolState(groupTag)`** — отдаёт те же слоты и состояние группы: `ShiftedKeys`,
  `MarkedKeys`, `Frozen` и `FrozenSeconds`
  ([самовосстановление](lx-urltest.ru.md#самовосстановление-пула)).
- **`GetDNSGroups()`** — отдаёт живое состояние каждого DNS-сервера `group`
  ([lx-dns-group.ru.md](lx-dns-group.ru.md)): по каждому участнику `clean` / `liveErrors` /
  `lastErrorAgeMs` / `liveWins` / `current`.
- **`GetRunningConfig()`** — отдаёт канонический JSON options, из которых построен
  работающий box, после override. Результат — объект с аксессором `Content()`: голый
  `string`-возврат ронял бы gomobile на android/arm64.
- **`GetEffectiveConfig()`** — отдаёт конфиг после lx-предобработки в той же каноничной форме, что
  `GetRunningConfig`: хопы цепочек (тип `chain-hop`) добавлены, `detour` узлов на позициях цепочек
  переписан, MTU выровнен, `lx.detour` записан развёрнутым
  ([SPEC 165](../SPECS/TASKS/165-CHAIN_DETOUR_REWRITE/SPEC.md)). Тот же объект с `Content()`.
- **`GetOutboundConfig(tag)`** — отдаёт один outbound, endpoint, группу или хоп цепочки JSON-объектом
  `{tag, original, effective, changes}`: `original` — как в `GetRunningConfig`, `effective` — как в
  `GetEffectiveConfig`. Каждое изменение — `{field, from, to, by}`: `field` — путь через точку
  (`tls.server_name`), `from` нет, если поля не было, `to` нет, если поле удалено, а `by` называет
  причину — `type` (`chain`, `chain_rewrite`, `lx.detour`, `mtu_align`) и, где применимо, `chain`, `position`,
  `node_type`, `mode`, `below`, `overhead`, `reason`. Изменения идут в порядке применения
  (`chain` → `chain_rewrite` → `mtu_align`); применённые к `original`, они дают `effective`. У хопа
  есть только `effective`. Неизвестный тег — ошибка (`NotFound` в gRPC).

  ```json
  {
    "tag": "NL-FREE#114",
    "original":  { "type": "wireguard", "tag": "NL-FREE#114", "detour": "o-ru" },
    "effective": { "type": "wireguard", "tag": "NL-FREE#114", "detour": "c-proton#0" },
    "changes": [
      { "field": "detour", "from": "o-ru", "to": "c-proton#0",
        "by": { "type": "chain", "chain": "c-proton", "position": 1 } }
    ]
  }
  ```
- **`SubscribeDNSQueries(includeAnswers, handler)`** — подписывает на структурный живой
  поток DNS-запросов. По каждому запросу приходят `domain`, `qtype`, `rcode`, CNAME-цепочка
  и ответы (при `includeAnswers`), привязка к процессу, `dnsServer` / `dnsServerType` /
  `outbound`. `rcode` `-1` означает ошибку резолва — это полноправное состояние. Пустой
  `outbound` означает direct/system — тоже валидное состояние, не баг.
- **`GetChainStatus(tag)`** — отдаёт состояние одного outbound `chain`
  ([lx-chain.ru.md](lx-chain.ru.md)): по позициям тег, группа ли это, узел, в который позиция
  разрешается сейчас, `transparent` (`direct` на позиции ≥ 1, дозвон идёт в хоп ниже), `disabled`
  и ошибки дозвона; плюс счётчики дозвонов и ошибок цепочки.
- **`SetChainPositionEnabled(chain, position, enabled)`** — выключает или включает одну позицию
  цепочки на ходу; возвращает только ошибку.
- **`SetEndpointEnabled(tag, enabled)`** — выключает или включает WG/AWG-endpoint на ходу
  ([lx-energy.ru.md §12](lx-energy.ru.md#12-ручной-переключатель-выключает-endpoint-без-правки-конфига)). Возвращает
  объект `EndpointToggleResult`; его поле `State` — состояние endpoint'а после вызова.

Поток соединений тоже расширен: отслеживаемое `Connection` несёт отдельное поле **`detourList`**
— хвост transport-detour'а финального outbound. Он отдаётся отдельно от прокси-`chain`, потому
что `chain` не включает detour.

Методы появляются в сборке с тегом:

```sh
make -f Makefile.lx lx-build   # включает with_lx_command (и with_xhttp/with_awg)
```

Из командной строки пул группы показывает `sing-box api group pool`, он работает через тот же
канал:

```sh
sing-box api group pool auto     # слоты, состояние, соединения и отказы пула round_robin группы auto
```
