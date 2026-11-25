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
  за слотом ([§3](lx-config.ru.md#3-режимы-выбора-узла-в-urltest)). По каждому слоту ещё отдаёт
  `Quarantined`, `Connections` и `ConfirmedFailures`.
- **`GetPoolState(groupTag)`** — отдаёт те же слоты и состояние группы: `ShiftedKeys`,
  `MarkedKeys`, `Frozen` и `FrozenSeconds`
  ([самовосстановление](lx-urltest.ru.md#самовосстановление-пула)).
- **`GetDNSGroups()`** — отдаёт живое состояние каждого DNS-сервера `group`
  ([§5](lx-config.ru.md#5-группа-dns-серверов)): по каждому участнику `clean` / `liveErrors` /
  `lastErrorAgeMs` / `liveWins` / `current`.
- **`GetRunningConfig()`** — отдаёт канонический JSON options, из которых построен
  работающий box, после override. Результат — объект с аксессором `Content()`: голый
  `string`-возврат ронял бы gomobile на android/arm64.
- **`SubscribeDNSQueries(includeAnswers, handler)`** — подписывает на структурный живой
  поток DNS-запросов. По каждому запросу приходят `domain`, `qtype`, `rcode`, CNAME-цепочка
  и ответы (при `includeAnswers`), привязка к процессу, `dnsServer` / `dnsServerType` /
  `outbound`. `rcode` `-1` означает ошибку резолва — это полноправное состояние. Пустой
  `outbound` означает direct/system — тоже валидное состояние, не баг.
- **`GetChains()`** — отдаёт состояние каждого outbound `chain`
  ([§10](lx-config.ru.md#10-outbound-chain--виртуальная-цепочка-хопов-из-групп-и-узлов)): по позициям
  разрешённый узел, а для позиций ≥ 1 — звено (`starting|active|idle`, живые соединения,
  эффективный MTU и причина, что снял `strip`, применён ли `rewrite`, последняя ошибка).
  Метод отдаёт и счётчики дозвонов, ошибок и звеньев.
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
