# PLAN 023 — Durable-зеркало musl-тулчейна

## 1. Архитектура: три источника, строгий приоритет

На каждую арку в `build_linux_musl`:

```
1. actions/cache@v4   (тёплый; ref-scoped, вытесняется)   ── попадание → download-toolchain = no-op
2. lx release-mirror  (musl-toolchain-cache/<cronet>)      ── попадание → распаковали, download-toolchain = no-op
3. snapshot.debian.org (download-toolchain, retry×5)       ── последний fallback (как сегодня)
```

Падение только если промахнулись **все три**. Шаги 1 и 3 уже есть; SPEC добавляет шаг 2 между ними и **producer** для наполнения зеркала.

## 2. Что кешируется (роли директорий под `~/cronet-go/naiveproxy/src/`)

| Директория | Роль | Зависит от арки? |
|------------|------|------------------|
| `third_party/llvm-build/` | clang (host-arch toolchain) | нет (SHARED) |
| `gn/out/` | gn binary | нет (SHARED) |
| `chrome/build/pgo_profiles/` | PGO-профили | нет (SHARED) |
| `out/sysroot-build/` | Debian sysroot | **да (PER-ARCH)** |

**Решение по упаковке:** один tar на арку, корень `naiveproxy/src`, содержит все 4 директории. clang дублируется на 4 арки — расточительно по storage, но storage бесплатен, а restore тривиален (`tar -C naiveproxy/src -xf`). Сжатие **zstd** (clang жмётся ~3-4×); ожидаемый размер ассета ~0.5–0.9 ГБ — под лимитом GitHub 2 ГБ/файл. Если вырастет за лимит — разбить на `shared` + per-arch `sysroot` (fallback-план, §4).

## 3. Producer — `.github/workflows/lx-musl-toolchain-mirror.yml` (новый)

- Триггер: `workflow_dispatch` (запускается **вручную при бампе `CRONET_GO_VERSION`**).
- Матрица арок = та же, что в release (amd64/arm64/armv7/mipsle).
- Шаги (переиспользуют логику release-джобы 1:1, чтобы дерево совпадало):
  1. checkout, setup-go.
  2. Clone cronet-go (pin `CRONET_GO_VERSION`).
  3. Regenerate Debian keyring (retry — как в release).
  4. `download-toolchain` (retry — как в release; это единственная точка, где producer зависит от snapshot.debian.org, разово).
  5. `tar --zstd -C ~/cronet-go/naiveproxy/src -cf toolchain-<arch>-<cronet>.tar.zst third_party/llvm-build gn/out chrome/build/pgo_profiles out/sysroot-build`.
  6. Убедиться, что релиз `musl-toolchain-cache` существует (`gh release create musl-toolchain-cache --prerelease --notes … || true`), затем `gh release upload musl-toolchain-cache <asset> --clobber`.
- `permissions: contents: write` (нужно для upload).
- Concurrency: последовательная заливка не требуется (`--clobber` идемпотентен per-asset-name).

**Имя ассета:** `toolchain-<arch>-<CRONET_GO_VERSION>.tar.zst` — версия в имени → бамп cronet автоматически даёт новый ассет, старый не перетирается (можно чистить вручную).

## 4. Restore-шов в `lx-release.yml` (правка lx-owned)

Новый шаг **между** `Cache Chromium toolchain` (actions/cache) и `Download Chromium musl toolchain`:

```yaml
- name: Restore musl toolchain from lx mirror (fallback for snapshot.debian.org)
  env:
    GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
  run: |
    set -xeuo pipefail
    SRC=~/cronet-go/naiveproxy/src
    # actions/cache hit? then everything is already in place — nothing to do.
    if [ -d "$SRC/out/sysroot-build" ] && [ -d "$SRC/third_party/llvm-build" ]; then
      echo "actions/cache hit — skipping lx mirror"; exit 0
    fi
    CRONET_GO_VERSION="$(cat .github/CRONET_GO_VERSION)"
    ASSET="toolchain-${{ matrix.arch }}-${CRONET_GO_VERSION}.tar.zst"
    if gh release download musl-toolchain-cache --repo "$GITHUB_REPOSITORY" \
         --pattern "$ASSET" --dir /tmp/lxtc 2>/dev/null; then
      tar --zstd -C "$SRC" -xf "/tmp/lxtc/$ASSET"
      echo "restored toolchain from lx mirror ($ASSET)"
    else
      echo "lx mirror miss ($ASSET) — falling back to snapshot.debian.org"
    fi
```

- `gh` и `GITHUB_TOKEN` дают read-доступ к релизам того же репо — токена достаточно, секретов не нужно.
- Существующий `Download Chromium musl toolchain` **не меняется** (остаётся fallback): если restore заполнил дерево, `build-naive download-toolchain` увидит всё на месте и не пойдёт в сеть; если mirror промахнулся — тянет с snapshot как сегодня.

## 5. Порядок «курица-яйцо»

1. Слить SPEC-правки (restore-шаг + producer workflow).
2. Один раз запустить producer **при живом snapshot.debian.org** → зеркало заполнено.
3. С этого момента любой release restore'ит из зеркала на cache-miss; snapshot.debian.org больше не блокирует релизы (только как 3-й fallback).

## 6. DoD

- `actionlint`/синтаксис workflow валиден (проверить локально `actionlint` если есть, иначе YAML-parse).
- Producer собрал и залил 4 ассета в `musl-toolchain-cache`.
- Тестовый release-прогон на cache-miss восстановился из зеркала (в логе `restored toolchain from lx mirror`), snapshot.debian.org не дёргался.
- IMPLEMENTATION_REPORT.md заполнен; статус SPEC → C; Roadmap обновлён.

## 7. Зона ребейза

Нулевая. Обе workflow lx-owned, upstream их не содержит и не трогает.
