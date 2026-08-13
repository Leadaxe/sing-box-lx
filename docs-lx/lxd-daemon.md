# The lxd daemon: what it is and how to set it up

Operator's guide: what `sing-box lxd` is, why it exists, how it is installed on
macOS, and the setup approaches on Linux. Русская версия —
[lxd-daemon-ru.md](lxd-daemon-ru.md).

---

## 1. What it is and why

`sing-box lxd` is a **daemon that hosts the sing-box core in-process** and
exposes a long-lived control channel. What sets it apart from `sing-box run`:

- **The channel survives reloads.** With `run`, a config change means a process
  restart: the client (launcher) loses its connection, status and observability
  streams break. With lxd the listening port belongs to the daemon, not to the
  box instance: apply swaps the core *under* a live server, and the client sees
  the STARTED/STOPPING/… transitions on one uninterrupted stream.
- **The daemon is reachable exactly when the data plane is down.** The control
  channel comes up BEFORE the core: a broken or missing config leaves the
  daemon online — you fix the config over the very channel you need most when
  traffic is not flowing.
- **Config changes with guarantees.** `POST /admin/apply` validates the
  candidate (`sing-box check` with the daemon's own binary), rolls back to the
  last config known to work (**last-good**) when a start fails, and remembers
  an interrupted apply and the run intent (`was_running`) across restarts and
  reboots.
- **Remote management with real trust.** mTLS: the daemon is its own CA,
  clients enroll with a one-time invite and are recognized by their certificate
  from then on.

Typical roles: a local engine for the launcher on the same Mac (the channel
survives reloads), and a **remote node** — e.g. the core on a router or server
managed by the launcher over the network.

One port carries two planes:

| Plane | Protocol | What it provides |
|---|---|---|
| observability | gRPC `daemon.StartedService` | statuses, logs, groups/urltest, DNS stream, connections — the protocol shared with the Android line |
| administration | REST `/admin/*` | apply / rollback / start / stop / config / status / info, client enrollment |

## 2. Quick start (dev, no installation)

```bash
sing-box lxd --state-dir lxd-state -c config.json
```

Without a `daemon.json` the **dev defaults** apply: plain h2c on
`127.0.0.1:9091`, no secret, no mTLS, no client registry. The log stays on the
screen. `-c` is an optional seed: it is only used while there is no last-good;
without it the daemon starts empty (IDLE) and waits for the first apply.

Check:

```bash
curl -s http://127.0.0.1:9091/admin/status
```

## 3. daemon.json — the daemon's settings

Lives in `<state-dir>/daemon.json` (0600). The **only** source of connection
settings: the command has no `--listen/--tls/--secret` flags by construction —
the "file or flag" question cannot even be asked. No file → dev defaults; the
file is never created implicitly (it is written by `--service=install` on macOS
or by the operator's editor).

| Key | Default | Meaning |
|---|---|---|
| `listen` | `127.0.0.1:9091` | channel address (both planes); a `"host:port"` string, or `{"address": [...], "port": N}` to bind several addresses — see below |
| `tls` | `false` | mTLS with client enrollment; `false` = plain h2c, loopback/dev only |
| `secret` | empty | Bearer secret for the operator routes; the only gate when `tls: false` (empty = no authentication) |
| `log_max_size_mb` | `20` | log rotation: safety size ceiling |
| `log_max_backups` | `1` | how many rotated generations (`lxd.log.1…N`) to keep |
| `log_max_age_hours` | `24` | rotation by file age |

The rotation defaults give "about a day of history"; 0/absent key = default,
and there is deliberately no "unlimited" setting. Changing any setting is a
file edit + service restart, never a reinstall.

### 3.1. listen: one address or several

`listen` takes two forms with one meaning — the addresses the control channel
binds. Both planes (gRPC and admin REST) are served identically on every one.

```jsonc
"listen": "127.0.0.1:19091"                                  // one address
"listen": {"address": ["192.168.10.1", "127.0.0.1"], "port": 19091}  // several
```

The second form exists because one address is often genuinely not enough: a
daemon reachable from a LAN interface **and** from loopback cannot be expressed
as a single bind. `0.0.0.0` is not the answer — it also exposes every other
interface the host happens to have, including ones you never meant to serve on.

Rules worth knowing:

- **All or nothing.** If any configured address fails to bind (typo, or an
  interface that is not up yet), the daemon exits with an error naming it. A
  daemon half-listening — healthy-looking but unreachable exactly where you
  asked for it — is the failure this prevents. On a host where the address
  appears late (a bridge or tunnel configured after boot), order the service
  after that interface.
- **The first address is the advertised one.** Enrollment invites, the local
  client, and the install summary all point at the first entry, so put the
  address launchers should dial first.
- **No netmasks.** `192.168.10.1/32` is rejected: the kernel binds one address,
  never a range, so a mask could only make the file claim something the daemon
  does not do. Restricting *who* may connect is the firewall's job.
- The string form is unchanged and keeps working — existing `daemon.json` files
  need no edit, and a single address is written back as a string.

## 4. Command-line keys

| Key | Meaning |
|---|---|
| `--state-dir <dir>` | the daemon's home: daemon.json, last-good, run-state, client registry, keys (default `lxd-state`) |
| `-c <file>` | seed config (exactly one file; `-C` directories are not supported) |
| `--config-force <file>` | always boot from this file, overriding last-good |
| `--run` | bring the core up regardless of the recorded run state |
| `--service install\|install-user\|uninstall` | service installation (see the OS sections) |
| `--purge` | with `uninstall` — also delete the state directory |
| `--dry-run` | with `--service` — show what would be done, change nothing |
| `client add [--name <label>]` | mint a one-time invite for a new client |
| `client list` / `client remove <name-or-fingerprint>` | list / revoke trusted clients |

The subcommand exists only in builds with the `with_lxd` tag.

## 5. Security: who authenticates with what

- **A client (the launcher)** — with the trusted certificate obtained at
  enrollment. The certificate is the full credential for both planes; a client
  needs no Bearer and never learns the secret.
- **The operator (a human with a shell on the host)** — with the Bearer secret
  from daemon.json on the **loopback-only** routes (`client add/list/remove`).
  Minting an invite grants trust, so these routes are unreachable from the
  network by design.
- **Enrollment** is the only road to trust: a one-time code
  (`address#server-fingerprint#code`) that burns on first use; the client pins
  the server by its fingerprint.
- With `tls: false` (dev) the Bearer is the only gate; with no secret there is
  no authentication at all — which is why plain mode is loopback-only.

## 6. Logs

Under a service manager (stdout is not a terminal) the daemon **owns** the
`<support>/lxd.log` file: it captures the process's stdout/stderr (everything
lands in the file, including the core's log and runtime panics) and rotates it
by age and size with the daemon.json limits. When run by hand in a terminal the
log stays on the screen and no file is touched. Clients discover the log path
and state dir from `GET /admin/info` — nothing needs to be hard-coded.
Implemented on macOS and Linux; not on Windows (neither is the service).

**Two log channels, and they carry different things.** The gRPC `SubscribeLog`
stream carries the **core's** log — what the running instance emits. The
daemon's own lines (`lxd: …`, bootstrap errors, runtime panics) go to
stdout/stderr, i.e. into `lxd.log`. That difference matters precisely when
things break: with no core up there is nothing to stream, and the reason sits
in the file. `GET /admin/logs?tail=N` serves its tail over the network, so a
remote client does not need shell access to the host:

```bash
curl -s --cert client.pem --key client.key -k \
  "https://server:9091/admin/logs?tail=500"
```

## 7. macOS — automatic installation

The only platform with a full `--service`. Two scopes:

```bash
sudo sing-box lxd --service=install    # system LaunchDaemon: root, starts before login, TUN
sing-box lxd --service=install-user    # LaunchAgent: no sudo, starts at login, desktop UX
```

Install does everything itself:

1. creates `…/Application Support/sing-box-lxd/` (0700) with `state/` inside;
2. **materializes daemon.json**: an existing address is kept (a reinstall never
   moves the channel out from under enrolled clients), otherwise the first free
   loopback port from 19091 up; `tls` — always; the secret — kept or generated;
3. writes the plist (`com.leadaxe.sing-box-lxd`) and bootstraps the service;
   the plist degenerates to `sing-box lxd --state-dir <dir>` — every setting
   lives in daemon.json;
4. prints the summary: channel address, admin secret, daemon.json path, the
   restart command — and a **one-time invite** to pair the launcher.

Paths: system — `/Library/Application Support/sing-box-lxd/`, user —
`~/Library/Application Support/sing-box-lxd/`. The log is `lxd.log` beside
`state/`.

Other actions:

```bash
sing-box lxd --service=install --dry-run  # show the plist and what would happen, touch nothing
sing-box lxd --service=uninstall          # remove the service; state is kept
sing-box lxd --service=uninstall --purge  # remove the service AND the state (clients, keys, last-good)
sing-box lxd --service=uninstall --dry-run --purge   # show what would be removed
sudo sing-box lxd client add --name mac-book   # a fresh invite on a live daemon (state-dir is found automatically)
```

## 8. Linux — setup approaches

**Principle: on Linux `--service` ONLY PRINTS.** Everything that touches the
disk — the unit/init script, `daemon.json`, deleting state — is run by the
operator. Why: launchd is one vendor with one API, while Linux is a zoo
(systemd hosts, OpenWrt/procd routers, containers with neither), and a wrong
guess that mutates `/etc` is worse than an exact printout. Read-only also has
no half-states: a recipe that is never partially applied cannot leave the host
stranded between two configurations.

So `--service=install` detects the init system and prints a ready-to-paste
recipe — the daemon home, `daemon.json`, the unit/init script, the enabling
commands and the pairing step — with a link to the matching section here.
`--service=uninstall` prints the removal steps the same way; `--purge` prints
the `rm -rf` command instead of running it. `--dry-run` is accepted and
changes nothing here — on Linux every action is already a printout.

The daemon itself is fully functional on Linux: mTLS, apply/rollback and log
rotation all work; the `GOOS=linux GOARCH=arm64` cross-build (static binary,
musl-compatible) is verified. The secret is never printed on screen: the recipe
generates it in place with `$(head -c 32 /dev/urandom | xxd -p -c 64)`, so it
exists only inside `daemon.json` on the host.

The sections below are what that recipe prints, if you prefer to do it by hand.

### 8.1. Common part (any init)

```bash
mkdir -p /var/lib/sing-box-lxd/state        # OpenWrt: /etc/sing-box-lxd/state — see 8.3
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

> ⚠️ **OpenWrt/busybox has no `xxd`** — the substitution above yields an **empty**
> secret with no error, and `"secret": ""` under `tls: true` means an
> unauthenticated control channel. There, mint it with `$(openssl rand -hex 32)`
> (openssl is almost always present on OpenWrt) and **verify what was written**
> (`grep secret daemon.json`) rather than trusting the command's exit. The
> `--service=install` recipe already prints the right generator per init.

To manage the daemon from another machine set `listen` to a LAN address (e.g.
`192.168.10.1:19091`), or keep loopback alongside it with the object form
(`{"address": ["192.168.10.1", "127.0.0.1"], "port": 19091}`) — see 3.1. mTLS is
mandatory, and do not open the port outward in the firewall without need.
Operator commands (`client add`) still run only on the host itself.

### 8.2. systemd (a regular server/desktop)

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

Under systemd stdout is not a terminal, so the daemon takes the log over into
`/var/lib/sing-box-lxd/lxd.log` and rotates it; journald only receives the
early output produced before the takeover.

### 8.3. OpenWrt / procd (routers)

Platform specifics:

- `/var` is tmpfs and dies on reboot → keep the state directory somewhere
  persistent: `/etc/sing-box-lxd/state` (overlay) or extroot (`/root/…`).
- On routers **without extroot** a log beside the state dir writes into the
  NAND overlay — flash wear; with extroot there is no issue. The default
  rotation limits (20 MB × 2 files) bound the damage, but extroot is better.

**Uploading the binary.** OpenWrt often ships no sftp-server, so `scp`/`rsync`
fail. Stream it over ssh instead (download and unpack the archive on your
workstation, not the router — it may lack the room for the tar):

```bash
ssh root@HOST 'cat > /root/sing-box' < sing-box            # the unpacked binary
ssh root@HOST 'chmod +x /root/sing-box && /root/sing-box version'
```

Use a **static** build (`GOOS=linux`, musl-compatible) and confirm it before
running: `file sing-box` must say `statically linked` for the right arch — a
glibc-dynamic binary will not start on a musl router. Check the `version` output
for the `with_lxd` tag — without it there is no `lxd`, no `client`.

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

`sysupgrade -b` picks up neither the init script, nor the state, nor the
binary — add the paths to `/etc/sysupgrade.conf`, or a firmware upgrade will
wipe the installation.

To build a separate VPN Wi-Fi over the daemon (Wi-Fi → bridge → tun → fail-closed
firewall) — [openwrt-vpn-ssid.md](openwrt-vpn-ssid.md).

### 8.4. What Linux does not have

- Automatic installation — by principle, not by omission (see above): the
  recipe is printed, the operator runs it.
- Self-update — updating the binary means "deliver the file + restart the
  service".

## 9. Pairing a client (the same on every OS)

1. On the daemon's host: `sing-box lxd client add --name <label>` (with sudo on
   macOS when the service is system-scope). It prints a one-time invite
   `address#fingerprint#code`.
2. Paste the invite into the launcher: it pins the server by the fingerprint,
   registers with the code (`POST /admin/enroll`), and is trusted by its
   certificate from then on. The code burns.
3. Inspect/revoke: `client list`, `client remove <name-or-fingerprint>`.

**Pairing gotchas over the network** (proven on a router — three failed tries):

- **Operator commands (`client add/list/remove`) run on loopback only.** Over
  the network the daemon answers `403 operator routes are loopback-only`:
  minting an invite is granting trust, so that route is closed outward. If
  `listen` points at a **single** LAN address, loopback is not listened on at
  all and the command runs from nowhere. Fix: the object form of listen with
  both addresses (`{"address": ["192.168.10.1", "127.0.0.1"], "port": 19091}`,
  see 3.1); you do not need `0.0.0.0` just for this, and it is unsafe.
- **The code lives in the process memory, not the state dir.** Any daemon
  restart between `client add` and entering the code kills enrollment: the
  client gets `enroll: no active enrollment code`. Strict order: set listen →
  restart the service → **mint the code** → enter it in the launcher → and only
  then change anything else.
- **The invite's address comes from `listen`.** When listen is loopback or the
  object form, replace the address in the launcher with a reachable one
  (`192.168.10.1:19091`); leave fingerprint and code as-is. On success the trust
  lands in `clients.json` and survives a daemon restart and a host reboot.

## 10. Admin REST (reference)

| Route | What it does |
|---|---|
| `POST /admin/apply` | body = config; 200 applied / 422 invalid / 500 failure (+`rolled_back`) |
| `POST /admin/rollback` | roll back to last-good (404 — nothing recorded) |
| `POST /admin/start` · `POST /admin/stop` | core lifecycle apart from the config (stop is remembered) |
| `GET /admin/config` | the active config |
| `GET /admin/status` | `idle\|started\|fatal`, active/last-good sha, `last_error`, `interrupted_apply` |
| `GET /admin/info` | identity card: version, state_dir, listen, tls, fingerprint, pid, uptime, log_path |
| `POST /admin/enroll` | client registration with a one-time code |
| `GET /admin/resources` · `PUT`/`GET`/`DELETE /admin/resources/{name}` | the files a config REFERS to (`.srs`, geo bases): list with sha256, upload, fetch, remove; 409 while the active or last-good config references the name |
| `GET /admin/memory` | process memory: heap/stack/sys, goroutines, GC, and **two** RSS numbers — `rss_current_bytes` and `rss_peak_bytes` (raw bytes; the peak never decreases, so it is reported apart from the current size) |
| `GET /admin/stats` | the **core's** uptime, `uplink_total`/`downlink_total`, active connections — all `null` (status still 200) when no core is up |
| `GET /admin/logs?tail=N` | tail of `lxd.log` — the DAEMON's own log, which the gRPC log stream does not carry (that one carries the core's) |
| `GET /admin/pprof` | profiles with counts; `block`/`mutex` report `enabled:false` until a rate is set |
| `GET /admin/pprof/{name}` | `heap`, `allocs`, `goroutine`, `threadcreate`, `block`, `mutex` — served instantly from what the runtime already collects; `?debug=2` on `goroutine` is a stack dump |
| `GET /admin/pprof/profile?seconds=N` | CPU profile — this one RECORDS for N seconds (default 30, max 120); a second concurrent request gets 409 |
| `GET /admin/pprof/trace?seconds=N` | runtime trace, same recording rules |
| `POST /admin/pprof/block?rate=N` · `POST /admin/pprof/mutex?fraction=N` | turn the two lock profiles on/off (`0` = off); not persisted, gone after a restart |
| `GET /admin/clients-info` | IP → device directory: `name`, `mac`, `ssid`, `iface`, `port`, `source` for every client the host knows about; answers with no core up |
| `PUT`/`DELETE /admin/clients-info/labels/{key}` | operator's own name for a client; key is an IP or a MAC (400 otherwise) |
| `GET /admin/host` | the MACHINE the daemon runs on: CPU (per-core), memory, thermal zones, disks, file descriptors — as opposed to `/admin/memory`, which is the process |
| `GET /admin/host/interfaces` | every network interface with raw counters and derived rates |

SIGHUP to the daemon = re-read the config file (`--config-force`/`-c`) and
apply it through the same validated, rollback-protected apply pipeline.

## 10a. Naming the devices on the LAN

A connection inspector showing `192.168.20.238:50558` is readable for one
device and useless for fifteen. `GET /admin/clients-info` is the lookup table
that turns those addresses into devices — a **directory, not a per-connection
field**: names change on a scale of hours, so a client fetches the map once a
minute and joins it against connection source addresses itself. The connection
stream is untouched.

```bash
curl -s .../admin/clients-info
```

```json
{
  "clients": {
    "192.168.20.238": {"name": "iPhone-Vasya", "mac": "be:ab:bd:ec:70:40",
                       "ssid": "LexVPN2G", "iface": "phy0-ap1", "port": "",
                       "source": "lease+arp+wireless"},
    "192.168.20.51":  {"name": "NAS", "mac": "dc:a6:32:de:ad:be", "ssid": "",
                       "iface": "br-lan", "port": "lan2",
                       "source": "lease+arp+bridge"}
  },
  "sources": ["lease", "arp", "bridge", "wireless"],
  "updated_unix": 1755087234
}
```

Five providers fill the map, later ones refining earlier ones:

| Provider | Fills | From |
|---|---|---|
| `lease` | `name`, `mac` | DHCP leases (`/tmp/dhcp.leases` and the usual distro paths) |
| `arp` | `mac`, `iface` | `/proc/net/arp` — also covers clients with a static IP, which no lease knows |
| `bridge` | `port` | `bridge fdb show` — which socket a wired client is plugged into |
| `wireless` | `ssid`, `iface` | `ubus call hostapd.*` on OpenWrt; refines `br-lan` to the actual AP |
| `label` | `name` | your own labels, final |

`source` is part of every entry on purpose: when a device loses its name, the
question is always which provider went quiet — `"source": "label"` alone says
the DHCP lease expired. An **empty field is a state, not an error**: a wired
client has no `ssid`, and off Linux the last three providers report nothing at
all (the endpoint still answers, with leases and labels).

`processInfo` cannot do this job: it looks a process up in the LOCAL socket
table, and a LAN client's connection was opened by a different host entirely.

Labels are yours and survive restarts (`<state_dir>/client-labels.json`):

```bash
curl -X PUT -d '{"name":"Living room TV"}' .../admin/clients-info/labels/192.168.20.77
curl -X PUT -d '{"name":"Work laptop"}'   .../admin/clients-info/labels/be:ab:bd:ec:70:40
curl -X DELETE .../admin/clients-info/labels/192.168.20.77
```

A MAC label follows the device across addresses — but modern phones randomize
their MAC per network and change it on reconnect, so for those an **IP label
plus a DHCP reservation is the stabler pairing**.

The map is cached for 60 seconds; a label write takes effect immediately.
Leases are looked for in the usual places, overridable in `daemon.json`:

```json
{"dhcp_lease_files": ["/etc/custom/leases"]}
```

## 10b. Host telemetry

`/admin/memory` describes the daemon **process**. When the router itself starts
struggling that is not enough — `GET /admin/host` describes the **machine**:

```bash
curl -s .../admin/host
curl -s .../admin/host/interfaces
```

What it answers:

| Section | Tells you |
|---|---|
| `cpu` | `usage_percent` plus `per_core_percent` — one pinned core among three idle is a diagnosis the average hides. `load_1/5/15` come free from the kernel |
| `memory` | `used_percent` is computed from `available_bytes`, not `free_bytes`: a router keeps most of its RAM in page cache, and a free-based figure screams "full" with 120 MB actually free |
| `thermal` | every sensor as `zones[]`, plus `max_celsius` for a single indicator. `null` when the machine has no sensors |
| `disk` | `mounts[]` with `read_only` and `holds_state_dir`. `max_used_percent` **ignores read-only filesystems** — OpenWrt's squashfs root is permanently 100% full, and an always-red indicator is one nobody reads |
| `fd` | the daemon's open descriptors and limit, plus the system's. Hitting either stops new connections with a symptom that looks like nothing at all |

**Percentages need two samples.** `usage_percent` is a delta between two reads
of `/proc/stat`, so the first request after startup reports `null` with
`interval_seconds: 0` — a zero would read as "idle", which is a different
statement. `interval_seconds` tells you the window each percentage describes:
12.4% over five seconds and over an hour mean different things.

**Counters and rates both.** Interfaces report raw `rx_bytes` alongside
`rx_bytes_per_second`: a counter survives restarts and gaps, a rate is
convenient but lies across them. Graph the counter, read the rate.

Every interface is listed, `lo` and down ones included — "wan went down" is
exactly what you want to see. Filtering is the UI's job.

**Read the platform from `os_family`** (`linux`/`darwin`/`windows`), not from
the `os` string, which is human-facing and formatted by the distribution.
`os_family` is what tells "this platform never has that field" apart from "the
source went quiet".

**Off Linux the shape stays the same and unavailable fields are `null`** — on
macOS that means no thermals (they need CGO) and no CPU percentages (Mach API),
while memory, disks, descriptors and load averages all work. A client checks
for `null` rather than branching on `os`.

## 11. Diagnosing a misbehaving daemon

Everything below rides the normal control port behind the normal client
certificate — there is no second, unauthenticated debug port to open. (The
core's own `experimental.debug.listen` does open one, with no authentication
whatsoever; do not use it on a server reachable from the network.)

**Is anything wrong at all** — cheap enough to poll on a schedule:

```bash
curl -s .../admin/memory   # goroutines and heap over time tell you about leaks
curl -s .../admin/stats    # core uptime, traffic totals, live connections
```

A steadily climbing `goroutines` or `heap_inuse_bytes` is the signature of a
leak. Watch `rss_current_bytes` rather than `rss_peak_bytes`: the peak is a
high-water mark and never comes back down.

**The daemon is wedged** — the fastest answer, and it is instant:

```bash
curl -s ".../admin/pprof/goroutine?debug=2" > stacks.txt
```

Every goroutine with its stack, exactly like a panic dump: whatever is stuck is
visible in it.

**The daemon is eating CPU** — this one records, so it takes as long as you ask:

```bash
curl -s ".../admin/pprof/profile?seconds=30" > cpu.pb.gz
go tool pprof -top sing-box cpu.pb.gz
```

**Memory is growing** — fetch a heap profile twice, an hour apart, and compare:

```bash
curl -s .../admin/pprof/heap > heap-1.pb.gz
# … later …
curl -s .../admin/pprof/heap > heap-2.pb.gz
go tool pprof -base heap-1.pb.gz sing-box heap-2.pb.gz
```

Note that `heap`, `allocs`, `goroutine` and `threadcreate` are **snapshots**:
the runtime keeps them continuously, so a GET returns in milliseconds and
already covers the whole life of the process. Nothing has to be switched on in
advance, and there is no background cost to leaving these routes available.

**Suspected lock contention** — the one case that must be enabled first, since
its accounting is not free:

```bash
curl -s -X POST ".../admin/pprof/mutex?fraction=100"
# … let it run under load …
curl -s .../admin/pprof/mutex > mutex.pb.gz
curl -s -X POST ".../admin/pprof/mutex?fraction=0"     # turn it back off
```

Use the binary that is actually running as the pprof argument — symbolization
happens on your machine, which is why the daemon does not serve `/symbol`.
