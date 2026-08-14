# VPN-SSID на OpenWrt — установщик

> Ручная сборка того же сегмента по шагам, с объяснением каждого решения:
> **[docs-lx/openwrt-vpn-ssid.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/docs-lx/openwrt-vpn-ssid.md)**
> ([по-русски](https://github.com/Leadaxe/sing-box-lx/blob/lx/docs-lx/openwrt-vpn-ssid.ru.md)).
> Демон, `daemon.json`, сопряжение, телеметрия:
> [docs-lx/lxd-daemon.ru.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/docs-lx/lxd-daemon.ru.md).
> Здесь — автоматический установщик и рецепты эксплуатации.

Отдельный Wi-Fi, весь трафик которого идёт через ядро sing-box-lx прямо на роутере. Основная сеть роутера при этом не зависит от ядра ни в какой форме — ошибка в конфиге не может отрезать SSH/LAN.

```
SSID → мост → tun (auto_route + include_interface) → ядро sing-box → upstream
```

Проверено на RouteRich AX3000 (форк OpenWrt 24.10, mediatek/filogic, aarch64), работает в бою.

## Требования

- OpenWrt 22.03+ с fw4/nftables (форки с `ID="openwrt"` в `/etc/os-release` подходят) и dnsmasq;
- ~80 МБ свободного места (бинарь ~50 МБ; на маленьком overlay нужен extroot);
- архитектура из релизов форка: aarch64, x86_64, armv7l, mips, mipsel.

## Установка

Зайти на роутер (`ssh root@РОУТЕР`) и выполнить:

```bash
wget -O /tmp/lxd-setup.sh https://raw.githubusercontent.com/Leadaxe/sing-box-lx/lx/scripts-lx/openwrt/lxd-openwrt-setup.sh && sh /tmp/lxd-setup.sh
```

Если busybox-wget ругается на SSL: `opkg install ca-bundle libustream-mbedtls` и повторить.

Запасной вариант — если у роутера нет прямого доступа к GitHub, доставить файл с компьютера и запустить через `ssh -t` (скрипт интерактивный, ему нужен терминал — пайп в `sh -` без pty он распознаёт и отказывается работать):

```bash
curl -fsSL https://raw.githubusercontent.com/Leadaxe/sing-box-lx/lx/scripts-lx/openwrt/lxd-openwrt-setup.sh | ssh root@РОУТЕР 'cat > /tmp/lxd-setup.sh'
```

```bash
ssh -t root@РОУТЕР 'sh /tmp/lxd-setup.sh'
```

(из локальной копии репозитория первая команда — `ssh root@РОУТЕР 'cat > /tmp/lxd-setup.sh' < lxd-openwrt-setup.sh`)

Семь вопросов: пароль Wi-Fi (подставляет найденный в существующих сетях), SSID для 5 ГГц, нужна ли сеть на 2.4 ГГц, имя и адрес tun-интерфейса, имя моста, нужен ли доступ к управлению снаружи. Остальное скрипт делает сам: качает бинарь со сверкой sha256, поднимает мост, DHCP, firewall, службу под procd, выпускает invite.

Радио поднимается **последним шагом, по нажатию Enter** — чтобы обрыв Wi-Fi не убил скрипт до того, как он выведет invite.

Итог печатается и сохраняется в `/root/lxd-setup-summary.txt` (chmod 600 — там код сопряжения и пароль Wi-Fi):

```
Pair invite:       <LAN-адрес-роутера>:19091#<отпечаток-сервера>#<КОД>
tun name:          lxd-tun0
tun address:       172.16.0.1/30
include_interface: br-lxdvpn          ← в UI лаунчера поле "LAN interfaces"
```

После установки ядро крутит каркасный конфиг: сегмент работает, но выходит **напрямую через WAN**. Боевой upstream заливается из лаунчера одним apply — сеть, firewall и Wi-Fi при этом не трогаются.

Во всех рецептах ниже — имена по умолчанию (`br-lxdvpn`, `lxd-tun0`, служба `sing-box-lxd`). Если при установке задавали свои — подставьте значения из сводки.

---

## Сразу после установки

### 1. Сопрячь лаунчер

Вбить invite из сводки. Адрес в нём уже подставлен рабочий (LAN-адрес роутера), отпечаток и код — как есть; по отпечатку лаунчер пинит TLS-сертификат демона.

⚠️ **Не перезапускать демон, пока не введён код.** Код живёт в памяти процесса; рестарт даёт `enroll: no active enrollment code`, и invite придётся выпускать заново.

### 2. Залить боевой конфиг

Из лаунчера. В tun-inbound должны быть значения из сводки:

```json
{
  "type": "tun",
  "interface_name": "lxd-tun0",
  "address": ["172.16.0.1/30"],
  "auto_route": true,
  "include_interface": ["br-lxdvpn"],
  "stack": "system"
}
```

`include_interface` — **мост VPN-сегмента**, не `br-lan`. Именно он запирает перехват: `auto_route` сам по себе жадный и увёл бы в туннель весь роутер.

### 3. Проверить, что сегмент ходит

Подключиться телефоном к новому SSID и открыть любой сайт. Если реального клиента нет — виртуальный, прямо на роутере (см. рецепт ниже).

---

## Рецепты

### Проверить сегмент без реального клиента

Виртуальный клиент в мосту сегмента — единственный способ проверить всё, включая fail-closed, не трогая живых пользователей:

```bash
opkg install kmod-veth        # в базовой прошивке его нет
cat > /tmp/dc.sh <<'EOF'
#!/bin/sh
case "$1" in bound) ip addr add $ip/$subnet dev $interface; ip route add default via $router;; esac
exit 0
EOF
chmod +x /tmp/dc.sh

ip link add v0 type veth peer name v1
ip netns add t && ip link set v1 netns t
ip link set v0 master br-lxdvpn && ip link set v0 up
ip netns exec t ip link set v1 up
mkdir -p /etc/netns/t && echo "nameserver 8.8.8.8" > /etc/netns/t/resolv.conf
ip netns exec t udhcpc -i v1 -n -q -f -s /tmp/dc.sh     # получил адрес?
ip netns exec t wget -qO- http://api.ipify.org           # внешний IP = upstream?

# убрать за собой
ip netns del t; ip link del v0; rm -rf /etc/netns/t /tmp/dc.sh
```

Свой `resolv.conf` для netns обязателен: иначе он наследует `127.0.0.1` роутера, которого внутри netns нет.

### Проверить fail-closed

```bash
/etc/init.d/sing-box-lxd stop
# из сегмента: ping не идёт, TCP отбит, в WAN НЕ течёт
/etc/init.d/sing-box-lxd start
# ядро поднялось из last_good, клиент снова в сети
```

### Убедиться, что основная сеть не затронута

```bash
ip rule | head -3
# 9000: from all iif br-lxdvpn goto 9002   ← только сегмент уходит в туннель
# 9001: from all goto 9010                 ← всё остальное мимо, в main

ip route get 8.8.8.8 from <адрес-клиента-в-LAN> iif br-lan    # → через WAN, как раньше
```

### Кто сейчас в сегменте и через какой SSID

```bash
cat /tmp/dhcp.leases                                   # IP → MAC → имя устройства
for i in $(iw dev | awk '$1=="Interface"{print $2}'); do
  echo "$i ($(iwinfo $i info | grep -o 'ESSID: ".*"'))"
  iw dev $i station dump | grep Station
done
```

Связка идёт по MAC: lease даёт имя, `station dump` — на каком AP, `iwinfo` — какой у AP SSID.

### Замерить нагрузку и потолок скорости

```bash
TUN=lxd-tun0                  # имя из сводки
P=$(pgrep -f "^/usr/bin/sing-box lxd" | head -1)
read _ u1 n1 s1 i1 w1 q1 sq1 _ < /proc/stat; A=$(awk '{print $14+$15}' /proc/$P/stat)
R1=$(cat /sys/class/net/$TUN/statistics/rx_bytes)
sleep 10
read _ u2 n2 s2 i2 w2 q2 sq2 _ < /proc/stat; B=$(awk '{print $14+$15}' /proc/$P/stat)
R2=$(cat /sys/class/net/$TUN/statistics/rx_bytes)
T=$(( (u2+n2+s2+i2+w2+q2+sq2)-(u1+n1+s1+i1+w1+q1+sq1) ))
awk -v d=$((B-A)) -v t=$T -v c=$(grep -c ^processor /proc/cpuinfo) \
    -v r=$((R2-R1)) 'BEGIN{printf "sing-box: %.1f%% ядра при %.1f Мбит/с\n", d*100/t*c, r*8/10/1e6}'
```

### Добавить/отозвать клиента

```bash
sing-box lxd client add --name <имя> --state-dir /etc/sing-box-lxd/state
sing-box lxd client list   --state-dir /etc/sing-box-lxd/state
sing-box lxd client remove <имя> --state-dir /etc/sing-box-lxd/state
```

Работает **только с loopback** (`403 operator routes are loopback-only` по сети), поэтому `127.0.0.1` должен слушаться и стоять **первым** в `listen.address` (скрипт так и настраивает). Между `add` и вводом кода демон не перезапускать.

### Служба и логи

```bash
/etc/init.d/sing-box-lxd status | restart | stop | start
logread | grep sing-box | tail -20        # системный лог
tail -f /etc/sing-box-lxd/lxd.log         # собственный лог демона, ротация своя
```

Apply, rollback и статус ядра — **только из лаунчера**: они за mTLS, `curl` с роутера отвечает `client certificate not trusted`.

### Снять и восстановить бэкап

```bash
# снять (на роутере нет sftp-server, поэтому потоком)
ssh root@РОУТЕР 'sysupgrade -b /tmp/bk.tar.gz >/dev/null 2>&1; cat /tmp/bk.tar.gz' > backup.tar.gz
ssh root@РОУТЕР 'rm /tmp/bk.tar.gz'

# восстановить
ssh root@РОУТЕР 'cat > /tmp/bk.tar.gz' < backup.tar.gz
ssh root@РОУТЕР 'sysupgrade -r /tmp/bk.tar.gz && reboot'
```

Бинарь и state попадают в бэкап только потому, что скрипт внёс их в `/etc/sysupgrade.conf`.

### Снести всё установленное

```bash
wget -O /tmp/lxd-uninstall.sh https://raw.githubusercontent.com/Leadaxe/sing-box-lx/lx/scripts-lx/openwrt/lxd-openwrt-uninstall.sh && sh /tmp/lxd-uninstall.sh
```

Скрипт [lxd-openwrt-uninstall.sh](lxd-openwrt-uninstall.sh) сам находит имя сегмента (по forwarding в зону `sbtun`), гасит и удаляет службу, state и uci-секции, перезагружает network/firewall/dnsmasq и в конце перезапускает радио. Терпит полуустановленное состояние — годится и как уборка после установки, оборванной на любом шаге. Флаг `--yes` — без вопросов (запуск без tty). Основная сеть не затрагивается.

Важный порядок внутри (если снимаете руками): `ifdown` **до** удаления uci-секций и `/etc/init.d/network reload` **после** — иначе netifd не узнает об удалении, останется мост-сирота, а повторная установка упрётся в «интерфейс уже существует».

---

## Главная мина: рассинхрон имени и адреса туннеля

Два поля конфига ядра держат firewall-обвязку. Любой залитый конфиг (в том числе из лаунчера) может их сменить — и правила, привязанные к старым значениям, тихо перестанут совпадать. **Ядро при этом выглядит полностью исправным.**

| Что в конфиге ядра | Связано с | Симптом рассинхрона |
|---|---|---|
| `interface_name` | `firewall.sbtun.device` | сегмент вообще без интернета (зона пуста) |
| `address` | `firewall.sbtun_tcp.dest_ip` | **`connection refused` на TCP при живых ICMP и DNS** |

Второй случай коварнее: пинги идут, DNS резолвится, а ни одна страница не открывается.

```bash
ip addr show lxd-tun0 | grep inet                          # адрес туннеля сейчас
uci show firewall.sbtun.device firewall.sbtun_tcp.dest_ip  # что в firewall
nft list ruleset | grep Allow-sbtun-systemstack-tcp        # packets 0 при живом трафике = промах

uci set firewall.sbtun_tcp.dest_ip='<новый адрес>'
uci commit firewall && fw4 reload
```

Избавиться от связки совсем: `"stack": "gvisor"` в конфиге ядра. Тогда sing-box разбирает TCP внутри себя, локальный листенер не появляется и правило `sbtun_tcp` не нужно. Цена — выше нагрузка на CPU.

## Грабли OpenWrt, зашитые в скрипт

- **`bridge_empty=1`** обязателен для моста без ethernet-портов (только Wi-Fi-AP, они цепляются динамически при старте hostapd). Без флага netifd мост не поднимет вовсе.
- **`nohup` в busybox нет.** Фоновый запуск — `cmd >/log 2>&1 </dev/null &`.
- **`xxd` и `od` в busybox нет** — секрет генерируется через `openssl rand -hex 32` (fallback — `hexdump`).
- **`ID` из `/etc/os-release`, а не `DISTRIB_ID`.** Форки пишут в `DISTRIB_ID` своё имя (у RouteRich там `RouteRich`), а `ID="openwrt"` сохраняют.
- **`listen: ["127.0.0.1", "0.0.0.0"]` валит демон** — `bind: address already in use`, ядро не поднимается. `0.0.0.0` идёт один, loopback он покрывает сам.
- **`/releases/latest` игнорирует pre-release**, а релизы форка выходят как rc — берётся первый из общего списка.
- **`wifi reload` рвёт SSH-сессию** — поэтому он последним шагом, после того как invite выпущен и сводка сохранена.
- **Рестарт dnsmasq роняет DNS всего LAN** на секунду — при удалённой работе учитывать.

## Производительность (ориентир)

Замер на RouteRich AX3000 (Cortex-A53, 2 ядра, без аппаратного AES), выход через VLESS+Reality:

| Нагрузка | Трафик | CPU ядра sing-box |
|---|---|---|
| простой | 0 | ~1% ядра |
| средняя | 45 Мбит/с | ~33% ядра |
| **потолок** | **~70 Мбит/с** | **94% ядра** |

Упор — в шифрование на CPU. Основной Wi-Fi при этом получает полную скорость: он идёт мимо ядра, через аппаратный offload.
