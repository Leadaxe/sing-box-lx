# TASKS 139 — StartConnectionTest

> 🌐 English version: **[TASKS.md](TASKS.md)** — основной чеклист; этот файл — русская копия для ознакомления.

Два релиза (SPEC §9). Релиз 1 закрывает #59;
состав релиза 2 определяется по его полевому опыту.

## Релиз 1

### Этап 0 — до кода

- [x] FEATURE 006: вызов в таблицу, правила и границы проверки как чёрный ящик (CONSTITUTION §3.1 в)

### Этап 1 — провод и каркас

- [x] `started_service.proto`: `rpc StartConnectionTest` и сообщения §3.1 SPEC внутри `lx:begin/end lx_command`
- [x] `make -f Makefile.lx lx-proto`; откатить шум protogen и gofumpt в `submodules/wireguard-go`
- [x] `common/lxtrace`: держатель, `Begin`/`End`/`Mark`/`Attr`/`LostRace`/`Routed`, вложенность через ctx, эмит в поток, ловля цикла резолва, потолок 512 стадий, корень по §5.2 SPEC; stub без тега
- [x] Handler + stub: валидация, семафор на 4, таймаут, метаданные источника (`PackageManager` только если есть), отмена
- [x] Через узел: путь 058 + трасса
- [x] Через роутер: `net.Pipe` → `RouteConnectionEx`, ошибка из `onClose`
- [x] Вставка в `route/route.go`: `lxtrace.Routed` до трекеров (стадия `route` + останов `DEPTH_ROUTE`, TCP и UDP)

- [x] Вставка в `route/conn.go`: `dial` первого хопа вокруг `DialContext` / `DialSerialNetwork` (TCP и UDP) — отдельным коммитом
- [x] HTTP-клиент с `httptrace`: `target_tls`, `first_byte`, `http`
- [x] Корень, итог, `stagesTruncated`; все соединения проверки закрыты по концу (SPEC §5.14); `OnError` только на транспортный сбой (SPEC §3.3)

### Этап 2 — путь до узла

- [x] Вставки: `default.go` (`tcp_connect`, обёртка conn), `detour.go` (`detour`, вложенный `dial`), `resolve.go` (`lookup` + цикл)
- [x] Вставки в `protocol/wireguard/endpoint.go` `DialContext`: `lookup` по правилам, `tcp_connect` внутри туннеля
- [x] DNS: `client_log.go` → стадия `dns`; вставка в `dns/router.go` (сработавшее правило) — отдельным коммитом
- [x] TLS: вставка в `common/tls/client.go` (`tls`)
- [x] Наши пакеты: XHTTP `transport`, `chain`, группы (выбор, слот, `IsProbe` без штрафов, истории, деградации и закреплений)
- [x] `proxy_header` `NO_ACK` по типу узла
- [x] Внешние модули: одна стадия `quic + auth` / `auth` на dial, классификация по ошибке

### Этап 3 — глубина и appDNS

- [x] `app_dns` для A, AAAA, HTTPS с метаданными источника; `DisableCache` от `fresh` передаётся напрямую
- [x] `DEPTH_DNS` (подразумевает `appDNS`), `DEPTH_ROUTE` (через `Routed`), `DEPTH_CONNECT` (конец `dial` первого хопа)
- [x] `network = "udp"` до маршрута
- [x] `connectIP` без выводов по fakeip

### Этап 4 — libbox и CLI

- [x] Запрос с сеттерами, handler, сессия, геттеры, итераторы
- [x] Страж 038 без исключений
- [x] `api connection-test`

### Этап 5 — проверка

- [x] Стенд `lx-test/conntest`: в процессе shadowsocks ×2 + trojan/TLS, тестовый DNS по TCP, цель httptest (серверы AWG/Hysteria2/REALITY/XHTTP из приложения А в процессе не поднимаются — см. IMPLEMENTATION_REPORT)
- [x] Критерии приёмки SPEC 1, 3, 8, 9, 10, 11; критерий 2 без вывода
- [x] `-race` на пакетах `lxtrace`, `daemon`, затронутых пакетах; сборка с тегами и без
- [x] Строки в lx-ci для новых пакетов: lx-ci гоняет тесты только по явному списку
- [x] `gofmt` lx-файлов

### Этап 6 — документы

- [x] FEATURE 006: сверить с тем, что легло
- [x] SPEC §10: число вставок по факту
- [x] IMPLEMENTATION_REPORT.md
- [ ] Ответ в #59 со ссылкой на коммит, закрыть

## Релиз 2

### Этап 7 — переиспользование и fresh на вложенных уровнях

- [x] Атрибуты `app_dns` по полевому прогону (#59): `answer` на каждую запись, `authority` при NXDOMAIN / NODATA, `source` / `cache_age`; `cache_age` на кэшированной стадии `dns` (2 вставки в `dns/client.go`)
- [x] Вставки в `dns/client.go` (`DisableCache` от `lxtrace.Fresh`) и `conn_pool.go` (`created == false` → `REUSED`, `fresh` → новое соединение)
- [x] WG/AWG: `wake`, `wg_handshake`; `reuse=uncontrolled` для внешних модулей
- [x] MASQUE и TrustTunnel `auth`

### Этап 8 — REALITY и fakeip

- [x] Вердикт в `reality_client.go` (`accepted` / `dest_cert` / `invalid_cert`)
- [x] lx-доступ к хранилищу fakeip; вставка счётчика оборотов в `store.go`
- [x] `connectIP` из диапазона fakeip: `route` `FAILED` + выводы

### Этап 9 — выводы

- [x] `findings_lx.go`: все коды §6 SPEC (`ip_rules_not_evaluated` по индексу выбранного правила, без вставки в `matchRule`)
- [x] Юниты на синтетических трассах

### Этап 10 — проверка и документы

- [x] Стенд: добавить Hysteria2 в `lx-test/conntest`
- [x] Критерии приёмки SPEC 2 (вывод), 4, 5, 6, 7
- [x] `-race`, сборки, lx-ci, `gofmt` как в этапе 5
- [x] FEATURE 006, SPEC §10, IMPLEMENTATION_REPORT.md обновлены

### Этап 11 — после rc.3

- [x] Редирект — это ответ: `CheckRedirect` → последний ответ, `location` в `http`; ошибка закрытия маршрутизированного соединения после dial первого хопа — не обрыв пути (поле: 302 давал HTTP 0 без корня)
- [x] DoH / DoH3 при `fresh`: одноразовый клиент, `connection=new`, `pool=detached`
- [x] `wg_handshake`: атрибут `detour`
- [x] Внутренние стадии Hysteria2 / TUIC: не планируются, причины в SPEC §7
