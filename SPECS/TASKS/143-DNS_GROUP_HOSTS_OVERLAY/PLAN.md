# PLAN: 143 — DNS_GROUP_HOSTS_OVERLAY

**Фича:** [013-DNS_GROUP](../../FEATURES/013-DNS_GROUP/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Файлы

| Файл | Что меняется |
|------|--------------|
| `option/dns.go` | внутри lx-блока `dns-group`: поле `Hosts badoption.Listable[string] \`json:"hosts,omitempty" reference:"dns_server"\`` после `Servers` |
| `dns/transport/group/group.go` | `Transport`: `hostsTags []string`, `hosts []*hostsMember`. `NewTransport`: валидация `hosts` (дубликаты, самовключение, пересечение с `servers`), зависимости адаптера = `servers` + `hosts`. `Start`: резолв тегов `hosts`, проверка типа `C.DNSTypeHosts` и наличия `DNSTransportWithPreferredDomain`; текст ошибки для `hosts` в `servers` — `server type hosts is not allowed in servers, put it under "hosts": <tag>` (для `fakeip` — прежний). `Exchange`: после `PushGroup`, до `selectTarget` — `if response, ok := t.exchangeHosts(ctx, message); ok { return response, nil }` |
| `dns/transport/group/hosts.go` (новый) | `hostsMember{tag, transport adapter.DNSTransport, preferred adapter.DNSTransportWithPreferredDomain}`; `exchangeHosts(ctx, message) (*mDNS.Msg, bool)`: гейт `Qtype` A/AAAA, канонизация имени, цикл по `t.hosts`: `PreferredDomain` → стадия SPEC 139 (`beginMemberLx`/`endMemberLx` принимают `*member` — дать им общий минимальный ввод: тег, тип, outbound) → `Exchange` → при `err == nil && Rcode == NOERROR`: `SetEffectiveServer`, `RecordAttempt{answered, rtt}`, debug-лог, `return response, true`; иначе следующий |
| `dns/transport/group/conntest_lx.go` | без изменений: `hostsMember` встраивает `member`, `beginMemberLx`/`endMemberLx` получают `&h.member` |
| `dns/transport/group/hosts_test.go` (новый) | тесты SPEC §5: фейк `hosts` (`C.DNSTypeHosts`, `PreferredDomain` по карте имя→адреса, `Exchange` = `dns.FixedResponse` / NXDOMAIN как у реального) + один тест через менеджер с реальным `hosts.RegisterTransport` и `option.HostsDNSServerOptions{Predefined: …}`; хелпер `newTestGroupWithHosts` рядом с `newTestGroup` |
| `dns/transport/group/group_test.go` | `TestManagerRejectsLocalSourceMember`: новый текст ошибки для `hosts`, отдельный кейс `fakeip` |
| `dns/transport/group/conntest_lx_test.go` | стадия `dns_member` на попадании `hosts`; её отсутствие на промахе |
| `SPECS/FEATURES/013-DNS_GROUP/FEATURE.md` | таблица параметров: строка `hosts`; Data flow: стадия «слой hosts» перед выбором цели; «Правила и гарантии»: состав (`hosts` только через ключ), здоровье слоя; «Границы»: порядок `hosts` значим, семейства без фолбэка; таблица задач: строка 143 |
| `SPECS/TASKS/033-DNS_GROUP_SERVER/SPEC.md` | §2 «отказ `fakeip`/`hosts`» → «в `servers`; `hosts` — через ключ `hosts`, SPEC 143» |
| `docs-lx/lx-config.md`, `docs-lx/lx-config.ru.md` | §5: поле `hosts` в блоке полей, абзац семантики слоя (попадание/промах/пустой NOERROR/без здоровья), пример §3 SPEC; строка таблицы фич — упоминание SPEC 143 |
| `SPECS/README.md` (Roadmap), `SPECS/FEATURES/README.md` (строка 013: `033 · 035 · 143`), `docs-lx/lx-changelog.md` (`#### Unreleased` над последним тегом) | индексы |
| `SPECS/TASKS/143-DNS_GROUP_HOSTS_OVERLAY/IMPLEMENTATION_REPORT.md` | что сделано, прогоны, статус I в шапке SPEC и Roadmap |

## Решения

- **Попадание по `PreferredDomain`, а не по rcode.** Это апстримный интерфейс «локальный
  источник знает имя» (им живёт rule-item `preferred_by`), а rcode-контракт `hosts`
  (NXDOMAIN = не знаю) — деталь реализации чужого файла. Проверка `Rcode == NOERROR` после
  обмена остаётся страховкой от гонки с перечитыванием файла.
- **Слой до `selectTarget` и вне `access`.** `t.hosts` неизменен после `Start`, обмен
  с `hosts` локальный и не блокирует; под мьютекс группы его не тащить (правило PLAN 033:
  `Exchange` участника под замком не звать).
- **Зависимости адаптера включают `hosts`**: иначе менеджер может стартовать группу раньше
  `hosts`-транспорта; цикл с `hosts` невозможен, но проверка единая.
- **Проба `answered` на попадании.** Трасса SPEC 035 — хронология проб; без неё событие
  «ответил hosts» видно только по `effective`. На промахе — ничего: промах не проба.
- **Стадия SPEC 139 на попадании** — иначе в тесте соединения источник адреса пропадает.
- **Сообщение в сеть то же.** `hosts.Exchange` не мутирует запрос; копия не нужна.

## Порядок

1. Тесты `hosts_test.go` + правки `group_test.go` — падают до реализации.
2. `option/dns.go`, `group.go`, `hosts.go`, `conntest_lx.go` — тесты пакета зелёные.
3. Доки и индексы.
4. IMPLEMENTATION_REPORT.md, статус I.

## Проверка

```
GOTOOLCHAIN=$(cat go.version) gofmt -l dns/transport/group/ option/
GOTOOLCHAIN=$(cat go.version) go vet ./dns/transport/group/ ./option/
GOTOOLCHAIN=$(cat go.version) go test -race -count=1 -ldflags=-checklinkname=0 ./dns/transport/group/ | tail -3
GOTOOLCHAIN=$(cat go.version) go build ./...
GOTOOLCHAIN=$(cat go.version) go run ./cmd/sing-box check -c <конфиг §3 SPEC>
```

`lx-ci.yml` гоняет `./dns/transport/group/` в джобе SPEC 139 (`-race`) — новые тесты
попадут в CI без правки воркфлоу.

## Грабли

- `badoption.Listable` + badjson: `"hosts": []` приходит как nil — тест «пустой список =
  нет ключа» через `badjson`-парсинг, не через литерал структуры.
- `fakeMember` тестов имеет тип `C.DNSTypeUDP`; для слоя нужен фейк с типом `hosts`
  **и** интерфейсом `PreferredDomain` — без него `Start` обязан отвергать тег.
- `-race` обязателен: веер промаха пишет записи из горутин, слой — нет; тест «промах =
  группа без hosts» сравнивает счётчики вызовов, а не тайминги.
- Реальный `hosts` без `path` читает системный файл (`NewDefault`); в тесте через менеджер
  давать `Path: [<пустой temp-файл>]` + `Predefined`, чтобы не зависеть от `/etc/hosts`.
