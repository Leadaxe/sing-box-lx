# SPEC: 147 — CACHEFILE_RELEASE_TAKEOVER

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) (условие снятия)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — апстримный `cachefile` держит `cache.db` до последнего шага `Scope.Close`; зависший Box того же процесса ломает следующий старт |
| Статус | I (implemented) — 2026-11-01: код и юниты в дереве (пять тестов, два из них красные без хуков); на устройстве не прогонялось |
| Ветка | `lx` |
| Build-tag | — (ядро `experimental/cachefile`, собирается всегда) |
| Источник | [issue #61](https://github.com/Leadaxe/sing-box-lx/issues/61): LxBox на Android, `Failed to start service: start or reload service: initialize cache-file: timeout`; пользователям помогали force-stop и «сброс DNS-кэша» (удаление файла) |
| Связано | [146](../146-MASQUE_CLOSE_WEDGE_DETOUR_DEADLINE/SPEC.md) (само зависание закрытия, найденное по дампу того же дня), [#50](https://github.com/Leadaxe/sing-box-lx/issues/50), [030](../030-FAST_BOX_SHUTDOWN/SPEC.md) (сторож закрытия), LxBox §703 (страховка приложения: удалить файл и повторить старт один раз) |

Второй слой к 146: что бы ни зависло в закрытии старого Box, новый Box того же процесса должен
стартовать, не теряя кеш.

## 1. Причина

- `cachefile.start()` открывает bbolt с `Timeout: 1s`, десять попыток; на `ErrTimeout` просто
  повторяет и после десятой возвращает `timeout`. Самолечение реагирует только на
  `ErrInvalid`/`ErrChecksum`/`ErrVersionMismatch`, блокировку не лечит.
- bbolt держит `flock` на собственном открытом файловом описании, поэтому второй `Open` того же
  файла **в том же процессе** конфликтует (проверено на пине; `bolt_android.go` форка — тот же
  `flock` с запасным `F_OFD_SETLK`, семантика та же).
- `cache-file` — первый внутренний сервис (`box.go`, `preStart`), а `Scope.Close` идёт в обратном
  порядке регистрации: его cleanup бежит после `service`, `inbound`, `endpoint`, `outbound`, …,
  `network`. Зависание в любом из них держит файл навсегда.
- LxBox работает в одном процессе: ограниченная остановка (§668: 2 с у force-stop, 20 с у штатной)
  бросает зависший Box, следующий Start создаёт новый `CommandServer`, и его Box ждёт файл 10 с.
  Порты inbound к этому моменту свободны, `cache.db` — единственный монопольный ресурс за зависанием.
- Снаружи приложение этого не чинит: дескриптор у Go, снятие `flock` через `/proc/self/fd`
  оставило бы два bbolt на одном файле (зависший продолжит писать, когда отвиснет: таймер
  метаданных FakeIP, rule-set, RDRC). Удаление файла работает, но теряет выдачи FakeIP, выбор в
  группах и кеш удалённых rule-set.

Провальный `Box.Start` файл не держит: `Start` при ошибке сам зовёт `Close`.

## 2. Фикс

Владение файлом остаётся внутри пакета `experimental/cachefile`; `box.go` не меняется.

### 2.1 `CacheFile.Release()`

Новый файл `release_lx.go`. Идемпотентный `Release()`: останавливает таймер отложенных
метаданных FakeIP и синхронно записывает их, если есть; под `dbAccess` ставит флаг `released`
и забирает указатель на базу; снимает регистрацию (§2.3); закрывает bbolt **вне** `dbAccess`
(`db.Close()` ждёт транзакцию в полёте, `view`/`batch`/`update` берут `dbAccess` только ради
указателя). Флаг ставится после записи метаданных, иначе запись упёрлась бы в собственный
`os.ErrClosed`; идемпотентность держит отдельный мьютекс.

После флага `view`/`batch`/`update` возвращают `os.ErrClosed` (отфильтровывается `Scope.Close`,
без шума в логе), `resetDB` ничего не делает — иначе восстановленная паника переоткрыла бы и
снова залочила файл. Сам bbolt после `Close` безопасен (транзакции — `ErrDatabaseNotOpen`,
второй `Close` — no-op); флаг защищает именно от `resetDB`.

### 2.2 Метаданные FakeIP переживают перехват

`FakeIPMetadata()` на старте не читает ключ, а читает и **удаляет** его; обратно его пишет
`Store.Close` fakeip-хранилища во время `scope.Close` старого Box — до него зависший Box не
доходит, а у перехваченного экземпляра запись уже вернёт `ErrClosed`. Без ответной меры сессия
без единой выдачи теряла бы указатель, и следующий старт делал бы `FakeIPReset` всем выдачам.

Поэтому `FakeIPMetadata()` кладёт прочитанное в отложенные `saveMetadata` (только если там
пусто, чтобы не затереть более свежее), а `FakeIPReset()` их обнуляет. `Release()` всегда
дописывает указатель обратно. Ответная запись `Store.Close` на штатном пути остаётся, значения
совпадают: после каждой выдачи хранилище шлёт полное текущее состояние.

### 2.3 Реестр по пути и перехват на старте

Процессный реестр открытых экземпляров по `c.path` (уже абсолютный после `filemanager.BasePath`).
`start()` перед циклом `bbolt.Open` зовёт `Release()` у всех чужих экземпляров с тем же путём
(Warn `cache-file: taking over <path> from a previous instance still holding it`), регистрирует
себя после успешного открытия. Cleanup в `Start` теперь `c.Release` вместо голого `db.Close()`:
штатное закрытие тоже снимает регистрацию и сбрасывает метаданные, реестр не копит мёртвые
экземпляры. Цикл ожидания остаётся как защита от чужого процесса.

Если два Box живут одновременно (VPN и проба LxBox по ошибке), старший просто теряет кеш:
записи — `ErrClosed`, чтения — пустые значения, как `Load*` и так ведут себя при ошибке; файл
он испортить не может. Это совпадает с правилом приложения «VPN важнее пробной сессии».

### 2.4 Что отвергнуто

`Release()` первым шагом `Box.Close` (пункт 2 issue): реестр закрывает внутрипроцессный случай
целиком, межпроцессный не лечит ни один из шагов, а ранний `Release` ломал бы ответную запись
метаданных fakeip-хранилища при каждом штатном стопе на всех платформах.

## 3. Условие снятия

Апстрим перестанет держать `cache.db` до конца `Scope.Close` (освободит файл раньше компонентов
или даст cachefile собственный перехват) **или** даст `bbolt.Open` путь без ожидания при
блокировке из того же процесса. При мерже: конфликт в `Start`/`start` cachefile = сверить §2.

## 4. Проверка

- `experimental/cachefile/release_lx_test.go`:
  - `TestTakeoverFromUnreleasedInstance` — A открыт и не закрыт; B на том же пути стартует без
    ожидания (< 900 мс против 10 с), `StoreSelected` у A — `os.ErrClosed`, B читает данные A,
    `checkDatabase` чистый. Без `takeOverPath` — красный через 9,8 с.
  - `TestReleaseFlushesPendingFakeIPMetadata` — отложенные метаданные в файле после `Release()`.
  - `TestFakeIPMetadataSurvivesSessionWithoutAllocations` — сессия без выдач → следующий
    экземпляр читает тот же указатель; после `FakeIPReset` — nil. Без засева — красный.
  - `TestReleaseIdempotentAndResetDBNoop` — двойной `Release()`, `ErrClosed` на трёх путях,
    `resetDB` после release не переоткрывает файл (свежий `bbolt.Open` на путь проходит).
  - `TestNormalCloseUnregisters` — `scope.Close()` снимает регистрацию.
- `go test -race ./experimental/cachefile/` — зелёный; пакет добавлен в lx-ci (шаг
  «wireguard endpoint, route, close watchdog, startclose stands»), раньше его тесты нигде не бежали.
- Устройство (LxBox): проба → стоп с закрытием дольше 3 с → сразу старт VPN → без
  `initialize cache-file: timeout`, выбор в группах сохранён. За владельцем; после lx.28
  страховка §703 в приложении должна перестать срабатывать.

## 5. Файлы

- `experimental/cachefile/release_lx.go` — реестр, `takeOverPath`, `Release`, засев метаданных (новый)
- `experimental/cachefile/cache.go` — поле `releaseState`, `scope.Add(c.Release)`, `takeOverPath`/`registerCacheFile` в `start`, флаг в `view`/`batch`/`update`/`resetDB` (`// lx: SPEC 147`)
- `experimental/cachefile/fakeip.go` — засев в `FakeIPMetadata`, сброс в `FakeIPReset` (`// lx: SPEC 147`)
- `experimental/cachefile/release_lx_test.go` — пять тестов (новый)
- `.github/workflows/lx-ci.yml` — пакет в списке race-прогона
