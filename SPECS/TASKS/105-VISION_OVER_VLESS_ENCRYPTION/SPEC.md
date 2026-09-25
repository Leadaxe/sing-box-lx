# SPEC: 105 — VISION_OVER_VLESS_ENCRYPTION

**Фича:** [VLESS_ENCRYPTION](../../FEATURES/012-VLESS_ENCRYPTION/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — зазор нашего слоя шифрования (SPEC 032) с Vision из sing-vmess |
| Статус | I (implemented) — юнит и страж в `protocol/vless/encryption/lx_vision_test.go` (red-check: ошибка §1), стенд §4 п.4 зелёный |
| Ветка | `lx` |
| База | `16de9e9ad` (`v1.14.2-lx.2`) |
| Issue | [#29](https://github.com/Leadaxe/sing-box-lx/issues/29) |
| Связано | [032](../032-VLESS_ENCRYPTION_MLKEM768/SPEC.md) (слой шифрования) |

Заявка пришла со стороны LxBox 2026-09-25: узлы подписки с `flow: xtls-rprx-vision` и `encryption: mlkem768x25519plus…` одновременно не подключаются ни на `tcp`+REALITY, ни на `xhttp`+REALITY. Vision без шифрования и шифрование без Vision у того же провайдера работают. Стенд и реквизиты лежат в `config.d/vision-over-vless-encryption/` (вне git).

---

## 1. Дефект

`protocol/vless/outbound.go` оборачивает транспортный conn в `*encryption.CommonConn` до VLESS-клиента, и в `vless.Client.prepareConn` этот conn приходит как `tlsConn`. `NewVisionConn` из sing-vmess v0.2.8 (`vless/vision.go`) ищет под собой TLS через реестр `tlsRegistry`, где записаны только `*tls.Conn` и utls. `CommonConn` там нет, поэтому соединение падает сразу:

```
initialize vision: vision: not a valid supported TLS connection: *encryption.CommonConn
```

## 2. Как это сделано в Xray (эталон)

Ссылки по `xray-core` из go.mod-кэша (`v1.260327.1-0.20260901045710-cd4ce973e9f6`).

- `proxy/vless/outbound/outbound.go`: для Vision поверх шифрования `input` и `rawInput` берутся reflect'ом из `*encryption.CommonConn`, так же как из `tls.Conn`. Поля с этими именами у `CommonConn` заведены ради Vision.
- `proxy/proxy.go`, `UnwrapRawConn`: прямая запись и чтение после того, как Vision увидел внутренний TLS 1.3, идут в `commonConn.Conn`, то есть в слой под шифрованием. Внешний TLS/REALITY при этом не снимается (флаг `isEncryption`). Если под шифрованием `XorConn`, байты идут в него.
- Транспорт любой, `xhttp` тоже. Не-RAW транспорт и `XorConn` только запрещают kernel splice (`CanSpliceCopy = 3`), прямое копирование остаётся.

Наш `protocol/vless/encryption/common.go` повторяет структуру Xray: поля `rawInput bytes.Buffer` и `input bytes.Reader` уже на месте.

## 3. Решение

Одна запись в `tlsRegistry` sing-vmess для `*encryption.CommonConn`:

- `netConn` = `commonConn.Conn` (слой под шифрованием, как в `UnwrapRawConn` Xray);
- `reflectType` = `reflect.TypeOf(commonConn).Elem()`, `reflectPointer` = адрес `commonConn`.

Реестр неэкспортирован, поэтому доступ через `//go:linkname` на переменную `github.com/sagernet/sing-vmess/vless.tlsRegistry` из нового lx-файла `protocol/vless/encryption/lx_vision.go`. Ограничение Go 1.23 на pull-linkname касается только стандартной библиотеки, флаг `-checklinkname=0` не нужен. Пакет импортирует `sing-vmess/vless`, поэтому его `init` с базовыми записями отработает раньше нашего.

Upstream-файлы и sing-vmess не меняются, провод и опции тоже.

Отвергнуто:
- форк sing-vmess — пятый форк-сабмодуль ради одной записи;
- своя реализация Vision — копия чужого кода.

## 4. Критерии приёмки

1. Юнит: `vless.NewVisionConn` принимает `*encryption.CommonConn`, а `input`/`rawInput` указывают на поля этого conn'а. Red-check: без записи в реестре — ошибка из §1.
2. Страж-тест на имя и тип `tlsRegistry`: при бампе sing-vmess, где реестр переименован или сменил сигнатуру, сборка или тест падает, а не ломается Vision молча.
3. `go test ./protocol/vless/...`, `go vet`, gofmt, сборка `make -f Makefile.lx lx-build`.
4. Стенд `config.d/vision-over-vless-encryption/`: порты 20801 и 20802 (Vision + шифрование; tcp и xhttp) отдают 204; 20804–20806 не ломаются; 20803 (без flow) остаётся ❌, сервер требует Vision.

Итог 2026-09-25 (Mac, бинарь `lx-build` с правкой): 20801 и 20802 — 204 по HTTP и HTTPS, три попытки из трёх; загрузка 20 МБ по HTTPS через оба порта проходит (режим прямого копирования после внутреннего TLS 1.3); 20804–20806 — 204; 20803 — ❌. Пункты 1–3 выполнены.

## 5. За чем следить

- Бамп sing-vmess: имя и сигнатура `tlsRegistry`, выбор полей по именам `input`/`rawInput`. Ловит страж-тест §4 п.2.
- Мерж SPEC 032 с Xray: при переименовании полей `CommonConn` Vision получит нулевые смещения. Ловит юнит §4 п.1.
- Kernel splice у нас не используется, поэтому ограничение Xray `CanSpliceCopy = 3` для `XorConn` и не-RAW транспорта переносить не нужно.
