# Отчёт 139 — draft

**Фича:** [002-XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

На 25.10.2026 код перенесён из переданного NOVA WIP на закреплённый sing-box-lx 20f89c2. Исходный импорт сохраняется отдельным коммитом; история не переписывалась. Требование разнести исходные правки по зонам при окончательном принятии нужно согласовать с владельцем.

Проверки: Windows Go 1.25.5, `gofmt`, `go vet -p 2 -tags with_xhttp,with_quic,with_utls,with_reality ./transport/v2rayxhttp ./option`, затем `go test -count=1` для тех же пакетов. Результат: 298 тестов/подтестов pass, 0 fail, 0 skip, включая 27 официальных Xray-сценариев. CI: https://github.com/leoraijin/sing-box-lx/actions/runs/40208616205 .

Дополнительный CI: https://github.com/leoraijin/sing-box-lx/actions/runs/40209154598 — Windows regression и Linux `go test -race -count=5` успешны. Это не долгосрочное нагрузочное испытание.

Xray v26.10.21: SHA256 ZIP `fc58c39ee2f2717b4c54d28d374362389e5319b20ae44aea76ea45a5243ec827`, EXE `c77a00eb84fb93ae044c2d771a1cabcc033b81e487253fbc35b12df6879c1e15`.

H3 проверяется на двух TLS-фронтендах перед одним официальным H2 Xray. Ни прямой H3 ingress Xray, ни независимая REALITY-аутентификация этим не доказаны. Незакрытый список — TASKS.md. Production AAR NOVA не заменён.
