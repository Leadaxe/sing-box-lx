# TASKS 139 — StartConnectionTest

> 🌐 English version: **[TASKS.md](TASKS.md)** — основной чеклист; этот файл — русская копия для ознакомления.

Два релиза (SPEC §9). Релиз 1 закрывает #59;
состав релиза 2 определяется по его полевому опыту.

## Релиз 1

### Этап 0 — до кода

- [x] FEATURE 006: вызов в таблицу, правила и границы проверки как чёрный ящик (CONSTITUTION §3.1 в)

### Этап 1 — провод и каркас

- [ ] `started_service.proto`: `rpc StartConnectionTest` и сообщения §3.1 SPEC внутри `lx:begin/end lx_command`
- [ ] `make -f Makefile.lx lx-proto`; откатить шум protogen и gofumpt в `submodules/wireguard-go`
- [ ] `common/lxtrace`: держатель, `Begin`/`End`/`Mark`/`Attr`/`LostRace`/`Routed`, вложенность через ctx, эмит в поток, ловля цикла резолва, потолок 512 стадий, корень по §5.2 SPEC; stub без тега
- [ ] Handler + stub: валидация, семафор на 4, таймаут, метаданные источника (`PackageManager` только если есть), отмена
- [ ] Через узел: путь 058 + трасса
- [ ] Через роутер: `net.Pipe` → `RouteConnectionEx`, ошибка из `onClose`
- [ ] Вставка в `route/route.go`: `lxtrace.Routed` до трекеров (стадия `route` + останов `DEPTH_ROUTE`, TCP и UDP)

- [ ] Вставка в `route/conn.go`: `dial` первого хопа вокруг `DialContext` / `DialSerialNetwork` (TCP и UDP) — отдельным коммитом
- [ ] HTTP-клиент с `httptrace`: `target_tls`, `first_byte`, `http`
- [ ] Корень, итог, `stagesTruncated`; все соединения проверки закрыты по концу (SPEC §5.14); `OnError` только на транспортный сбой (SPEC §3.3)

### Этап 2 — путь до узла

- [ ] Вставки: `default.go` (`tcp_connect`, обёртка conn), `detour.go` (`detour`, вложенный `dial`), `resolve.go` (`lookup` + цикл)
- [ ] Вставки в `protocol/wireguard/endpoint.go` `DialContext`: `lookup` по правилам, `tcp_connect` внутри туннеля
- [ ] DNS: `client_log.go` → стадия `dns`; вставка в `dns/router.go` (сработавшее правило) — отдельным коммитом
- [ ] TLS: вставка в `common/tls/client.go` (`tls`)
- [ ] Наши пакеты: XHTTP `transport`, `chain`, группы (выбор, слот, `IsProbe` без штрафов, истории, деградации и закреплений)
- [ ] `proxy_header` `NO_ACK` по типу узла
- [ ] Внешние модули: одна стадия `quic + auth` / `auth` на dial, классификация по ошибке

### Этап 3 — глубина и appDNS

- [ ] `app_dns` для A, AAAA, HTTPS с метаданными источника; `DisableCache` от `fresh` передаётся напрямую
- [ ] `DEPTH_DNS` (подразумевает `appDNS`), `DEPTH_ROUTE` (через `Routed`), `DEPTH_CONNECT` (конец `dial` первого хопа)
- [ ] `network = "udp"` до маршрута
- [ ] `connectIP` без выводов по fakeip

### Этап 4 — libbox и CLI

- [ ] Запрос с сеттерами, handler, сессия, геттеры, итераторы
- [ ] Страж 038 без исключений
- [ ] `api connection-test`

### Этап 5 — проверка

- [ ] Стенд `lx-test/conntest`: локальные серверы VLESS+REALITY+XHTTP, AWG, Shadowsocks, DoH; конфиг из приложения А SPEC
- [ ] Критерии приёмки SPEC 1, 3, 8, 9, 10, 11; критерий 2 без вывода
- [ ] `-race` на пакетах `lxtrace`, `daemon`, затронутых пакетах; сборка с тегами и без
- [ ] Строки в lx-ci для новых пакетов: lx-ci гоняет тесты только по явному списку
- [ ] `gofmt` lx-файлов

### Этап 6 — документы

- [ ] FEATURE 006: сверить с тем, что легло
- [ ] SPEC §10: число вставок по факту
- [ ] IMPLEMENTATION_REPORT.md
- [ ] Ответ в #59 со ссылкой на коммит, закрыть

## Релиз 2

### Этап 7 — переиспользование и fresh на вложенных уровнях

- [ ] Вставки в `dns/client.go` (`DisableCache` от `lxtrace.Fresh`, ответ из кэша → `REUSED` с возрастом) и `conn_pool.go` (`created == false` → `REUSED`, `fresh` → новое соединение)
- [ ] WG/AWG: `wake`, `wg_handshake`; `reuse=uncontrolled` для внешних модулей
- [ ] MASQUE и TrustTunnel `auth`

### Этап 8 — REALITY и fakeip

- [ ] Вердикт в `reality_client.go` (`accepted` / `dest_cert` / `invalid_cert`)
- [ ] lx-доступ к хранилищу fakeip; вставка счётчика оборотов в `store.go`
- [ ] `connectIP` из диапазона fakeip: `route` `FAILED` + выводы

### Этап 9 — выводы

- [ ] `findings_lx.go`: все коды §6 SPEC (`ip_rules_not_evaluated` по индексу выбранного правила, без вставки в `matchRule`)
- [ ] Юниты на синтетических трассах

### Этап 10 — проверка и документы

- [ ] Стенд: добавить Hysteria2 в `lx-test/conntest`
- [ ] Критерии приёмки SPEC 2 (вывод), 4, 5, 6, 7
- [ ] `-race`, сборки, lx-ci, `gofmt` как в этапе 5
- [ ] FEATURE 006, SPEC §10, IMPLEMENTATION_REPORT.md обновлены
