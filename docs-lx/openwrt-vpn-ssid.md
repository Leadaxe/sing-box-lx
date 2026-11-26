# A VPN SSID over the lxd daemon on OpenWrt

> 🌐 Русская версия: **[openwrt-vpn-ssid.ru.md](openwrt-vpn-ssid.ru.md)**.

This guide builds a separate Wi-Fi whose whole traffic goes through the sing-box
core and leaves the main home network untouched. It extends
[lxd-daemon.md](lxd-daemon.md). The daemon guide installs and configures
`sing-box lxd` itself; this page covers the network plumbing around it (Wi-Fi →
bridge → tun → firewall).

> ⚡ The whole segment, daemon included, installs with one script:
> **[scripts-lx/openwrt](../scripts-lx/openwrt/README.md)**. The script
> downloads the binary with an sha256 check and sets up the bridge, DHCP, a
> fail-closed firewall, the procd service and a pairing invite. A
> teardown/rollback script and operations recipes sit next to it. This page
> stays the reference for how it all works inside.

> This is an applied recipe for OpenWrt/fw4, not a fork feature. Every address,
> name and SSID below is a placeholder: substitute your own. Tested on OpenWrt
> 24.10 (fw4/nftables, mt76). The daemon must already be installed per
> [§8.3 of the daemon guide](lxd-daemon.md#83-openwrt--procd-routers).

Placeholder key:

| Name | Meaning | Replace with |
|---|---|---|
| `LxdVPN2G`/`LxdVPN5G` | VPN-segment SSIDs | your network names |
| `br-lxdvpn` | the segment's bridge | your bridge name |
| `192.168.20.0/24` | segment subnet (gateway `.1`) | a subnet free on your side |
| `lxd-tun0` | the core's tun interface | the name from the core config |
| `172.19.0.1/30` | tunnel p2p subnet | any spare /30 |
| `lxdvpn`, `sbtun` | segment and tunnel firewall zones | your zone names |

## Table of contents

- [0. Preconditions (check before §2)](#0-preconditions-check-before-2)
- [1. The main network does not depend on the core](#1-the-main-network-does-not-depend-on-the-core)
- [2. Bridge](#2-bridge)
- [3. The segment's gateway interface](#3-the-segments-gateway-interface)
- [4. DHCP for the segment](#4-dhcp-for-the-segment)
- [5. Firewall (fail-closed)](#5-firewall-fail-closed)
- [6. Wi-Fi into the bridge](#6-wi-fi-into-the-bridge)
- [7. Core config (applied via the daemon)](#7-core-config-applied-via-the-daemon)
- [8. Tunnel firewall — closing fail-closed](#8-tunnel-firewall--closing-fail-closed)
  - [8.1. Without `sbtun_tcp`, fw4 silently drops TCP under `stack: "system"`](#81-without-sbtun_tcp-fw4-silently-drops-tcp-under-stack-system)
- [9. The tun name and address must match the firewall rules](#9-the-tun-name-and-address-must-match-the-firewall-rules)
- [10. Verification](#10-verification)
  - [10.1. A virtual client on the router checks fail-closed](#101-a-virtual-client-on-the-router-checks-fail-closed)
- [11. Persistence (OpenWrt)](#11-persistence-openwrt)

---

## 0. Preconditions (check before §2)

Check the base before building the segment. Without it the build stops at the
very first step.

**The daemon is already installed and running.** Installation, the procd service
and uploading the binary are covered in the daemon guide,
[§8.3](lxd-daemon.md#83-openwrt--procd-routers). This page assumes `sing-box lxd`
is already running on the router.

**The base network is alive: WAN is up, there is internet.** Without it neither
`opkg update`, nor uploading the core, nor `apply` with an external config will
go through.

```bash
ping -c1 8.8.8.8 && nslookup openwrt.org        # router's WAN and DNS work
```

**There is enough room.** The binary is ~50 MB and does not fit a router without
extroot. You need extroot/USB; details and the reason are in the daemon guide,
§8.3. Check free space where the binary and state go:

```bash
df -h /overlay /                                # or the extroot mount point
```

**Wi-Fi radios exist and you know their names.** The guide uses
`radio0`/`radio1`, typical for a dual-band mt76. Your router's names and radio
count may differ: verify them and substitute your own in §6.

```bash
uci show wireless | grep '=wifi-device'         # radio0, radio1, …
iw dev                                          # physical interfaces and their phy
```

**Tools are present.** busybox has no `nohup`. The checks below need `ip`,
`uci`, `fw4` and `nft`, all standard on OpenWrt 24.10. `nslookup` comes with the
`dnsmasq`/`libustream` package.

**Take a config backup now, before the first step.** Rolling back only makes
sense to the state before the edits, so a backup at the end is useless. OpenWrt
often has no sftp-server, and `scp` fails. Stream the file over ssh instead:

```bash
ssh root@HOST 'sysupgrade -b /tmp/bk.tar.gz && cat /tmp/bk.tar.gz' > bk-before-vpn.tar.gz
```

To restore, run `sysupgrade -r bk-before-vpn.tar.gz` after uploading the file
back by the same stream.

## 1. The main network does not depend on the core

The goal is a separate Wi-Fi through the core while the main network depends on
the core in no way at all. The router is managed remotely, so losing LAN/SSH is
unacceptable. Two decisions follow, and the whole design rests on them:

1. **Isolation by interface, not by address.** The core intercepts traffic only
   from the VPN-segment bridge; `br-lan` is outside its remit. The mechanism is
   `include_interface` on the tun inbound; the field is covered in
   [§7](#7-core-config-applied-via-the-daemon).
2. **Fail-closed, not fail-open.** If the core dies, the VPN segment loses
   internet. A silent leak from the home IP is worse than a visible outage. The
   mechanism is the absence of a `lxdvpn→wan` forwarding, see
   [§5](#5-firewall-fail-closed).

```
                        ┌─ br-lan (192.168.10.0/24) ─→ zone lan ──────────┐
Wi-Fi (2 radios) ───────┤                                                  ├─→ WAN → internet
                        └─ br-lxdvpn (192.168.20.0/24) ─→ zone lxdvpn ─────┘
                                        │ forwarding: ONLY lxdvpn→sbtun
                                        ▼
                              iif br-lxdvpn → lxd-tun0 (zone sbtun)
                                        │
                              sing-box lxd (procd, control channel)
                                        │
                                  outbound → upstream
```

The upper branch (home network) does not change. The lower one is everything
added.

Below is the build from scratch, step by step, with a check after each. Wi-Fi
comes up last, once the bridge, addressing and firewall are ready. Otherwise the
APs attach to nothing.

> ⚠️ If you work over SSH remotely, read the warnings in steps 4 and 6 first.
> Restarting dnsmasq drops the whole LAN's DNS for a second. `wifi reload` tears
> down both radios for ~10 seconds, and your Wi-Fi session with them. Working
> over the wire or from a separate management interface is safer.

## 2. Bridge

```bash
uci set network.brlxdvpn=device
uci set network.brlxdvpn.name='br-lxdvpn'
uci set network.brlxdvpn.type='bridge'
uci set network.brlxdvpn.bridge_empty='1'
uci commit network
reload_config
```

`bridge_empty='1'` is mandatory. Without it netifd refuses to bring up a
portless bridge, and the interface never appears (`Device "br-lxdvpn" does not
exist`). The bridge is empty at creation time: the Wi-Fi APs attach to it later
and dynamically, when hostapd starts.

Check:

```bash
ip link show br-lxdvpn        # the interface exists (even without carrier)
```

## 3. The segment's gateway interface

```bash
uci set network.lxdvpn=interface
uci set network.lxdvpn.device='br-lxdvpn'
uci set network.lxdvpn.proto='static'
uci set network.lxdvpn.ipaddr='192.168.20.1'
uci set network.lxdvpn.netmask='255.255.255.0'
uci commit network
reload_config
```

`ip6assign` is left unset: IPv6 is not wanted in the segment. With it a client
would get an address via RA and leak around the tunnel, because sing-box
`auto_route` catches IPv4 in this configuration. The subnet must not overlap LAN
or any upstream subnets, such as WG instances.

Check:

```bash
ip addr show br-lxdvpn        # expect inet 192.168.20.1/24
```

The `NO-CARRIER … DOWN` state is normal: nobody is in the bridge yet.

## 4. DHCP for the segment

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

- **Option 6 (DNS)** must not point at the router's dnsmasq. Otherwise
  resolution goes around the tunnel and leaks domains to the ISP. With a direct
  outbound this is external DNS. With an upstream, DNS is hijacked inside the
  tunnel (hijack-dns), and the address becomes a nominal marker. Leave
  `ra`/`dhcpv6` unset.

> ⚠️ Restarting dnsmasq drops the whole LAN's DNS for a second. Account for it
> when working remotely.

Check — the main LAN's DNS is alive:

```bash
nslookup openwrt.org 192.168.1.1         # your LAN gateway address (OpenWrt default)
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

No `lxdvpn→wan` forwarding is created at all. The only allowed path outward is
into the tunnel zone `sbtun`. [§8](#8-tunnel-firewall--closing-fail-closed)
creates that zone and the `sbtun_tcp` rule once the tun interface is up. No
tunnel means no path, and nftables rejects the traffic. This is a property of
the design, not a just-in-case setting: for the segment to leak into WAN, someone
has to add a rule on purpose.

- `input=ACCEPT` on the `lxdvpn` zone lets clients reach DHCP and the gateway.
- No forwarding is created between `lxdvpn` and `lan`: a segment client does not
  see home devices.

## 6. Wi-Fi into the bridge

```bash
uci set wireless.lxdvpn_2g=wifi-iface
uci set wireless.lxdvpn_2g.device='radio0'
uci set wireless.lxdvpn_2g.mode='ap'
uci set wireless.lxdvpn_2g.network='lxdvpn'
uci set wireless.lxdvpn_2g.ssid='LxdVPN2G'
uci set wireless.lxdvpn_2g.encryption='psk2'
uci set wireless.lxdvpn_2g.key='your-password'
uci commit wireless
```

(likewise `lxdvpn_5g` on `radio1` with `ssid='LxdVPN5G'`)

The `network='lxdvpn'` line ties the AP to the bridge: it lands in `br-lxdvpn`
automatically. mt76 holds several APs per radio, so existing networks are
untouched.

Apply it detached from the terminal. `wifi reload` tears down both radios for
~10 seconds and will kill your SSH session if you are on Wi-Fi:

```bash
wifi reload >/tmp/wifi.log 2>&1 </dev/null &
```

> ⚠️ busybox has no `nohup` — do not use it.

Check after reconnecting — the APs landed in the bridge:

```bash
ls /sys/class/net/br-lxdvpn/brif/        # expect phy0-ap1, phy1-ap1
```

Empty output means a typo in `network='lxdvpn'`: the AP came up, but in the
wrong bridge. The symptom is misleading: the client associates to Wi-Fi but gets
no address, and the cause is not DHCP.

## 7. Core config (applied via the daemon)

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

The fields that matter:

| Field | Why this value |
|---|---|
| `include_interface: ["br-lxdvpn"]` | The isolation guarantee. `auto_route` alone is greedy and would pull the whole host into the tunnel. This filter narrows it to a single bridge; `br-lan` and router traffic are untouched. It holds the VPN-segment bridge name, nothing else |
| `auto_route: true` | the core writes policy routing itself instead of manual `ip rule` |
| `strict_route: false` | strict mode adds extra blocking rules, one more source of conflicts on a router with fw4 |
| `stack: "system"` | the system stack is faster than gvisor on a weak CPU; the price is the TCP nuance in [§8.1](#81-without-sbtun_tcp-fw4-silently-drops-tcp-under-stack-system) |
| `mtu: 1400` | headroom for future upstream overhead (WG/VLESS), so the outbound swap needs no rework |
| `address: 172.19.0.1/30` | the tunnel's service p2p subnet |

`direct-out` is a stub: traffic exits to WAN as-is. A real upstream (WG, VLESS,
…) is set by replacing `outbounds`/`route` in one apply. The scaffold, firewall
and Wi-Fi stay untouched.

Deliver the config over the daemon's channel, not by editing a file on disk:

```bash
curl -sk -X POST https://DAEMON:PORT/admin/apply --cert … --key … -d @config.json
```

Authentication works as in the daemon guide,
[§5](lxd-daemon.md#5-clients-authenticate-by-certificate-the-operator-by-secret) and
[§9](lxd-daemon.md#9-pairing-a-client-the-same-on-every-os); locally on the
router, use loopback. After apply the `lxd-tun0` interface comes up, and the
firewall can be closed onto the tunnel
([§8](#8-tunnel-firewall--closing-fail-closed)).

**The isolation check** shows whether the whole router went into the tunnel.
`auto_route` with `include_interface` writes policy routing, visible in
`ip rule`:

```bash
ip rule | head -3
# 9000: from all iif br-lxdvpn goto 9002   ← only traffic from the segment bridge → tunnel
# 9001: from all goto 9010                 ← everything else goes past, into main
```

The decision is made by incoming interface (`iif br-lxdvpn`), not by source
address. `br-lan` and the router's own traffic never touch the tunnel. If there
is no `iif br-lxdvpn` rule, or it is `from all` without `iif`,
`include_interface` did not take effect: check the bridge name in the core
config.

## 8. Tunnel firewall — closing fail-closed

This step comes after the core config: the zone binds to an already-existing tun
device. It has three parts: the tunnel zone, the single allowed forwarding from
the segment into it, and a targeted TCP ACCEPT. Why the ACCEPT is needed is
explained in [§8.1](#81-without-sbtun_tcp-fw4-silently-drops-tcp-under-stack-system).

```bash
# the tunnel zone
uci set firewall.sbtun=zone
uci set firewall.sbtun.name='sbtun'
uci add_list firewall.sbtun.device='lxd-tun0'
uci set firewall.sbtun.input='REJECT'
uci set firewall.sbtun.output='ACCEPT'
uci set firewall.sbtun.forward='REJECT'
# the only path outward: segment → tunnel
uci set firewall.lxdvpn2tun=forwarding
uci set firewall.lxdvpn2tun.src='lxdvpn'
uci set firewall.lxdvpn2tun.dest='sbtun'
# ACCEPT for system-stack TCP (see below)
uci set firewall.sbtun_tcp=rule
uci set firewall.sbtun_tcp.name='Allow-sbtun-systemstack-tcp'
uci set firewall.sbtun_tcp.src='sbtun'
uci set firewall.sbtun_tcp.dest_ip='172.19.0.1'
uci set firewall.sbtun_tcp.proto='tcp'
uci set firewall.sbtun_tcp.target='ACCEPT'
uci commit firewall
fw4 reload
```

`firewall.sbtun.device='lxd-tun0'` binds the zone to the device by name. If the
tun name changes in the core config, the zone goes empty. The whole segment then
loses internet, because the only forwarding leads into `sbtun`. How to keep the
names in sync is in
[§9](#9-the-tun-name-and-address-must-match-the-firewall-rules).

Check:

```bash
nft list ruleset | grep -A2 Allow-sbtun-systemstack-tcp
```

### 8.1. Without `sbtun_tcp`, fw4 silently drops TCP under `stack: "system"`

Symptom: UDP and ICMP traverse the tunnel and DNS resolves, but TCP does not
establish at all. The core log shows only `inbound packet connection`, not a
single TCP connect.

Cause: under `stack: "system"` sing-box does not handle TCP straight from the
tun fd. It redirects TCP to its own local listener on the tunnel address
(`172.19.0.1:<dynamic port>`). To the Linux kernel this is an ordinary inbound to
a local address. It goes through INPUT in zone `sbtun`, where `input=REJECT`,
and is rejected there. UDP/ICMP do not take this path, since sing-box reads them
straight from the tun fd, which is why only TCP fails. `sbtun_tcp` is a targeted
ACCEPT to the tunnel address over TCP; fail-closed stays intact.

> ⚠️ The rule is bound to the IP. Changing the tun inbound's `address`, including
> by a foreign apply, breaks it: clients get `connection refused` on any TCP
> while ICMP/DNS stay alive. Keep `dest_ip` in sync with the config's `address`,
> see [§9](#9-the-tun-name-and-address-must-match-the-firewall-rules).

## 9. The tun name and address must match the firewall rules

Two core-config fields hold the firewall plumbing. A desync in either breaks the
segment, while the core looks healthy:

| What | Where | Tied to |
|---|---|---|
| `interface_name` | core config | ← → `firewall.sbtun.device` (§8) |
| `address` | core config | ← → `firewall.sbtun_tcp.dest_ip` (§8) |

Any third-party apply, such as one pushed from the launcher, can change
`interface_name` or `address`. Firewall rules bound to the old name or IP then
silently stop matching. The daemon is not at fault: it applied the config it was
sent. Check after every non-trivial apply:

```bash
ip addr show lxd-tun0 | grep inet          # the tunnel's current address
nft list ruleset | grep Allow-sbtun-systemstack-tcp   # packets 0 under live traffic = desync
```

## 10. Verification

| Check | Expectation |
|---|---|
| DHCP | client gets an address from `192.168.20.0/24`, gw `.1`, DNS from option 6 |
| ICMP | `ping 8.8.8.8` from the segment works |
| DNS | resolution through the tunnel works |
| TCP/HTTP | external IP = the upstream's address (with `direct`, the router's WAN address) |
| Transit | `tx_bytes` on `lxd-tun0` grows in step with traffic |
| Fail-closed | stop the daemon service → tunnel gone, ping 100% loss, TCP dropped, no WAN leak |
| Recovery | start the service → daemon brings up last-good, client back online |

Check the main channel (LAN/SSH/your Mac's internet) after every layer. It must
always stay alive: that is what the isolation is for.

### 10.1. A virtual client on the router checks fail-closed

During remote setup there is nobody to associate a real device with the new
SSID, and without a client you cannot prove fail-closed. The client is built
right on the router: a `veth` pair, one leg in the segment bridge, the other in
an isolated network namespace. The namespace mimics a separate device behind
Wi-Fi.

```bash
opkg update && opkg install kmod-veth
ip link add v0 type veth peer name v1
ip netns add t && ip link set v1 netns t
ip link set v0 master br-lxdvpn && ip link set v0 up
ip netns exec t ip link set v1 up
# the netns inherits the router's resolv.conf (127.0.0.1) — absent inside it; give it its own:
mkdir -p /etc/netns/t && echo "nameserver 8.8.8.8" > /etc/netns/t/resolv.conf

# get a DHCP lease and check the exit
ip netns exec t udhcpc -i v1 -n -q -s /usr/share/udhcpc/default.script
ip netns exec t ping -c1 8.8.8.8                 # ICMP through the tunnel
ip netns exec t wget -qO- http://api.ipify.org   # external IP = the upstream's?
```

The fail-closed check: stop the daemon service and repeat the requests.

```bash
/etc/init.d/sing-box-lxd stop
ip netns exec t ping -c2 8.8.8.8                  # expect 100% loss
ip netns exec t wget -qO- --timeout=5 http://api.ipify.org  # expect failure, NOT a WAN IP
/etc/init.d/sing-box-lxd start                    # restore
```

If an external IP still comes back with the daemon stopped, traffic leaks into
WAN. There is a stray forwarding somewhere, compare against
[§5](#5-firewall-fail-closed): the segment must stay offline.

Clean up:

```bash
ip netns del t && ip link del v0 && rm -rf /etc/netns/t
```

> ⚠️ `kmod-veth` is not in the base firmware. Without it `ip link add … type
> veth` gives `Unknown device type`. You need `opkg install kmod-veth`, which
> requires a live WAN, see [§0](#0-preconditions-check-before-2).

> ⚠️ The netns does not inherit a working resolv.conf: the router's `127.0.0.1`
> does not exist inside it. Its own `/etc/netns/t/resolv.conf` is therefore
> mandatory, or DNS in the namespace is dead.

## 11. Persistence (OpenWrt)

`sysupgrade` keeps neither the binary, the state, nor the daemon's init script,
see [§8.3 of the daemon guide](lxd-daemon.md#83-openwrt--procd-routers). The UCI
sections (`wireless`/`network`/`dhcp`/`firewall`) survive `sysupgrade -b` but
not `sysupgrade -n` (reset to defaults). The config backup is taken before the
build begins, see [§0](#0-preconditions-check-before-2).
