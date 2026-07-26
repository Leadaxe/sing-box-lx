# TASKS 037 — GetRunningConfig

- [x] Шов в `started_service.proto`: rpc + `message RunningConfig` (lx-маркеры)
- [x] Регенерация pb пиненым тулчейном (`protogen` + `gofumpt`), ревёрт non-SPEC-шума
      (submodule `wireguard-go`, `route/rule/rule_item_rule_set_test.go`)
- [x] `Instance.runningConfig` + захват в `newInstance` (lx-швы в `instance.go`)
- [x] `captureRunningConfig` — tag-пара `instance_command_lx{,_stub}.go`
- [x] Handler в `started_service_command_lx.go` + stub-двойник
- [x] `CommandClient.GetRunningConfig()` в `command_client_command_lx.go`
- [x] Юниты: round-trip захвата, машина состояний handler'а, stub-эквивалентность
- [x] Сборки: `daemon`+`libbox` с тегом/без; полный бинарь (LX-теги минус
      `badlinkname`/naive, go1.25-хост); `check -c lx-test/config/minimal.json`
- [x] `gofmt -l` чистый по lx-файлам
- [x] SPEC/PLAN/TASKS/REPORT + Roadmap + FEATURE.md (OBSERVABILITY)
- [ ] Полевая проверка из LxBox после AAR-релиза
