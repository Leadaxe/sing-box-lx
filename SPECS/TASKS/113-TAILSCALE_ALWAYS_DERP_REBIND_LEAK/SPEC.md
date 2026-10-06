# SPEC: 113 — TAILSCALE_ALWAYS_DERP_REBIND_LEAK

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B — дефект апстрима tailscale, проявляется в lxd как зависший Stop/Apply |
| Статус | W (wait) — не планируется, решение владельца 2026-10-06; разбор зафиксирован, патч не делаем |
| Ветка | `lx` |
| Base | `be6558602` (v1.14.2-lx.11 + merge upstream/stable) |
| Связанные | Issue [#34](https://github.com/Leadaxe/sing-box-lx/issues/34); SPEC 111, 112 (соседние tailscale-задачи); `lxd/apply.go` (Stop/Apply) |

**Touches:** ничего. Патч потребовал бы форк `sagernet/tailscale` (модуль из GOMODCACHE, не
сабмодуль) или новую опцию в `protocol/tailscale`. Оба варианта описаны ниже и отложены.

## Why

При переменной окружения `TS_DEBUG_ALWAYS_USE_DERP=true` штатные `Stop` и `Apply` lxd
зависают навсегда, если за время работы tailscale-endpoint'а случился хотя бы один
`Rebind()` magicsock. Пока rebind'а не было, остановка проходит нормально, поэтому дефект
выглядит плавающим.

Переменная — отладочная ручка tailscale (принудительно гнать весь трафик через DERP). В наших
спеках и конфигах она не фигурирует; использовалась вручную на стенде для DERP-only проверок.

### Причина

`sagernet/tailscale v1.102.1-sing-box-1.14-mod.5`, `wgengine/magicsock/magicsock.go:3773`,
функция `bindSocket`:

1. Штатная UDP-ветка перед каждой попыткой bind закрывает прежний сокет (`ruc.closeLocked()`
   в цикле по портам), затем ставит новый через `setConnLocked`.
2. Ветка `debugAlwaysDERP()` вызывает `ruc.setConnLocked(newBlockForeverConn(), ...)` **без**
   `closeLocked()`. Прежний pconn остаётся открытым, ссылка на него теряется.
3. `blockForeverConn` — заглушка, чьё чтение блокируется до её собственного `Close()`.
   Приёмная горутина wireguard-go `RoutineReceiveIncoming` (udp4 и udp6) сидит в
   `ReadFromUDPAddrPort` прежней заглушки, и закрыть её больше некому.
4. На остановке `connBind.Close()` и `Conn.Close()` закрывают только текущую заглушку.
5. `device.Close()` в форке wireguard-go ждёт `device.net.stopping.Wait()` — навсегда.
6. Выше по стеку: tsnet `lb.Shutdown` → `userspaceEngine.Close` → `Endpoint.Close` →
   `box.Close` → `StartedService.CloseService()`.
7. В lxd `controller.Stop()` держит `applyAccess` на время `CloseService`; за ним бесконечно
   стоят Apply, Start, Rollback и `shutdown()` демона. `Apply` виснет и сам: `StartOrReloadService`
   сначала закрывает прежний инстанс.

Первый bind в `NewConn` утечки не даёт (прежнего pconn нет). Утечку даёт любой последующий
`Rebind()`: major link change из netmon (смена IP или default-интерфейса, time jump после
сна), `InjectEvent` из `Endpoint.InterfaceUpdated` sing-box, netcheck с ошибкой отправки.

Upstream `tailscale/tailscale` main на 2026-10-06 содержит тот же код (проверено по raw
`wgengine/magicsock/magicsock.go`).

### Воспроизведение

Тест на публичном API `magicsock` без tsnet, логина и второго узла: `NewConn` →
`Bind().Open()` → горутины на receive-функциях → `Rebind()` → `bind.Close()` + `Close()`.

| `TS_DEBUG_ALWAYS_USE_DERP` | `Rebind()` | receive-горутины после Close |
|---|---|---|
| нет | нет | все три вышли |
| нет | да | все три вышли |
| `true` | нет | все три вышли |
| `true` | да | вышла только DERP; udp4 и udp6 висят |

Код теста — в разделе «Тест» ниже; в репозиторий не добавлен (чужой пакет, патч не делаем).

## What

Задача не реализуется. Записаны два пути на случай, если DERP-only режим понадобится
регулярно; выбор — за владельцем.

**A. Форк `sagernet/tailscale` с однострочным патчем.** В ветке `debugAlwaysDERP()`
`bindSocket` вызвать `ruc.closeLocked()` (с тем же игнорированием `net.ErrClosed` и
`errNilPConn`, что в цикле портов) перед `setConnLocked`. Лечит саму переменную на всех
платформах. Цена: пятый форк-сабмодуль (`submodules/tailscale`, `replace` в `go.mod` по
схеме utls/gvisor), сверка патча при каждом бампе `sagernet/tailscale`.

**B. Опция endpoint'а вместо переменной.** lx-поле `force_derp` в `TailscaleEndpointOptions`:
при нём `Endpoint.listenPacket` (`protocol/tailscale/endpoint.go:386`, крючок
`netns.SetListenPacketFunc`) возвращает ошибку вместо сокета. `bindSocket` не биндит ни один
порт и уходит в хвостовую ветку «failed to bind any ports», где прежний сокет уже закрыт циклом
— утечки нет. Без UDP нет STUN и своих endpoint'ов, узел общается через DERP. Без форка, но:
крючок стоит только в ветке без platform-интерфейса (десктоп, lxd; на Android и Apple не
действует), в логе ошибка bind на каждом rebind, health помечает UDP4 unbound, и это новая
фича по тесту §3.1 CONSTITUTION.

### Условие снятия

Апстрим tailscale закрывает прежний pconn в ветке `debugAlwaysDERP()` `bindSocket` (или
убирает ветку). Проверять при бампе `sagernet/tailscale`: `grep -n -A4 "if debugAlwaysDERP()"
wgengine/magicsock/magicsock.go`. Пока условие не выполнено — переменную на стендах с штатным
Stop/Apply не использовать; для разовой DERP-only проверки запускать ядро отдельно и гасить kill.

## Тест

Если задача будет взята в работу, критерий — тот же тест (вариант A: в форке как
`wgengine/magicsock/*_lx_test.go`; вариант B: в `protocol/tailscale`), четыре клетки таблицы
выше зелёные, плюс на стенде lxd: tailscale-endpoint, переменная или опция, смена IP интерфейса,
`Stop` возвращается, `Apply` новой конфигурации проходит.

Эталон (2026-10-06, зелёный на UDP-клетках, красный на DERP+Rebind):

```go
func run(t *testing.T, alwaysDERP bool, rebind bool) {
	envknob.Setenv("TS_DEBUG_ALWAYS_USE_DERP", strconv.FormatBool(alwaysDERP))
	bus := eventbus.New()
	defer bus.Close()
	nm, _ := netmon.New(bus, logger.Discard, nil)
	defer nm.Close()
	c, _ := magicsock.NewConn(magicsock.Options{
		EventBus: bus, Logf: t.Logf, NetMon: nm, DisablePortMapper: true,
		Metrics: new(usermetric.Registry), HealthTracker: health.NewTracker(bus),
	})
	bind := c.Bind()
	fns, _, _ := bind.Open(0)
	exited := make(chan int, len(fns))
	for i, fn := range fns {
		go func(i int, fn conn.ReceiveFunc) {
			n := bind.BatchSize()
			bufs := make([][]byte, n)
			for j := range bufs {
				bufs[j] = make([]byte, 65535)
			}
			for {
				if _, err := fn(bufs, make([]int, n), make([]conn.Endpoint, n)); err != nil {
					exited <- i
					return
				}
			}
		}(i, fn)
	}
	time.Sleep(100 * time.Millisecond)
	if rebind {
		c.Rebind()
	}
	bind.Close()
	c.Close()
	for range fns {
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Fatal("receive goroutine still blocked 5s after Close — leaked pconn")
		}
	}
}
```

## Документы

- `SPECS/README.md` (Roadmap), `SPECS/FEATURES/004-HOTFIXES/FEATURE.md` (реестр): строка задачи.
- Issue #34: описание проблемы, закрыт со ссылкой на эту спеку как «не планируется».
