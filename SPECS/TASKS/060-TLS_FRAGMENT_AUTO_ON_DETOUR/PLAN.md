# PLAN — SPEC 060 `lx.sniff_guard`

**Фича:** [019-SNIFF_GUARD](../../FEATURES/019-SNIFF_GUARD/FEATURE.md) · спека — [SPEC.md](SPEC.md) · шаги — [TASKS.md](TASKS.md)

Как SPEC ложится на код: пакеты, контракты между ними, порядок волн. Поведение описано в SPEC и
FEATURE; здесь только то, что нужно, чтобы несколько исполнителей не разошлись в типах.

## 1. Раскладка по пакетам

| Что | Где | Род |
|---|---|---|
| Конфиг `lx.sniff_guard`: разбор, проверка, канонизация, resolved-типы | `option/lx_sniff_guard.go` (+ тест), одно поле в `LXOptions` и одно в `LXResolved` в `option/lx.go` | новый файл + вставка `// lx:` |
| Хранилище меток, ожидающие пары, личный отказ, окна, решение, отметка рукопожатия | новый пакет `common/lxsniffguard` | новый пакет |
| Факты об узлах и носителях из конфига (совпадение SNI, поддержка, отпечаток, шифрованность) | `box_lx.go` + `common/lxsniffguard` | lx-файл |
| Обёртка outbound'а: повтор дайла | `adapter/outbound` (lx-файл + одна строка в `registry.go`) | новый файл + вставка |
| TLS-хук: форма ClientHello по решению | `common/tls` (lx-файл + вставка в `ClientHandshake`) | новый файл + вставка |
| Писатель метода `tls.delay` | `common/tls` или `common/lxsniffguard` | новый файл |
| QUIC-швы | существующие: `common/lxdatagram`, `protocol/masque`, `protocol/trusttunnel`, `transport/v2rayxhttp`, `transport/wireguard` | правка lx-кода |
| `persist` | бакет в `experimental/cachefile` | вставка |

Зависимости: `common/lxsniffguard` импортирует `option`, `adapter`, `log`; **не** импортирует
`common/tls` и `common/lxdatagram` (оба импортируют его). Признак «сокет ядра» вызывающий
передаёт в `Path` готовым.

## 2. Контракт `option` (фиксирован, волна 1 делает его буква в букву)

```go
// option/lx_sniff_guard.go
const (
    LXSniffGuardNever   = "never"
    LXSniffGuardClean   = "clean"
    LXSniffGuardSuspect = "suspect"

    LXSniffGuardTLSRecordFragment = "record_fragment"
    LXSniffGuardTLSFragment       = "fragment"
    LXSniffGuardQUICDecoy         = "decoy_first_byte"
    LXSniffGuardDelay             = "delay" // общий для tls и quic
    LXSniffGuardOff               = "off"

    LXSniffGuardDirect   = "direct" // ключ карты для прямого пути
    LXSniffGuardWildcard = "*"
)

// LXSniffGuardResolved — lx.sniff_guard с применёнными дефолтами.
// Нулевое значение = блока нет = фича выключена.
type LXSniffGuardResolved struct {
    SNIMismatchOnly bool     // nodes.sni_mismatch: true
    OutboundTypes   []string // nil = фильтра нет
    Override        bool
    TLS             LXSniffGuardSectionResolved
    QUIC            LXSniffGuardSectionResolved
}

type LXSniffGuardSectionResolved struct {
    Method  string        // "" = off
    Delay   time.Duration // 300ms, если ключа нет
    Persist bool
    // DetourTypes — итоговая карта: дефолт (direct, ssh, socks = clean; "*" = suspect),
    // поверх общая карта блока, поверх карта секции.
    DetourTypes map[string]string
}

func (r LXSniffGuardResolved) Enabled() bool                    // хотя бы у одной секции есть метод
func (r LXSniffGuardResolved) CoversType(outboundType string) bool // фильтр outbound_type
func (s LXSniffGuardSectionResolved) Enabled() bool
func (s LXSniffGuardSectionResolved) StartState(carrierType string) string // never|clean|suspect; "direct" — прямой путь
```

`LXResolved` получает поле `SniffGuard LXSniffGuardResolved`, `LXOptions` — поле
`SniffGuard *LXSniffGuardOptions` с тегом `sniff_guard`. Старый `QUICSniffDecoy` в волне 1 не
трогается: его снимает волна 3, оставляя сторожа удалённого ключа по образцу
`LXRemovedAWGMasquerade` с подсказкой на `lx.sniff_guard.quic`.

Проверки, которым нужны реестры сборки или соседние секции (неизвестный тип в карте,
`persist` без `cache_file`), — не в `option`, а в `box_lx.go` (волна 2), на месте
`checkSniffDecoyDetourTypes`.

## 3. Контракт `common/lxsniffguard` (имена фиксированы, внутренности — за волной 1)

```go
type Proto uint8 // ProtoTLS, ProtoQUIC

// Path — куда ушёл дайл. Direct — под conn сокет ядра. Иначе носитель по записи
// adapter.CarrierRecord; пустой CarrierTag при !Direct = путь неизвестен (без защиты, без метки).
type Path struct {
    Direct      bool
    CarrierType string
    CarrierTag  string
}

// NodeInfo и CarrierInfo считаются один раз на загрузке из типизированных опций.
type NodeInfo struct {
    Type        string
    SNIMismatch bool // право двигать метку; иначе — только личный отказ
}
type CarrierInfo struct {
    Type        string
    Fingerprint string // тип|адрес|порт — для persist
    Encrypted   bool   // false: socks, http без TLS — сброс на смене сети, в кеш не пишется
}

type Config struct {
    Policy   option.LXSniffGuardResolved
    Nodes    map[string]NodeInfo    // только узлы, прошедшие nodes; остальных в карте нет
    Carriers map[string]CarrierInfo // по тегу
    Cache    Cache                  // nil = persist выключен
    Logger   logger.Logger
    Now      func() time.Time       // для тестов
}

type Cache interface {
    LoadSniffGuard() map[string]CacheEntry        // ключ "тег|proto"
    StoreSniffGuard(key string, entry CacheEntry) error
}
type CacheEntry struct{ Fingerprint, State string }

func New(config Config) *Guard          // nil, если Policy не Enabled
func FromContext(ctx context.Context) *Guard // service.FromContext; nil-safe во всех методах
func (g *Guard) Node(tag string) *Node  // nil для узла вне nodes
func (g *Guard) NetworkChanged()        // сброс локальных ключей, пар и отказов на них

// Dial — повтор вокруг одного дайла узла. do вызывается один или два раза с ctx, несущим
// Attempt; второй раз — с противоположным решением, если SPEC это разрешает.
func (n *Node) Dial(ctx context.Context, do func(ctx context.Context) error) error

// Сторона рукопожатия.
func AttemptFromContext(ctx context.Context) *Attempt // nil = хук не вмешивается
type Decision struct {
    Protect  bool
    Method   string
    Delay    time.Duration
    Override bool
}
// Decide вызывается слоем, который делает рукопожатие, один раз на рукопожатие.
// applicable=false — метод к этому узлу не применим или узел сделал явный выбор при
// override=false: решение «не применено», исход в пару не идёт.
func (a *Attempt) Decide(proto Proto, path Path, applicable bool) Decision

// Для швов без общего дайла.
func (n *Node) Peek(proto Proto, path Path) Decision                 // AmneziaWG: без отметки
func (n *Node) Report(proto Proto, path Path, protected, ok bool)    // hysteria2/tuic: окно 30 с
```

Исход попытки судит `Node.Dial` по ошибке `do`, но только если за этот вызов был `Decide` с
`applicable=true`. Судить по ошибке самого рукопожатия нельзя: уведённый на живой хост TLS с
`insecure` завершает рукопожатие и падает позже, на первом запросе протокола.

Отпечаток релея сверяется при подъёме кеша: в пределах одного box конфиг не меняется, а новый
конфиг — это новый box (исполнитель волны 2 подтверждает по коду apply и пишет, если не так).

## 4. Волны

| Волна | Исполнители | Что | Файлы-владения |
|---|---|---|---|
| 1 | A ‖ B ‖ D | A: `option`; B: `common/lxsniffguard`; D: черновик документации | A: `option/lx_sniff_guard*.go`, `option/lx.go`, `option/lx_test.go`; B: `common/lxsniffguard/**`; D: `docs-lx/lx-config*.md`, `docs-lx/lx-changelog.md` |
| 2 | C | обёртка, проводка в box, `persist`, смена сети, TLS-хук, писатель `delay`, снятие `DialedThroughDetour` из восьми протоколов | `adapter/outbound`, `box_lx.go`, `common/tls`, `experimental/cachefile`, `protocol/*/outbound.go` (TLS-поле) |
| 3 | E | QUIC-швы на хранилище, `quic.delay`, снятие политики приманки и ключа `quic_sniff_decoy` | `common/lxdatagram`, `protocol/masque`, `protocol/trusttunnel`, `transport/v2rayxhttp`, `transport/wireguard`, `option/lx.go` |
| 4 | контроль | ревью диффа, сборка под `LX_TAGS` и без тегов, тесты затронутых пакетов, `sing-box check` | — |

Волны 2 и 3 идут по очереди: обе трогают `protocol/masque/outbound.go` и
`protocol/trusttunnel/outbound.go`, а дерево одно. Стенды из SPEC §6 (loopback Xray, прямой
путь) — после волны 4, отдельно и со словом владельца: это сетевые прогоны.

## 5. Общие правила исполнителям

- Дерево общее, в нём лежат чужие незакоммиченные правки (переименование ссылок SPEC 151 → 060
  в комментариях и документах). Не коммитить, не делать `stash`, `checkout`, `reset`, worktree.
  Править только свои файлы-владения; нужна правка в чужом — написать в отчёте.
- Новое — в новых файлах; вставки в апстримные файлы — блоками `// lx:begin SPEC 060` /
  `// lx:end SPEC 060` или однострочным `// lx: SPEC 060`. Чужой код не копировать.
- Код, комментарии — английский, в стиле апстрима; `gofmt` своих файлов.
- Сначала вся логика, тесты в конце; гонять только тесты своих пакетов, вывод — `| tail`.
  Go — из `go.version` (`GOTOOLCHAIN`), ldflags `-checklinkname=0`, теги —
  `make -f Makefile.lx lx-print-tags`. Полные прогоны и линтеры не запускать. Сеть не трогать.
- Отчёт: изменённые файлы по родам (новый пакет / вставка `// lx:` / проводка), что проверено и
  чем, отступления от SPEC и PLAN с причиной, открытые вопросы.
