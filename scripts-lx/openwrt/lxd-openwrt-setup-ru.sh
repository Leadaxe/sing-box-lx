#!/bin/sh
# lxd-openwrt-setup.sh — установка sing-box-lx (lxd-демон) + VPN-SSID на OpenWrt.
#
# Поднимает отдельный Wi-Fi, весь трафик которого идёт через ядро sing-box:
#   SSID → мост → tun (auto_route + include_interface) → ядро → outbound
# Основная сеть (br-lan) не затрагивается: ядро видит только новый мост,
# поэтому ошибка в конфиге не может отрезать SSH/LAN.
#
# Запускать НА РОУТЕРЕ, из-под root, С ТЕРМИНАЛОМ (скрипт интерактивный):
#   ssh root@РОУТЕР 'cat > /tmp/lxd-setup.sh' < lxd-openwrt-setup-ru.sh
#   ssh -t root@РОУТЕР 'sh /tmp/lxd-setup.sh'
# Вариант `ssh host 'sh -' < script` НЕ годится: без pty нет /dev/tty,
# и все вопросы молча ушли бы в дефолты — скрипт это проверяет и откажется.
#
# Проверено на RouteRich 24.10.5 (форк OpenWrt 24.10, mediatek/filogic, aarch64).
#
# Дока (ручная сборка того же сегмента по шагам, с объяснением решений):
#   https://github.com/Leadaxe/sing-box-lx/blob/lx/docs-lx/openwrt-vpn-ssid.ru.md

set -e

VERSION="1.1"
STATE_DIR="/etc/sing-box-lxd/state"
BIN="/usr/bin/sing-box"
INIT="/etc/init.d/sing-box-lxd"
PORT="19091"
REPO="Leadaxe/sing-box-lx"

say()  { printf '%s\n' "$*"; }
step() { printf '\n=== %s ===\n' "$*"; }
warn() { printf '!! %s\n' "$*" >&2; }
die()  { printf '!! %s\n' "$*" >&2; exit 1; }

# ── ask <вопрос> <дефолт> — печатает ответ в stdout, промпт в stderr ─────────
ask() {
    printf '%s [%s]: ' "$1" "$2" >&2
    read -r _a </dev/tty || _a=""
    [ -n "$_a" ] && printf '%s\n' "$_a" || printf '%s\n' "$2"
}

ask_yn() {   # ask_yn <вопрос> — по умолчанию НЕТ
    printf '%s [y/N]: ' "$1" >&2
    read -r _a </dev/tty || _a=""
    case "$_a" in [yYдД]*) return 0 ;; *) return 1 ;; esac
}

ask_yn_default() {   # ask_yn_default <вопрос> — по умолчанию ДА
    printf '%s [Y/n]: ' "$1" >&2
    read -r _a </dev/tty || _a=""
    case "$_a" in [nNнН]*) return 1 ;; *) return 0 ;; esac
}

# ── 0. Предусловия ──────────────────────────────────────────────────────────
step "Проверка системы"

[ "$(id -u)" = 0 ] || die "нужен root"

# Интерактив живёт на /dev/tty. Без pty (ssh без -t) каждый read молча вернул
# бы дефолт — установка "сама ответила" бы на все вопросы. Лучше отказаться.
( : </dev/tty ) 2>/dev/null || die "нет терминала (/dev/tty). Запустите так:
  ssh root@РОУТЕР 'cat > /tmp/lxd-setup.sh' < lxd-openwrt-setup-ru.sh
  ssh -t root@РОУТЕР 'sh /tmp/lxd-setup.sh'"

# ID="openwrt" переживает ребрендинг форков, DISTRIB_ID в openwrt_release — нет
# (у RouteRich там 'RouteRich', проверка на 'OpenWrt' провалилась бы).
if [ -r /etc/os-release ]; then
    . /etc/os-release
    case "$ID $ID_LIKE" in
        *openwrt*) say "система:    ${PRETTY_NAME:-OpenWrt}" ;;
        *) die "это не OpenWrt (ID=$ID). Скрипт рассчитан на OpenWrt/fw4." ;;
    esac
else
    die "нет /etc/os-release — не могу определить систему"
fi

command -v uci  >/dev/null || die "нет uci"
command -v fw4  >/dev/null || die "нет fw4 (нужен OpenWrt 22.03+ с nftables)"
command -v wifi >/dev/null || die "нет wifi"
# DHCP сегмента раздаёт dnsmasq; на odhcpd-only сборках секция молча не заработает
[ -x /etc/init.d/dnsmasq ] || die "нет dnsmasq — DHCP сегмента раздавать нечем (odhcpd-only сборка не поддерживается)"

# TUN обязателен для tun-inbound
if [ ! -c /dev/net/tun ]; then
    say "нет /dev/net/tun — ставлю kmod-tun"
    opkg update >/dev/null 2>&1 || warn "opkg update не прошёл"
    opkg install kmod-tun >/dev/null 2>&1 || die "не удалось поставить kmod-tun"
    [ -c /dev/net/tun ] || die "/dev/net/tun так и не появился"
fi

ARCH_RAW=$(uname -m)
case "$ARCH_RAW" in
    aarch64) ARCH="linux-arm64" ;;
    x86_64)  ARCH="linux-amd64" ;;
    armv7l)  ARCH="linux-armv7" ;;
    mips)    ARCH="linux-mips-softfloat"  ;;
    mipsel)  ARCH="linux-mipsle-softfloat" ;;
    *) die "архитектура $ARCH_RAW: подберите релиз вручную" ;;
esac
say "архитектура: $ARCH_RAW → $ARCH"

# Бинарь ~41 МБ: на NAND-overlay маленького роутера не влезет
AVAIL_KB=$(df -k /overlay 2>/dev/null | awk 'NR==2{print $4}')
[ -z "$AVAIL_KB" ] && AVAIL_KB=$(df -k / | awk 'NR==2{print $4}')
say "свободно:    $((AVAIL_KB / 1024)) МБ"
[ "$AVAIL_KB" -lt 50000 ] && warn "меньше 50 МБ свободно — бинарь ~41 МБ может не влезть (нужен extroot)"

# ── Бэкап pre-lxd: до любых изменений, один раз ─────────────────────────────
# Точка полного отката — скрипт сноса восстанавливается из него (--restore).
# Создаётся только если его ещё нет: повторный запуск после оборванной
# установки не должен затереть чистое состояние грязным.
if ls /root/backup-pre-lxd-*.tar.gz >/dev/null 2>&1; then
    say "бэкап:       уже есть $(ls /root/backup-pre-lxd-*.tar.gz | head -1) — не трогаю"
else
    BK="/root/backup-pre-lxd-$(date +%Y%m%d-%H%M%S).tar.gz"
    if sysupgrade -b "$BK" >/dev/null 2>&1 && tar -tzf "$BK" >/dev/null 2>&1; then
        say "бэкап:       $BK ($(du -k "$BK" | awk '{print $1}') КБ)"
    else
        rm -f "$BK"
        warn "sysupgrade -b не отработал — продолжаю БЕЗ точки отката"
    fi
fi

# ── 1. Радио ────────────────────────────────────────────────────────────────
step "Wi-Fi радио"

RADIO_5G=""; RADIO_2G=""
for r in $(uci show wireless 2>/dev/null | sed -n "s/^wireless\.\([a-z0-9]*\)=wifi-device$/\1/p"); do
    band=$(uci -q get "wireless.$r.band")
    # конфиги, мигрированные со старых версий, вместо band несут hwmode (11a/11g)
    if [ -z "$band" ]; then
        case "$(uci -q get "wireless.$r.hwmode")" in
            11a|11ac|11ax) band="5g" ;;
            11g|11b|11ng)  band="2g" ;;
        esac
    fi
    case "$band" in
        5g|6g) [ -z "$RADIO_5G" ] && RADIO_5G="$r" ;;
        2g)    [ -z "$RADIO_2G" ] && RADIO_2G="$r" ;;
    esac
    say "  $r — band ${band:-?}"
done
[ -z "$RADIO_5G" ] && [ -z "$RADIO_2G" ] && die "не нашёл ни одного wifi-device"

# ── 2. Пароль из существующих сетей ─────────────────────────────────────────
step "Пароль"

FOUND_KEY=""; FOUND_SSID=""
for s in $(uci show wireless 2>/dev/null | sed -n "s/^wireless\.\([A-Za-z0-9_]*\)=wifi-iface$/\1/p"); do
    [ "$(uci -q get "wireless.$s.mode")" = "ap" ] || continue
    k=$(uci -q get "wireless.$s.key"); e=$(uci -q get "wireless.$s.encryption")
    case "$e" in none|""|open) continue ;; esac
    if [ -n "$k" ]; then
        FOUND_KEY="$k"; FOUND_SSID=$(uci -q get "wireless.$s.ssid"); break
    fi
done

if [ -n "$FOUND_KEY" ]; then
    say "нашёл пароль сети \"$FOUND_SSID\": $FOUND_KEY"
    WIFI_KEY=$(ask "пароль для нового Wi-Fi (Enter — оставить этот)" "$FOUND_KEY")
else
    warn "в существующих сетях пароля нет (открытые или не настроены)"
    WIFI_KEY=$(ask "задайте пароль для нового Wi-Fi (мин. 8 символов)" "")
fi
[ ${#WIFI_KEY} -ge 8 ]  || die "пароль короче 8 символов — WPA2 такой не примет"
[ ${#WIFI_KEY} -le 63 ] || die "пароль длиннее 63 символов — WPA2 такой не примет"

# ── 3. Имена сетей ──────────────────────────────────────────────────────────
step "Имена сетей"

if [ -n "$RADIO_5G" ]; then
    SSID_5G=$(ask "SSID для 5 ГГц" "LxdVPN 5G")
else
    warn "5 ГГц радио не найдено — пропускаю"; SSID_5G=""
fi

SSID_2G=""
if [ -n "$RADIO_2G" ] && ask_yn "Добавить вторую сеть на 2.4 ГГц?"; then
    SSID_2G=$(ask "SSID для 2.4 ГГц" "LxdVPN 2G")
fi
[ -z "$SSID_5G" ] && [ -z "$SSID_2G" ] && die "не выбрано ни одной сети"

# ── 4. Свободная подсеть ────────────────────────────────────────────────────
step "Сеть сегмента"
# Занятость считаем по нескольким источникам сразу. Адресов интерфейсов мало:
# при double NAT сеть провайдерского роутера видна только маршрутом, а её
# коллизия с сегментом ломает роутинг уже после установки, молча.
net_taken() {   # net_taken <a.b.c> — 0, если /24 занята
    ip -4 addr  show          2>/dev/null | grep -q "inet $1\." && return 0
    ip -4 route show          2>/dev/null | grep -q "^$1\."     && return 0
    ip -4 route show table all 2>/dev/null | grep -q "^$1\."    && return 0
    # адрес, выданный WAN-у провайдером: его /24 занята, даже без маршрута
    [ -n "$WAN_NET" ] && [ "$1" = "$WAN_NET" ] && return 0
    # Маршрут шире /24 может накрыть кандидата целиком: при LAN 10.0.0.1/8
    # сегмент 10.0.1.0/24 лежит ВНУТРИ br-lan, и трафик уйдёт в LAN мимо
    # туннеля. Строковое сравнение выше такое перекрытие не видит.
    _c1=${1%%.*}; _cr=${1#*.}; _c2=${_cr%%.*}; _c3=${_cr#*.}
    _cip=$(( _c1 * 16777216 + _c2 * 65536 + _c3 * 256 ))
    { ip -4 route show 2>/dev/null; ip -4 route show table all 2>/dev/null; } \
      | sed -n 's|^\([0-9]*\.[0-9]*\.[0-9]*\.[0-9]*\)/\([0-9]*\) .*|\1 \2|p' \
      | while read -r _rnet _rlen; do
            [ "$_rlen" -ge 24 ] && continue   # /24 и уже — поймано строкой выше
            [ "$_rlen" -le 0  ] && continue   # default (0.0.0.0/0) — не занятость
            _r1=${_rnet%%.*}; _rr=${_rnet#*.}; _r2=${_rr%%.*}; _rr=${_rr#*.}
            _r3=${_rr%%.*};   _r4=${_rr#*.}
            _rip=$(( _r1 * 16777216 + _r2 * 65536 + _r3 * 256 + _r4 ))
            _mask=$(( 4294967295 ^ (( 1 << (32 - _rlen) ) - 1) ))
            [ $(( _cip & _mask )) -eq $(( _rip & _mask )) ] && exit 7
        done
    [ "$?" = 7 ] && return 0
    return 1
}

# /24 WAN-интерфейса (double NAT: обычно 192.168.0/1/2.x от CPE провайдера)
WAN_NET=""
_wan_dev=$(uci -q get network.wan.device || true)
[ -z "$_wan_dev" ] && _wan_dev=$(ip -4 route show default 2>/dev/null | sed -n 's/.* dev \([^ ]*\).*/\1/p' | head -1)
if [ -n "$_wan_dev" ]; then
    WAN_NET=$(ip -4 addr show dev "$_wan_dev" 2>/dev/null \
        | sed -n 's/.*inet \([0-9]*\.[0-9]*\.[0-9]*\)\..*/\1/p' | head -1)
fi

# База — сеть LAN роутера (его Wi-Fi в том же бридже, отдельной сети у него
# нет). Идём вверх от неё: пользователю понятно "домашняя .1, сегмент .2",
# а занятые соседи (гостевой SSID и т.п.) отсеются проверкой выше.
LAN_BASE=$(uci -q get network.lan.ipaddr | sed -n 's/^\([0-9]*\.[0-9]*\.[0-9]*\)\..*/\1/p')

NET_BASE=""
if [ -n "$LAN_BASE" ]; then
    _o1=${LAN_BASE%%.*}; _rest=${LAN_BASE#*.}
    _o2=${_rest%%.*};    _o3=${_rest#*.}
    # Инкремент в пределах того же префикса RFC1918: 10.0.0 → 10.0.1,
    # 172.16.5 → 172.16.6, 192.168.1 → 192.168.2. Верхняя граница третьего
    # октета — 254; выше уходим в запасной список, а не за пределы блока.
    _n=$((_o3 + 1))
    while [ "$_n" -le 254 ]; do
        if ! net_taken "$_o1.$_o2.$_n"; then NET_BASE="$_o1.$_o2.$_n"; break; fi
        _n=$((_n + 1))
    done
fi

# Запасной список — редкие октеты, которые почти никто не занимает дефолтом.
if [ -z "$NET_BASE" ]; then
    for n in 20 21 22 30 40 50; do
        if ! net_taken "192.168.$n"; then NET_BASE="192.168.$n"; break; fi
    done
fi

# Автовыбор не молчаливый: показываем и даём заменить. Ошибка здесь стоит
# дорого — коллизия проявится после установки, когда сегмент уже поднят.
if [ -n "$NET_BASE" ]; then
    say "предлагаемая сеть сегмента: $NET_BASE.0/24 (шлюз $NET_BASE.1)"
    [ -n "$LAN_BASE" ] && say "  сеть LAN роутера: $LAN_BASE.x"
    [ -n "$WAN_NET" ]  && say "  сеть за WAN:      $WAN_NET.x (исключена)"
    ask_yn_default "Использовать её?" || NET_BASE=""
else
    warn "свободной подсети не нашлось автоматически"
fi

while [ -z "$NET_BASE" ]; do
    NET_BASE=$(ask "сеть сегмента, первые три октета (например 192.168.30)" "")
    case "$NET_BASE" in
        [0-9]*.[0-9]*.[0-9]*) ;;
        *) warn "нужен вид a.b.c, например 192.168.30"; NET_BASE=""; continue ;;
    esac
    case "$NET_BASE" in *[!0-9.]*|*.*.*.*) warn "нужен вид a.b.c, например 192.168.30"; NET_BASE=""; continue ;; esac
    _bad=0
    for _o in $(printf '%s\n' "$NET_BASE" | tr '.' ' '); do
        [ "$_o" -le 255 ] 2>/dev/null || _bad=1
    done
    [ "$_bad" = 1 ] && { warn "октет больше 255"; NET_BASE=""; continue; }
    if net_taken "$NET_BASE"; then
        warn "$NET_BASE.0/24 уже занята"
        ask_yn "Всё равно использовать?" || NET_BASE=""
    fi
done

GW="$NET_BASE.1"
say "подсеть:     $NET_BASE.0/24, шлюз $GW"

# tun p2p-подсеть: 172.16.x.0/30, свободная. Проверяем той же net_taken, что и
# сегмент: строковый grep не видел ни маршрутов, ни перекрытия широким
# префиксом (172.16.0.0/12 на роутере накрыл бы все четыре кандидата).
TUN_NET=""
for n in 16 17 18 19; do
    if ! net_taken "172.$n.0"; then TUN_NET="$n"; break; fi
done
TUN_FALLBACK=0
[ -z "$TUN_NET" ] && { TUN_NET=16; TUN_FALLBACK=1; }

# Имя и адрес туннеля попадут И в конфиг ядра, И в firewall (зона sbtun
# привязана к устройству по имени, правило sbtun_tcp — к адресу).
# Спрашиваем здесь, чтобы обе стороны заведомо совпали.
TUN_IF=$(ask "имя tun-интерфейса" "lxd-tun0")
case "$TUN_IF" in
    *[!A-Za-z0-9_-]*|"") die "недопустимое имя интерфейса: $TUN_IF" ;;
    # Односимвольный ответ — почти всегда y/n, набранный по инерции после
    # предыдущего Y/n-вопроса. Формально имя валидно, и такой промах уезжает
    # в конфиг ядра и в firewall незамеченным.
    [yYnNдДнН]) die "\"$TUN_IF\" похоже на ответ y/n, а не на имя интерфейса — Enter принимает lxd-tun0" ;;
esac
[ ${#TUN_IF} -le 15 ] || die "имя интерфейса длиннее 15 символов — ядро Linux такое не примет"
ip link show "$TUN_IF" >/dev/null 2>&1 && die "интерфейс $TUN_IF уже существует"

# Адрес туннеля тоже предъявляем: коллизия здесь проявится не при установке, а
# позже — правило sbtun_tcp привязано к адресу, и TCP из сегмента отобьётся при
# живых ICMP/DNS. Автовыбор показываем, ручной ввод валидируем полностью.
TUN_ADDR=""
_tun_auto="172.$TUN_NET.0.1"
say "предлагаемый адрес туннеля: $_tun_auto/30 (p2p)"
[ "$TUN_FALLBACK" = 1 ] && warn "все 172.{16..19}.0.x заняты — предложенный адрес тоже; задайте свой"
if ask_yn_default "Использовать его?"; then
    TUN_ADDR="$_tun_auto"
fi
while [ -z "$TUN_ADDR" ]; do
    TUN_ADDR=$(ask "адрес туннеля (p2p /30)" "")
    case "$TUN_ADDR" in
        [0-9]*.[0-9]*.[0-9]*.[0-9]*) ;;
        *) warn "адрес туннеля должен быть IPv4, например 172.16.0.1"; TUN_ADDR=""; continue ;;
    esac
    case "$TUN_ADDR" in *[!0-9.]*) warn "адрес туннеля должен быть IPv4"; TUN_ADDR=""; continue ;; esac
    _bad=0
    for _o in $(printf '%s\n' "$TUN_ADDR" | tr '.' ' '); do
        [ "$_o" -le 255 ] 2>/dev/null || _bad=1
    done
    [ "$_bad" = 1 ] && { warn "октет больше 255"; TUN_ADDR=""; continue; }
    # /30 туннеля внутри сети сегмента — маршруты передерутся
    _t3=${TUN_ADDR%.*}
    if [ "$_t3" = "$NET_BASE" ]; then
        warn "адрес туннеля не должен лежать в сети сегмента ($NET_BASE.0/24)"
        TUN_ADDR=""; continue
    fi
    if net_taken "$_t3"; then
        warn "$_t3.0/24 уже занята"
        ask_yn "Всё равно использовать?" || TUN_ADDR=""
    fi
done
say "туннель:     $TUN_IF ($TUN_ADDR/30)"

# DNS для клиентов сегмента уходит DHCP-опцией 6. Резолвер роутера сюда не
# годится: dnsmasq ответил бы мимо туннеля, и запросы утекли бы с домашнего IP.
# Нужен внешний адрес — но какой именно, решает владелец, а не скрипт.
SEG_DNS=$(ask "DNS для клиентов сегмента" "8.8.8.8")
case "$SEG_DNS" in
    [0-9]*.[0-9]*.[0-9]*.[0-9]*) ;;
    *) die "DNS должен быть IPv4-адресом: $SEG_DNS" ;;
esac
case "$SEG_DNS" in *[!0-9.]*) die "DNS должен быть IPv4-адресом: $SEG_DNS" ;; esac
for _o in $(printf '%s\n' "$SEG_DNS" | tr '.' ' '); do
    [ "$_o" -le 255 ] 2>/dev/null || die "октет больше 255: $SEG_DNS"
done
# Адрес самого роутера = dnsmasq = резолв мимо туннеля.
[ "$SEG_DNS" = "$GW" ] && die "нельзя указывать шлюз сегмента: резолв уйдёт мимо туннеля"
if [ -n "$LAN_BASE" ] && [ "${SEG_DNS%.*}" = "$LAN_BASE" ]; then
    warn "$SEG_DNS — адрес в LAN роутера; если это dnsmasq, резолв уйдёт мимо туннеля"
fi

# Имя моста тоже уходит в конфиг ядра — в include_interface (в UI лаунчера это
# поле "LAN interfaces"). Именно оно запирает перехват в VPN-сегменте: укажешь
# здесь br-lan — ядро утащит в туннель всю домашнюю сеть.
BR=$(ask "имя моста VPN-сегмента (пойдёт в include_interface)" "br-lxdvpn")
case "$BR" in
    *[!A-Za-z0-9_-]*|"") die "недопустимое имя моста: $BR" ;;
    br-lan) die "br-lan нельзя: это основная сеть, ядро утащит её в туннель" ;;
    [yYnNдДнН]) die "\"$BR\" похоже на ответ y/n, а не на имя моста — Enter принимает br-lxdvpn" ;;
esac
[ ${#BR} -le 15 ] || die "имя моста длиннее 15 символов — ядро Linux такое не примет"
ip link show "$BR" >/dev/null 2>&1 && die "интерфейс $BR уже существует"

# UCI-секции именуем от моста, чтобы всё хозяйство сегмента читалось единообразно
NET=$(printf '%s' "$BR" | sed 's/^br-//; s/[^A-Za-z0-9]//g')
[ -n "$NET" ] || NET="lxdvpn"
ZONE="$NET"; ZTUN="sbtun"

# Не затирать чужие секции: network.guest у человека может быть чем-то живым.
for sect in "network.$NET" "dhcp.$NET" "firewall.$ZONE" "firewall.$ZTUN"; do
    [ -n "$(uci -q get "$sect" 2>/dev/null)" ] && \
        die "uci-секция $sect уже существует — выберите другое имя моста или удалите её вручную"
done
say "мост/сеть:   $BR / uci-секция \"$NET\""

# ── 5. Бинарь ───────────────────────────────────────────────────────────────
step "Загрузка sing-box-lx"

if [ -x "$BIN" ] && "$BIN" version 2>/dev/null | grep -q lx; then
    say "уже установлен: $("$BIN" version | head -1)"
    ask_yn "Перекачать последнюю версию?" && NEED_DL=1 || NEED_DL=0
else
    NEED_DL=1
fi

if [ "$NEED_DL" = 1 ]; then
    command -v wget >/dev/null || die "нет wget"
    # /releases/latest игнорирует pre-release, а в этом репозитории релизы
    # выходят именно как rc — поэтому берём первый из общего списка (он новейший).
    TAG=$(wget -qO- "https://api.github.com/repos/$REPO/releases?per_page=1" 2>/dev/null \
          | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
    # запасной путь: стабильный релиз, если список не отдался
    [ -z "$TAG" ] && TAG=$(wget -qO- "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
                           | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
    if [ -z "$TAG" ]; then
        warn "не смог узнать последний релиз (нет интернета или лимит GitHub API)"
        TAG=$(ask "укажите тег релиза вручную (см. https://github.com/$REPO/releases)" "")
        [ -n "$TAG" ] || die "тег не задан"
    fi
    VER="${TAG#v}"
    BASE="https://github.com/$REPO/releases/download/$TAG"
    TARBALL="sing-box-$VER-$ARCH.tar.gz"
    say "версия:      $TAG"

    cd /tmp
    rm -f "$TARBALL" SHA256SUMS
    # -O обязателен: GitHub отдаёт ассет через redirect, и busybox-wget без -O
    # сохраняет файл под именем из КОНЕЧНОГО URL — не под именем ассета.
    wget -q -O "$TARBALL" "$BASE/$TARBALL" || die "не скачался $TARBALL
  (если ошибка про SSL/TLS: opkg install ca-bundle libustream-mbedtls
   либо скачайте архив на компьютере и залейте бинарь вручную —
   см. §8.3 в docs-lx/lxd-daemon.ru.md)"
    # при неудаче -O оставляет пустой файл — убрать, иначе сверка ниже
    # примет его за настоящий SHA256SUMS и завалит установку
    wget -q -O SHA256SUMS "$BASE/SHA256SUMS" \
        || { rm -f SHA256SUMS; warn "SHA256SUMS не скачался — пропускаю сверку"; }

    if [ -f SHA256SUMS ]; then
        # сверка по точному имени файла: substring-grep мог бы зацепить соседний ассет
        WANT=$(awk -v f="$TARBALL" '$NF==f {print $1; exit}' SHA256SUMS)
        GOT=$(sha256sum "$TARBALL" | awk '{print $1}')
        [ "$WANT" = "$GOT" ] || die "sha256 НЕ СОВПАЛ (ожидался $WANT, получен $GOT)"
        say "sha256:      совпал"
    fi

    tar xzf "$TARBALL" || die "не распаковался"
    # точный путь: find по всему /tmp мог бы подобрать старый бинарь от прежней распаковки
    SRC="/tmp/sing-box-$VER-$ARCH/sing-box"
    [ -f "$SRC" ] || SRC=$(find "/tmp/sing-box-$VER-$ARCH" -name sing-box -type f 2>/dev/null | head -1)
    [ -n "$SRC" ] && [ -f "$SRC" ] || die "в архиве нет бинаря sing-box"
    [ -x "$INIT" ] && "$INIT" stop >/dev/null 2>&1 || true
    mv "$SRC" "$BIN"; chmod +x "$BIN"
    rm -rf "/tmp/$TARBALL" /tmp/SHA256SUMS "/tmp/sing-box-$VER-$ARCH"
fi

"$BIN" version >/dev/null 2>&1 || die "бинарь не запускается (не та архитектура?)"
"$BIN" version | grep -qE "with_lx_command|with_lxd" \
    || die "в сборке нет тега with_lx_command/with_lxd — режим lxd недоступен"
say "готов:       $("$BIN" version | head -1)"

# ── 6. Демон: state, daemon.json, служба ────────────────────────────────────
step "Демон"

mkdir -p "$STATE_DIR"

# Секрет: команда из офиц. рецепта (xxd) на busybox не работает — нет ни xxd, ни od.
if [ -f "$STATE_DIR/daemon.json" ]; then
    SECRET=$(sed -n 's/.*"secret" *: *"\([a-f0-9]*\)".*/\1/p' "$STATE_DIR/daemon.json" | head -1)
fi
if [ -z "$SECRET" ]; then
    if command -v openssl >/dev/null 2>&1; then
        SECRET=$(openssl rand -hex 32)
    else
        # busybox-вариант: openssl на минимальных прошивках отсутствует
        SECRET=$(head -c 32 /dev/urandom | hexdump -ve '1/1 "%02x"')
    fi
fi
[ ${#SECRET} -eq 64 ] || die "секрет получился неправильной длины (${#SECRET})"

LAN_IP=$(uci -q get network.lan.ipaddr); [ -z "$LAN_IP" ] && LAN_IP="127.0.0.1"

# порт может держать другой сервис (или прежняя установка вручную)
if netstat -ltn 2>/dev/null | grep -q ":$PORT "; then
    "$INIT" running 2>/dev/null || die "порт $PORT уже занят другим процессом (netstat -ltnp | grep $PORT)"
fi

# ── Доступ к управлению снаружи (по умолчанию — нет) ───────────────────────
WAN_IP=""; WAN_EXPOSE=0
say ""
say "По умолчанию управление доступно только из LAN (loopback + LAN)."
say "Канал защищён mTLS: без клиентского сертификата TLS-рукопожатие не проходит."
if ask_yn "Разрешить доступ к управлению снаружи (из WAN)?"; then
    WAN_EXPOSE=1

    if ask_yn "У роутера статический внешний IP?"; then
        # ubus — штатный источник; ip route — fallback, если wan назван иначе
        DET=$(ubus call network.interface.wan status 2>/dev/null \
              | sed -n 's/.*"address": *"\([0-9.]*\)".*/\1/p' | head -1)
        [ -z "$DET" ] && DET=$(ip -4 route get 8.8.8.8 2>/dev/null | sed -n 's/.*src \([0-9.]*\).*/\1/p')

        if [ -n "$DET" ]; then
            say "определён внешний адрес: $DET"
            # Фиксация надёжнее по смыслу, но при смене адреса демон не сможет
            # забиндиться. 0.0.0.0 слушает все интерфейсы и смену переживёт.
            if ask_yn_default "Фиксировать этот адрес? (n — слушать 0.0.0.0)"; then
                WAN_IP="$DET"
            else
                WAN_IP="0.0.0.0"
            fi
        else
            warn "не удалось определить внешний адрес — слушаю 0.0.0.0"
            WAN_IP="0.0.0.0"
        fi
    else
        # Адрес меняется — фиксировать нечего
        WAN_IP="0.0.0.0"
        say "адрес не фиксируем: демон будет слушать 0.0.0.0 (все интерфейсы)"
    fi

    say "порт $PORT будет открыт в firewall для зоны wan"
fi

# ВАЖНО: loopback ПЕРВЫМ. Первый адрес идёт в enrollment-invite и в него же
# стучится локальный CLI; иначе `client add/list/remove` → 403 loopback-only.
# 0.0.0.0 идёт ОДИН: он уже включает loopback и LAN, а явный второй биндинг
# на 127.0.0.1 рядом с ним валит демон (`bind: address already in use`,
# ядро не поднимается вовсе — проверено). По той же причине никакой адрес
# не должен попасть в список дважды: LAN_IP при нестандартном имени
# lan-секции съезжает в fallback 127.0.0.1 — дубль не пишем.
if [ "$WAN_IP" = "0.0.0.0" ]; then
    LISTEN_ADDR="\"0.0.0.0\""
else
    LISTEN_ADDR="\"127.0.0.1\""
    [ "$LAN_IP" != "127.0.0.1" ] && LISTEN_ADDR="$LISTEN_ADDR, \"$LAN_IP\""
    [ "$WAN_EXPOSE" = 1 ] && [ "$WAN_IP" != "$LAN_IP" ] && LISTEN_ADDR="$LISTEN_ADDR, \"$WAN_IP\""
fi

# log_file на tmpfs: ротируемый лог демона не должен изнашивать overlay-флеш.
cat > "$STATE_DIR/daemon.json" <<EOF
{
  "listen": {
    "address": [$LISTEN_ADDR],
    "port": $PORT
  },
  "log_file": "/tmp/lxd.log",
  "tls": true,
  "secret": "$SECRET"
}
EOF
chmod 600 "$STATE_DIR/daemon.json"

# Каркасный конфиг: сегмент работает, выходит через WAN (direct).
# Боевой upstream заливается потом одним apply — каркас при этом не трогается.
cat > "$STATE_DIR/min.json" <<EOF
{
  "log": { "level": "info" },
  "inbounds": [
    {
      "type": "tun",
      "tag": "tun-in",
      "interface_name": "$TUN_IF",
      "address": ["$TUN_ADDR/30"],
      "mtu": 1400,
      "auto_route": true,
      "strict_route": false,
      "include_interface": ["$BR"],
      "stack": "system"
    }
  ],
  "outbounds": [ { "type": "direct", "tag": "direct-out" } ],
  "route": { "final": "direct-out", "auto_detect_interface": true }
}
EOF
"$BIN" check -c "$STATE_DIR/min.json" >/dev/null 2>&1 || die "каркасный конфиг не прошёл sing-box check"

cat > "$INIT" <<EOF
#!/bin/sh /etc/rc.common
# sing-box-lx daemon (lxd)
START=95
USE_PROCD=1

start_service() {
    procd_open_instance
    procd_set_param command $BIN lxd --state-dir $STATE_DIR -c $STATE_DIR/min.json
    procd_set_param respawn
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
EOF
chmod +x "$INIT"
"$INIT" enable >/dev/null 2>&1

# sysupgrade -b не забирает ни бинарь, ни state, ни init-скрипт
for p in "$INIT" "/etc/sing-box-lxd/" "$BIN"; do
    grep -qxF "$p" /etc/sysupgrade.conf 2>/dev/null || echo "$p" >> /etc/sysupgrade.conf
done
say "служба:      $INIT (procd, autostart)"

# ── 7. Сеть: мост → интерфейс → DHCP ───────────────────────────────────────
step "Сеть"

# bridge_empty обязателен: у моста нет ethernet-портов, только Wi-Fi-AP,
# которые цепляются динамически при старте hostapd. Без флага netifd мост не поднимет.
# uci set <секция>=<тип> создаёт секцию, но по пути печатает "Invalid
# argument" — секции ещё нет, и uci ругается, хотя работу делает. Гасим stderr и код возврата: шум не должен
# читаться как сбой, а ненулевой статус — обрывать установку через set -e; результат проверяем
# ниже по факту (мост поднимается, fw4 перезагружается без ошибок).
uci -q delete "network.${NET}dev" 2>/dev/null || true
uci set "network.${NET}dev=device" 2>/dev/null || true
uci set "network.${NET}dev.name=$BR"
uci set "network.${NET}dev.type=bridge"
uci set "network.${NET}dev.bridge_empty=1"

uci set "network.$NET=interface" 2>/dev/null || true
uci set "network.$NET.device=$BR"
uci set "network.$NET.proto=static"
uci set "network.$NET.ipaddr=$GW"
uci set "network.$NET.netmask=255.255.255.0"
# ip6assign НЕ задаём: иначе клиент получит IPv6 по RA и утечёт мимо туннеля
uci commit network

# Опция 6 — внешний DNS, НЕ dnsmasq роутера: иначе резолв уйдёт мимо туннеля
uci set "dhcp.$NET=dhcp" 2>/dev/null || true
uci set "dhcp.$NET.interface=$NET"
uci set "dhcp.$NET.start=100"
uci set "dhcp.$NET.limit=150"
uci set "dhcp.$NET.leasetime=12h"
uci set "dhcp.$NET.dhcpv4=server"
uci -q delete "dhcp.$NET.dhcp_option" 2>/dev/null || true
uci add_list "dhcp.$NET.dhcp_option=6,$SEG_DNS"
uci commit dhcp

# reload_config иногда не дёргает netifd на свежесозданную device-секцию, а
# reload — no-op, если netifd считает свой стейт актуальным (даже когда
# устройство реально отсутствует). Эскалация: reload → ifup → reload → ifup;
# ifup заставляет netifd пересобрать интерфейс безусловно.
reload_config
n=0
while ! ip link show "$BR" >/dev/null 2>&1; do
    n=$((n+1))
    [ "$n" -eq 3 ] && ifup "$NET" >/dev/null 2>&1
    [ "$n" -eq 6 ] && /etc/init.d/network reload >/dev/null 2>&1
    [ "$n" -eq 9 ] && ifup "$NET" >/dev/null 2>&1
    [ "$n" -ge 20 ] && die "мост $BR не поднялся. Диагностика:
  ifstatus $NET
  uci show network | grep $NET
  logread | grep -iE 'netifd|bridge' | tail -20"
    sleep 1
done
say "мост:        $BR ($GW/24)"

# ⚠ рестарт dnsmasq на секунду роняет DNS всего LAN
/etc/init.d/dnsmasq restart >/dev/null 2>&1
say "dhcp:        $NET_BASE.100–249, DNS $SEG_DNS"

# ── 8. Firewall: fail-closed ───────────────────────────────────────────────
step "Firewall"

# Единственный путь наружу — в зону туннеля. Forwarding в wan НЕ создаём:
# упало ядро → сегмент без интернета, но и утечки с домашнего IP нет.
uci set "firewall.$ZONE=zone" 2>/dev/null || true
uci set "firewall.$ZONE.name=$ZONE"
uci -q delete "firewall.$ZONE.network" 2>/dev/null || true
uci add_list "firewall.$ZONE.network=$NET"
uci set "firewall.$ZONE.input=ACCEPT"     # клиентам нужен DHCP и шлюз
uci set "firewall.$ZONE.output=ACCEPT"
uci set "firewall.$ZONE.forward=REJECT"

uci set "firewall.$ZTUN=zone" 2>/dev/null || true
uci set "firewall.$ZTUN.name=$ZTUN"
uci -q delete "firewall.$ZTUN.device" 2>/dev/null || true
uci add_list "firewall.$ZTUN.device=$TUN_IF"
uci set "firewall.$ZTUN.input=REJECT"
uci set "firewall.$ZTUN.output=ACCEPT"
uci set "firewall.$ZTUN.forward=REJECT"

uci set "firewall.${ZONE}2tun=forwarding" 2>/dev/null || true
uci set "firewall.${ZONE}2tun.src=$ZONE"
uci set "firewall.${ZONE}2tun.dest=$ZTUN"

# stack:"system" редиректит TCP на локальный листенер на адресе туннеля.
# Для ядра это входящий пакет → INPUT в зоне sbtun (input=REJECT) → TCP молча
# дохнет при живых UDP/ICMP. Правило привязано к IP: сменится address у
# tun-inbound — правило надо править (иначе connection refused).
uci set "firewall.${ZTUN}_tcp=rule" 2>/dev/null || true
uci set "firewall.${ZTUN}_tcp.name=Allow-sbtun-systemstack-tcp"
uci set "firewall.${ZTUN}_tcp.src=$ZTUN"
uci set "firewall.${ZTUN}_tcp.dest_ip=$TUN_ADDR"
uci set "firewall.${ZTUN}_tcp.proto=tcp"
uci set "firewall.${ZTUN}_tcp.target=ACCEPT"
# Порт управления снаружи — только если пользователь явно разрешил.
# Без этого правила адрес в listen бесполезен: зона wan режет input.
if [ "$WAN_EXPOSE" = 1 ]; then
    uci set "firewall.lxd_admin_wan=rule" 2>/dev/null || true
    uci set "firewall.lxd_admin_wan.name=Allow-lxd-admin-wan"
    uci set "firewall.lxd_admin_wan.src=wan"
    uci set "firewall.lxd_admin_wan.proto=tcp"
    uci set "firewall.lxd_admin_wan.dest_port=$PORT"
    uci set "firewall.lxd_admin_wan.target=ACCEPT"
else
    uci -q delete firewall.lxd_admin_wan 2>/dev/null || true
fi

uci commit firewall
fw4 reload >/dev/null 2>&1
say "зоны:        $ZONE → $ZTUN (единственный forwarding, fail-closed)"
[ "$WAN_EXPOSE" = 1 ] && say "wan:         порт $PORT ОТКРЫТ снаружи (mTLS)" \
                      || say "wan:         закрыт (управление только из LAN)"

# ── 9. Запуск ядра ─────────────────────────────────────────────────────────
step "Запуск демона"

"$INIT" restart >/dev/null 2>&1
sleep 6
ip link show "$TUN_IF" >/dev/null 2>&1 || warn "туннель $TUN_IF не появился — проверьте: logread | grep sing-box"
say "демон:       слушает [$LISTEN_ADDR] порт $PORT (TLS+mTLS)"

# ── 10. Wi-Fi (последним: wifi reload рвёт радио ~10 сек) ──────────────────
step "Wi-Fi"

add_ap() {  # add_ap <секция> <радио> <ssid>
    uci set "wireless.$1=wifi-iface"
    uci set "wireless.$1.device=$2"
    uci set "wireless.$1.mode=ap"
    uci set "wireless.$1.network=$NET"
    uci set "wireless.$1.ssid=$3"
    uci set "wireless.$1.encryption=psk2"
    uci set "wireless.$1.key=$WIFI_KEY"
    uci -q delete "wireless.$1.disabled" 2>/dev/null || true
}
[ -n "$SSID_5G" ] && { add_ap "${NET}_5g" "$RADIO_5G" "$SSID_5G"; say "  5 ГГц:   $SSID_5G"; }
[ -n "$SSID_2G" ] && { add_ap "${NET}_2g" "$RADIO_2G" "$SSID_2G"; say "  2.4 ГГц: $SSID_2G"; }
uci commit wireless
say "секции записаны — радио поднимем последним шагом"

# ── 11. Enrollment ─────────────────────────────────────────────────────────
# ДО подъёма Wi-Fi: `wifi reload` рвёт радио, а вместе с ним и SSH-сессию, из
# которой запущен скрипт. Прервись он здесь — invite не выпустится и итог не
# напечатается: сеть поднята, а подключиться нечем.
step "Сопряжение"

# Код живёт в памяти процесса, не в state-dir: любой рестарт демона между
# `client add` и вводом кода в лаунчер убьёт его (enroll: no active enrollment code).
# Invite демона: адрес#отпечаток-сервера#код (lxd/admin.go, handleClientCode).
# Адрес внутри — первый listen-адрес (loopback); лаунчеру нужен LAN-адрес,
# поэтому меняем ТОЛЬКО адресную часть. Хвост не трогать: посередине стоит
# отпечаток TLS-сертификата, по нему лаунчер пинит сервер — подставить туда
# что-то своё значит сломать сопряжение ("server fingerprint does not match").
INVITE=$("$BIN" lxd client add --name launcher --state-dir "$STATE_DIR" 2>&1 | tail -1)
FP=$(printf '%s' "$INVITE" | awk -F'#' '{print $2}')
CODE=$(printf '%s' "$INVITE" | awk -F'#' '{print $3}')
if [ -z "$FP" ] || [ -z "$CODE" ]; then
    warn "не удалось выпустить invite (демон не отвечает?): $INVITE"
    warn "после починки выпустить вручную: sing-box lxd client add --name launcher"
    FP="<отпечаток>"; CODE="<код>"
fi

# ── Итог ───────────────────────────────────────────────────────────────────
# Печатаем ДО подъёма Wi-Fi и дублируем в файл: `wifi reload` рвёт SSH-сессию,
# и вывод на экране может не дойти. Файл останется на роутере в любом случае.
SUMMARY="/root/lxd-setup-summary.txt"
# 0.0.0.0 в сводке бессмыслен как адрес подключения — подставляем плейсхолдер
WAN_SHOW="$WAN_IP"; [ "$WAN_IP" = "0.0.0.0" ] && WAN_SHOW="<внешний-IP-роутера>"
exec 3>&1
{
printf '\n'
printf '════════════════════════════════════════════════════════════\n'
printf '  ГОТОВО\n'
printf '════════════════════════════════════════════════════════════\n\n'

printf 'Pair invite:     %s:%s#%s#%s\n' "$LAN_IP" "$PORT" "$FP" "$CODE"
printf '\n'
printf '── для конфига ядра ─────────────────────────────────────────\n'
printf 'tun name:            %s\n' "$TUN_IF"
printf 'tun address:         %s/30\n' "$TUN_ADDR"
printf 'include_interface:   %s              (в UI лаунчера — поле "LAN interfaces")\n' "$BR"
printf '\n'
printf 'ВНИМАНИЕ: имя и адрес туннеля прописаны в firewall и должны\n'
printf 'совпадать с конфигом ядра. Меняете в конфиге — правьте здесь:\n'
printf '  firewall.%s.device      = %s\n' "$ZTUN" "$TUN_IF"
printf '  firewall.%s_tcp.dest_ip = %s\n' "$ZTUN" "$TUN_ADDR"
printf 'Рассинхрон имени → сегмент без интернета; рассинхрон адреса →\n'
printf 'connection refused на TCP при живых ICMP/DNS.\n'
printf '─────────────────────────────────────────────────────────────\n'
printf '\n'
[ -n "$SSID_5G" ] && printf 'Wi-Fi 5 ГГц:     %s\n' "$SSID_5G"
[ -n "$SSID_2G" ] && printf 'Wi-Fi 2.4 ГГц:   %s\n' "$SSID_2G"
printf 'пароль Wi-Fi:    %s\n' "$WIFI_KEY"
printf 'сегмент:         %s.0/24 (шлюз %s)\n' "$NET_BASE" "$GW"
printf 'управление:      https://%s:%s (TLS+mTLS)\n' "$LAN_IP" "$PORT"
if [ "$WAN_EXPOSE" = 1 ]; then
    printf 'снаружи:         https://%s:%s — порт открыт в firewall\n' "$WAN_SHOW" "$PORT"
    printf '                 invite для внешнего клиента: %s:%s#%s#<новый код>\n' "$WAN_SHOW" "$PORT" "$FP"
    printf '                 (новый код: sing-box lxd client add --name <имя>)\n'
else
    printf 'снаружи:         закрыт. Нужен доступ не из дома — ssh-туннель:\n'
    printf '                 ssh -N -L %s:%s:%s root@<роутер>\n' "$PORT" "$LAN_IP" "$PORT"
    printf '                 затем подключаться на 127.0.0.1:%s\n' "$PORT"
fi
printf '\n'
printf 'Сейчас ядро крутит каркасный конфиг: сегмент работает, но выходит\n'
printf 'НАПРЯМУЮ через WAN (direct). Боевой upstream заливается из лаунчера\n'
printf 'одним apply — сеть, firewall и Wi-Fi при этом не трогаются.\n'
printf '\n'
printf 'ВАЖНО: не перезапускайте демон, пока не введёте invite —\n'
printf 'код сопряжения живёт в памяти процесса и рестарт его убьёт.\n'
printf '\n'
printf 'Проверка:  logread | grep sing-box | tail\n'
printf 'Клиенты:   sing-box lxd client list\n'
printf 'Эта сводка сохранена: %s\n' "$SUMMARY"
printf '════════════════════════════════════════════════════════════\n'
} | tee "$SUMMARY" >&3
exec 3>&-
chmod 600 "$SUMMARY"

# ── Последний шаг: поднимаем радио ─────────────────────────────────────────
# ТОЛЬКО после того, как всё важное напечатано и сохранено. `wifi reload`
# гасит оба радио на ~10 секунд и рвёт SSH-сессию, из которой запущен скрипт;
# отвязываем от терминала (nohup в busybox нет), чтобы разрыв его не убил.
printf '\n'
printf 'Осталось поднять Wi-Fi — оба радио погаснут на ~10 секунд, и если вы\n'
printf 'подключены по Wi-Fi, SSH-сессия прервётся. Вся работа уже сделана,\n'
printf 'сводка сохранена в %s\n' "$SUMMARY"
printf '\n'
printf 'Скопируйте invite и данные выше, затем нажмите Enter — подниму Wi-Fi.\n'
printf 'Enter для продолжения: '
read -r _ </dev/tty || true
printf '\n'
wifi reload >/tmp/lxd-wifi-reload.log 2>&1 </dev/null &
printf 'Wi-Fi поднимается. Через ~15 секунд сети "%s"%s будут в эфире.\n' \
    "${SSID_5G:-$SSID_2G}" "$([ -n "$SSID_5G" ] && [ -n "$SSID_2G" ] && printf ' и "%s"' "$SSID_2G")"
exit 0
