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

## REALITY, конкурентные сессии и сохранение причины отказа — 26.10.2026

CI 40219674838 на 71c354a5 успешен: Windows 301 pass, Linux race count=5 — 1365 pass; no-tags, канонический CLI и восемь check-сценариев прошли.

Следующая локальная проверка: 361 тест/подтест pass, 0 fail/skip, targeted vet и gofmt зелёные. Матрица официального backend расширена до 48 случаев (16 пар транспорта × 3 режима); 21 случай содержит REALITY на одном либо обоих независимых фронтендах. У каждого REALITY-фронтенда отдельные X25519 ключи, short_id и SNI; секреты существуют только в памяти. Проверяется настоящий HTTP/2 обмен после handshake. Закреплённый REALITY server оставляет ALPN в ConnectionState пустым, поэтому тест проверяет завершённый handshake/SNI и фактический HTTP/2 ответ, а не выдуманное значение ALPN. Отдельно прошли classical, default и hybrid key share. Ошибочные public_key, short_id, SNI и подстановка ключа upload в download отклоняются без fallback и без оставшихся аренд. Camouflage TLS в этих отрицательных тестах самоподписан: отдельного сценария с доверенным camouflage сертификатом пока нет.

REALITY frontend здесь — native sing-box, за ним общий официальный Xray H2 backend. Прямой REALITY/H3 ingress официального Xray и Android TUN этой host-матрицей не доказаны.

Обнаружена и воспроизведена ошибка диагностики: отказ download отменяет активный POST, и Write возвращал вторичный context canceled вместо исходной TLS ошибки. Минимальная правка возвращает уже опубликованную readerErr только при отменённом POST. Независимые ошибки upload и обычная отмена сохраняются; четыре детерминированных red/green сценария прошли. Изменение runtime требует нового preview .3: существующий AAR .2 его не содержит.

На исправленном коде выполнены по 300 секунд auto, packet-up и stream-up: 12 одновременных постоянных VLESS echo соединений через раздельные H2/TLS frontend, отдельные лимиты XMUX 3/2. Проверены уникальные payload, фактические лимиты аренды, отсутствие обращений к неверному направлению и освобождение аренд до закрытия клиента. Все три режима прошли (901.10 секунды суммарно). Это loopback H2/H2 нагрузка; длительные H1/H3, физическая смена сети, Android bootstrap и 16КБ runtime остаются открытыми. В CI добавлен отдельный такой же 15-минутный шаг. Результат нового CI ещё ожидается.

## Upload adoption и Android .4 — 26.10.2026

`2b08fc5c` исправляет нарушение контракта SPEC 077: ранние успешные download
headers больше не завершают dial, пока upload не принят транспортом. Успешный
download также не скрывает отмену незавершённого upload. Два детерминированных
red/green теста включены в `upload_readiness_lx_test.go`; runtime-дельта только
в `awaitRaise`. Регрессия воспроизвелась в Android .3 как плавающий H1/H2 EOF;
после исправления прошли шесть подряд H1/H2 прогонов (36 IPv4/IPv6 проб) и
остальные восемь HTTP-пар. Это не доказательство отсутствия всех возможных EOF.

Локальный extended run на `2b08fc5c` завершён: 300 секунд на каждый из трёх
режимов, по 12 постоянных VLESS сессий, 104414 обменов / 1710718976 байт payload.
Проверены payload/sequence, независимость пулов, лимиты XMUX, направления запросов
и освобождение аренд. Все три режима pass за 901.77 секунды. Это Windows H2/H2
loopback через отдельные frontend к официальному Xray; не Android endurance.

На API36 x86_64 с реальными 16КБ страницами в отдельном NOVA preview .4 прошли:
- прямой официальный Xray 26.10.21 H3/TLS ingress: packet-up/stream-up/auto,
  IPv4/IPv6 из отдельного UID через Android TUN; неверная download TLS identity
  блокирует обе семьи без рабочего fallback; правильная identity работает снова;
- прямой официальный Xray REALITY/H2 ingress с локальным TLS1.3 target:
  те же три режима и отдельный UID; другой корректно сформированный download
  public key блокирует обе семьи, затем правильный ключ восстанавливает связь;
- официальный Xray-клиент отдельно подтвердил Reality fixture IPv4/IPv6;
- после расширения test runner повторены H3/TLS modes и identity refusal.

В прямых Android тестах обе стороны использовали один адрес/путь Xray. Разные
адреса/SNI/пути проверены на frontend-сценариях; независимые Reality server,
доверенный camouflage negative control, Android bootstrap обоих направлений,
длительные H1/H3/Reality и мобильная сеть остаются открытыми. Новая .4 на
физическом ARM64 пока не проверена. Проверка ELF не подменяет runtime.

Все четыре ABI .4 собраны из восстановленного архива с пустым кэшем offline;
AAR побайтово совпал с тестируемым кандидатом, SHA256
`67abf3aafeff36f5b042929d74f75fbc646aa7085a3b3bb69091874a1e70559c`.
Исправлена переносимость исходников CGo при CRLF Git archive на Windows.
Production AAR и стабильная подпись NOVA не менялись. Исходники и отчёты NOVA
сохранены отдельно; чувствительные данные провайдеров в PR не передаются.
Новый CI на исправленной ревизии ещё должен завершиться; draft и статус W сохраняются.
