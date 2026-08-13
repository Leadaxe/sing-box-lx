# VPN-SSID поверх lxd-демона на OpenWrt

> 🌐 English version: **[openwrt-vpn-ssid.md](openwrt-vpn-ssid.md)**.

Как построить отдельный Wi-Fi, весь трафик которого идёт через ядро sing-box,
не трогая основную домашнюю сеть. Расширяет
[lxd-daemon.ru.md](lxd-daemon.ru.md): демон-гайд ставит и настраивает сам
`sing-box lxd`, здесь — сетевая обвязка вокруг него (Wi-Fi → мост → tun →
firewall).

> Это прикладной рецепт для OpenWrt/fw4, а **не** свойство форка. Все адреса,
> имена и SSID ниже — **плейсхолдеры**: подставьте свои. Проверено на OpenWrt
> 24.10 (fw4/nftables, mt76). Демон уже должен быть поставлен по §8.3 демон-гайда.

Обозначения плейсхолдеров:

| Имя | Значение | Замените на |
|---|---|---|
| `LxdVPN2G`/`LxdVPN5G` | SSID VPN-сегмента | свои имена сети |
| `br-lxdvpn` | мост VPN-сегмента | своё имя моста |
| `192.168.20.0/24` | подсеть сегмента (шлюз `.1`) | свободную у вас подсеть |
| `lxd-tun0` | tun-интерфейс ядра | имя из конфига ядра |
| `172.19.0.1/30` | p2p-подсеть туннеля | любую служебную /30 |
| `lxdvpn`, `sbtun` | firewall-зоны сегмента и туннеля | свои имена зон |

---

## 0. Предусловия (проверить до §2)

Прежде чем строить сегмент, убедитесь, что база готова — иначе сборка развалится
на первом же шаге.

**Демон уже поставлен и работает.** Установка, служба procd и заливка бинарника —
в демон-гайде [§8.3](lxd-daemon.ru.md). Здесь предполагается живой `sing-box lxd`
на роутере.

**Базовая сеть жива: WAN поднят, есть интернет.** Без него не пройдут ни
`opkg update`, ни заливка ядра, ни `apply` с внешним конфигом.

```bash
ping -c1 8.8.8.8 && nslookup openwrt.org        # WAN и DNS роутера работают
```

**Хватает места.** Бинарь ~50 МБ — на роутер без extroot он не помещается;
нужен extroot/USB (детали и почему — демон-гайд §8.3). Проверить свободное место
там, куда кладёте бинарь и state:

```bash
df -h /overlay /                                # или точку монтирования extroot
```

**Есть Wi-Fi-радио и вы знаете их имена.** Гайд использует `radio0`/`radio1`
(типично для двухдиапазонного mt76), но у вашего роутера имена и число радио
могут отличаться — сверьте и подставьте свои в §6:

```bash
uci show wireless | grep '=wifi-device'         # radio0, radio1, …
iw dev                                          # физические интерфейсы и их phy
```

**Инструменты на месте.** На busybox нет `nohup`; для проверок ниже нужны
`ip`, `uci`, `fw4`, `nft` (штатны в OpenWrt 24.10). `nslookup` даёт пакет
`dnsmasq`/`libustream`.

**Снять бэкап конфигурации — сейчас, до первого шага** (а не в конце: откатывать
имеет смысл к состоянию *до* правок). На OpenWrt часто нет sftp-server, поэтому
`scp` не работает — забирать файл потоком через ssh:

```bash
ssh root@HOST 'sysupgrade -b /tmp/bk.tar.gz && cat /tmp/bk.tar.gz' > bk-before-vpn.tar.gz
```

Восстановление при необходимости — `sysupgrade -r bk-before-vpn.tar.gz` (залив
файл обратно тем же потоком).

## 1. Принципы (почему именно так)

Задача: отдельный Wi-Fi через ядро, при этом **основная сеть не зависит от ядра
ни в какой форме** — потеря LAN/SSH недопустима (роутером управляют удалённо).
Отсюда два решения, определившие всю конструкцию:

1. **Изоляция по интерфейсу, а не по адресам.** Ядро перехватывает трафик
   только с моста VPN-сегмента; `br-lan` в его зону ответственности не входит.
   Механизм — `include_interface` в tun-inbound (см. §5).
2. **Fail-closed, а не fail-open.** Умерло ядро → VPN-сегмент остаётся без
   интернета. Молчаливая утечка трафика с домашнего IP хуже явного отвала.
   Механизм — отсутствие forwarding `lxdvpn→wan` (см. §4).

```
                        ┌─ br-lan (192.168.10.0/24) ─→ зона lan ─────────┐
Wi-Fi (2 радио) ────────┤                                                 ├─→ WAN → интернет
                        └─ br-lxdvpn (192.168.20.0/24) ─→ зона lxdvpn ────┘
                                        │ forwarding: ТОЛЬКО lxdvpn→sbtun
                                        ▼
                              iif br-lxdvpn → lxd-tun0 (зона sbtun)
                                        │
                              sing-box lxd (procd, канал управления)
                                        │
                                  outbound → upstream
```

Верхняя ветка (домашняя сеть) не меняется. Нижняя — всё, что добавляется.

Ниже — сборка с нуля, шаг за шагом, с проверкой после каждого. Порядок важен:
Wi-Fi поднимаем **последним**, когда мост, адресация и firewall уже готовы —
иначе AP цепляются в пустоту.

> **Если работаете по SSH удалённо — читайте предупреждения в шагах 3 и 5
> заранее.** Рестарт dnsmasq на секунду роняет DNS всего LAN, а `wifi reload`
> рвёт оба радио на ~10 секунд (и вашу Wi-Fi-сессию вместе с ними). По проводу
> или с отдельного управляющего интерфейса — безопаснее.

## 2. Мост

```bash
uci set network.brlxdvpn=device
uci set network.brlxdvpn.name='br-lxdvpn'
uci set network.brlxdvpn.type='bridge'
uci set network.brlxdvpn.bridge_empty='1'
uci commit network
reload_config
```

`bridge_empty='1'` — **обязателен**. Без него netifd отказывается поднимать мост
без портов, и интерфейс просто не появится (`Device "br-lxdvpn" does not exist`).
Wi-Fi-AP подключаются к мосту позже и динамически, при старте hostapd, — на
момент создания он пустой.

Проверка:

```bash
ip link show br-lxdvpn        # интерфейс существует (пусть и без carrier)
```

## 3. Интерфейс со шлюзом сегмента

```bash
uci set network.lxdvpn=interface
uci set network.lxdvpn.device='br-lxdvpn'
uci set network.lxdvpn.proto='static'
uci set network.lxdvpn.ipaddr='192.168.20.1'
uci set network.lxdvpn.netmask='255.255.255.0'
uci commit network
reload_config
```

`ip6assign` намеренно **не задаём** — IPv6 в сегменте не нужен, иначе клиент
получит адрес по RA и утечёт мимо туннеля (`auto_route` sing-box в этой
конфигурации ловит IPv4). Подсеть не должна пересекаться ни с LAN, ни с подсетями
upstream (например WG-инстансов).

Проверка:

```bash
ip addr show br-lxdvpn        # ожидаем inet 192.168.20.1/24
```

Состояние `NO-CARRIER … DOWN` — нормально, в мосту пока никого.

## 4. DHCP для сегмента

```bash
uci set dhcp.lxdvpn=dhcp
uci set dhcp.lxdvpn.interface='lxdvpn'
uci set dhcp.lxdvpn.start='100'
uci set dhcp.lxdvpn.limit='150'
uci set dhcp.lxdvpn.leasetime='12h'
uci set dhcp.lxdvpn.dhcpv4='server'
uci add_list dhcp.lxdvpn.dhcp_option='6,8.8.8.8'
uci commit dhcp
/etc/init.d/dnsmasq restart
```

- **Опция 6 (DNS) не должна указывать на dnsmasq роутера.** Иначе резолв уйдёт
  мимо туннеля и раскроет домены провайдеру. При direct-выходе это буквально
  внешний DNS; с upstream DNS перехватывается внутри туннеля (hijack-dns), и
  адрес становится условным маркером. `ra`/`dhcpv6` не включаем.

> ⚠️ **Рестарт dnsmasq на секунду роняет DNS всего LAN.** При удалённой работе
> учитывайте это заранее.

Проверка — DNS основного LAN жив:

```bash
nslookup openwrt.org 192.168.1.1         # адрес шлюза вашего LAN (дефолт OpenWrt)
```

## 5. Firewall (fail-closed)

```bash
uci set firewall.lxdvpn=zone
uci set firewall.lxdvpn.name='lxdvpn'
uci add_list firewall.lxdvpn.network='lxdvpn'
uci set firewall.lxdvpn.input='ACCEPT'
uci set firewall.lxdvpn.output='ACCEPT'
uci set firewall.lxdvpn.forward='REJECT'
uci commit firewall
fw4 reload
```

Ключевой факт: **forwarding `lxdvpn→wan` не создаём вовсе.** Единственный
разрешённый путь наружу — в зону туннеля `sbtun` (её и правило `sbtun_tcp` —
создаём в §8, когда tun-интерфейс уже поднят). Нет туннеля → нет пути →
трафик отбивается на уровне nftables. Это не «настройка на случай аварии», а
структурное свойство: чтобы сегмент потёк в WAN, нужно **осознанно** добавить
правило.

- `input=ACCEPT` в зоне `lxdvpn` — чтобы клиенты доставали DHCP и шлюз.
- Между `lxdvpn` и `lan` forwarding **не создаём** — клиент сегмента не видит
  домашние устройства.

## 6. Wi-Fi в мост

```bash
uci set wireless.lxdvpn_2g=wifi-iface
uci set wireless.lxdvpn_2g.device='radio0'
uci set wireless.lxdvpn_2g.mode='ap'
uci set wireless.lxdvpn_2g.network='lxdvpn'
uci set wireless.lxdvpn_2g.ssid='LxdVPN2G'
uci set wireless.lxdvpn_2g.encryption='psk2'
uci set wireless.lxdvpn_2g.key='ваш-пароль'
uci commit wireless
```

(аналогично `lxdvpn_5g` на `radio1` с `ssid='LxdVPN5G'`)

Связь с мостом даёт строка `network='lxdvpn'` — AP попадёт в `br-lxdvpn`
автоматически. mt76 держит несколько AP на радио, так что существующие сети
не затрагиваются.

Применение — **отвязанно от терминала**, потому что `wifi reload` рвёт оба радио
на ~10 секунд и убьёт вашу SSH-сессию, если вы по Wi-Fi:

```bash
wifi reload >/tmp/wifi.log 2>&1 </dev/null &
```

> `nohup` в busybox нет — не используйте его.

Проверка после переподключения — **AP реально попали в мост**:

```bash
ls /sys/class/net/br-lxdvpn/brif/        # ожидаем phy0-ap1, phy1-ap1
```

Пусто = опечатка в `network='lxdvpn'`: AP поднялся, но не в тот мост. Симптом
коварный — клиент подключается к Wi-Fi, но не получает адрес, а причина не в
DHCP.

## 7. Конфиг ядра (заливается через apply демона)

```json
{
  "log": { "level": "info" },
  "inbounds": [
    {
      "type": "tun",
      "tag": "tun-in",
      "interface_name": "lxd-tun0",
      "address": ["172.19.0.1/30"],
      "mtu": 1400,
      "auto_route": true,
      "strict_route": false,
      "include_interface": ["br-lxdvpn"],
      "stack": "system"
    }
  ],
  "outbounds": [ { "type": "direct", "tag": "direct-out" } ],
  "route": { "final": "direct-out", "auto_detect_interface": true }
}
```

Значимые поля:

| Поле | Зачем именно так |
|---|---|
| `include_interface: ["br-lxdvpn"]` | **Главная гарантия изоляции.** `auto_route` сам по себе жадный — увёл бы в туннель весь хост. Фильтр сужает его до одного моста; `br-lan` и трафик роутера не затрагиваются. Здесь пишется **имя моста VPN-сегмента**, ничего больше |
| `auto_route: true` | ядро само прописывает policy-routing вместо ручных `ip rule` |
| `strict_route: false` | строгий режим ставит дополнительные блокирующие правила — на роутере с fw4 лишний источник конфликтов |
| `stack: "system"` | системный стек вместо gvisor — быстрее на слабом CPU; цена — TCP-нюанс из §8.1 |
| `mtu: 1400` | запас под оверхед будущего upstream (WG/VLESS), чтобы не переделывать при замене outbound |
| `address: 172.19.0.1/30` | служебная p2p-подсеть туннеля |

`direct-out` — заглушка (трафик уйдёт в WAN как есть). Реальный upstream (WG,
VLESS, …) ставится заменой `outbounds`/`route` **одним apply**; каркас, firewall
и Wi-Fi при этом не трогаются.

Заливка — по каналу демона, а не правкой файла на диске:

```bash
curl -sk -X POST https://DAEMON:PORT/admin/apply --cert … --key … -d @config.json
```

(аутентификация — как в демон-гайде §5/§9; локально с роутера — по loopback).
После apply интерфейс `lxd-tun0` поднимается — теперь можно замкнуть firewall на
туннель (§8).

**Проверка изоляции — снимает главный страх «не увело ли весь роутер в
туннель».** `auto_route` с `include_interface` прописывает policy-routing, и его
видно в `ip rule`:

```bash
ip rule | head -3
# 9000: from all iif br-lxdvpn goto 9002   ← только трафик с моста сегмента → туннель
# 9001: from all goto 9010                 ← всё остальное идёт мимо, в main
```

Решение принимается по **входящему интерфейсу** (`iif br-lxdvpn`), а не по
адресу источника — `br-lan` и трафик самого роутера туннеля не касаются. Если
правила `iif br-lxdvpn` нет или оно `from all` без `iif` — `include_interface`
не сработал, проверьте имя моста в конфиге ядра.

## 8. Firewall туннеля — замыкаем fail-closed

Делается **после** конфига ядра: зона привязывается к уже существующему
tun-устройству. Три части: зона туннеля, единственный разрешённый forwarding
из сегмента в неё, и точечный ACCEPT для TCP (см. пояснение ниже).

```bash
# зона туннеля
uci set firewall.sbtun=zone
uci set firewall.sbtun.name='sbtun'
uci add_list firewall.sbtun.device='lxd-tun0'
uci set firewall.sbtun.input='REJECT'
uci set firewall.sbtun.output='ACCEPT'
uci set firewall.sbtun.forward='REJECT'
# единственный путь наружу: сегмент → туннель
uci set firewall.lxdvpn2tun=forwarding
uci set firewall.lxdvpn2tun.src='lxdvpn'
uci set firewall.lxdvpn2tun.dest='sbtun'
# ACCEPT для system-stack TCP (см. ниже)
uci set firewall.sbtun_tcp=rule
uci set firewall.sbtun_tcp.name='Allow-sbtun-systemstack-tcp'
uci set firewall.sbtun_tcp.src='sbtun'
uci set firewall.sbtun_tcp.dest_ip='172.19.0.1'
uci set firewall.sbtun_tcp.proto='tcp'
uci set firewall.sbtun_tcp.target='ACCEPT'
uci commit firewall
fw4 reload
```

`firewall.sbtun.device='lxd-tun0'` **привязывает зону к устройству по имени.**
Сменится имя tun в конфиге ядра — зона окажется пустой, и весь сегмент останется
без интернета (единственный forwarding ведёт именно в `sbtun`). См. §9.

Проверка:

```bash
nft list ruleset | grep -A2 Allow-sbtun-systemstack-tcp
```

### 8.1. Зачем `sbtun_tcp`: `stack: "system"` + fw4 молча роняет TCP

Симптом: UDP и ICMP через туннель ходят, DNS резолвится, а **TCP не
устанавливается вовсе**; в логе ядра — только `inbound packet connection`, ни
одного TCP-коннекта.

Причина: при `stack: "system"` sing-box не обрабатывает TCP прямо из tun-fd — он
редиректит его на свой локальный листенер на адресе туннеля
(`172.19.0.1:<динамический порт>`). Для ядра Linux это обычный входящий на
локальный адрес — он идёт через **INPUT** в зоне `sbtun`, где `input=REJECT`, и
там умирает. UDP/ICMP этим путём не идут (их sing-box читает прямо из tun-fd),
поэтому и выглядело избирательно. `sbtun_tcp` — точечный ACCEPT на адрес туннеля
по TCP; fail-closed при этом не размывается.

> ⚠️ Правило привязано к **IP**. Смена `address` у tun-inbound (в т.ч. чужим
> apply) ломает его: клиенты получают `connection refused` на любой TCP при
> живых ICMP/DNS. Держать `dest_ip` в согласии с `address` конфига (см. §9).

## 9. Согласованность имён и адресов — главная мина

Два поля конфига ядра держат firewall-обвязку, и рассинхрон любого рвёт сегмент,
причём ядро при этом выглядит полностью исправным:

| Что | Где | Связано с |
|---|---|---|
| `interface_name` | конфиг ядра | ← → `firewall.sbtun.device` (§8) |
| `address` | конфиг ядра | ← → `firewall.sbtun_tcp.dest_ip` (§8) |

**Любой сторонний apply (например, залитый из лаунчера) может сменить
`interface_name` или `address`** — и firewall-правила, привязанные к старому
имени/IP, тихо перестанут совпадать. Демон тут ни при чём: он честно применил
присланный конфиг. Проверять после каждого нетривиального apply:

```bash
ip addr show lxd-tun0 | grep inet          # какой адрес у туннеля сейчас
nft list ruleset | grep Allow-sbtun-systemstack-tcp   # packets 0 при живом трафике = рассинхрон
```

## 10. Проверка

| Проверка | Ожидание |
|---|---|
| DHCP | клиент получает адрес из `192.168.20.0/24`, gw `.1`, DNS из опции 6 |
| ICMP | `ping 8.8.8.8` из сегмента идёт |
| DNS | резолв через туннель работает |
| TCP/HTTP | внешний IP = адрес upstream (при `direct` — WAN-адрес роутера) |
| Транзит | `tx_bytes` на `lxd-tun0` растёт синхронно с трафиком |
| **Fail-closed** | стоп службы демона → туннель исчез, ping 100% потерь, TCP отбит, **утечки в WAN нет** |
| Восстановление | старт службы → демон поднял last-good, клиент снова в сети |

Основной канал (LAN/SSH/интернет мака) проверять после каждого слоя — он должен
оставаться живым всегда: в этом весь смысл изоляции.

### 10.1. Виртуальный клиент — как проверить без телефона

При удалённой настройке подключить реальное устройство к новому SSID **некому**,
а без клиента доказать fail-closed нельзя. Собираем клиента прямо на роутере:
пара `veth`, одна нога в мост сегмента, вторая — в изолированный сетевой namespace
(имитирует отдельное устройство за Wi-Fi).

```bash
opkg update && opkg install kmod-veth
ip link add v0 type veth peer name v1
ip netns add t && ip link set v1 netns t
ip link set v0 master br-lxdvpn && ip link set v0 up
ip netns exec t ip link set v1 up
# netns наследует resolv.conf роутера (127.0.0.1) — внутри netns его нет; дать свой:
mkdir -p /etc/netns/t && echo "nameserver 8.8.8.8" > /etc/netns/t/resolv.conf

# получить адрес по DHCP и проверить выход
ip netns exec t udhcpc -i v1 -n -q -s /usr/share/udhcpc/default.script
ip netns exec t ping -c1 8.8.8.8                 # ICMP через туннель
ip netns exec t wget -qO- http://api.ipify.org   # внешний IP = адрес upstream?
```

Проверка **fail-closed** (главная): остановить службу демона и повторить —

```bash
/etc/init.d/sing-box-lxd stop
ip netns exec t ping -c2 8.8.8.8                  # ожидаем 100% потерь
ip netns exec t wget -qO- --timeout=5 http://api.ipify.org  # ожидаем отказ, НЕ WAN-IP
/etc/init.d/sing-box-lxd start                    # вернуть
```

Если при остановленном демоне внешний IP всё же получен — **утечка в WAN**,
где-то есть лишний forwarding (см. §5): сегмент обязан оставаться без сети.

Убрать за собой:

```bash
ip netns del t && ip link del v0 && rm -rf /etc/netns/t
```

> Две грабли: **`kmod-veth` в базовой прошивке нет** — без него `ip link add …
> type veth` даёт `Unknown device type` (нужен `opkg install kmod-veth`, а он
> требует живого WAN — см. §0). И **netns не наследует рабочий resolv.conf**:
> внутри него `127.0.0.1` роутера не существует, поэтому свой
> `/etc/netns/t/resolv.conf` обязателен, иначе DNS в namespace мёртв.

## 11. Персистентность (OpenWrt)

`sysupgrade` не забирает ни бинарник, ни state, ни init-скрипт демона — см. §8.3
демон-гайда. UCI-разделы (`wireless`/`network`/`dhcp`/`firewall`) переживают
`sysupgrade -b`, но **не** переживают `sysupgrade -n` (сброс к дефолту). Бэкап
конфигурации снимается **до** начала сборки — см. §0.
