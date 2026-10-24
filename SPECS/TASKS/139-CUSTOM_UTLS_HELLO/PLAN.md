# PLAN 139

1. Добавить тесты конфигурации и реального TLS ClientHello.
2. Добавить поле `client_hello` в `option/tls.go` (один маркированный шов).
3. Добавить малые маркированные швы в `common/tls/utls_client.go` и `common/tls/reality_client.go` для выбора и применения пользовательского профиля.
4. Разместить разбор и проверку профиля в новых файлах с `with_lx_utls_custom`; заглушку — в файле с обратным тегом.
5. Включить тег в Makefile, libbox и LX CI; добавить пример конфига.
6. Проверить тесты, сборку, конфиг и diff.

Зона касания upstream: `option/tls.go` (один блок), `common/tls/utls_client.go` (структура, Client, Clone, конструктор), `common/tls/reality_client.go` (один блок), `cmd/internal/build_libbox/main.go` (один блок). Собственные файлы: `Makefile.lx`, LX CI, пример и тесты. Зависимости не меняются: используется уже подключенный metacubex/utls.
