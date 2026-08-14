#!/bin/sh
# lxd-openwrt-uninstall.sh — full rollback of a lxd-openwrt-setup.sh install.
# Removes the VPN-SSID segment and the sing-box-lx daemon; the main network is
# untouched. Tolerates a half-installed state: works after an install broken
# at any step — every step is self-contained and silently skips what's absent.
#
# Two modes:
#   teardown (default) — remove the service, state, and the segment's uci sections;
#   --restore          — same, plus restore the configs from the pre-lxd backup
#                        the installer took into /root/backup-pre-lxd-*.tar.gz,
#                        and reboot the router ("as before the install").
#
# Run on the router:
#   wget -O /tmp/lxd-uninstall.sh https://raw.githubusercontent.com/Leadaxe/sing-box-lx/lx/scripts-lx/openwrt/lxd-openwrt-uninstall.sh
#   sh /tmp/lxd-uninstall.sh              # asks for confirmation
#   sh /tmp/lxd-uninstall.sh --yes        # no questions (no tty / automation)
#   sh /tmp/lxd-uninstall.sh --restore    # roll back to the pre-lxd backup + reboot

set -u   # NOT -e: the rollback must reach the end over any debris

VERSION="1.1"
STATE_ROOT="/etc/sing-box-lxd"
BIN="/usr/bin/sing-box"
INIT="/etc/init.d/sing-box-lxd"
SUMMARY="/root/lxd-setup-summary.txt"
ZTUN="sbtun"

say()  { printf '%s\n' "$*"; }
warn() { printf '!! %s\n' "$*" >&2; }

[ "$(id -u)" = 0 ] || { warn "must run as root"; exit 1; }

YES=0; RESTORE=0
for _arg in "$@"; do
    case "$_arg" in
        --yes)     YES=1 ;;
        --restore) RESTORE=1 ;;
        *) warn "unknown argument: $_arg (there are --yes and --restore)"; exit 1 ;;
    esac
done

BK=$(ls /root/backup-pre-lxd-*.tar.gz 2>/dev/null | head -1)

# ── the segment name ────────────────────────────────────────────────────────
# The reliable trace of an install is the forwarding into the tunnel zone: the
# ${NET}2tun section with dest='sbtun'. If the firewall never got written (a
# broken install), fall back to the installer's default, adjustable on a live tty.
NET=$(uci show firewall 2>/dev/null \
      | sed -n "s/^firewall\.\([A-Za-z0-9_]*\)2tun\.dest='$ZTUN'\$/\1/p" | head -1)
if [ -z "$NET" ]; then
    NET="lxdvpn"
    if [ "$YES" = 0 ] && ( : </dev/tty ) 2>/dev/null; then
        printf 'segment uci name [%s]: ' "$NET" >&2
        read -r _a </dev/tty || _a=""
        [ -n "$_a" ] && NET="$_a"
    fi
fi
BR=$(uci -q get "network.${NET}dev.name")
[ -n "$BR" ] || BR="br-$NET"

# ── what we are about to do ─────────────────────────────────────────────────
if [ "$RESTORE" = 1 ] && [ -z "$BK" ]; then
    warn "no pre-lxd backup found (/root/backup-pre-lxd-*.tar.gz) — doing a plain teardown"
    RESTORE=0
fi
if [ "$RESTORE" = 0 ] && [ "$YES" = 0 ] && [ -n "$BK" ]; then
    printf 'Found a pre-lxd backup: %s\nRestore the configs from it and reboot (full rollback)? [y/N]: ' "$BK" >&2
    read -r _a </dev/tty 2>/dev/null || _a=""
    case "$_a" in [yYдД]*) RESTORE=1 ;; esac
fi

if [ "$RESTORE" = 1 ]; then
    say "mode: restore from $BK + reboot"
else
    say "mode: teardown (uci sections \"$NET\", bridge $BR, service $INIT, state $STATE_ROOT)"
fi
if [ "$YES" = 0 ]; then
    printf 'Proceed? [y/N]: ' >&2
    read -r _a </dev/tty 2>/dev/null || _a=""
    case "$_a" in [yYдД]*) : ;; *) say "cancelled"; exit 0 ;; esac
fi

# ── service, files, persistence (needed in both modes: restore does not
# remove files that appeared after the backup was taken, nor bring back a
# default sysupgrade.conf — unchanged files never make it into a backup) ────
[ -x "$INIT" ] && { "$INIT" stop >/dev/null 2>&1; "$INIT" disable >/dev/null 2>&1; }
rm -f "$INIT" "$SUMMARY"
rm -rf "$STATE_ROOT"
[ -f /etc/sysupgrade.conf ] && sed -i '\#sing-box#d' /etc/sysupgrade.conf

if [ -x "$BIN" ]; then
    DELBIN=1
    if [ "$RESTORE" = 0 ] && [ "$YES" = 0 ]; then
        printf 'Remove the binary %s (a reinstall downloads it again, ~21 MB)? [Y/n]: ' "$BIN" >&2
        read -r _a </dev/tty 2>/dev/null || _a=""
        case "$_a" in [nNнН]*) DELBIN=0 ;; esac
    fi
    [ "$DELBIN" = 1 ] && rm -f "$BIN"
fi

# ── restore mode: the backup brings the configs back, no uci teardown needed ─
if [ "$RESTORE" = 1 ]; then
    if ! tar -tzf "$BK" >/dev/null 2>&1; then
        warn "archive $BK is corrupt — restore cancelled, continuing with a plain teardown"
    else
        say "restoring the configs and rebooting (the SSH session will drop, the router is back in ~2 minutes)"
        # detach from the terminal: the reboot kills SSH and the chain must not die with it
        ( sysupgrade -r "$BK" && sleep 2 && reboot ) >/dev/null 2>&1 </dev/null &
        exit 0
    fi
fi

# ── teardown: the network. ifdown BEFORE deleting the sections — netifd tears
# the interface down while it still knows about it; deleting the config
# without this leaves an orphan bridge behind (state desync) ────────────────
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
/etc/init.d/dnsmasq restart >/dev/null 2>&1   # ⚠ drops DNS for the whole LAN for ~a second

sleep 2
if ip link show "$BR" >/dev/null 2>&1; then
    # the config is gone — netifd no longer owns the bridge, finishing the orphan is safe
    warn "bridge $BR still visible — removing by hand"
    ip link del "$BR" 2>/dev/null
fi

say "done: the segment is removed, the main network untouched."
[ -n "$BK" ] && say "the pre-lxd backup stays in place: $BK"
say "One thing left: restarting the radio — Wi-Fi blinks for ~10 seconds (drops Wi-Fi SSH)."
wifi reload >/tmp/lxd-wifi-reload.log 2>&1 </dev/null &
exit 0
