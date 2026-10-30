# IMPLEMENTATION REPORT: 143 — DNS_GROUP_HOSTS_OVERLAY

**Фича:** [013-DNS_GROUP](../../FEATURES/013-DNS_GROUP/FEATURE.md) · контракт — [SPEC.md](SPEC.md) · план — [PLAN.md](PLAN.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `option/dns.go` | внутри lx-блока `dns-group`: поле `Hosts badoption.Listable[string] \`json:"hosts,omitempty" reference:"dns_server"\`` после `Servers`; вне блока ничего не менялось |
| `dns/transport/group/group.go` | поля `Transport.hostsTags` (из конфига, неизменны) и `Transport.hosts []*hostsMember` (пишет `Start` под `access`). `NewTransport`: тег `hosts` не равен группе (`group cannot contain itself`), не входит в `servers` (`server <tag> is listed in both servers and hosts`), без дубликатов (`duplicate hosts server: <tag>`); зависимости адаптера — `servers` + `hosts`. `Start`: `hosts` в `servers` — `server type hosts is not allowed in servers, put it under "hosts": <tag>`, `fakeip` в `servers` — прежний текст `server type fakeip is not allowed in a group: <tag>`; в конце — `startHosts`. `Exchange`: после `PushGroup`, до `selectTarget` — `exchangeHosts`, попадание возвращается сразу. Абзац о слое в комментарии пакета |
| `dns/transport/group/hosts.go` (новый) | `hostsMember` — встроенный `member` + `adapter.DNSTransportWithPreferredDomain`. `startHosts`: тега нет в менеджере — `DNS server not found: <tag>`; тип не `hosts` — `hosts accepts only servers of type hosts, got <type>: <tag>`; тип `hosts` без `PreferredDomain` — `server of type hosts does not report the names it knows: <tag>`. `exchangeHosts` — слой (ниже) |
| `dns/transport/group/conntest_lx.go` | без изменений: `hostsMember` встраивает `member`, и `beginMemberLx`/`endMemberLx` получают `&h.member` — сигнатуры и поведение для сетевых участников прежние |
| `dns/transport/group/hosts_test.go` (новый) | фейк `fakeHosts` (тип `hosts`, `PreferredDomain` по каноническому имени, `Exchange` = `dns.FixedResponse` / NXDOMAIN как у `dns/transport/hosts`), `staleHosts` (знает имя, отвечает иначе), `newTestGroupWithHosts`; 14 тестов — список ниже |
| `dns/transport/group/group_test.go` | `newTestManager` регистрирует реальный `hosts.RegisterTransport` и фейк `fakefakeip` (`fakeFakeIP` с `Store()`: менеджер приводит тип `fakeip` к `adapter.FakeIPTransport`); `TestManagerRejectsLocalSourceMember` — подтесты `hosts` (текст с подсказкой) и `fakeip` (прежний текст) |
| `dns/transport/group/conntest_lx_test.go` | `TestHostsHitMemberStage_LX` |

## Слой `hosts`

1. Группа без ключа (`len(hostsTags) == 0`) — промах до любых действий: ни замка, ни аллокаций.
2. `Qtype` не A/AAAA — промах без обращения к участникам.
3. Снимок `t.hosts` берётся под `access`: `Exchange` может прийти до `Start` (случай из
   `selectTarget`), тогда снимок пуст и запрос получает прежнюю ошибку `not started`.
   Имя канонизируется `mDNS.CanonicalName`.
4. Участники по порядку ключа: `PreferredDomain(name)` false — следующий; true — `Exchange`
   участника вне замка через `timedExchange` (rtt); `err == nil`, ответ не nil и
   `Rcode == NOERROR` — попадание, иначе следующий.
5. **Попадание:** стадия `dns_member` (subject — тег участника, `type: hosts`, без `outbound`),
   закрыта OK; проба `answered` с тегом, типом `hosts` и rtt (`traceAttempt`);
   `SetEffectiveServer` (`recordEffective`); debug `group[<group>]: hosts hit via <member>`;
   ответ отдаётся как есть, включая пустой NOERROR для имени только с другим семейством.
6. **Промах:** ни пробы, ни стадии, ни записи здоровья; тот же `message` уходит в `selectTarget`
   и дальше по пути SPEC 033.

Участники `hosts` не входят в `members`, поэтому выбор цели, веер, выживание, `records`,
`current`, `GroupState()` и `Reset` их не видят — кода там нет.

**Стадия `dns_member` на попадании открывается после обмена.** lxtrace публикует стадию в момент
`Begin`, а промах — включая гонку «`PreferredDomain` true, `Exchange` → NXDOMAIN» — по §2.3.11
стадии не оставляет. Обмен `hosts` локальный и ничего не диалит: дочерних стадий у него нет,
длительность при разрешении в миллисекундах та же (0 мс). Так прочитано требование §2.3.10
«открывается и закрывается вокруг обмена».

## Тесты

`hosts_test.go`:

- `TestHostsHitAnswersLocally` — stable / fastest / parallel: A и AAAA (имя в другом регистре)
  из `hosts`, сетевые участники — 0 вызовов.
- `TestHostsOtherFamilyIsEmptyNoError` — запись только с IPv4, запрос AAAA: NOERROR без ответов,
  сеть — 0 вызовов.
- `TestHostsKnownNameOtherQtypeGoesToNetwork` — TXT / HTTPS / MX известного имени идут в сеть,
  `hosts` — 0 вызовов.
- `TestHostsFirstKnowingMemberAnswers` — имя знают оба участника — отвечает первый по ключу;
  знает только второй — отвечает второй, первый не опрашивается.
- `TestHostsPreferredButNotAnsweredIsMiss` — `PreferredDomain` true, `Exchange` вернул NXDOMAIN /
  SERVFAIL / ошибку: ответ из сети, в трассе одна проба сетевого участника.
- `TestHostsMissEqualsGroupWithoutHosts` — 3 режима × {все отвечают, все падают (первый запрос —
  веер: rescue / election / parallel, дальше — выживание)}, 4 промаха (имя, которое «устаревший»
  участник знает и отвечает NXDOMAIN; TXT известного имени; AAAA неизвестного; AAAA того же
  NXDOMAIN-имени). По каждому запросу число вызовов сетевых участников, наличие ошибки, rcode,
  `fanned`, `survival` равны группе без ключа; проб и `effective` от `hosts` нет.
- `TestHostsMembersHaveNoHealth` — 3 режима, серия попаданий и промахов (участник `hosts`
  с ошибкой обмена, сетевой участник падает посередине): в `records` нет тегов `hosts`,
  `GroupState().Members` = `[a b]`, `Current` — не тег `hosts`.
- `TestHostsTraceHit` — `effective` = `local` / `hosts` / без outbound, одна проба `answered`
  типа `hosts`, `GroupPath` = `[grp]`, `fanned` и `survival` сброшены (режим parallel).
- `TestHostsTraceMissEqualsGroupWithoutHosts` — снимок трассы промаха (RTT обнулён) равен
  снимку той же группы без ключа.
- `TestHostsNestedGroupLayer` — внутренняя группа со слоем как участник внешней: ответ из `hosts`
  внутренней, `effective` = тег `hosts`, сеть — 0 вызовов.
- `TestHostsRealTransportThroughManager` — реальный транспорт `hosts` через менеджер: `Path` —
  пустой temp-файл (иначе `hosts` читает системный файл), `Predefined` — `wg.home` (IPv4 + IPv6)
  и `v4.home` (IPv4). A / AAAA `wg.home` — из `hosts` с TTL `C.DefaultDNSTTL`; AAAA `v4.home` —
  пустой NOERROR; сеть — 0 вызовов; `example.org` A и `wg.home` TXT — в сеть;
  `Dependencies()` группы = `net` + `local`.
- `TestHostsConstructorValidation` — `hosts` без `servers` (существующая ошибка
  `servers is required`), дубликат, тег группы, тег в обоих списках; зависимости `[a local]`.
- `TestHostsEmptyListEqualsNoKey` — `"hosts": []` через
  `json.UnmarshalExtendedContext[option.DNSServerOptions]` с реестром (путь конфига, badjson):
  опции равны конфигу без ключа, `Dependencies()` = `servers`, `hostsTags` пуст.
- `TestManagerValidatesHostsMembers` — через менеджер: тег не найден; `fakeudp`, `fakeip`,
  `group` в `hosts`; тип `hosts` без `PreferredDomain`; реальный `hosts` принимается.

`group_test.go`: `TestManagerRejectsLocalSourceMember` (подтесты `hosts`, `fakeip`).

`conntest_lx_test.go` (`with_lx_command`): `TestHostsHitMemberStage_LX` — попадание: одна стадия
`dns_member` с subject `local`, OK, `type: hosts`, без `outbound`; промахи (NXDOMAIN
«устаревшего» участника, неизвестное имя, TXT) — одна стадия сетевого `a`, стадий `hosts` нет.

## Проверка

go1.26.10 darwin/arm64, `GOTOOLCHAIN=$(cat go.version)`.

```
$ gofmt -l dns/transport/group/ option/
(пусто)
$ go vet ./dns/transport/group/ ./option/                      — чисто
$ go vet -tags with_lx_command ./dns/transport/group/          — чисто
$ go test -race -count=1 -ldflags=-checklinkname=0 ./dns/transport/group/
ok  	github.com/sagernet/sing-box/dns/transport/group	6.444s
$ go test -race -count=1 -ldflags=-checklinkname=0 -tags with_lx_command ./dns/transport/group/
ok  	github.com/sagernet/sing-box/dns/transport/group	6.487s
$ go test -count=1 -ldflags=-checklinkname=0 ./option/
ok  	github.com/sagernet/sing-box/option	0.379s
$ go build ./...
exit 0 (в выводе только предупреждения линкера ld: ignoring duplicate libraries: '-lobjc')
$ go build -tags "$(make -f Makefile.lx lx-print-tags)" -ldflags=-checklinkname=0 ./cmd/sing-box
exit 0; этот бинарь: `check` конфига §3 — ok, группа с `hosts` без `servers` — прежняя ошибка
```

В пакете 57 тестов с `with_lx_command` (53 без тега), из них 15 новых. До реализации пакет
тестов не собирался (`unknown field Hosts in struct literal of type option.GroupDNSServerOptions`).
Контроль чувствительности: без проверки `Rcode == NOERROR` падают
`TestHostsPreferredButNotAnsweredIsMiss` (nxdomain, servfail),
`TestHostsMissEqualsGroupWithoutHosts` (5 из 6) и `TestHostsHitMemberStage_LX`; без гейта
`Qtype` — `TestHostsKnownNameOtherQtypeGoesToNetwork` и `TestHostsMissEqualsGroupWithoutHosts`
(6 из 6).

`sing-box check` (`go run ./cmd/sing-box check -c …`, без тегов):

```
конфиг §3 SPEC                                  → exit 0, без вывода
group с "hosts": ["local"] без servers          → exit 1
  FATAL[0000] initialize DNS server[1] group[resolver]: group[resolver]: servers is required and must not be empty
group с "servers": ["local", "google"]          → exit 0
```

`check` выполняет только `box.New`, а тип участников группа проверяет в `Start` — запрет `hosts`
в `servers` на `check` не виден (так же, как запрет `fakeip`). Он проверен запуском собранного
бинаря `sing-box run` на том же файле:

```
FATAL[0000] start service: start dns-transport: start dns/group[resolver]: group[resolver]: server type hosts is not allowed in servers, put it under "hosts": local
```

Конфиг §3 под `sing-box run` стартует (`sing-box started`, остановлен SIGTERM) — `startHosts`
принимает реальный `hosts` в полной сборке box.

## Не проверено

- Живой прогон на устройстве или роутере (п. 7 TASKS, статус D): `wg.home` с IPv4 + IPv6 в адресе
  endpoint'а через `default_domain_resolver` → группа, выбор семейства `strategy`.
- Кеш над группой (§2.1.5: ответ попадания под тегом группы с TTL `hosts`) юнитами не покрыт —
  код кеша не менялся.
- `GetDNSGroups` через daemon не прогонялся: снапшот не менялся, проверен `GroupState()`.
- Тесты вне `dns/transport/group` и `option`, матрица тегов `LX_TAGS`, `lx-ci` — не запускались.

## Документация

FEATURE 013 (параметры, data flow, правила, границы, таблица задач), SPEC 033 §2 (состав),
`docs-lx/lx-config.md` / `.ru.md` §5 (поле `hosts`, подраздел «Local names first» /
«Локальные имена первыми», пример с `wg.home`, якоря `033035143` в оглавлении, таблице фич
и корневых README), Roadmap `SPECS/README.md`, `SPECS/FEATURES/README.md`,
`docs-lx/lx-changelog.md` (`#### Unreleased`).
