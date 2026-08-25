#!/bin/sh
# lxd-openwrt-setup.sh — install sing-box-lx (the lxd daemon) + a VPN-SSID on OpenWrt.
#
# Brings up a separate Wi-Fi whose entire traffic goes through the sing-box core:
#   SSID → bridge → tun (auto_route + include_interface) → core → outbound
# The main network (br-lan) is untouched: the core only ever sees the new
# bridge, so a broken config cannot cut off SSH/LAN.
#
# Run ON THE ROUTER, as root, WITH A TERMINAL (the script is interactive):
#   ssh root@ROUTER 'cat > /tmp/lxd-setup.sh' < lxd-openwrt-setup.sh
#   ssh -t root@ROUTER 'sh /tmp/lxd-setup.sh'
# The `ssh host 'sh -' < script` form does NOT work: no pty means no /dev/tty,
# and every question would silently fall back to its default — the script
# checks for this and refuses to run.
#
# Verified on RouteRich 24.10.5 (an OpenWrt 24.10 fork, mediatek/filogic, aarch64).
#
# Docs (manual build of the same segment, step by step, decisions explained):
#   https://github.com/Leadaxe/sing-box-lx/blob/lx/docs-lx/openwrt-vpn-ssid.md

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

# ── ask <question> <default> — prints the answer to stdout, prompt to stderr ─
ask() {
    printf '%s [%s]: ' "$1" "$2" >&2
    read -r _a </dev/tty || _a=""
    [ -n "$_a" ] && printf '%s\n' "$_a" || printf '%s\n' "$2"
}

ask_yn() {   # ask_yn <question> — default NO
    printf '%s [y/N]: ' "$1" >&2
    read -r _a </dev/tty || _a=""
    case "$_a" in [yYдД]*) return 0 ;; *) return 1 ;; esac
}

ask_yn_default() {   # ask_yn_default <question> — default YES
    printf '%s [Y/n]: ' "$1" >&2
    read -r _a </dev/tty || _a=""
    case "$_a" in [nNнН]*) return 1 ;; *) return 0 ;; esac
}

# ── 0. Preconditions ────────────────────────────────────────────────────────
step "System check"

[ "$(id -u)" = 0 ] || die "must run as root"

# Interactivity lives on /dev/tty. Without a pty (ssh without -t) every read
# would silently return the default — the install would "answer" every
# question by itself. Better to refuse.
( : </dev/tty ) 2>/dev/null || die "no terminal (/dev/tty). Run it like this:
  ssh root@ROUTER 'cat > /tmp/lxd-setup.sh' < lxd-openwrt-setup.sh
  ssh -t root@ROUTER 'sh /tmp/lxd-setup.sh'"

# ID="openwrt" survives fork rebranding, DISTRIB_ID in openwrt_release does
# not (RouteRich puts 'RouteRich' there; a check for 'OpenWrt' would fail).
if [ -r /etc/os-release ]; then
    . /etc/os-release
    case "$ID $ID_LIKE" in
        *openwrt*) say "system:      ${PRETTY_NAME:-OpenWrt}" ;;
        *) die "this is not OpenWrt (ID=$ID). The script targets OpenWrt/fw4." ;;
    esac
else
    die "no /etc/os-release — cannot identify the system"
fi

command -v uci  >/dev/null || die "no uci"
command -v fw4  >/dev/null || die "no fw4 (OpenWrt 22.03+ with nftables required)"
command -v wifi >/dev/null || die "no wifi"
# segment DHCP is served by dnsmasq; on odhcpd-only builds the section would silently do nothing
[ -x /etc/init.d/dnsmasq ] || die "no dnsmasq — nothing to serve segment DHCP with (odhcpd-only builds are not supported)"

# TUN is mandatory for the tun inbound
if [ ! -c /dev/net/tun ]; then
    say "no /dev/net/tun — installing kmod-tun"
    opkg update >/dev/null 2>&1 || warn "opkg update failed"
    opkg install kmod-tun >/dev/null 2>&1 || die "failed to install kmod-tun"
    [ -c /dev/net/tun ] || die "/dev/net/tun still did not appear"
fi

ARCH_RAW=$(uname -m)
case "$ARCH_RAW" in
    aarch64) ARCH="linux-arm64" ;;
    x86_64)  ARCH="linux-amd64" ;;
    armv7l)  ARCH="linux-armv7" ;;
    # mips releases are built softfloat-only — the asset name carries the suffix
    mips)    ARCH="linux-mips-softfloat" ;;
    mipsel)  ARCH="linux-mipsle-softfloat" ;;
    *) die "architecture $ARCH_RAW: pick a release manually" ;;
esac
say "architecture: $ARCH_RAW → $ARCH"

# The binary is ~50 MB: won't fit the NAND overlay of a small router
AVAIL_KB=$(df -k /overlay 2>/dev/null | awk 'NR==2{print $4}')
[ -z "$AVAIL_KB" ] && AVAIL_KB=$(df -k / | awk 'NR==2{print $4}')
say "free space:  $((AVAIL_KB / 1024)) MB"
[ "$AVAIL_KB" -lt 80000 ] && warn "less than 80 MB free — the ~50 MB binary may not fit (extroot needed)"

# ── pre-lxd backup: before any changes, exactly once ────────────────────────
# The full-rollback point — the teardown script restores from it (--restore).
# Created only if absent: a re-run after a broken install must not overwrite
# the clean state with a dirty one.
if ls /root/backup-pre-lxd-*.tar.gz >/dev/null 2>&1; then
    say "backup:      already have $(ls /root/backup-pre-lxd-*.tar.gz | head -1) — keeping it"
else
    BK="/root/backup-pre-lxd-$(date +%Y%m%d-%H%M%S).tar.gz"
    if sysupgrade -b "$BK" >/dev/null 2>&1 && tar -tzf "$BK" >/dev/null 2>&1; then
        say "backup:      $BK ($(du -k "$BK" | awk '{print $1}') KB)"
    else
        rm -f "$BK"
        warn "sysupgrade -b failed — continuing WITHOUT a rollback point"
    fi
fi

# ── 1. Radios ───────────────────────────────────────────────────────────────
step "Wi-Fi radios"

RADIO_5G=""; RADIO_2G=""
for r in $(uci show wireless 2>/dev/null | sed -n "s/^wireless\.\([a-z0-9]*\)=wifi-device$/\1/p"); do
    band=$(uci -q get "wireless.$r.band")
    # configs migrated from old releases carry hwmode (11a/11g) instead of band
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
[ -z "$RADIO_5G" ] && [ -z "$RADIO_2G" ] && die "no wifi-device found at all"

# ── 2. Password from existing networks ──────────────────────────────────────
step "Password"

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
    say "found the password of network \"$FOUND_SSID\": $FOUND_KEY"
    WIFI_KEY=$(ask "password for the new Wi-Fi (Enter — keep this one)" "$FOUND_KEY")
else
    warn "no password in existing networks (open or unconfigured)"
    WIFI_KEY=$(ask "set a password for the new Wi-Fi (min 8 chars)" "")
fi
[ ${#WIFI_KEY} -ge 8 ]  || die "password shorter than 8 characters — WPA2 won't accept it"
[ ${#WIFI_KEY} -le 63 ] || die "password longer than 63 characters — WPA2 won't accept it"

# ── 3. Network names ────────────────────────────────────────────────────────
step "Network names"

if [ -n "$RADIO_5G" ]; then
    SSID_5G=$(ask "SSID for 5 GHz" "LxdVPN 5G")
else
    warn "no 5 GHz radio found — skipping"; SSID_5G=""
fi

SSID_2G=""
if [ -n "$RADIO_2G" ] && ask_yn "Add a second network on 2.4 GHz?"; then
    SSID_2G=$(ask "SSID for 2.4 GHz" "LxdVPN 2G")
fi
[ -z "$SSID_5G" ] && [ -z "$SSID_2G" ] && die "no network selected"

# ── 4. A free subnet ────────────────────────────────────────────────────────
step "Segment network"

# Occupancy is checked against several sources at once. Interface addresses
# alone are not enough: behind double NAT the ISP router's network shows up
# only as a route, and a collision breaks routing silently, after setup.
net_taken() {   # net_taken <a.b.c> — 0 if the /24 is taken
    ip -4 addr  show          2>/dev/null | grep -q "inet $1\." && return 0
    ip -4 route show          2>/dev/null | grep -q "^$1\."     && return 0
    ip -4 route show table all 2>/dev/null | grep -q "^$1\."    && return 0
    # the address the ISP handed to WAN: its /24 is taken even without a route
    [ -n "$WAN_NET" ] && [ "$1" = "$WAN_NET" ] && return 0
    # A route wider than /24 can cover the candidate whole: with LAN 10.0.0.1/8
    # the segment 10.0.1.0/24 sits INSIDE br-lan, so traffic would go to the
    # LAN bypassing the tunnel. The string checks above miss such an overlap.
    _c1=${1%%.*}; _cr=${1#*.}; _c2=${_cr%%.*}; _c3=${_cr#*.}
    _cip=$(( _c1 * 16777216 + _c2 * 65536 + _c3 * 256 ))
    { ip -4 route show 2>/dev/null; ip -4 route show table all 2>/dev/null; } \
      | sed -n 's|^\([0-9]*\.[0-9]*\.[0-9]*\.[0-9]*\)/\([0-9]*\) .*|\1 \2|p' \
      | while read -r _rnet _rlen; do
            [ "$_rlen" -ge 24 ] && continue   # /24 or narrower — caught above
            [ "$_rlen" -le 0  ] && continue   # default route — not occupancy
            _r1=${_rnet%%.*}; _rr=${_rnet#*.}; _r2=${_rr%%.*}; _rr=${_rr#*.}
            _r3=${_rr%%.*};   _r4=${_rr#*.}
            _rip=$(( _r1 * 16777216 + _r2 * 65536 + _r3 * 256 + _r4 ))
            _mask=$(( 4294967295 ^ (( 1 << (32 - _rlen) ) - 1) ))
            [ $(( _cip & _mask )) -eq $(( _rip & _mask )) ] && exit 7
        done
    [ "$?" = 7 ] && return 0
    return 1
}

# WAN interface /24 (double NAT: usually 192.168.0/1/2.x from the ISP CPE)
WAN_NET=""
_wan_dev=$(uci -q get network.wan.device || true)
[ -z "$_wan_dev" ] && _wan_dev=$(ip -4 route show default 2>/dev/null | sed -n 's/.* dev \([^ ]*\).*/\1/p' | head -1)
if [ -n "$_wan_dev" ]; then
    WAN_NET=$(ip -4 addr show dev "$_wan_dev" 2>/dev/null \
        | sed -n 's/.*inet \([0-9]*\.[0-9]*\.[0-9]*\)\..*/\1/p' | head -1)
fi

# The base is the router's own LAN network (its Wi-Fi shares that bridge and
# has no separate network). Counting up from it keeps things recognisable —
# "home is .1, the segment is .2" — and busy neighbours (a guest SSID and the
# like) are skipped by the occupancy check above.
LAN_BASE=$(uci -q get network.lan.ipaddr | sed -n 's/^\([0-9]*\.[0-9]*\.[0-9]*\)\..*/\1/p')

NET_BASE=""
if [ -n "$LAN_BASE" ]; then
    _o1=${LAN_BASE%%.*}; _rest=${LAN_BASE#*.}
    _o2=${_rest%%.*};    _o3=${_rest#*.}
    # Increment within the same RFC1918 prefix: 10.0.0 → 10.0.1,
    # 172.16.5 → 172.16.6, 192.168.1 → 192.168.2. The third octet stops at
    # 254; past that we fall back to the list instead of leaving the block.
    _n=$((_o3 + 1))
    while [ "$_n" -le 254 ]; do
        if ! net_taken "$_o1.$_o2.$_n"; then NET_BASE="$_o1.$_o2.$_n"; break; fi
        _n=$((_n + 1))
    done
fi

# Fallback list — rare octets that almost nobody ships as a default.
if [ -z "$NET_BASE" ]; then
    for n in 20 21 22 30 40 50; do
        if ! net_taken "192.168.$n"; then NET_BASE="192.168.$n"; break; fi
    done
fi

# The pick is never silent: show it and allow a replacement. A mistake here is
# expensive — the collision only surfaces once the segment is already up.
if [ -n "$NET_BASE" ]; then
    say "proposed segment network: $NET_BASE.0/24 (gateway $NET_BASE.1)"
    [ -n "$LAN_BASE" ] && say "  router LAN network: $LAN_BASE.x"
    [ -n "$WAN_NET" ]  && say "  network behind WAN: $WAN_NET.x (excluded)"
    ask_yn_default "Use it?" || NET_BASE=""
else
    warn "no free subnet found automatically"
fi

while [ -z "$NET_BASE" ]; do
    NET_BASE=$(ask "segment network, first three octets (e.g. 192.168.30)" "")
    case "$NET_BASE" in
        [0-9]*.[0-9]*.[0-9]*) ;;
        *) warn "need the a.b.c form, e.g. 192.168.30"; NET_BASE=""; continue ;;
    esac
    case "$NET_BASE" in *[!0-9.]*|*.*.*.*) warn "need the a.b.c form, e.g. 192.168.30"; NET_BASE=""; continue ;; esac
    _bad=0
    for _o in $(printf '%s\n' "$NET_BASE" | tr '.' ' '); do
        [ "$_o" -le 255 ] 2>/dev/null || _bad=1
    done
    [ "$_bad" = 1 ] && { warn "octet greater than 255"; NET_BASE=""; continue; }
    if net_taken "$NET_BASE"; then
        warn "$NET_BASE.0/24 is already taken"
        ask_yn "Use it anyway?" || NET_BASE=""
    fi
done

GW="$NET_BASE.1"
say "subnet:      $NET_BASE.0/24, gateway $GW"

# tun p2p subnet: 172.16.x.0/30, first free. Checked with the same
# net_taken as the segment: the plain grep saw neither routes nor a wider
# prefix overlap (172.16.0.0/12 on the router covers all four candidates).
TUN_NET=""
for n in 16 17 18 19; do
    if ! net_taken "172.$n.0"; then TUN_NET="$n"; break; fi
done
TUN_FALLBACK=0
[ -z "$TUN_NET" ] && { TUN_NET=16; TUN_FALLBACK=1; }

# The tunnel name and address end up BOTH in the core config AND in the
# firewall (the sbtun zone is bound to the device by name, the sbtun_tcp rule
# — to the address). Asking here guarantees the two sides match.
TUN_IF=$(ask "tun interface name" "lxd-tun0")
case "$TUN_IF" in
    *[!A-Za-z0-9_-]*|"") die "invalid interface name: $TUN_IF" ;;
esac
[ ${#TUN_IF} -le 15 ] || die "interface name longer than 15 characters — the Linux kernel won't accept it"
ip link show "$TUN_IF" >/dev/null 2>&1 && die "interface $TUN_IF already exists"

# The tunnel address is shown too: a collision here surfaces later, not during
# setup — the sbtun_tcp rule is bound to the address, so TCP from the segment
# gets refused while ICMP/DNS stay alive. Manual input is fully validated.
TUN_ADDR=""
_tun_auto="172.$TUN_NET.0.1"
say "proposed tunnel address: $_tun_auto/30 (p2p)"
[ "$TUN_FALLBACK" = 1 ] && warn "all of 172.{16..19}.0.x are taken — so is the proposed address; set your own"
if ask_yn_default "Use it?"; then
    TUN_ADDR="$_tun_auto"
fi
while [ -z "$TUN_ADDR" ]; do
    TUN_ADDR=$(ask "tunnel address (p2p /30)" "")
    case "$TUN_ADDR" in
        [0-9]*.[0-9]*.[0-9]*.[0-9]*) ;;
        *) warn "the tunnel address must be IPv4, e.g. 172.16.0.1"; TUN_ADDR=""; continue ;;
    esac
    case "$TUN_ADDR" in *[!0-9.]*) warn "the tunnel address must be IPv4"; TUN_ADDR=""; continue ;; esac
    _bad=0
    for _o in $(printf '%s\n' "$TUN_ADDR" | tr '.' ' '); do
        [ "$_o" -le 255 ] 2>/dev/null || _bad=1
    done
    [ "$_bad" = 1 ] && { warn "octet greater than 255"; TUN_ADDR=""; continue; }
    # a tunnel /30 inside the segment network would make the routes fight
    _t3=${TUN_ADDR%.*}
    if [ "$_t3" = "$NET_BASE" ]; then
        warn "the tunnel address must not sit in the segment network ($NET_BASE.0/24)"
        TUN_ADDR=""; continue
    fi
    if net_taken "$_t3"; then
        warn "$_t3.0/24 is already taken"
        ask_yn "Use it anyway?" || TUN_ADDR=""
    fi
done
say "tunnel:      $TUN_IF ($TUN_ADDR/30)"

# DNS for segment clients is handed out via DHCP option 6. The router's own
# resolver won't do: dnsmasq would answer outside the tunnel and the queries
# would leak from the home IP. It has to be external — but which one is the
# owner's call, not the script's.
SEG_DNS=$(ask "DNS for segment clients" "8.8.8.8")
case "$SEG_DNS" in
    [0-9]*.[0-9]*.[0-9]*.[0-9]*) ;;
    *) die "DNS must be an IPv4 address: $SEG_DNS" ;;
esac
case "$SEG_DNS" in *[!0-9.]*) die "DNS must be an IPv4 address: $SEG_DNS" ;; esac
for _o in $(printf '%s\n' "$SEG_DNS" | tr '.' ' '); do
    [ "$_o" -le 255 ] 2>/dev/null || die "octet greater than 255: $SEG_DNS"
done
# The router's own address = dnsmasq = resolution outside the tunnel.
[ "$SEG_DNS" = "$GW" ] && die "the segment gateway is not allowed: resolution would bypass the tunnel"
if [ -n "$LAN_BASE" ] && [ "${SEG_DNS%.*}" = "$LAN_BASE" ]; then
    warn "$SEG_DNS is an address in the router LAN; if that is dnsmasq, resolution will bypass the tunnel"
fi

# The bridge name also goes into the core config — into include_interface (the
# "LAN interfaces" field in the launcher UI). It is what contains the capture
# to the VPN segment: put br-lan here and the core drags the whole home
# network into the tunnel.
BR=$(ask "VPN segment bridge name (goes into include_interface)" "br-lxdvpn")
case "$BR" in
    *[!A-Za-z0-9_-]*|"") die "invalid bridge name: $BR" ;;
    br-lan) die "br-lan is off-limits: it is the main network, the core would drag it into the tunnel" ;;
esac
[ ${#BR} -le 15 ] || die "bridge name longer than 15 characters — the Linux kernel won't accept it"
ip link show "$BR" >/dev/null 2>&1 && die "interface $BR already exists"

# UCI sections are named after the bridge so the whole segment reads uniformly
NET=$(printf '%s' "$BR" | sed 's/^br-//; s/[^A-Za-z0-9]//g')
[ -n "$NET" ] || NET="lxdvpn"
ZONE="$NET"; ZTUN="sbtun"

# Never clobber foreign sections: network.guest may be something alive.
for sect in "network.$NET" "dhcp.$NET" "firewall.$ZONE" "firewall.$ZTUN"; do
    [ -n "$(uci -q get "$sect" 2>/dev/null)" ] && \
        die "uci section $sect already exists — pick another bridge name or remove it manually"
done
say "bridge/net:  $BR / uci section \"$NET\""

# ── 5. The binary ───────────────────────────────────────────────────────────
step "Downloading sing-box-lx"

if [ -x "$BIN" ] && "$BIN" version 2>/dev/null | grep -q lx; then
    say "already installed: $("$BIN" version | head -1)"
    ask_yn "Re-download the latest version?" && NEED_DL=1 || NEED_DL=0
else
    NEED_DL=1
fi

if [ "$NEED_DL" = 1 ]; then
    command -v wget >/dev/null || die "no wget"
    # /releases/latest ignores pre-releases, and this repository ships releases
    # as rc — so take the first entry of the full list (it is the newest).
    TAG=$(wget -qO- "https://api.github.com/repos/$REPO/releases?per_page=1" 2>/dev/null \
          | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
    # fallback: the stable release, if the list did not come through
    [ -z "$TAG" ] && TAG=$(wget -qO- "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
                           | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
    if [ -z "$TAG" ]; then
        warn "could not learn the latest release (no internet or GitHub API limit)"
        TAG=$(ask "enter the release tag manually (see https://github.com/$REPO/releases)" "")
        [ -n "$TAG" ] || die "no tag given"
    fi
    VER="${TAG#v}"
    BASE="https://github.com/$REPO/releases/download/$TAG"
    TARBALL="sing-box-$VER-$ARCH.tar.gz"
    say "version:     $TAG"

    cd /tmp
    rm -f "$TARBALL" SHA256SUMS
    # -O is mandatory: GitHub serves assets via a redirect, and busybox-wget
    # without -O saves the file under the name from the FINAL URL — not the asset name.
    wget -q -O "$TARBALL" "$BASE/$TARBALL" || die "failed to download $TARBALL
  (on an SSL/TLS error: opkg install ca-bundle libustream-mbedtls
   or download the archive on a computer and upload the binary by hand —
   see §8.3 in docs-lx/lxd-daemon.md)"
    # on failure -O leaves an empty file — remove it, or the check below
    # mistakes it for a real SHA256SUMS and fails the install
    wget -q -O SHA256SUMS "$BASE/SHA256SUMS" \
        || { rm -f SHA256SUMS; warn "SHA256SUMS did not download — skipping the check"; }

    if [ -f SHA256SUMS ]; then
        # match the exact file name: a substring grep could hit a neighboring asset
        WANT=$(awk -v f="$TARBALL" '$NF==f {print $1; exit}' SHA256SUMS)
        GOT=$(sha256sum "$TARBALL" | awk '{print $1}')
        [ "$WANT" = "$GOT" ] || die "sha256 MISMATCH (expected $WANT, got $GOT)"
        say "sha256:      match"
    fi

    tar xzf "$TARBALL" || die "failed to unpack"
    # exact path: a find across all of /tmp could pick up a stale binary from an older unpack
    SRC="/tmp/sing-box-$VER-$ARCH/sing-box"
    [ -f "$SRC" ] || SRC=$(find "/tmp/sing-box-$VER-$ARCH" -name sing-box -type f 2>/dev/null | head -1)
    [ -n "$SRC" ] && [ -f "$SRC" ] || die "no sing-box binary in the archive"
    [ -x "$INIT" ] && "$INIT" stop >/dev/null 2>&1 || true
    mv "$SRC" "$BIN"; chmod +x "$BIN"
    rm -rf "/tmp/$TARBALL" /tmp/SHA256SUMS "/tmp/sing-box-$VER-$ARCH"
fi

"$BIN" version >/dev/null 2>&1 || die "the binary does not run (wrong architecture?)"
"$BIN" version | grep -qE "with_lx_command|with_lxd" \
    || die "the build lacks the with_lx_command/with_lxd tag — lxd mode unavailable"
say "ready:       $("$BIN" version | head -1)"

# ── 6. The daemon: state, daemon.json, service ──────────────────────────────
step "Daemon"

mkdir -p "$STATE_DIR"

# The secret: the official recipe's command (xxd) fails on busybox — no xxd, no od.
if [ -f "$STATE_DIR/daemon.json" ]; then
    SECRET=$(sed -n 's/.*"secret" *: *"\([a-f0-9]*\)".*/\1/p' "$STATE_DIR/daemon.json" | head -1)
fi
if [ -z "$SECRET" ]; then
    if command -v openssl >/dev/null 2>&1; then
        SECRET=$(openssl rand -hex 32)
    else
        # busybox path: minimal firmwares ship no openssl
        SECRET=$(head -c 32 /dev/urandom | hexdump -ve '1/1 "%02x"')
    fi
fi
[ ${#SECRET} -eq 64 ] || die "the secret came out the wrong length (${#SECRET})"

LAN_IP=$(uci -q get network.lan.ipaddr); [ -z "$LAN_IP" ] && LAN_IP="127.0.0.1"

# the port may be held by another service (or a previous manual install)
if netstat -ltn 2>/dev/null | grep -q ":$PORT "; then
    "$INIT" running 2>/dev/null || die "port $PORT is already taken by another process (netstat -ltnp | grep $PORT)"
fi

# ── Management access from outside (default — no) ───────────────────────────
WAN_IP=""; WAN_EXPOSE=0
say ""
say "By default management is reachable from LAN only (loopback + LAN)."
say "The channel is mTLS-protected: no client certificate, no TLS handshake."
if ask_yn "Allow management access from outside (from WAN)?"; then
    WAN_EXPOSE=1

    if ask_yn "Does the router have a static external IP?"; then
        # ubus is the canonical source; ip route is the fallback if wan is named differently
        DET=$(ubus call network.interface.wan status 2>/dev/null \
              | sed -n 's/.*"address": *"\([0-9.]*\)".*/\1/p' | head -1)
        [ -z "$DET" ] && DET=$(ip -4 route get 8.8.8.8 2>/dev/null | sed -n 's/.*src \([0-9.]*\).*/\1/p')

        if [ -n "$DET" ]; then
            say "detected external address: $DET"
            # Pinning is semantically firmer, but on an address change the
            # daemon cannot bind. 0.0.0.0 listens everywhere and survives it.
            if ask_yn_default "Pin this address? (n — listen on 0.0.0.0)"; then
                WAN_IP="$DET"
            else
                WAN_IP="0.0.0.0"
            fi
        else
            warn "could not detect the external address — listening on 0.0.0.0"
            WAN_IP="0.0.0.0"
        fi
    else
        # the address changes — nothing to pin
        WAN_IP="0.0.0.0"
        say "not pinning the address: the daemon will listen on 0.0.0.0 (all interfaces)"
    fi

    say "port $PORT will be opened in the firewall for the wan zone"
fi

# IMPORTANT: loopback FIRST. The first address goes into the enrollment invite
# and is what the local CLI dials; otherwise `client add/list/remove` → 403
# loopback-only. 0.0.0.0 goes ALONE: it already covers loopback and LAN, and
# an explicit second binding on 127.0.0.1 next to it kills the daemon
# (`bind: address already in use`, the core never comes up — verified).
# For the same reason no address may appear in the list twice: LAN_IP falls
# back to 127.0.0.1 on a non-standard lan section name — never write a dupe.
if [ "$WAN_IP" = "0.0.0.0" ]; then
    LISTEN_ADDR="\"0.0.0.0\""
else
    LISTEN_ADDR="\"127.0.0.1\""
    [ "$LAN_IP" != "127.0.0.1" ] && LISTEN_ADDR="$LISTEN_ADDR, \"$LAN_IP\""
    [ "$WAN_EXPOSE" = 1 ] && [ "$WAN_IP" != "$LAN_IP" ] && LISTEN_ADDR="$LISTEN_ADDR, \"$WAN_IP\""
fi

cat > "$STATE_DIR/daemon.json" <<EOF
{
  "listen": {
    "address": [$LISTEN_ADDR],
    "port": $PORT
  },
  "tls": true,
  "secret": "$SECRET"
}
EOF
chmod 600 "$STATE_DIR/daemon.json"

# Skeleton config: the segment works, exits via WAN (direct).
# The production upstream lands later in a single apply — the skeleton is untouched.
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
"$BIN" check -c "$STATE_DIR/min.json" >/dev/null 2>&1 || die "the skeleton config failed sing-box check"

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

# sysupgrade -b picks up neither the binary, nor the state, nor the init script
for p in "$INIT" "/etc/sing-box-lxd/" "$BIN"; do
    grep -qxF "$p" /etc/sysupgrade.conf 2>/dev/null || echo "$p" >> /etc/sysupgrade.conf
done
say "service:     $INIT (procd, autostart)"

# ── 7. Network: bridge → interface → DHCP ───────────────────────────────────
step "Network"

# bridge_empty is mandatory for a bridge with no ethernet ports (Wi-Fi APs
# only, attached dynamically when hostapd starts). Without it netifd will not
# bring the bridge up at all.
uci -q delete "network.${NET}dev" 2>/dev/null || true
uci set "network.${NET}dev=device"
uci set "network.${NET}dev.name=$BR"
uci set "network.${NET}dev.type=bridge"
uci set "network.${NET}dev.bridge_empty=1"

uci set "network.$NET=interface"
uci set "network.$NET.device=$BR"
uci set "network.$NET.proto=static"
uci set "network.$NET.ipaddr=$GW"
uci set "network.$NET.netmask=255.255.255.0"
# NO ip6assign: the client would get IPv6 via RA and leak around the tunnel
uci commit network

# Option 6 — an external DNS, NOT the router's dnsmasq: resolution must not bypass the tunnel
uci set "dhcp.$NET=dhcp"
uci set "dhcp.$NET.interface=$NET"
uci set "dhcp.$NET.start=100"
uci set "dhcp.$NET.limit=150"
uci set "dhcp.$NET.leasetime=12h"
uci set "dhcp.$NET.dhcpv4=server"
uci -q delete "dhcp.$NET.dhcp_option" 2>/dev/null || true
uci add_list "dhcp.$NET.dhcp_option=6,$SEG_DNS"
uci commit dhcp

# reload_config sometimes fails to nudge netifd about a freshly created device
# section, and reload is a no-op when netifd believes its state is current
# (even when the device is really missing). Escalate: reload → ifup → reload
# → ifup; ifup rebuilds the interface unconditionally.
reload_config
n=0
while ! ip link show "$BR" >/dev/null 2>&1; do
    n=$((n+1))
    [ "$n" -eq 3 ] && ifup "$NET" >/dev/null 2>&1
    [ "$n" -eq 6 ] && /etc/init.d/network reload >/dev/null 2>&1
    [ "$n" -eq 9 ] && ifup "$NET" >/dev/null 2>&1
    [ "$n" -ge 20 ] && die "bridge $BR did not come up. Diagnostics:
  ifstatus $NET
  uci show network | grep $NET
  logread | grep -iE 'netifd|bridge' | tail -20"
    sleep 1
done
say "bridge:      $BR ($GW/24)"

# ⚠ a dnsmasq restart drops DNS for the whole LAN for a second
/etc/init.d/dnsmasq restart >/dev/null 2>&1
say "dhcp:        $NET_BASE.100–249, DNS $SEG_DNS"

# ── 8. Firewall: fail-closed ────────────────────────────────────────────────
step "Firewall"

# The only way out is into the tunnel zone. NO forwarding to wan: core down →
# segment offline, but nothing leaks from the home IP either.
uci set "firewall.$ZONE=zone"
uci set "firewall.$ZONE.name=$ZONE"
uci -q delete "firewall.$ZONE.network" 2>/dev/null || true
uci add_list "firewall.$ZONE.network=$NET"
uci set "firewall.$ZONE.input=ACCEPT"     # clients need DHCP and the gateway
uci set "firewall.$ZONE.output=ACCEPT"
uci set "firewall.$ZONE.forward=REJECT"

uci set "firewall.$ZTUN=zone"
uci set "firewall.$ZTUN.name=$ZTUN"
uci -q delete "firewall.$ZTUN.device" 2>/dev/null || true
uci add_list "firewall.$ZTUN.device=$TUN_IF"
uci set "firewall.$ZTUN.input=REJECT"
uci set "firewall.$ZTUN.output=ACCEPT"
uci set "firewall.$ZTUN.forward=REJECT"

uci set "firewall.${ZONE}2tun=forwarding"
uci set "firewall.${ZONE}2tun.src=$ZONE"
uci set "firewall.${ZONE}2tun.dest=$ZTUN"

# stack:"system" redirects TCP to a local listener on the tunnel address. To
# the kernel that is an inbound packet → INPUT in the sbtun zone (input=REJECT)
# → TCP silently dies while UDP/ICMP live. The rule is bound to the IP: change
# the tun inbound address — fix the rule (or get connection refused).
uci set "firewall.${ZTUN}_tcp=rule"
uci set "firewall.${ZTUN}_tcp.name=Allow-sbtun-systemstack-tcp"
uci set "firewall.${ZTUN}_tcp.src=$ZTUN"
uci set "firewall.${ZTUN}_tcp.dest_ip=$TUN_ADDR"
uci set "firewall.${ZTUN}_tcp.proto=tcp"
uci set "firewall.${ZTUN}_tcp.target=ACCEPT"
# The management port from outside — only when the user explicitly allowed it.
# Without this rule the listen address is useless: the wan zone cuts input.
if [ "$WAN_EXPOSE" = 1 ]; then
    uci set "firewall.lxd_admin_wan=rule"
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
say "zones:       $ZONE → $ZTUN (the only forwarding, fail-closed)"
[ "$WAN_EXPOSE" = 1 ] && say "wan:         port $PORT OPEN from outside (mTLS)" \
                      || say "wan:         closed (management from LAN only)"

# ── 9. Starting the core ────────────────────────────────────────────────────
step "Starting the daemon"

"$INIT" restart >/dev/null 2>&1
sleep 6
ip link show "$TUN_IF" >/dev/null 2>&1 || warn "tunnel $TUN_IF did not appear — check: logread | grep sing-box"
say "daemon:      listening on [$LISTEN_ADDR] port $PORT (TLS+mTLS)"

# ── 10. Wi-Fi (last: wifi reload drops the radios for ~10 s) ────────────────
step "Wi-Fi"

add_ap() {  # add_ap <section> <radio> <ssid>
    uci set "wireless.$1=wifi-iface"
    uci set "wireless.$1.device=$2"
    uci set "wireless.$1.mode=ap"
    uci set "wireless.$1.network=$NET"
    uci set "wireless.$1.ssid=$3"
    uci set "wireless.$1.encryption=psk2"
    uci set "wireless.$1.key=$WIFI_KEY"
    uci -q delete "wireless.$1.disabled" 2>/dev/null || true
}
[ -n "$SSID_5G" ] && { add_ap "${NET}_5g" "$RADIO_5G" "$SSID_5G"; say "  5 GHz:   $SSID_5G"; }
[ -n "$SSID_2G" ] && { add_ap "${NET}_2g" "$RADIO_2G" "$SSID_2G"; say "  2.4 GHz: $SSID_2G"; }
uci commit wireless
say "sections written — the radio comes up as the last step"

# ── 11. Enrollment ──────────────────────────────────────────────────────────
# BEFORE Wi-Fi comes up: `wifi reload` drops the radios and, with them, the
# SSH session running this script. Were it to die here, no invite would be
# minted and no summary printed: the network up, nothing to connect with.
step "Pairing"

# The code lives in process memory, not in the state dir: any daemon restart
# between `client add` and entering the code in the launcher kills it
# (enroll: no active enrollment code).
#
# The daemon's invite: address#server-fingerprint#code (lxd/admin.go,
# handleClientCode). The address inside is the first listen address
# (loopback); the launcher needs the LAN address, so ONLY the address part is
# replaced. Never touch the tail: the middle is the TLS certificate
# fingerprint the launcher pins the server by — substituting anything else
# breaks pairing ("server fingerprint does not match").
INVITE=$("$BIN" lxd client add --name launcher --state-dir "$STATE_DIR" 2>&1 | tail -1)
FP=$(printf '%s' "$INVITE" | awk -F'#' '{print $2}')
CODE=$(printf '%s' "$INVITE" | awk -F'#' '{print $3}')
if [ -z "$FP" ] || [ -z "$CODE" ]; then
    warn "failed to mint an invite (daemon not responding?): $INVITE"
    warn "after fixing, mint one manually: sing-box lxd client add --name launcher"
    FP="<fingerprint>"; CODE="<code>"
fi

# ── Summary ─────────────────────────────────────────────────────────────────
# Printed BEFORE Wi-Fi comes up and duplicated into a file: `wifi reload`
# drops the SSH session and the on-screen output may never arrive. The file
# stays on the router regardless.
SUMMARY="/root/lxd-setup-summary.txt"
# 0.0.0.0 is meaningless as a dial address in the summary — use a placeholder
WAN_SHOW="$WAN_IP"; [ "$WAN_IP" = "0.0.0.0" ] && WAN_SHOW="<router-external-IP>"
exec 3>&1
{
printf '\n'
printf '════════════════════════════════════════════════════════════\n'
printf '  DONE\n'
printf '════════════════════════════════════════════════════════════\n\n'

printf 'Pair invite:     %s:%s#%s#%s\n' "$LAN_IP" "$PORT" "$FP" "$CODE"
printf '\n'
printf '── for the core config ──────────────────────────────────────\n'
printf 'tun name:            %s\n' "$TUN_IF"
printf 'tun address:         %s/30\n' "$TUN_ADDR"
printf 'include_interface:   %s              (the "LAN interfaces" field in the launcher UI)\n' "$BR"
printf '\n'
printf 'ATTENTION: the tunnel name and address are wired into the\n'
printf 'firewall and must match the core config. Change them there —\n'
printf 'fix them here:\n'
printf '  firewall.%s.device      = %s\n' "$ZTUN" "$TUN_IF"
printf '  firewall.%s_tcp.dest_ip = %s\n' "$ZTUN" "$TUN_ADDR"
printf 'Name drift → segment offline; address drift → connection\n'
printf 'refused on TCP while ICMP/DNS work.\n'
printf '─────────────────────────────────────────────────────────────\n'
printf '\n'
[ -n "$SSID_5G" ] && printf 'Wi-Fi 5 GHz:     %s\n' "$SSID_5G"
[ -n "$SSID_2G" ] && printf 'Wi-Fi 2.4 GHz:   %s\n' "$SSID_2G"
printf 'Wi-Fi password:  %s\n' "$WIFI_KEY"
printf 'segment:         %s.0/24 (gateway %s)\n' "$NET_BASE" "$GW"
printf 'management:      https://%s:%s (TLS+mTLS)\n' "$LAN_IP" "$PORT"
if [ "$WAN_EXPOSE" = 1 ]; then
    printf 'from outside:    https://%s:%s — port open in the firewall\n' "$WAN_SHOW" "$PORT"
    printf '                 invite for an external client: %s:%s#%s#<new code>\n' "$WAN_SHOW" "$PORT" "$FP"
    printf '                 (new code: sing-box lxd client add --name <name>)\n'
else
    printf 'from outside:    closed. Need access from afar — an ssh tunnel:\n'
    printf '                 ssh -N -L %s:%s:%s root@<router>\n' "$PORT" "$LAN_IP" "$PORT"
    printf '                 then connect to 127.0.0.1:%s\n' "$PORT"
fi
printf '\n'
printf 'The core now runs a skeleton config: the segment works but exits\n'
printf 'DIRECTLY via WAN (direct). The production upstream is uploaded from\n'
printf 'the launcher in a single apply — network, firewall, and Wi-Fi stay put.\n'
printf '\n'
printf 'IMPORTANT: do not restart the daemon before entering the invite —\n'
printf 'the pairing code lives in process memory and a restart kills it.\n'
printf '\n'
printf 'Check:     logread | grep sing-box | tail\n'
printf 'Clients:   sing-box lxd client list\n'
printf 'This summary is saved to: %s\n' "$SUMMARY"
printf '════════════════════════════════════════════════════════════\n'
} | tee "$SUMMARY" >&3
exec 3>&-
chmod 600 "$SUMMARY"

# ── Last step: bring the radio up ───────────────────────────────────────────
# ONLY after everything important is printed and saved. `wifi reload` kills
# both radios for ~10 seconds and drops the SSH session this script runs in;
# detach from the terminal (busybox has no nohup) so the drop can't kill it.
printf '\n'
printf 'One step left: bringing up Wi-Fi — both radios go dark for ~10 seconds,\n'
printf 'and if you are connected over Wi-Fi the SSH session will drop. All the\n'
printf 'work is done, the summary is saved to %s\n' "$SUMMARY"
printf '\n'
printf 'Copy the invite and the data above, then press Enter to bring Wi-Fi up.\n'
printf 'Enter to continue: '
read -r _ </dev/tty || true
printf '\n'
wifi reload >/tmp/lxd-wifi-reload.log 2>&1 </dev/null &
printf 'Wi-Fi is coming up. In ~15 seconds the network "%s"%s will be on the air.\n' \
    "${SSID_5G:-$SSID_2G}" "$([ -n "$SSID_5G" ] && [ -n "$SSID_2G" ] && printf ' and "%s"' "$SSID_2G")"
exit 0
