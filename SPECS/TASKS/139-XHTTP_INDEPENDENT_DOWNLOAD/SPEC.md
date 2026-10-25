# SPEC: 139 — XHTTP_INDEPENDENT_DOWNLOAD

**Фича:** [002-XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F — расширение совместимости существующего XHTTP-клиента |
| Статус | W — draft внешнего участника; согласование владельца и полная приёмка не завершены |
| Ветка | `codex/xhttp-independent-downlink` |
| Build-tag | `with_xhttp` на включении транспорта, как у существующей фичи |
| Потребитель | NOVA Android: раздельный uplink/downlink Xray-конфигураций |
| Предложение | Draft PR #57; согласованного issue владельца пока нет |

## Контракт

Опциональный `download_settings` задаёт `server`, `server_port`, собственный `tls`, опциональный `dialer` и вложенный `xhttp`. Без него прежнее поведение сохраняется. Нет `tls` — открытый HTTP; TLS uplink не наследуется. Нет `dialer` — наследуется dialer uplink. Пулы XMUX и TLS-идентичности разделены даже при одинаковом адресе. Отказы download не должны скрываться fallback на upload; закрытие/отмена освобождает обе аренды.

Явный `stream-one`, неполный endpoint и рекурсивный `download_settings` отклоняются. `auto` с REALITY uplink и отдельным downlink выбирает `stream-up`; прежний `auto`+REALITY без отдельного downlink сохраняет `stream-one`.

## Изоляция и стоимость

Добавляется опция в `option/v2ray_xhttp.go`; меняются `client.go`, `conn.go`, `xmux.go` внутри уже существующего downstream-пакета XHTTP. Четыре новых файла: структура download-опций, реализация настройки download, обычные тесты и QUIC-тесты; Windows interop-тест — отдельный файл. Зависимости, go.mod и сабмодули не меняются. Возможные конфликты следующего синка локальны к этим четырём существующим файлам XHTTP.

## Приёмка

См. TASKS и IMPLEMENTATION_REPORT. Синтетика и официальный Xray на loopback нужны, но не заменяют независимый REALITY, обе bootstrap DNS-ветви, долгие/конкурентные сессии и физическую Android-приёмку. HTTP3-фронтенд перед H2 Xray не является прямым Xray H3 ingress.

## Условие снятия

Если поставляемая upstream-поверхность sing-box реализует тот же контракт, заменить downstream-код upstream-формой после проверки совместимости конфигураций.
