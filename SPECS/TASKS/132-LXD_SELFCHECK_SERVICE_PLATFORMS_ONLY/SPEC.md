# SPEC: 132 — LXD_SELFCHECK_SERVICE_PLATFORMS_ONLY

**Фича:** [LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) (сужение самопроверки [100](../100-LXD_ROOT_OWNED_BINARY/SPEC.md) §2.8 / [103](../103-LXD_WINDOWS_SERVICE/SPEC.md) §2.11)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — ложный WARN с неисполнимым советом на Linux ([issue #40](https://github.com/Leadaxe/sing-box-lx/issues/40)) |
| Статус | I (implemented) — 2026-10-14, решение владельца того же дня |

## 1. Дефект

`sing-box run` под root на Linux печатал при каждом старте:

```
WARN lxd: running as root from /tmp/mnt/opkg_flash/entware/root/sing-box/sing-box (uid 0, mode 0775): /: owned by uid 0, mode 0777, must be root-owned and not group/world-writable — not the launchd service (ppid 4480, XPC_SERVICE_NAME ""), starting anyway; the service would refuse this binary: run `sing-box lxd --service=install` to reinstall from a root-owned copy (or --service=copy)
```

(Asus Merlin + Entware, lx.16; в lx.20 то же.) На Linux каждое утверждение строки ложно: launchd нет;
службы, которая «откажет», нет — строгая ветка требует метку launchd и недостижима; `--service=install`
только печатает рецепт, `--service=copy` — «macOS only». На Merlin `/` = `0777`, инвариант не проходит
никакой путь — WARN неустраним.

## 2. Решение владельца

Самопроверка нужна там, где служба ставится **самим ядром** и исполняет файл по пути, который
выбрал установщик: macOS (LaunchDaemon, копия в `/Library/PrivilegedHelperTools`) и Windows (служба SCM
от `LocalSystem`, защищённая копия в `<ProgramFiles>\sing-box-lxd`). Строго только они; на остальных
платформах (Linux, включая OpenWrt, и прочие unix) проверки нет совсем — ни отказа, ни WARN.

Обоснование: на Linux `--service` — печать рецепта (SPEC 057), путь в unit выбирает оператор; ошибки
установщика, ради которой существует отказ §2.8, нет, а WARN без исполнимого совета не защищает.

## 3. Реализация

- `platformSelfCheckEnv` для macOS — `lxd/selfcheck_darwin.go` (`with_lxd && darwin`), перенос из
  `execsafe_unix.go` без изменений.
- `lxd/selfcheck_other.go` (`with_lxd && !darwin && !windows`) — пустое окружение: не привилегирован →
  `evaluateSelfCheck` молчит. Заменяет прежнюю заглушку в `execsafe_other.go`.
- `lstatOwner` и инвариант (`checkRootOwnedChain`, `unixSelfCheckEnv`) остаются на всём unix: ими
  пользуются копия и status на darwin, а табличный `TestInvariantSelfCheck` гоняет решение на Linux CI.
- `lxd --allow-unsafe-exec` вне macOS/Windows ни на что не влияет (отказывать нечему).
- Тест `TestSelfCheckOffOutsideServicePlatforms` (`selfcheck_other_test.go`): под root и с
  `allowUnsafe` — ни INFO, ни WARN, ни ошибки.

Ноль апстримных файлов.

## 4. Проверка

- `go vet` пакетов `lxd` и `cmd/sing-box` под `LX_TAGS` для linux, darwin, windows; тесты
  `SelfCheck|Invariant` на darwin; тест-бинарь `lxd` компилируется под linux/arm64.
- Живая проверка на Merlin — за репортёром issue #40 (строки в логе запуска нет).
