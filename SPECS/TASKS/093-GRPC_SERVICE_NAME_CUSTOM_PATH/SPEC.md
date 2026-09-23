# SPEC: 093 — GRPC_SERVICE_NAME_CUSTOM_PATH

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) (файлы `transport/v2raygrpclite/{client,server}.go`,
`transport/v2raygrpc/{custom_name,server}.go` — апстримные; helper — форк-нативный пакет)

| Поле | Значение |
|------|----------|
| Тип | I (interop) — совместимость с Xray-серверами, у которых `serviceName` задан в форме абсолютного пути |
| Статус | D (done, device-verified) — юниты и h2-стенд red/green; против живого Xray не прогонялось, выпущено в `v1.14.1-lx.8` по слову владельца 2026-09-18 (сверка по коду §1.0); закрыта решением владельца 2026-09-24: релизы более недели в поле без жалоб |

gRPC-транспорт (`v2raygrpclite` — то, что отгружается в наших сборках; `v2raygrpc` под
`with_grpc`) не умеет **многосегментный** путь: `service_name: "a/b"` уходит на провод как
`/a%2Fb/Tun`, и сервер Xray, сконфигурированный формой `"/a/b/Tun"`, отвечает 404 (строгое
сравнение пути). Переносим конвенцию Xray: **ведущий `/` в `service_name` = custom path**
(сегменты экранируются по отдельности, последний сегмент — имя стрима), имя без ведущего `/`
ведёт себя как сейчас.

Build-tag: нет. Scope: клиент (outbound) **и** сервер (inbound, второй приоритет — та же
конвенция нужна для симметрии lite client↔lite server и с Xray-клиентами).

---

## 1. Проблема

Заявка лаунчера 2026-09-18 (singbox-launcher #130): сервер Xray с `serviceName: "/a/b/Tun"`
недостижим из sing-box. Разбор 2026-09-03 по тому же классу жалоб (см. память
`grpc-service-name-slash-xray-convention`) уже установил:

- Xray (`transport/internet/grpc/config.go`, сверено 2026-09-18 по `main`):

  ```go
  func (c *Config) getServiceName() string {
      if !strings.HasPrefix(c.ServiceName, "/") { return url.PathEscape(c.ServiceName) }   // старая форма
      lastIndex := strings.LastIndex(c.ServiceName, "/")
      if lastIndex < 1 { lastIndex = 1 }
      rawServiceName := c.ServiceName[1:lastIndex]
      parts := strings.Split(rawServiceName, "/")
      for i := range parts { parts[i] = url.PathEscape(parts[i]) }
      return strings.Join(parts, "/")
  }
  func (c *Config) getTunStreamName() string {
      if !strings.HasPrefix(c.ServiceName, "/") { return "Tun" }
      endingPath := c.ServiceName[strings.LastIndex(c.ServiceName, "/")+1:]
      return url.PathEscape(strings.Split(endingPath, "|")[0])
  }
  ```

  Путь на проводе = `"/" + getServiceName() + "/" + getTunStreamName()`.
  Хвост `|…` после имени стрима — имя multi-стрима Xray (`TunMulti`), у нас не поддерживается
  и отбрасывается так же, как Xray отбрасывает его для `Tun`.

- Наш lite-клиент: `RawPath: "/" + url.PathEscape(ServiceName) + "/Tun"` — для старой формы
  это **ровно** Xray (`%2F`), апстрим-коммит d5f94b65b сделал это намеренно. Поэтому
  предложение «экранировать посегментно безусловно» **отвергается**: оно сломает
  документированную форму, которая сейчас работает с Xray.
- Наш lite-сервер сравнивает декодированный `request.URL.Path` с `"/" + ServiceName + "/Tun"`.
- Полный клиент `v2raygrpc` (`TunCustomName`) шлёт сырой `"/" + name + "/Tun"` (без
  экранирования — расходится с Xray для старой формы со слэшем; это апстримное поведение
  под `with_grpc`, **в этой задаче не меняем**).

### 1.0 Сверка с кодом Xray и grpc-go (2026-09-18, по исходникам, без живого прогона)

| Сторона | Что делает | Где |
|---|---|---|
| Xray-клиент | `NewStream(…, "/"+getServiceName()+"/"+getTunStreamName())` — в `:path` уходят уже экранированные части, без повторного кодирования | `transport/internet/grpc/encoding/customSeviceName.go`, `TunCustomName` |
| Xray-сервер | `RegisterGRPCServiceServerX(s, l, getServiceName(), getTunStreamName(), …)` → `grpc.ServiceDesc{ServiceName: <escaped>, StreamName: <stream>}` | `hub.go`, `customSeviceName.go` |
| grpc-go сервер | `:path` берётся **сырым** (`s.method = hf.Value`), режется по **последнему** `/`: до него — имя сервиса, после — метод; поиск в карте по строке, без декодирования | `internal/transport/http2_server.go`, `server.go` `handleStream` |

Следствия:

- совпадение решает **побайтовое** равенство `:path` клиента и `"/"+service+"/"+stream` сервера —
  ровно то, что пинит `TestWirePath` / `TestXrayPathInterop` (провод снимается голым h2c-листенером);
- для `/a/b/Tun` grpc-go получает сервис `a/b`, метод `Tun` — совпадает с регистрацией Xray;
  краевой `/Tun` даёт `//Tun` → сервис пустой, метод `Tun` — тоже совпадает;
- голый grpc-go на неизвестный путь отвечает HTTP 200 + `grpc-status: 12 (Unimplemented)`, **не 404**.
  Значит `404 Not Found` из issue #130 отдаёт фронт перед Xray (nginx `location /xxx/something/Tun`
  или fallback): nginx сопоставляет location по декодированному URI, и наш прежний
  `/%2Fxxx%2Fsomething%2FTun/Tun` превращался в `/xxx/something/Tun/Tun` — мимо location. После
  фикса путь равен location дословно;
- multi-режим Xray-клиента (`TunMulti`) ходит на другой метод — наш сервер его не обслуживал и
  не обслуживает (границы §5).

Чего сверка по коду **не** даёт: поведение конкретного фронта (nginx/caddy/CDN) и TLS/ALPN-обвязку —
это закрывает только живой прогон (§3).

### 1.1 Таблица форм (после фикса)

| `service_name` | Путь на проводе | Сервис / стрим | Примечание |
|---|---|---|---|
| `TunService` | `/TunService/Tun` | `TunService` / `Tun` | как сейчас |
| `a/b` | `/a%2Fb/Tun` | `a%2Fb` / `Tun` | как сейчас, = Xray старой формы |
| `/a/b/Tun` | `/a/b/Tun` | `a/b` / `Tun` | **новое**, = Xray custom path |
| `/a/b/Stream` | `/a/b/Stream` | `a/b` / `Stream` | **новое**, имя стрима из конфига |
| `/a b/Tun` | `/a%20b/Tun` | `a%20b` / `Tun` | посегментный PathEscape |
| `/a/b/Tun\|TunMulti` | `/a/b/Tun` | `a/b` / `Tun` | хвост multi отброшен, как у Xray |
| `/Tun` | `//Tun` | `` / `Tun` | краевой случай Xray (`lastIndex<1 → 1`), повторяем **дословно** |

## 2. Требования

1. Форк-нативный helper `common/grpcname` (новый пакет, без build-тегов):
   - `Split(serviceName string) (service, stream string)` — точный порт двух функций Xray
     выше (возвращает уже **экранированные** части).
   - `RawPath(serviceName string) string` = `"/" + service + "/" + stream`.
   - `Path(serviceName string) string` = `url.PathUnescape(RawPath)` (декодированная форма для
     сравнения на сервере и для `url.URL.Path`); при ошибке unescape — RawPath как есть.
   - `IsCustom(serviceName string) bool` — есть ли ведущий `/` (нужен `v2raygrpc`, где старая
     форма остаётся неэкранированной).
   Табличный юнит по §1.1 целиком, плюс `a%2Fb` для старой формы.
2. `transport/v2raygrpclite/client.go`: `Path: grpcname.Path(name)`, `RawPath: grpcname.RawPath(name)`
   вместо текущих двух строк. Маркер `// lx: SPEC 093`.
3. `transport/v2raygrpclite/server.go`: `path: grpcname.Path(name)` вместо `"/" + name + "/Tun"`.
   Сравнение остаётся по декодированному `request.URL.Path`. Маркер.
   ⚠️ Сравнивать по `EscapedPath()` **нельзя** (пробовали при реализации): `with_grpc`-клиент
   шлёт старую форму неэкранированной (`/a/b/Tun` для `a/b`), и сейчас lite-сервер `a/b` его
   принимает — строгое сравнение сломало бы эту пару. Цена: lite-сервер мягче Xray, сервер
   `a/b` и сервер `/a/b/Tun` принимают одно и то же множество путей.
4. `transport/v2raygrpc/custom_name.go`:
   - `ServerDesc(name)` → для custom-формы `ServiceName = service`, `StreamName = stream`;
     для старой формы — как сейчас (сырое имя, `Tun`).
   - `TunCustomName`: метод = `grpcname.RawPath(name)` для custom-формы; для старой формы —
     как сейчас (`"/" + name + "/Tun"`, без экранирования — апстримное поведение).
   Маркер. `server.go` не трогать (он зовёт `RegisterGunServiceCustomNameServer` → `ServerDesc`).
5. Ничего в `option` не добавлять: форма задаётся содержимым `service_name`, как у Xray.
   Валидации на «`/` в конце» и т.п. не вводить — повторяем Xray, включая краевые случаи.

## 3. Критерии приёмки

- `common/grpcname`: табличный юнит §1.1.
- `transport/v2raygrpclite`: h2-стенд «lite client → lite server» на loopback (образец —
  `stream_error_test.go` того же пакета, если там есть обвязка; иначе минимальный
  `httptest`/`net.Pipe`-стенд): для `service_name` ∈ {`TunService`, `a/b`, `/a/b/Tun`, `/a/b/Stream`}
  клиент и сервер сходятся (сервер не отвечает 404 «bad path»); кросс-проверка: клиент
  `/a/b/Tun` против сервера `a/b` **проходит** (сервер сравнивает декодированный путь, §2.3),
  клиент `/a/b/Stream` против сервера `/a/b/Tun` → 404. Пара lite↔lite red-check не даёт (до
  фикса обе стороны ошибались одинаково), поэтому red-check — на проводе: голый h2c-листенер
  снимает `:path` клиента и сверяет с тем, что вычисляет Xray (`TestWirePath`,
  `TestXrayPathInterop`); до фикса клиент слал `/%2Fa%2Fb%2FTun/Tun` вместо `/a/b/Tun`.
  Страж старой формы — `TestOldFormUnchanged` (Path/RawPath байт в байт).
- **Живой Xray обязателен до релиза** (стенд как в SPEC 083: Xray darwin-arm64 из GitHub
  Releases на loopback, inbound trojan или vless + grpc): сервер с `serviceName: "/a/b/Tun"` —
  наш lite-клиент с `service_name: "/a/b/Tun"` проходит, до фикса даёт
  `v2ray-grpc: unexpected status: 404 Not Found` (симптом issue лаунчера #130); регресс-кейсы
  против того же Xray: `TunService` и старая форма `a/b` (сервер `serviceName: "a/b"`) ходят
  как до фикса. Без этого прогона статус задачи не выше «реализовано, не проверено против Xray».
- `go build` под `with_grpc` и без; `go vet` обоих пакетов.
- `sing-box check` на конфиге с `"service_name": "/a/b/Tun"` проходит (валидации нет и не
  должно быть).

## 4. Документация

- `docs-lx/lx-protocols-transports.md` + `.ru.md`: короткий подраздел «gRPC `service_name`:
  формы Xray» с таблицей §1.1 (без краевого `/Tun`), пометка, что `with_grpc`-клиент для
  старой формы не экранирует (апстрим).
- Реестр HOTFIXES: строка [093] — патч в апстримных файлах lite/grpc; условие снятия —
  апстрим примет конвенцию custom path (или эквивалент) в `v2raygrpclite`; маркер проверять
  на каждом мерже. Helper `common/grpcname` — форк-нативный, снятию не подлежит.
- `SPECS/README.md` строка **093**; changelog `#### v1.14.1-lx.8`; ноты `docs-lx/releases/v1.14.1-lx.8.md`.

## 5. Границы

- Старая форма (`TunService`, `a/b`) — байт в байт как сейчас, на обоих транспортах.
- Multi-стрим Xray (`TunMulti`, хвост `|…`) не реализуется — хвост отбрасывается.
- XHTTP `path`, `host` и прочие транспорты не затрагиваются.
- Лаунчер и LxBox свои подсказки/санитайзеры по `service_name` (перевод `/svc/Tun` → `svc`)
  могут снять после пина на lx.8: ядро теперь принимает форму `/svc/Tun` напрямую и даёт тот
  же провод, что и `svc`.
