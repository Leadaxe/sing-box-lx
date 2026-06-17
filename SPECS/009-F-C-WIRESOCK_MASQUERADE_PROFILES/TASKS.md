# TASKS — 009-F-C-WIRESOCK_MASQUERADE_PROFILES

## Решения
- [x] Механизм: **I1 CPS только** (S1–S4 невозможен против WARP; сабмодуль не трогаем)
- [x] QUIC: **out-of-order фрагментированный QUIC Initial** (RFC 9001) с SNI=`id`
- [x] Структуры dns/stun/sip: порт из WireSock `transform.rs`
- [x] `Ib`: валидация (chrome|firefox|curl, только quic); на байты пакета не влияет (нет JA3)

## Код
- [x] `option/wireguard_awg.go`: `Id/Ip/Ib string` (json `id`/`ip`/`ib`)
- [x] `transport/wireguard/masque_awg.go`: `masqueI1` диспетчер + валидация + `cpsBuilder`
- [x] `validateMasqueDomain` (LDH, зеркало `is_valid_sni_hostname`) — security-граница
- [x] `normalizeMasqueBrowser` (chrome|firefox|curl; только quic)
- [x] DNS → EDNS OPT query (`masqueDNSQueryCPS`, QR=0, QTYPE HTTPS, QNAME из `Id`)
- [x] STUN → WebRTC Binding Request (`stun_request_awg.go`, FINGERPRINT + MESSAGE-INTEGRITY)
- [x] SIP → INVITE request + SDP (`sip_invite_awg.go`, PseudoGen-имена, `Id`/псевдо-host)
- [x] `transport/wireguard/quic_initial_awg.go`: varint, рандомизированный frame-план (I1–I4) + `quicGenParams`, сборка Initial
- [x] `transport/wireguard/quic_clienthello_awg.go`: реалистичный TLS 1.3 ClientHello (SNI=`Id`)
- [x] `transport/wireguard/quic_crypto_awg.go`: HKDF / AES-128-GCM / header protection
- [x] `device_awg.go`: вызов `masqueI1` в `awgIpcLines`, подстановка как `i1`

## Тест
- [x] `masque_awg_test.go`: структурная валидность dns/stun/sip обратным парсингом + валидация
- [x] `quic_initial_awg_test.go`: обратный разбор QUIC (decrypt, frame-walk, reassembly, SNI)
- [x] QUIC §5-векторы: AEAD-тег сходится (§5.1); ≥6 CRYPTO/≥1 PING, первый offset≠0 (I1/I2/I3);
      реассембл в валидный ClientHello, SNI=`id` (I4); размер 1250/length 1232; уникальность DCID+random
- [x] QUIC рандомизация: раскладка свежая на вызов, I1–I4 на каждом сэмпле, offset'ы различаются; robustness-ручки
- [x] длинный валидный домен (>77 симв.) генерируется без ошибки (flex-PADDING)
- [x] DNS: парсится как EDNS OPT query (QR=0, QTYPE HTTPS), QNAME=`Id`, RDLENGTH/OPTION-LENGTH до конца
- [x] STUN: парсится как Binding Request (0x0001), FINGERPRINT CRC-32 сходится, USERNAME+MESSAGE-INTEGRITY есть
- [x] SIP: request-line INVITE, обязательные заголовки + To без tag, SDP-тело, Content-Length точна, имена не захардкожены; пустой id → псевдо-host
- [x] валидация: конфликт с I1, неизвестный ip/ib, пустой id (quic/dns), ib без quic — ошибки
- [x] инъекция домена (CRLF/метасимволы) — отвергается
- [x] `masque_cps_test.go`: верный реплей CPS-парсера (зеркало `newObfChain`)
- [x] cross-check: сгенерированный QUIC Initial парсится боевым снифером `common/sniff/quic.go`
      (SNI извлечён, классификация chromium)

## Приёмка (DoD)
- [x] `go build ./...` без тегов — ок
- [x] `go build -tags "with_wireguard with_gvisor with_awg" ./cmd/sing-box` — ок
- [x] `go test -tags with_awg ./transport/wireguard/...` — зелёный
- [x] `sing-box check` на конфигах id/ip/ib (quic/dns/stun/sip); конфликт с i1 отвергнут; пустой id для quic отвергнут
- [x] gating: id/ip/ib без `with_awg` → «awg support not built»
- [x] `gofmt -l` lx-файлов — пусто

## Закрытие
- [x] адверсариальный ревью генераторов (workflow) — проведён, подтверждённые findings учтены
- [x] `docs/lx-config.md` — секция id/ip/ib (+ссылка на EXAMPLES.md)
- [x] `EXAMPLES.md` — how-to с примерами (прогнаны через `sing-box check`)
- [x] `IMPLEMENTATION_REPORT.md`
- [x] Device-результат (LTE/WARP DPI): **только `quic` проходит** (~340 мс); `dns`/`stun` — Timeout
      (DPI режет к WARP-edge `:2408` как класс протокола; `quic` обходит проверку назначения). `sip` — по аналогии.
