# Отчёт 139 — draft

**Фича:** [002-XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

На 25.10.2026 код перенесён из переданного NOVA WIP на закреплённый sing-box-lx 20f89c2. Исходный импорт сохраняется отдельным коммитом; история не переписывалась. Требование разнести исходные правки по зонам при окончательном принятии нужно согласовать с владельцем.

Проверки: Windows Go 1.25.5, `gofmt`, `go vet -p 2 -tags with_xhttp,with_quic,with_utls,with_reality ./transport/v2rayxhttp ./option`, затем `go test -count=1` для тех же пакетов. Результат: 298 тестов/подтестов pass, 0 fail, 0 skip, включая 27 официальных Xray-сценариев. CI: https://github.com/leoraijin/sing-box-lx/actions/runs/40208616205 .

Дополнительный CI: https://github.com/leoraijin/sing-box-lx/actions/runs/40209154598 — Windows regression и Linux `go test -race -count=5` успешны. Это не долгосрочное нагрузочное испытание.

Xray v26.10.21: SHA256 ZIP `fc58c39ee2f2717b4c54d28d374362389e5319b20ae44aea76ea45a5243ec827`, EXE `c77a00eb84fb93ae044c2d771a1cabcc033b81e487253fbc35b12df6879c1e15`.

H3 проверяется на двух TLS-фронтендах перед одним официальным H2 Xray. Ни прямой H3 ingress Xray, ни независимая REALITY-аутентификация этим не доказаны. Незакрытый список — TASKS.md. Production AAR NOVA не заменён.


## Дополнительный bootstrap regression

На локальном Windows-стенде воспроизведён отказ отдельного доменного download при IP uplink: DefaultDialer, созданный для IP, отклоняет неразрешённый домен. Тест использует только `uplink.invalid`/`downlink.invalid`, искусственный DNSRouter с проверкой выбранного bootstrap transport и HTTP loopback. До исправления IP-uplink сценарий падал; при доменном uplink проходил. После добавления resolver-обёртки вокруг того же DefaultDialer проходят оба. Исходные bind/protect параметры dialer сохраняются; уже resolving/detour/custom dialer не заменяются. Это host-проверка ядра, Android TUN/DNS и реальные физические сети ею не проверены.

## Host DoD и проверка конфигурации — 26.10.2026

На Windows/Go 1.25.5 прошли `CGO_ENABLED=0 go build -p 2 ./...` без тегов и сборка CLI с точным набором `LX_TAGS` из Makefile.lx, `-trimpath -ldflags "-checklinkname=0 -buildid="`. Локальный бинарь предназначен только для проверки, не является релизом. Пример `lx-test/config/xhttp_independent_download.json` использует loopback, разные SNI/пути/публичные REALITY-ключи и отдельные XMUX-настройки. Приватные ключи не сохранены.

`python lx-test/check-independent-download.py --binary <CLI> --work <NEW-DIRECTORY>` проверил восемь случаев: корректный независимый профиль, массив XMUX, допустимые обратные границы XMUX; отказы stream-one+downlink, вложенного downlink, неверной длины public_key, слишком длинного short_id и boolean вместо диапазона. Все восемь прошли. Это только конструирование/валидация (`check`), без запуска сервиса, не подтверждение REALITY-handshake. Первоначальный локальный проверяющий скрипт неверно ожидал отказа для обратного диапазона и искал public key вместо фактического public_key; исходный отрицательный результат сохранён в NOVA evidence, затем ожидания исправлены по закреплённому коду.

Полный `go vet -p 2 ./...` не зелёный: предупреждения unsafe.Pointer в `daemon/managed_service.go:75`, `experimental/boxdd/authenticode_windows.go:95`, `experimental/libbox/debug.go:11`. Все три файла идентичны закреплённой базе 20f89c2 (diff до HEAD пуст). Два места — существующие намеренные debug-crash, третье — чтение указателя из WinTrust. Vet затронутых XHTTP/option ранее прошёл; это не позволяет объявить зелёным полный vet. Baseline-предупреждения не подавлялись.

В CI добавлены no-tags/canonical сборки и восемь CLI-проверок с сохранением JSON-доказательств. Результат этого нового CI ещё ожидается. Runtime-матрица REALITY/длительный XMUX/Android bootstrap/16КБ остаётся открытой; статус задачи остаётся W.