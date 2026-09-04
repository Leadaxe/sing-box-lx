# HISTORY — SPEC 004 BUILD_CI_RELEASE

Хронология конвейера сборки/релизов. Актуальное состояние — в [SPEC.md](SPEC.md); здесь только «как было раньше и почему переделали».

---

## Потерянный шаг «Extract libcronet.dll» (обнаружено 2026-07-14)

**Симптом:** релизный `sing-box.exe` (windows amd64/arm64) с `naive`-outbound в конфиге падал на старте: `cronet: library not found. Place libcronet.dll in the executable directory or PATH`.

**Причина:** desktop-сборка идёт с `with_purego,with_naive_outbound` — cronet не вшит в бинарь, purego-лоадер грузит `libcronet.dll` в рантайме из каталога exe. В upstream `build.yml` windows-джоба имеет отдельный шаг «Extract libcronet.dll» (`build-naive extract-lib` из cronet-go), и dll едет в zip рядом с бинарём. При переносе воркфлоу в форк (`lx-release.yml`) этот шаг потерялся: шаг Package клал в архив только `sing-box.exe` + LICENSE/README. Ни один windows-архив, выпущенный `lx-release.yml` до фикса, dll не содержал — naive на windows-релизах не работал никогда. CI это не ловило: кросс-сборка проверяет компиляцию, а не содержимое архива и не рантайм-загрузку библиотеки.

**Фикс (2026-07-14, войдёт в первый тег после v1.14.0-lx.3):** шаг «Extract libcronet.dll» в job `build` для windows-таргетов + копирование dll в stage при упаковке; та же правка для `libcronet.so` в on-demand `lx-build.yml` job `binary` (linux glibc purego). Release notes дополнены строкой «держать dll рядом с exe».

**Почему НЕ скопировали upstream-ный `build-naive extract-lib`:** он резолвит *latest* коммит ветки `go` репозитория cronet-go (`git ls-remote … refs/heads/go`) и качает lib-модуль на нём (сам инструмент env не задаёт — upstream `build.yml` выставляет `GOPROXY=direct GOSUMDB=off` в шаге перед вызовом, иначе pseudo-version свежего коммита спотыкается о sumdb) — версия dll может уехать вперёд purego-биндингов, против которых собран бинарь, плюс нужен клон cronet-go. Вместо этого dll извлекается из lib-модуля, **запиненного в go.mod** (`go mod download -json …lib/windows_<arch>@$(go list -m -f '{{.Version}}' …)`): ровно та пара «биндинги ↔ библиотека», которую резолвит сам go.mod, верифицируется go.sum, клон не нужен.

**Darwin-находка по ходу диагноза:** упаковкой darwin не чинится — dylib для purego-лоадера не существует (lib-модули `cronet-go/lib/darwin_*` содержат только статическую `libcronet.a` для CGO-пути; `extract-lib` явно отвечает «macOS … use static linking via CGO instead»). Upstream собирает darwin отдельной джобой на `macos-latest` с `CGO_ENABLED=1` и тегами без `with_purego`. Наши darwin-бинари (ubuntu-кросс, purego) шли с включённым тегом, но нефункциональным naive — в `v1.14.0-lx.4-rc.1` это было зафиксировано в release notes как ограничение.

## Darwin переведён на macos-CGO джобу (2026-07-14, после rc.1)

Владелец решил «взять всё из upstream»: darwin выведен из ubuntu-джобы `build` в отдельную `build_darwin` (upstream `build_darwin` parity) — `macos-latest`, `CGO_ENABLED=1`, теги без `with_purego`, `libcronet.a` статикой; amd64 кросс-собирается CGO на том же arm64-раннере. NaïveProxy на darwin заработал впервые за историю форка. Verify-шаг гоняет `sing-box check` с naive-конфигом прямо на раннере (та самая точка падения purego-сборок). Upstream-ный legacy-вариант macos-10.13 НЕ взят: форк его никогда не поставлял, и у upstream он всё равно без naive (`DEFAULT_BUILD_TAGS_OTHERS`).

## `with_tailscale` включён для desktop и роутеров (2026-09-04, v1.14.0-lx.31)

Владелец решил включить `with_tailscale` в desktop- и роутерные бинари (AAR — нет). До этого тег был снят как «нет tailscale-endpoint'ов у VPN-клиента». Проверка перед решением: сборка `CGO_ENABLED=0` поверх `LX_TAGS` на darwin/arm64, windows/amd64, linux/arm64, linux/armv7, linux/mipsle softfloat, linux/mips softfloat — все зелёные против наших сабмодулей wireguard-go/sing-tun/gvisor (sagernet/tailscale тянет sagernet/wireguard-go, `replace` на форк с AWG-графтом конфликта не дал); живой старт endpoint'а на darwin — tsnet поднимает state-каталог, резолвит `controlplane.tailscale.com`, уходит в login. Musl+CGO вариант (linux с naive) локально не гонялся — его подтверждает первый прогон `lx-release.yml`. Цена — размер: darwin/arm64 49.8 → 63.2 МБ, mips softfloat 56.2 → 72.2 МБ. `ts_omit_*`-теги (как в upstream `build_libbox` для мобилок) НЕ применены: upstream desktop-набор их не использует, комбинация не проверена. Правки: `Makefile.lx` (LX_TAGS + комментарий), `lx-ci.yml` BASE_TAGS, блок фич в нотах `lx-release.yml`, README EN/RU, SPEC.md §LX_TAGS.
