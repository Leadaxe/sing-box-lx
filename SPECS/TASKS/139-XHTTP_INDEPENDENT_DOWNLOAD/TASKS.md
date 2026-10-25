# Задачи 139

**Фича:** [002-XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

- [x] Независимые endpoint/TLS/dialer/XMUX и запрет некорректных настроек.
- [x] Раздельное считывание health и освобождение обеих аренд при ошибках/отмене.
- [x] Windows gofmt/vet и тесты с checksum-pinned официальным Xray.
- [x] 27 комбинаций auto/packet-up/stream-up и H1/H2/H3 фронтендов, с реальным VLESS echo.
- [x] Пять повторов тестов под Linux race detector.
- [ ] Согласование владельца/issue и номера задачи.
- [x] Host red/green: IP uplink + domain downlink и оба домена через искусственный core bootstrap DNS.
- [x] Host REALITY: отдельные ключи/SNI/short_id, key share, отрицательные сценарии и HTTP/2 обмен через native frontend.
- [ ] Прямой официальный Xray REALITY ingress, доверенный camouflage control и Android TUN/DNS bootstrap обоих направлений.
- [x] Host H2/H2: 12 постоянных VLESS сессий, по 300 секунд в трёх режимах, лимиты XMUX и освобождение аренд.
- [ ] Длительные H1/H3 и реальные смены сети.
- [x] Полная host-сборка `go build ./...` без тегов.
- [x] CLI с точным каноническим LX_TAGS из Makefile.lx и check примера независимого downlink.
- [x] CLI: три корректных и пять отклоняемых синтетических конфигураций, без запуска туннеля.
- [ ] Полный `go vet ./...`: три unsafe.Pointer предупреждения в неизменённой базе; vet XHTTP/option зелёный. Полный DoD не закрыт.
- [ ] Физическая Android ARM64/16КБ/TUN приёмка.
- [ ] Финальная приёмка владельцем; статусы C/D не присвоены.
