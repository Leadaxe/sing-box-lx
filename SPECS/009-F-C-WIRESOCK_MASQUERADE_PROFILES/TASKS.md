# TASKS — 009-F-O-WIRESOCK_MASQUERADE_PROFILES

## Развилки (зафиксированы, см. PLAN)
- [x] Механизм: **I1 CPS только** (S1–S4 невозможен против WARP; сабмодуль не трогаем)
- [x] QUIC: **1-RTT short header** (не Initial; без SNI/JA3)
- [x] Структуры: порт из WireSock `transform.rs`
- [x] `Ib`: валидация + честная документация минимального эффекта (нет fingerprint)

## Код
- [x] `option/wireguard_awg.go`: +`Id/Ip/Ib string` (json `id`/`ip`/`ib`)
- [x] `transport/wireguard/masque_awg.go`: `masqueI1` диспетчер + валидация
- [x] `validateMasqueDomain` (LDH, зеркало `is_valid_sni_hostname`) — security-граница
- [x] `normalizeMasqueBrowser` (chrome|firefox|curl; только для quic)
- [x] `cpsBuilder` (`<b>`/`<r>`/`<rc>`/`<rd>`)
- [x] DNS → EDNS OPT response (порт `apply_dns_padding`, QNAME из Id)
- [x] STUN → Binding Success Response (порт `apply_stun_padding`)
- [x] SIP → response-текст (порт `apply_sip_padding`, Id как host)
- [x] `transport/wireguard/masque_quic_awg.go`: QUIC 1-RTT short header (порт `apply_quic_padding_short`)
- [x] `device_awg.go`: вызов `masqueI1` в `awgIpcLines`, подстановка как `i1`

## Тест
- [x] `masque_awg_test.go`: структурная валидность каждого профиля обратным парсингом
- [x] QUIC: первый байт `0x40|...`, reserved cleared; браузеры дают разный фикс-байт
- [x] DNS: парсится как EDNS OPT message, QNAME=Id, RDLENGTH/OPTION-LENGTH до конца
- [x] STUN: парсится как Binding Response, length 4-aligned, TLV рамки, SOFTWARE<128
- [x] SIP: status-line + обязательные заголовки + CRLF + Content-Length:0, без тела
- [x] валидация: конфликт с I1, неизвестный ip/ib, пустой id, ib без quic — ошибки
- [x] инъекция домена (CRLF/метасимволы) — отвергается
- [x] `masque_cps_test.go`: верный реплей CPS-парсера (зеркало `newObfChain`)
- [x] прогон всех профилей через реальный `newObfChain` (transient submodule test, удалён)

## Приёмка (DoD)
- [ ] `go build ./...` без тегов — ок
- [ ] `go build -tags "with_wireguard with_gvisor with_awg" ./cmd/sing-box` — ок
- [ ] `go test -tags with_awg ./transport/wireguard/...` — зелёный
- [ ] `sing-box check` на конфигах id/ip/ib (quic/dns/stun/sip); конфликт с i1 отвергнут
- [ ] `lx-test/config/awg2_*.json` — без регресса
- [ ] gating: id/ip/ib без `with_awg` → «awg support not built»
- [ ] `gofmt -l` lx-файлов — пусто

## Закрытие
- [x] адверсариальный ревью генераторов (workflow) — провести и учесть подтверждённое
- [x] `docs/lx-config.md` — секция id/ip/ib (+ссылка на EXAMPLES.md)
- [x] `SPECS/README.md` — roadmap-строка 009
- [x] `IMPLEMENTATION_REPORT.md`
- [x] `EXAMPLES.md` — подробный how-to с примерами (все 4 примера прогнаны через `sing-box check`)
- [x] Коммит `51d5cff1` (по разрешению пользователя), push в `origin/lx`
- [x] Механизм проверен вживую (туннель + трафик на 009)
- [x] Релиз `v1.13.13-lx.11` (тег → `lx-release.yml`); папка `009-F-O-` → `009-F-C-`
