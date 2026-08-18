# SPEC: 070 — WG_START_CLOSE_RACE_CRASH → поглощена задачей 072

Эта задача слита в [072-WG_DETOUR_LIFECYCLE_FREEZE](../072-WG_DETOUR_LIFECYCLE_FREEZE/SPEC.md) —
единого владельца семьи полевых отказов жизненного цикла WG-эндпоинта
(решение владельца 2026-08-18, см. [HISTORY](../072-WG_DETOUR_LIFECYCLE_FREEZE/HISTORY.md)).

Механизм жив и не менялся: `Start(stage)` под `resumeMu` с гейтом `closing`
(`protocol/wireguard/endpoint.go`) + CAS-идемпотентность `Box.Close`
(`box.go`), маркеры `// lx: SPEC 070` сохранены. Актуальное состояние,
верификация и условия снятия — в SPEC.md задачи 072. Полный исходный текст —
в git-истории этого файла.
