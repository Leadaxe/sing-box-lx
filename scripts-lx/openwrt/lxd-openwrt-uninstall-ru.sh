#!/bin/sh
# lxd-openwrt-uninstall.sh — полный откат установки lxd-openwrt-setup.sh.
# Снимает VPN-SSID сегмент и демон sing-box-lx; основную сеть не трогает.
# Терпит полуустановленное состояние: годится и после установки, оборванной
# на любом шаге, — каждый шаг самодостаточен и молча пропускает отсутствующее.
#
# Два режима:
#   снос (по умолчанию) — удалить службу, state, uci-секции сегмента;
#   --restore           — то же + восстановить конфиги из pre-lxd бэкапа,
#                         который установщик снял в /root/backup-pre-lxd-*.tar.gz,
#                         и перезагрузить роутер («как было до установки»).
#
# Запуск на роутере:
#   wget -O /tmp/lxd-uninstall.sh https://raw.githubusercontent.com/Leadaxe/sing-box-lx/lx/scripts-lx/openwrt/lxd-openwrt-uninstall-ru.sh
#   sh /tmp/lxd-uninstall.sh              # спросит подтверждение
#   sh /tmp/lxd-uninstall.sh --yes        # без вопросов (нет tty / автоматизация)
#   sh /tmp/lxd-uninstall.sh --restore    # откат на pre-lxd бэкап + reboot

set -u   # НЕ -e: откат обязан дойти до конца по любым обломкам

VERSION="1.1"
STATE_ROOT="/etc/sing-box-lxd"
BIN="/usr/bin/sing-box"
INIT="/etc/init.d/sing-box-lxd"
SUMMARY="/root/lxd-setup-summary.txt"
ZTUN="sbtun"

say()  { printf '%s\n' "$*"; }
warn() { printf '!! %s\n' "$*" >&2; }

[ "$(id -u)" = 0 ] || { warn "нужен root"; exit 1; }

YES=0; RESTORE=0
for _arg in "$@"; do
    case "$_arg" in
        --yes)     YES=1 ;;
        --restore) RESTORE=1 ;;
        *) warn "неизвестный аргумент: $_arg (есть --yes и --restore)"; exit 1 ;;
    esac
done

BK=$(ls /root/backup-pre-lxd-*.tar.gz 2>/dev/null | head -1)

# ── имя сегмента ────────────────────────────────────────────────────────────
# Надёжный след установки — forwarding в зону туннеля: секция ${NET}2tun с
# dest='sbtun'. Если firewall не успел записаться (оборванная установка),
# fallback — дефолт установщика, при живом tty его можно поправить.
NET=$(uci show firewall 2>/dev/null \
      | sed -n "s/^firewall\.\([A-Za-z0-9_]*\)2tun\.dest='$ZTUN'\$/\1/p" | head -1)
if [ -z "$NET" ]; then
    NET="lxdvpn"
    if [ "$YES" = 0 ] && ( : </dev/tty ) 2>/dev/null; then
        printf 'uci-имя сегмента [%s]: ' "$NET" >&2
        read -r _a </dev/tty || _a=""
        [ -n "$_a" ] && NET="$_a"
    fi
fi
BR=$(uci -q get "network.${NET}dev.name")
[ -n "$BR" ] || BR="br-$NET"

# ── что будем делать ────────────────────────────────────────────────────────
if [ "$RESTORE" = 1 ] && [ -z "$BK" ]; then
    warn "pre-lxd бэкап не найден (/root/backup-pre-lxd-*.tar.gz) — делаю обычный снос"
    RESTORE=0
fi
if [ "$RESTORE" = 0 ] && [ "$YES" = 0 ] && [ -n "$BK" ]; then
    printf 'Найден pre-lxd бэкап: %s\nВосстановить из него конфиги и перезагрузить (полный откат)? [y/N]: ' "$BK" >&2
    read -r _a </dev/tty 2>/dev/null || _a=""
    case "$_a" in [yYдД]*) RESTORE=1 ;; esac
fi

if [ "$RESTORE" = 1 ]; then
    say "режим: восстановление из $BK + reboot"
else
    say "режим: снос (uci-секции \"$NET\", мост $BR, служба $INIT, state $STATE_ROOT)"
fi
if [ "$YES" = 0 ]; then
    printf 'Продолжить? [y/N]: ' >&2
    read -r _a </dev/tty 2>/dev/null || _a=""
    case "$_a" in [yYдД]*) : ;; *) say "отменено"; exit 0 ;; esac
fi

# ── служба, файлы, персистентность (нужно в обоих режимах: restore не
# удаляет файлы, появившиеся после снятия бэкапа, и не возвращает дефолтный
# sysupgrade.conf — неизменённые файлы в бэкап не попадают) ─────────────────
[ -x "$INIT" ] && { "$INIT" stop >/dev/null 2>&1; "$INIT" disable >/dev/null 2>&1; }
rm -f "$INIT" "$SUMMARY"
rm -rf "$STATE_ROOT"
[ -f /etc/sysupgrade.conf ] && sed -i '\#sing-box#d' /etc/sysupgrade.conf

if [ -x "$BIN" ]; then
    DELBIN=1
    if [ "$RESTORE" = 0 ] && [ "$YES" = 0 ]; then
        printf 'Удалить бинарь %s (при переустановке скачается заново, ~21 МБ)? [Y/n]: ' "$BIN" >&2
        read -r _a </dev/tty 2>/dev/null || _a=""
        case "$_a" in [nNнН]*) DELBIN=0 ;; esac
    fi
    [ "$DELBIN" = 1 ] && rm -f "$BIN"
fi

# ── режим restore: конфиги вернёт бэкап, uci-снос не нужен ──────────────────
if [ "$RESTORE" = 1 ]; then
    if ! tar -tzf "$BK" >/dev/null 2>&1; then
        warn "архив $BK битый — restore отменён, продолжаю обычным сносом"
    else
        say "восстанавливаю конфиги и перезагружаюсь (SSH-сессия оборвётся, роутер вернётся через ~2 минуты)"
        # отвязка от терминала: reboot рвёт SSH, цепочка не должна умереть с ним
        ( sysupgrade -r "$BK" && sleep 2 && reboot ) >/dev/null 2>&1 </dev/null &
        exit 0
    fi
fi

# ── снос: сеть. ifdown ДО удаления секций — netifd разбирает интерфейс, пока
# ещё знает о нём; удаление конфига без этого оставляет мост-сироту ─────────
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
[ -n "$BK" ] && say "pre-lxd бэкап оставлен на месте: $BK"
say "Осталось перезапустить радио — Wi-Fi моргнёт ~10 секунд (SSH по Wi-Fi порвёт)."
wifi reload >/tmp/lxd-wifi-reload.log 2>&1 </dev/null &
exit 0
