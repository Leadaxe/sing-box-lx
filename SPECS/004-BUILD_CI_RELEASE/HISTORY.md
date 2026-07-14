# HISTORY — SPEC 004 BUILD_CI_RELEASE

Хронология конвейера сборки/релизов. Актуальное состояние — в [SPEC.md](SPEC.md); здесь только «как было раньше и почему переделали».

---

## Потерянный шаг «Extract libcronet.dll» (обнаружено 2026-07-14)

**Симптом:** релизный `sing-box.exe` (windows amd64/arm64) с `naive`-outbound в конфиге падал на старте: `cronet: library not found. Place libcronet.dll in the executable directory or PATH`.

**Причина:** desktop-сборка идёт с `with_purego,with_naive_outbound` — cronet не вшит в бинарь, purego-лоадер грузит `libcronet.dll` в рантайме из каталога exe. В upstream `build.yml` windows-джоба имеет отдельный шаг «Extract libcronet.dll» (`build-naive extract-lib` из cronet-go), и dll едет в zip рядом с бинарём. При переносе воркфлоу в форк (`lx-release.yml`) этот шаг потерялся: шаг Package клал в архив только `sing-box.exe` + LICENSE/README. Ни один windows-архив, выпущенный `lx-release.yml` до фикса, dll не содержал — naive на windows-релизах не работал никогда. CI это не ловило: кросс-сборка проверяет компиляцию, а не содержимое архива и не рантайм-загрузку библиотеки.

**Фикс (2026-07-14, войдёт в первый тег после v1.14.0-lx.3):** шаг «Extract libcronet.dll» в job `build` для windows-таргетов + копирование dll в stage при упаковке; та же правка для `libcronet.so` в on-demand `lx-build.yml` job `binary` (linux glibc purego). Release notes дополнены строкой «держать dll рядом с exe».

**Почему НЕ скопировали upstream-ный `build-naive extract-lib`:** он резолвит *latest* коммит ветки `go` репозитория cronet-go (`git ls-remote … refs/heads/go`) и качает lib-модуль на нём (сам инструмент env не задаёт — upstream `build.yml` выставляет `GOPROXY=direct GOSUMDB=off` в шаге перед вызовом, иначе pseudo-version свежего коммита спотыкается о sumdb) — версия dll может уехать вперёд purego-биндингов, против которых собран бинарь, плюс нужен клон cronet-go. Вместо этого dll извлекается из lib-модуля, **запиненного в go.mod** (`go mod download -json …lib/windows_<arch>@$(go list -m -f '{{.Version}}' …)`): ровно та пара «биндинги ↔ библиотека», которую резолвит сам go.mod, верифицируется go.sum, клон не нужен.

**Darwin-находка по ходу диагноза:** упаковкой darwin не чинится — dylib для purego-лоадера не существует (lib-модули `cronet-go/lib/darwin_*` содержат только статическую `libcronet.a` для CGO-пути; `extract-lib` явно отвечает «macOS … use static linking via CGO instead»). Upstream собирает darwin отдельной джобой на `macos-latest` с `CGO_ENABLED=1` и тегами без `with_purego`. Наши darwin-бинари (ubuntu-кросс, purego) шли и идут с включённым тегом, но нефункциональным naive; после фикса это явно зафиксировано в release notes. Вопрос «заводить ли macos-джобу ради naive на macOS» оставлен владельцу — см. SPEC.md §2.4.
