# PLAN: 150 — QUIC_DETOUR_OVERSIZE_DATAGRAM

Контракт — [SPEC.md](SPEC.md) (EN, основной) / [SPEC.ru.md](SPEC.ru.md).

## Этап 1. Хелпер

1. `common/lxdatagram/truncate.go`: `TruncateReads(conn, logger)` по SPEC §2.1 — проверка
   `N.UnwrapReader(conn).(syscall.Conn)` → сокет ядра проходит как есть; иначе `truncConn`
   с буфером 65535 под мьютексом, `Read` копирует `min(n, len(p))`, хвост выбрасывается, первое
   усечение — один раз на debug; `Upstream()`, `ReaderReplaceable() == false`.
   `Dialer(dialer, logger)` — обёртка `N.Dialer`: `DialContext` для `udp` → `TruncateReads`,
   остальное насквозь.
2. `common/lxdatagram/truncate_test.go` по SPEC §3: усечение и границы датаграмм, проход сокета
   ядра, `Dialer` только для `udp`, порядок с `deadline.NewConn` в обе стороны, прозрачность
   `NeedAdditionalReadDeadline`.

## Этап 2. Швы

1. `protocol/masque/outbound.go`, `connectH3WithBudget`: `rawConn` стартует с
   `lxdatagram.TruncateReads(udpConn, o.logger)`, дальше проверка SPEC 146 как была (одна строка
   `// lx: SPEC 150`).
2. `protocol/hysteria2/outbound.go`: `Dialer: lxdatagram.Dialer(outboundDialer, logger)` в
   `hysteria2.NewClient`.
3. `protocol/tuic/outbound.go`: то же в `tuic.NewClient`.
4. `protocol/masque/oversize_datagram_lx_test.go`: red/green на `connectH3WithBudget` с
   кадрирующим фальшивым detour-conn (SPEC §3).

## Этап 3. Сборка и проверка

- `go test -race -ldflags "-checklinkname=0"` с тегами lx-ci: `common/lxdatagram`,
  `protocol/masque/...`, `protocol/hysteria2`, `protocol/tuic`; red-check — тест masque на дереве
  без шва §2.1.
- `go build` с `LX_TAGS`; gofmt новых и тронутых файлов; `go vet` пакетов.
- `.github/workflows/lx-ci.yml`: три пакета в список.
- Живой прогон на Mac: финальная сборка, MASQUE h3 через релеи §1.1 с UDP (стенд в scratchpad `bl/`).

## Этап 4. Доки

FEATURE 009 (строка задачи, пункт в «Особенности сопровождения»), FEATURE 004 (строка реестра для
hysteria2/tuic с условием снятия), индекс фич, Roadmap в `SPECS/README.md`; секция changelog — при
релизе.
