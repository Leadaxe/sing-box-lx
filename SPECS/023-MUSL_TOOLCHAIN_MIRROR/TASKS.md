# TASKS 023 — Durable-зеркало musl-тулчейна

## Реализация
- [ ] 1. Producer-workflow `.github/workflows/lx-musl-toolchain-mirror.yml` (workflow_dispatch, матрица 4 арок, download-toolchain → tar.zst → gh release upload в `musl-toolchain-cache`).
- [ ] 2. Restore-шаг в `lx-release.yml` между `Cache Chromium toolchain` и `Download Chromium musl toolchain`.
- [ ] 3. YAML/синтаксис обеих workflow валиден (actionlint или parse).

## Наполнение и проверка
- [ ] 4. Запустить producer при живом snapshot.debian.org → 4 ассета в релизе `musl-toolchain-cache`.
- [ ] 5. Тестовый release-прогон на cache-miss: в логе `restored toolchain from lx mirror`, snapshot.debian.org не дёргается.

## Закрытие
- [ ] 6. IMPLEMENTATION_REPORT.md, статус SPEC → C, Roadmap в SPECS/README.md.

## Заметка по эксплуатации
- При каждом бампе `.github/CRONET_GO_VERSION` — **вручную** запустить `lx-musl-toolchain-mirror` (иначе зеркало устареет и release упадёт обратно на snapshot.debian.org).
