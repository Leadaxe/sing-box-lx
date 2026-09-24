# PLAN: 098 — LX_ROOT_CONFIG_BLOCK

## Файлы

| Файл | Тег | Содержимое |
|------|-----|-----------|
| `option/lx.go` | — | `LXOptions{WG *LXWGOptions; MASQUE *LXMASQUEOptions}`; `LXWGOptions` (`idle_suspend`, `idle_suspend_reachable`, `idle_teardown *`, `lazy_build`, `build_max`, `build_overflow`); `LXMASQUEOptions{IdleTimeout}`; `LXResolved` — разрешённые значения (длительности как `time.Duration`, `IdleTeardownSet`, `BuildOverflow` как перечисление); `ResolveLX(options *Options) (*LXResolved, []string, error)` — алиасы, валидация с полными путями ключей, каноникализация `options.LX` и обнуление `route.lx_idle_*` |
| `option/lx_test.go` | — | парсинг блока; каждое правило валидации; алиасы: только `route`, только `lx`, оба совпадают (предупреждение), оба различаются (ошибка); каноническая форма после резолва; неизвестный ключ/подблок `naive` — ошибка |
| `option/options.go` | upstream | одна строка `LX *LXOptions \`json:"lx,omitempty"\`` под `// lx: SPEC 098` |
| `option/route.go` | upstream, блок `lx:begin idle-suspend` | три поля остаются как алиасы; комментарии → «deprecated, see lx.wg.*» |
| `option/masque.go` | — | `IdleTimeout *badoption.Duration` — явный `"0"` в узле отличим от отсутствия |
| `box_lx.go` | — | `applyLXOptions(ctx, *option.Options, log) (ctx, error)`: вызывает `ResolveLX`, пишет предупреждения алиасов в лог, регистрирует `*option.LXResolved` в контексте (`service.ContextWithPtr`) |
| `box.go` | upstream | одна строка вызова `applyLXOptions` до `route.NewRouter` (endpoint'ы и outbound'ы создаются позже — контекст уже несёт блок) |
| `route/router.go` | upstream, строки `lx:` | четыре поля idle берутся из `service.FromContext[*option.LXResolved](ctx)` (nil → нули), не из `options.LXIdle*` |
| `route/reachability_lx.go`, `route/idle_suspend_stub_lx.go` | `with_lx_idle_suspend` / stub | тексты ошибок → `lx.wg.*`; stub отвергает и `lazy_build`/`build_max` |
| `protocol/masque/outbound.go` | — | `idleWindow(node *badoption.Duration, global time.Duration)`: узел > глобальный > выкл; явный `"0"` узла = выкл |
| `daemon/instance.go` | upstream, строка `lx:` | `captureRunningConfig` после `ResolveLX` (тот же вызов, что в box: резолв идемпотентен) — running-config отдаёт канонический `lx` |
| `lx-test/config/lx_block.json` | — | конфиг с блоком `lx` (все ключи) — под `sing-box check` |
| `Makefile.lx` | — | `lx-check` гоняет все `lx-test/config/*.json` |
| `docs-lx/lx-config.md`, `.ru.md` | — | раздел «Блок `lx`»; пометка deprecated у `route.lx_idle_*`; пример в §0 |
| `docs-lx/lx-energy.md`, `.ru.md` | — | пути ключей `lx.wg.*` |
| `SPECS/FEATURES/008-ENERGY/FEATURE.md`, `009-MASQUE_WARP/FEATURE.md` | — | таблицы параметров под новые пути |
| `docs-lx/lx-changelog.md` | — | секция следующей версии |

## Ключевые решения

- **Один резолв — три потребителя.** `ResolveLX` живёт в `option`, мутирует `Options`
  в каноническую форму (`LX` заполнен, `route.lx_idle_*` обнулены) и возвращает
  разрешённые значения. Его зовут `box.New` (поведение) и демон перед снимком
  running-config (SPEC 037). Повторный вызов над канонической формой — no-op.
- **Через контекст, не через сигнатуры.** Router, WG-endpoint (097) и MASQUE читают
  `*option.LXResolved` из контекста: апстримные сигнатуры `NewRouter`/`NewOutbound`
  не меняются. Отсутствие значения в контексте (тесты, `box.New` без блока) = все
  ручки выключены.
- **Алиасы = предупреждение на ключ.** Для каждого из трёх `route.lx_idle_*`: задан
  только он — `WARN route.lx_idle_suspend is deprecated, use lx.wg.idle_suspend`;
  задан и он, и новый с тем же значением — то же предупреждение; с разным —
  ошибка старта `route.lx_idle_suspend conflicts with lx.wg.idle_suspend`.
  Снятие алиасов — отдельная задача после перехода LxBox и лаунчера.
- **`idle_teardown` — указатель**, как сегодня: явный `"0"` — значение.
- **`build_overflow`** парсится в перечисление (`wait` по умолчанию, `build`);
  прочие строки — ошибка. Ключи 097 валидируются, но в 098 не читаются никем.
- **Гейт сборки** остаётся в роутере (stub): любой ненулевой ключ `lx.wg.*`
  без тега → ошибка с прежним текстом и новым путём ключа.
- **`naive`** — неизвестный подблок: `badjson` отвергает, отдельного кода нет.

## Зона касания upstream

`option/options.go` (1 строка), `option/route.go` (комментарии в lx-блоке),
`box.go` (1 строка), `route/router.go` (4 строки инициализации в lx-блоке),
`daemon/instance.go` (1 строка). Всё под маркерами `// lx:`.

## Риски

- Тесты роутера, конструирующие `NewRouter` с `LXIdleSuspend` в `RouteOptions`,
  переходят на контекст с `LXResolved` — иначе тик не стартует и они молча зелёные.
- `badoption.Duration` в указателе: `omitempty` и `"0"` — проверить round-trip
  running-config (`"0"` должен остаться в снимке).
