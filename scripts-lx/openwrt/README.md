# VPN-SSID on OpenWrt — installer

> 🌐 Русская версия: **[README.ru.md](README.ru.md)**.

> Manual build of the same segment, step by step, with every decision explained:
> **[docs-lx/openwrt-vpn-ssid.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/docs-lx/openwrt-vpn-ssid.md)**
> ([in Russian](https://github.com/Leadaxe/sing-box-lx/blob/lx/docs-lx/openwrt-vpn-ssid.ru.md)).
> The daemon, `daemon.json`, pairing, telemetry:
> [docs-lx/lxd-daemon.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/docs-lx/lxd-daemon.md).
> This page — the automated installer and operations recipes.

A separate Wi-Fi network whose entire traffic goes through the sing-box-lx core right on the router. The main network does not depend on the core in any way — a broken config cannot cut off SSH/LAN.

```
SSID → bridge → tun (auto_route + include_interface) → sing-box core → upstream
```

Built and verified on a RouteRich AX3000 (OpenWrt 24.10 fork, mediatek/filogic, aarch64), running in production.

## Requirements

- OpenWrt 22.03+ with fw4/nftables (forks that keep `ID="openwrt"` in `/etc/os-release` qualify) and dnsmasq;
- ~80 MB free space (the binary is ~50 MB; small overlays need extroot);
- an architecture the fork releases cover: aarch64, x86_64, armv7l, mips, mipsel.

## Install

**The backup takes care of itself.** The first thing the installer does is take a full router backup into `/root/backup-pre-lxd-<date>.tar.gz` — once, before any changes; re-runs never overwrite the clean copy. The [teardown script](#remove-everything) can roll the router back from this file entirely (`--restore`). Still worth pulling a copy to your computer in case the router becomes unreachable (no sftp on the router, so stream it):

```bash
ssh root@ROUTER 'cat /root/backup-pre-lxd-*.tar.gz' > backup-pre-lxd.tar.gz
```

**Install.** SSH into the router (`ssh root@ROUTER`) and run:

```bash
wget -O /tmp/lxd-setup.sh https://raw.githubusercontent.com/Leadaxe/sing-box-lx/lx/scripts-lx/openwrt/lxd-openwrt-setup.sh && sh /tmp/lxd-setup.sh
```

If busybox-wget complains about SSL: `opkg install ca-bundle libustream-mbedtls` and retry.

Fallback — when the router has no direct GitHub access, deliver the file from your computer and run it via `ssh -t` (the script is interactive and needs a terminal — it detects a pty-less `sh -` pipe and refuses to run):

```bash
curl -fsSL https://raw.githubusercontent.com/Leadaxe/sing-box-lx/lx/scripts-lx/openwrt/lxd-openwrt-setup.sh | ssh root@ROUTER 'cat > /tmp/lxd-setup.sh'
```

```bash
ssh -t root@ROUTER 'sh /tmp/lxd-setup.sh'
```

(from a local repo checkout the first command is `ssh root@ROUTER 'cat > /tmp/lxd-setup.sh' < lxd-openwrt-setup.sh`)

Seven questions: the Wi-Fi password (prefills the one found in existing networks), the 5 GHz SSID, whether to add a 2.4 GHz network, the tun interface name and address, the bridge name, and whether to expose management to the outside. Everything else the script does itself: downloads the binary with an sha256 check, brings up the bridge, DHCP, firewall, a procd service, and mints an invite.

The radio comes up **as the last step, on Enter** — so a Wi-Fi drop cannot kill the script before it prints the invite.

The summary is printed and saved to `/root/lxd-setup-summary.txt` (chmod 600 — it contains the pairing code and the Wi-Fi password):

```
Pair invite:       <router-LAN-address>:19091#<server-fingerprint>#<CODE>
tun name:          lxd-tun0
tun address:       172.16.0.1/30
include_interface: br-lxdvpn          ← the "LAN interfaces" field in the launcher UI
```

After the install the core runs a skeleton config: the segment works but exits **directly via WAN**. The production upstream is uploaded from the launcher in a single apply — network, firewall, and Wi-Fi stay untouched.

All recipes below use the default names (`br-lxdvpn`, `lxd-tun0`, service `sing-box-lxd`). If you picked your own during the install, substitute the values from the summary.

---

## Right after the install

### 1. Pair the launcher

The launcher is **[singbox-launcher](https://github.com/Leadaxe/singbox-launcher)** — download it from that page's releases. In it: the **Remote** tab → the **+ Add** button → the "Add remote machine" dialog:

- **Name** — anything you like (empty = the address);
- **Invite** — the full line from the summary (`address#fingerprint#code`). The address in it is already the working one (the router's LAN address); the fingerprint and code go in as-is — the launcher pins the daemon's TLS certificate by that fingerprint;
- **Platform** — `linux`, **Architecture** — the router's (aarch64 → `arm64`);
- **Add**.

The code is one-time and burns after the first attempt — if pairing fails, mint a fresh invite (`sing-box lxd client add`, recipe below) and paste the new line.

![The Add remote machine dialog](img/launcher-add-remote.png)

⚠️ **Do not restart the daemon before the code is entered.** The code lives in process memory; a restart yields `enroll: no active enrollment code` and the invite has to be minted again.

### 2. Upload the production config

From the launcher, via the Config Wizard.

The **Target** tab:

- **Config target** — `Remote machine`;
- **Target OS** — `linux`, **Target architecture** — the router's (aarch64 → `arm64`);
- tick **Gateway mode (share to LAN)**;
- **LAN interfaces (include_interface)** — ⚠️ don't forget to enter the bridge from the summary (`br-lxdvpn`).

![Config Wizard, the Target tab](img/wizard-target.png)

The **Settings** tab, TUN block:

- **Enable TUN** — on;
- **TUN interface name** — the name from the summary (`lxd-tun0`): the wizard itself warns the core default cannot be relied on — on a router the firewall and routes are bound to that name;
- **TUN interface addresses** — the address from the summary (`172.16.0.1/30`): the `sbtun_tcp` firewall rule is bound to it;
- **TUN strict route** — off; **TUN stack** — `system`; the MTU — the suggested one is fine.

![Config Wizard, the Settings tab — TUN block](img/wizard-settings-tun.png)

The resulting tun inbound must carry the values from the summary:

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

`include_interface` is the **VPN segment bridge**, not `br-lan`. It is what contains the capture: `auto_route` on its own is greedy and would drag the whole router into the tunnel.

### 3. Check the segment routes

Join the new SSID with a phone and open any site. No real client at hand — use a virtual one right on the router (see the recipe below).

---

## Recipes

### Add/revoke a client

```bash
sing-box lxd client add --name <name>
sing-box lxd client list
sing-box lxd client remove <name>
```

`--state-dir` is not needed: the `client` subcommands find the installed service's state on their own (via `daemon.json` at the platform default; on OpenWrt that is `/etc/sing-box-lxd/state`). The flag only matters for a non-standard path.

Works **only over loopback** (`403 operator routes are loopback-only` over the network), so `127.0.0.1` must be listened on and stand **first** in `listen.address` (the script sets it up exactly so). Do not restart the daemon between `add` and entering the code.

### Test the segment without a real client

A virtual client inside the segment bridge is the only way to test everything, including fail-closed, without touching live users:

```bash
opkg install kmod-veth        # not in the stock firmware
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
ip netns exec t udhcpc -i v1 -n -q -f -s /tmp/dc.sh     # got an address?
ip netns exec t wget -qO- http://api.ipify.org           # external IP = upstream?

# clean up after yourself
ip netns del t; ip link del v0; rm -rf /etc/netns/t /tmp/dc.sh
```

A dedicated `resolv.conf` for the netns is mandatory: otherwise it inherits the router's `127.0.0.1`, which does not exist inside the netns.

### Test fail-closed

```bash
/etc/init.d/sing-box-lxd stop
# from the segment: ping dead, TCP refused, NOTHING leaks to WAN
/etc/init.d/sing-box-lxd start
# the core comes back from last_good, the client is online again
```

### Confirm the main network is untouched

```bash
ip rule | head -3
# 9000: from all iif br-lxdvpn goto 9002   ← only the segment goes into the tunnel
# 9001: from all goto 9010                 ← everything else bypasses it, into main

ip route get 8.8.8.8 from <LAN-client-address> iif br-lan    # → via WAN, as before
```

### Who is in the segment and on which SSID

```bash
cat /tmp/dhcp.leases                                   # IP → MAC → device name
for i in $(iw dev | awk '$1=="Interface"{print $2}'); do
  echo "$i ($(iwinfo $i info | grep -o 'ESSID: ".*"'))"
  iw dev $i station dump | grep Station
done
```

The join key is the MAC: the lease gives the name, `station dump` — which AP, `iwinfo` — which SSID that AP serves.

### Measure load and the speed ceiling

```bash
TUN=lxd-tun0                  # name from the summary
P=$(pgrep -f "^/usr/bin/sing-box lxd" | head -1)
read _ u1 n1 s1 i1 w1 q1 sq1 _ < /proc/stat; A=$(awk '{print $14+$15}' /proc/$P/stat)
R1=$(cat /sys/class/net/$TUN/statistics/rx_bytes)
sleep 10
read _ u2 n2 s2 i2 w2 q2 sq2 _ < /proc/stat; B=$(awk '{print $14+$15}' /proc/$P/stat)
R2=$(cat /sys/class/net/$TUN/statistics/rx_bytes)
T=$(( (u2+n2+s2+i2+w2+q2+sq2)-(u1+n1+s1+i1+w1+q1+sq1) ))
awk -v d=$((B-A)) -v t=$T -v c=$(grep -c ^processor /proc/cpuinfo) \
    -v r=$((R2-R1)) 'BEGIN{printf "sing-box: %.1f%% of a core at %.1f Mbit/s\n", d*100/t*c, r*8/10/1e6}'
```

### Service and logs

```bash
/etc/init.d/sing-box-lxd status | restart | stop | start
logread | grep sing-box | tail -20        # system log
tail -f /etc/sing-box-lxd/lxd.log         # the daemon's own log, self-rotated
```

Apply, rollback, and core status — **from the launcher only**: they sit behind mTLS, `curl` from the router answers `client certificate not trusted`.

### Take and restore a backup

```bash
# take one (no sftp-server on the router, so stream it)
ssh root@ROUTER 'sysupgrade -b /tmp/bk.tar.gz >/dev/null 2>&1; cat /tmp/bk.tar.gz' > backup.tar.gz
ssh root@ROUTER 'rm /tmp/bk.tar.gz'

# restore: the archive check, restore, and reboot are detached from the terminal —
# the reboot kills the SSH session and the chain must not die with it
ssh root@ROUTER 'cat > /tmp/bk.tar.gz' < backup.tar.gz
ssh root@ROUTER 'tar -tzf /tmp/bk.tar.gz >/dev/null && ( sysupgrade -r /tmp/bk.tar.gz && sleep 2 && reboot ) >/dev/null 2>&1 </dev/null &'
```

The binary and state land in the backup only because the script added them to `/etc/sysupgrade.conf`.

⚠️ Restoring a backup taken **before** the install is not a full cleanup: it does not remove files that appeared after the backup was taken (the binary, the init script, the state) and will not bring back a default `/etc/sysupgrade.conf` (unchanged files never make it into `sysupgrade -b`, so there is nothing to overwrite it with). Run the [teardown script](#remove-everything) before the restore — it removes both the files and the `sysupgrade.conf` lines.

### Remove everything

```bash
wget -O /tmp/lxd-uninstall.sh https://raw.githubusercontent.com/Leadaxe/sing-box-lx/lx/scripts-lx/openwrt/lxd-openwrt-uninstall.sh && sh /tmp/lxd-uninstall.sh
```

The [lxd-openwrt-uninstall.sh](lxd-openwrt-uninstall.sh) script finds the segment name on its own (via the forwarding into the `sbtun` zone), stops and removes the service, state, and uci sections, reloads network/firewall/dnsmasq, and restarts the radio at the end. It tolerates a half-installed state — works as cleanup after an install that broke at any step. The main network is untouched.

Flags: `--yes` — no questions (no-tty runs); `--restore` — instead of the uci teardown, restore the configs from the pre-lxd backup the installer took (`/root/backup-pre-lxd-*.tar.gz`) and reboot the router — the "as before the install" state. The daemon files and the `sysupgrade.conf` lines are cleaned in both modes (restore does not do that by itself — see the warning in the backup recipe).

Section names derive from the bridge: `br-lxdvpn` → `lxdvpn`; when tearing down by hand, `ifdown` **before** deleting the uci sections and `/etc/init.d/network reload` **after** are mandatory — otherwise netifd never learns about the deletion, an orphan bridge stays behind, and a re-install trips over "interface already exists".

---

## The main landmine: tunnel name/address drift

Two fields of the core config hold up the firewall plumbing. Any uploaded config (including one from the launcher) may change them — and the rules bound to the old values silently stop matching. **The core looks perfectly healthy the whole time.**

| Core config field | Tied to | Drift symptom |
|---|---|---|
| `interface_name` | `firewall.sbtun.device` | segment has no internet at all (empty zone) |
| `address` | `firewall.sbtun_tcp.dest_ip` | **`connection refused` on TCP while ICMP and DNS work** |

The second case is the treacherous one: pings pass, DNS resolves, yet no page opens.

```bash
ip addr show lxd-tun0 | grep inet                          # tunnel address right now
uci show firewall.sbtun.device firewall.sbtun_tcp.dest_ip  # what the firewall has
nft list ruleset | grep Allow-sbtun-systemstack-tcp        # packets 0 under live traffic = miss

uci set firewall.sbtun_tcp.dest_ip='<new address>'
uci commit firewall && fw4 reload
```

To drop the coupling entirely: `"stack": "gvisor"` in the core config. sing-box then parses TCP internally, no local listener appears, and the `sbtun_tcp` rule is unnecessary. The price — higher CPU load.

## OpenWrt quirks baked into the script

- **`bridge_empty=1`** is mandatory for a bridge with no ethernet ports (Wi-Fi APs only, attached dynamically when hostapd starts). Without the flag netifd will not bring the bridge up at all.
- **busybox has no `nohup`.** Background runs — `cmd >/log 2>&1 </dev/null &`.
- **busybox has no `xxd` or `od`** — the secret is generated via `openssl rand -hex 32` (fallback — `hexdump`).
- **`ID` from `/etc/os-release`, not `DISTRIB_ID`.** Forks write their own name into `DISTRIB_ID` (RouteRich puts `RouteRich` there) while keeping `ID="openwrt"`.
- **`listen: ["127.0.0.1", "0.0.0.0"]` kills the daemon** — `bind: address already in use`, the core never comes up. `0.0.0.0` goes alone; it covers loopback by itself.
- **`/releases/latest` ignores pre-releases**, and the fork ships releases as rc — so the first entry of the full list is taken.
- **`wget` on OpenWrt is `uclient-fetch`**: without `-O` it saves a redirected GitHub asset under the name from the **final** URL — the download "succeeds" into a file you did not ask for.
- **`wifi reload` tears down the SSH session** — which is why it is the last step, after the invite is minted and the summary saved.
- **A dnsmasq restart drops DNS for the whole LAN** for a second — keep in mind when working remotely.

## Performance (a reference point)

Measured on a RouteRich AX3000 (Cortex-A53, 2 cores, no hardware AES), exiting via VLESS+Reality:

| Load | Traffic | sing-box CPU |
|---|---|---|
| idle | 0 | ~1% of a core |
| medium | 45 Mbit/s | ~33% of a core |
| **ceiling** | **~70 Mbit/s** | **94% of a core** |

The bottleneck is CPU crypto. The main Wi-Fi keeps full speed meanwhile: it bypasses the core entirely via hardware offload.
