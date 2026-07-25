# TASKS 023 — Durable-зеркало musl-тулчейна

## Реализация
- [x] 1. Producer-workflow `.github/workflows/lx-musl-toolchain-mirror.yml` (workflow_dispatch, матрица 4 арок, download-toolchain → tar.zst → gh release upload в `musl-toolchain-cache`).
- [x] 2. Restore-шаг в `lx-release.yml` между `Cache Chromium toolchain` и `Download Chromium musl toolchain`.
- [x] 3. YAML/синтаксис обеих workflow валиден (actionlint — чисто).

## Наполнение и проверка
- [x] 4. Запустить producer при живом snapshot.debian.org → 4 ассета в релизе `musl-toolchain-cache` (amd64/arm64/arm/mipsle, ~222–227 МБ).
- [x] 5. Restore проверен локально (имя ассета, `gh release download`, структура tar под `naiveproxy/src`). На реальном release-прогоне при cache-miss — подтвердится следующим релизом.

## Закрытие
- [x] 6. IMPLEMENTATION_REPORT.md, статус SPEC → C, Roadmap в SPECS/README.md.

## Заметка по эксплуатации
- При каждом бампе `.github/CRONET_GO_VERSION` — **вручную** запустить `lx-musl-toolchain-mirror` (иначе зеркало устареет и release упадёт обратно на snapshot.debian.org).
