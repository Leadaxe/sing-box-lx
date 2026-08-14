#!/bin/sh
# lxd-openwrt-uninstall.sh — полный откат установки lxd-openwrt-setup.sh.
# Снимает VPN-SSID сегмент и демон sing-box-lx; основную сеть не трогает.
# Терпит полуустановленное состояние: годится и после установки, оборванной
# на любом шаге, — каждый шаг самодостаточен и молча пропускает отсутствующее.
#
# Запуск на роутере:
#   wget -O /tmp/lxd-uninstall.sh https://raw.githubusercontent.com/Leadaxe/sing-box-lx/lx/scripts-lx/openwrt/lxd-openwrt-uninstall.sh
#   sh /tmp/lxd-uninstall.sh          # спросит подтверждение
#   sh /tmp/lxd-uninstall.sh --yes    # без вопросов (нет tty / автоматизация)

set -u   # НЕ -e: откат обязан дойти до конца по любым обломкам

VERSION="1.0"
STATE_ROOT="/etc/sing-box-lxd"
BIN="/usr/bin/sing-box"
INIT="/etc/init.d/sing-box-lxd"
SUMMARY="/root/lxd-setup-summary.txt"
ZTUN="sbtun"
YES="${1:-}"

say()  { printf '%s\n' "$*"; }
warn() { printf '!! %s\n' "$*" >&2; }

[ "$(id -u)" = 0 ] || { warn "нужен root"; exit 1; }

# ── имя сегмента ────────────────────────────────────────────────────────────
# Надёжный след установки — forwarding в зону туннеля: секция ${NET}2tun с
# dest='sbtun'. Если firewall не успел записаться (оборванная установка),
# fallback — дефолт установщика, при живом tty его можно поправить.
NET=$(uci show firewall 2>/dev/null \
      | sed -n "s/^firewall\.\([A-Za-z0-9_]*\)2tun\.dest='$ZTUN'\$/\1/p" | head -1)
if [ -z "$NET" ]; then
    NET="lxdvpn"
    if [ "$YES" != "--yes" ] && ( : </dev/tty ) 2>/dev/null; then
        printf 'uci-имя сегмента [%s]: ' "$NET" >&2
        read -r _a </dev/tty || _a=""
        [ -n "$_a" ] && NET="$_a"
    fi
fi
BR=$(uci -q get "network.${NET}dev.name")
[ -n "$BR" ] || BR="br-$NET"

say "снимаю: uci-секции \"$NET\", мост $BR, служба $INIT, state $STATE_ROOT"
if [ "$YES" != "--yes" ]; then
    printf 'Продолжить? [y/N]: ' >&2
    read -r _a </dev/tty 2>/dev/null || _a=""
    case "$_a" in [yYдД]*) : ;; *) say "отменено"; exit 0 ;; esac
fi

# ── служба, файлы, персистентность ──────────────────────────────────────────
[ -x "$INIT" ] && { "$INIT" stop >/dev/null 2>&1; "$INIT" disable >/dev/null 2>&1; }
rm -f "$INIT" "$SUMMARY"
rm -rf "$STATE_ROOT"
[ -f /etc/sysupgrade.conf ] && sed -i '\#sing-box#d' /etc/sysupgrade.conf

if [ -x "$BIN" ]; then
    DELBIN=1
    if [ "$YES" != "--yes" ]; then
        printf 'Удалить бинарь %s (при переустановке скачается заново, ~21 МБ)? [Y/n]: ' "$BIN" >&2
        read -r _a </dev/tty 2>/dev/null || _a=""
        case "$_a" in [nNнН]*) DELBIN=0 ;; esac
    fi
    [ "$DELBIN" = 1 ] && rm -f "$BIN"
fi

# ── сеть: ifdown ДО удаления секций — netifd разбирает интерфейс, пока ещё
# знает о нём; удаление конфига без этого оставляет мост-сироту (рассинхрон)
ifdown "$NET" >/dev/null 2>&1

for s in "wireless.${NET}_5g" "wireless.${NET}_2g" \
         "network.$NET" "network.${NET}dev" "dhcp.$NET" \
         "firewall.$NET" "firewall.$ZTUN" "firewall.${NET}2tun" \
         "firewall.${ZTUN}_tcp" "firewall.lxd_admin_wan"; do
    uci -q delete "$s" 2>/dev/null
done
uci commit

/etc/init.d/network reload >/dev/null 2>&1
fw4 reload >/dev/null 2>&1
/etc/init.d/dnsmasq restart >/dev/null 2>&1   # ⚠ роняет DNS всего LAN на ~секунду

sleep 2
if ip link show "$BR" >/dev/null 2>&1; then
    # конфига больше нет — netifd мост не держит, добить сироту безопасно
    warn "мост $BR ещё виден — удаляю вручную"
    ip link del "$BR" 2>/dev/null
fi

say "готово: сегмент снят, основная сеть не тронута."
say "Осталось перезапустить радио — Wi-Fi моргнёт ~10 секунд (SSH по Wi-Fi порвёт)."
wifi reload >/tmp/lxd-wifi-reload.log 2>&1 </dev/null &
exit 0
