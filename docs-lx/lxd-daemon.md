# Демон lxd: что это и как настроить

Практическое руководство оператора: что такое `sing-box lxd`, зачем он нужен, как
ставится на macOS и какие есть подходы на Linux. Актуальное состояние функций —
[FEATURE 014-LXD_DAEMON](../SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md); детали
решений — SPECS/TASKS [055](../SPECS/TASKS/055-LXD_DAEMON_SKELETON/SPEC.md),
[056](../SPECS/TASKS/056-LXD_APPLY_ROLLBACK/SPEC.md),
[057](../SPECS/TASKS/057-LXD_MTLS_SERVICE/SPEC.md).

---

## 1. Что это и зачем

`sing-box lxd` — **демон, который хостит ядро sing-box внутри себя** и выставляет
долгоживущий канал управления. Ключевое отличие от `sing-box run`:

- **Канал переживает reload.** У `run` смена конфига = рестарт процесса: клиент
  (лаунчер) теряет соединение, стримы статусов и наблюдаемости рвутся. У lxd
  слушающий порт принадлежит демону, а не box-инстансу: apply подменяет ядро
  «под» живым сервером, клиент видит переходы STARTED/STOPPING/… одним стримом
  без переподключения.
- **Демон доступен именно тогда, когда данные не ходят.** Канал управления
  поднимается ДО ядра: битый или отсутствующий конфиг оставляет демон на связи —
  чинить конфиг можно по тому же каналу, который нужен как раз при лежащем
  data-plane.
- **Конфиг с гарантиями.** `POST /admin/apply` валидирует кандидата
  (`sing-box check` собственным бинарником), при провале старта откатывается на
  последний рабочий конфиг (**last-good**), помнит прерванный apply и
  run-намерение (`was_running`) через рестарты и ребуты.
- **Удалённое управление с доверием.** mTLS: демон — сам себе CA, клиенты
  регистрируются одноразовым инвайтом и дальше опознаются по сертификату.

Типовые роли: локальный движок для лаунчера на том же маке (канал переживает
reload'ы) и **удалённый узел** — например, ядро на роутере/сервере, которым
лаунчер управляет по сети.

Один порт несёт две плоскости:

| Плоскость | Протокол | Что даёт |
|---|---|---|
| наблюдаемость | gRPC `daemon.StartedService` | статусы, логи, группы/urltest, DNS-поток, соединения — общий протокол с Android-линейкой |
| администрирование | REST `/admin/*` | apply / rollback / start / stop / config / status / info, enrollment клиентов |

## 2. Быстрый старт (dev, без установки)

```bash
sing-box lxd --state-dir lxd-state -c config.json
```

Без `daemon.json` действуют **dev-дефолты**: plain h2c на `127.0.0.1:9091`, без
секрета, без mTLS и без реестра клиентов. Лог — на экран. `-c` — необязательный
seed: используется только пока нет last-good; без него демон стартует пустым
(IDLE) и ждёт первый apply.

Проверка:

```bash
curl -s http://127.0.0.1:9091/admin/status
```

## 3. daemon.json — настройки демона

Живёт в `<state-dir>/daemon.json` (0600). **Единственный** источник
connection-настроек: у команды нет флагов `--listen/--tls/--secret` по
построению — вопрос «файл или флаг» не существует. Нет файла → dev-дефолты;
файл никогда не создаётся неявно (его пишет `--service=install` на macOS или
редактор оператора).

| Ключ | Дефолт | Значение |
|---|---|---|
| `listen` | `127.0.0.1:9091` | адрес канала (обе плоскости); для доступа с другой машины — LAN-адрес |
| `tls` | `false` | mTLS с регистрацией клиентов; `false` = plain h2c, только loopback/dev |
| `secret` | пусто | Bearer операторских маршрутов; единственный гейт при `tls: false` (пусто = аутентификации нет) |
| `log_max_size_mb` | `20` | ротация лога: страховочный потолок размера |
| `log_max_backups` | `1` | сколько старых поколений (`lxd.log.1…N`) хранить |
| `log_max_age_hours` | `24` | ротация по возрасту файла |

Дефолты ротации дают «≈сутки истории»; 0/отсутствие ключа = дефолт,
«безлимита» нет намеренно. Смена любых настроек = правка файла + рестарт службы,
не переустановка.

## 4. Ключи командной строки

| Ключ | Значение |
|---|---|
| `--state-dir <dir>` | дом демона: daemon.json, last-good, run-state, реестр клиентов, ключи (дефолт `lxd-state`) |
| `-c <файл>` | seed-конфиг (строго один файл; каталоги `-C` не поддерживаются) |
| `--config-force <файл>` | всегда бутиться с этого файла, поверх last-good |
| `--run` | поднять ядро независимо от записанного run-состояния |
| `--service install\|install-user\|uninstall\|print` | установка службой (см. разделы ОС) |
| `--purge` | с `uninstall` — снести и state-каталог |
| `client add [--name <метка>]` | сминтить одноразовый инвайт для нового клиента |
| `client list` / `client remove <имя-или-отпечаток>` | просмотр / отзыв доверенных клиентов |

Сабкоманда существует только в сборках с тегом `with_lx_command`.

## 5. Безопасность: кто чем аутентифицируется

- **Клиент (лаунчер)** — доверенным сертификатом, полученным при enrollment.
  Сертификат — полный мандат обеих плоскостей; Bearer клиенту не нужен и секрета
  он не знает.
- **Оператор (человек с шеллом на хосте)** — Bearer-секретом из daemon.json на
  **loopback-only** маршрутах (`client add/list/remove`). Минт инвайта = выдача
  доверия, поэтому из сети эти маршруты недоступны в принципе.
- **Enrollment** — единственный маршрут до доверия: одноразовый код
  (`адрес#отпечаток-сервера#код`), сгорает на первом использовании; клиент пинит
  сервер по отпечатку.
- При `tls: false` (dev) весь гейт — Bearer; без секрета аутентификации нет —
  поэтому plain-режим только на loopback.

## 6. Логи

Под службой (stdout — не терминал) демон **сам владеет** файлом
`<support>/lxd.log`: перехватывает stdout/stderr процесса (в файл попадает всё,
включая лог ядра и паники) и ротирует по возрасту/размеру с лимитами из
daemon.json. При ручном запуске в терминале лог остаётся на экране, файл не
трогается. Путь к логу и state-dir клиент узнаёт из `GET /admin/info` — ничего
хардкодить не нужно. Реализовано на macOS и Linux; на Windows — нет (как и
служба).

## 7. macOS — автоматическая установка

Единственная платформа с полным `--service`. Две области:

```bash
sudo sing-box lxd --service=install    # системный LaunchDaemon: root, старт до логина, TUN
sing-box lxd --service=install-user    # LaunchAgent: без sudo, старт при логине, десктоп-UX
```

Install делает всё сам:

1. создаёт `…/Application Support/sing-box-lxd/` (0700) и `state/` внутри;
2. **материализует daemon.json**: существующий адрес сохраняется (реинсталл не
   двигает канал из-под сопряжённых клиентов), иначе первый свободный
   loopback-порт от 19091; `tls` — всегда; секрет — существующий или генерируется;
3. пишет plist (`com.leadaxe.sing-box-lxd`) и бутстрапит службу; plist вырожден
   до `sing-box lxd --state-dir <dir>` — все настройки в daemon.json;
4. печатает сводку: адрес канала, админ-секрет, путь daemon.json, команду
   рестарта — и **одноразовый инвайт** для сопряжения лаунчера.

Пути: system — `/Library/Application Support/sing-box-lxd/`, user —
`~/Library/Application Support/sing-box-lxd/`. Лог — `lxd.log` рядом со `state/`.

Прочее:

```bash
sing-box lxd --service=print              # сухой прогон: показать plist, ничего не трогая
sing-box lxd --service=uninstall          # снять службу; state сохраняется
sing-box lxd --service=uninstall --purge  # снять службу и снести state (клиенты, ключи, last-good)
sudo sing-box lxd client add --name mac-book   # новый инвайт на живом демоне (state-dir найдётся сам)
```

## 8. Linux — подходы к настройке

`--service` на Linux пока **заглушка** — автоматической установки нет
(обсуждается режим «инструкций»: детект init-системы и печать готового
unit/init-скрипта; см. «Отложено» в
[SPEC 057](../SPECS/TASKS/057-LXD_MTLS_SERVICE/SPEC.md)). Сам демон на Linux
полноценен: mTLS, apply/rollback, ротация лога — всё работает; кросс-сборка
`GOOS=linux GOARCH=arm64` (статический бинарник, musl-совместим) проверена.
Установка — три файла руками.

### 8.1. Общая часть (любой init)

```bash
mkdir -p /var/lib/sing-box-lxd/state        # OpenWrt: /etc/sing-box-lxd/state — см. 8.3
cat > /var/lib/sing-box-lxd/state/daemon.json <<EOF
{
  "listen": "127.0.0.1:19091",
  "tls": true,
  "secret": "$(head -c 32 /dev/urandom | xxd -p -c 64)"
}
EOF
chmod 700 /var/lib/sing-box-lxd /var/lib/sing-box-lxd/state
chmod 600 /var/lib/sing-box-lxd/state/daemon.json
```

Для управления с другой машины `listen` — LAN-адрес (например
`192.168.10.1:19091`); mTLS обязателен, наружу порт не открывать файрволом без
необходимости. Операторские команды (`client add`) всё равно выполняются только
на самом хосте.

### 8.2. systemd (обычный сервер/десктоп)

`/etc/systemd/system/sing-box-lxd.service`:

```ini
[Unit]
Description=sing-box-lx daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/sing-box lxd --state-dir /var/lib/sing-box-lxd/state
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now sing-box-lxd
```

stdout под systemd — не терминал, поэтому демон сам заберёт лог в
`/var/lib/sing-box-lxd/lxd.log` и будет ротировать; journald получит только
ранний вывод до перехвата.

### 8.3. OpenWrt / procd (роутеры)

Особенности платформы:

- `/var` — tmpfs, умирает на ребуте → state-каталог держать в персистентном
  месте: `/etc/sing-box-lxd/state` (overlay) или extroot (`/root/…`).
- На роутерах **без extroot** лог рядом со state будет писаться в NAND-overlay —
  износ; с extroot проблемы нет. Дефолтные лимиты ротации (20 МБ × 2 файла)
  ограничивают ущерб, но лучше extroot.
- Бинарник (~50 МБ) — только на extroot, во встроенную flash он не влезет.

`/etc/init.d/sing-box-lxd`:

```sh
#!/bin/sh /etc/rc.common
START=95
USE_PROCD=1

start_service() {
    procd_open_instance
    procd_set_param command /root/sing-box lxd --state-dir /etc/sing-box-lxd/state
    procd_set_param respawn
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
```

```bash
chmod +x /etc/init.d/sing-box-lxd
/etc/init.d/sing-box-lxd enable
/etc/init.d/sing-box-lxd start
```

`sysupgrade -b` не забирает ни init-скрипт, ни state, ни бинарник — дописать
пути в `/etc/sysupgrade.conf`, иначе прошивка-апгрейд снесёт установку.

### 8.4. Чего на Linux пока нет

- `--service=install/uninstall/print` — заглушки (планируется режим инструкций);
- автопоиска state-dir установленной службы для `client …` — передавать
  `--state-dir` явно;
- self-update — обновление бинарника это «доставить файл + рестарт службы».

## 9. Сопряжение клиента (одинаково на всех ОС)

1. На хосте демона: `sing-box lxd client add --name <метка>` (на macOS с
   системной службой — под sudo). Печатается одноразовый инвайт
   `адрес#отпечаток#код`.
2. Инвайт вставляется в лаунчер: тот пинит сервер по отпечатку, регистрируется
   кодом (`POST /admin/enroll`), получает доверие по своему сертификату. Код
   сгорает.
3. Проверка/отзыв: `client list`, `client remove <имя-или-отпечаток>`.

## 10. Админ-REST (справочно)

| Маршрут | Что делает |
|---|---|
| `POST /admin/apply` | тело = конфиг; 200 применён / 422 невалиден / 500 сбой (+`rolled_back`) |
| `POST /admin/rollback` | откат на last-good (404 — нечего) |
| `POST /admin/start` · `POST /admin/stop` | жизнь ядра отдельно от конфига (stop запоминается) |
| `GET /admin/config` | активный конфиг |
| `GET /admin/status` | `idle\|started\|fatal`, sha активного/last-good, `last_error`, `interrupted_apply` |
| `GET /admin/info` | паспорт: версия, state_dir, listen, tls, отпечаток, pid, uptime, log_path |
| `POST /admin/enroll` | регистрация клиента по одноразовому коду |

SIGHUP демону = перечитать файл конфига (`--config-force`/`-c`) и применить через
тот же apply-пайплайн с валидацией и откатом.
