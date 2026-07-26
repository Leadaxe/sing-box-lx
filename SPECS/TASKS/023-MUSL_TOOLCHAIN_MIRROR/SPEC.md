# SPEC 023 — Durable-зеркало Chromium musl-тулчейна для релиз-сборок

**Фича:** [BUILD_CI_CD](../../FEATURES/001-BUILD_CI_CD/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature — CI/build инфраструктура) |
| Статус | C (complete — зеркало заполнено, restore проверен; см. IMPLEMENTATION_REPORT) |

**Файлы:** `.github/workflows/lx-musl-toolchain-mirror.yml` (новый), `.github/workflows/lx-release.yml` (шов restore), `.github/CRONET_GO_VERSION` (существует). Зона касания upstream: **нулевая** — обе workflow целиком lx-owned (в upstream отсутствуют), маркеры `// lx:` не нужны.

---

## 1. Проблема

Джоба `build_linux_musl` в `lx-release.yml` собирает статические musl-бинари для роутеров (OpenWrt/Merlin/Keenetic) с NaïveProxy. Для этого она **строит Chromium-sysroot с нуля**: `go run ./cmd/build-naive … download-toolchain` тянет десятки `.deb` (gcc-10, libstdc++, libtsan, libubsan, …) с **закреплённого снапшота** `snapshot.debian.org/…/20250129T203412Z/…` плюс clang.

`snapshot.debian.org` **периодически отдаёт `503 "No healthy backends" / "All backends failed"`** — не мгновенный blip, а окна недоступности в минуты-десятки минут. Существующий step-level retry (5 попыток, backoff) их не переживает, когда зеркало легло целиком.

**Наблюдение (2026-07-02, релиз `v1.14.0-lx.2-rc.1`):** прогон падал **дважды подряд** на скачивании тулчейна; во втором прогоне 3 из 4 арок скачались, а `mipsle-softfloat` снова упал на 503. Публикация релиза гейтится на успех **всех** сборок → релиз не выходит, хотя код и остальные арки готовы.

### 1.1 Почему `actions/cache` не спасает

Уже стоит `actions/cache@v4` (ключ `chromium-toolchain-musl-<arch>-<hash(CRONET_GO_VERSION)>`). Он закрывает **тёплый** случай, но промахивается в двух реальных ситуациях:

1. **Ref-scoping кеша GitHub Actions.** Кеш виден только своей ветке + default-ветке. Сборка **тега** `v1.14.0-lx.1` (утром) НЕ делится кешем со сборкой **тега** `v1.14.0-lx.2-rc.1` — поэтому cache-miss, хотя `CRONET_GO_VERSION` не менялся. Это и случилось.
2. **Вытеснение** (7 дней без обращения / лимит 10 ГБ на репо).

На cache-miss **единственный** источник — `snapshot.debian.org`. Когда он лежит — сборка невозможна.

## 2. Цель

Свой **durable-источник** собранного тулчейна, не зависящий ни от ref-scoping кеша Actions, ни от аптайма `snapshot.debian.org`:

- собранный тулчейн (clang + gn + pgo + per-arch sysroot) складывается **release-ассетом** в наш репозиторий (тег-релиз `musl-toolchain-cache`), ключ — версия cronet-go;
- `lx-release.yml` на cache-miss **сначала** тянет наш ассет, и только если и он отсутствует — падает в `snapshot.debian.org` (как сегодня);
- ассеты **не в git-истории** → репозиторий не раздувается; release-storage бесплатен.

## 3. Критерии приёмки

1. Новый manual workflow (`workflow_dispatch`) собирает тулчейн для всех 4 арок (amd64/arm64/armv7/mipsle) и заливает ассеты в релиз `musl-toolchain-cache`, ключ — `<cronet-version>`.
2. В `lx-release.yml` перед шагом `Download Chromium musl toolchain` добавлен restore-шаг: если `actions/cache` промахнулся, тянем ассет из `musl-toolchain-cache` и распаковываем в дерево `naiveproxy/src`; при попадании последующий `download-toolchain` — no-op (всё уже на месте).
3. Порядок источников: **actions/cache → наш release-mirror → snapshot.debian.org**. Ни один источник не обязателен по отдельности; падение возможно только если промахнулись **все три**.
4. Никаких правок upstream-файлов; обе workflow — lx-owned. `download-toolchain`-шаг с ретраем остаётся как последний fallback (не удаляется).
5. Ассет не превышает лимит GitHub 2 ГБ на файл (zstd-сжатие; при риске превышения — разбить shared/ per-arch, см. PLAN §4).

## 4. Вне скоупа

- Устранение самой зависимости от `snapshot.debian.org` при **первом** заполнении зеркала (курица-яйцо: чтобы собрать ассет, producer один раз всё же качает с snapshot — но при живом snapshot это делается разово и больше не повторяется на каждой сборке).
- Готовые musl-`libcronet.a` из чужих источников — их нет: cronet-go публикует только динамические glibc `.so` (проверено: `libcronet-linux-*.so`), не наш статический musl-таргет.
- Автоматический ре-триггер producer при бампе `CRONET_GO_VERSION` — producer запускается вручную при бампе (задокументировано в PLAN).

## 5. Замечание о немедленном релизе

`v1.14.0-lx.2-rc.1` заблокирован не кодом, а окном недоступности `snapshot.debian.org`. Пока зеркало не заполнено, релиз доводится ре-раном упавшей джобы при живом snapshot. После заполнения зеркала (этот SPEC) такие окна перестают блокировать релизы.
