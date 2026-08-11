# The lxd daemon: what it is and how to set it up

Operator's guide: what `sing-box lxd` is, why it exists, how it is installed on
macOS, and the setup approaches on Linux. Русская версия —
[lxd-daemon-ru.md](lxd-daemon-ru.md). Current feature state lives in
[FEATURE 014-LXD_DAEMON](../SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md); design
details — SPECS/TASKS [055](../SPECS/TASKS/055-LXD_DAEMON_SKELETON/SPEC.md),
[056](../SPECS/TASKS/056-LXD_APPLY_ROLLBACK/SPEC.md),
[057](../SPECS/TASKS/057-LXD_MTLS_SERVICE/SPEC.md).

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
| `listen` | `127.0.0.1:9091` | channel address (both planes); use a LAN address to reach the daemon from another machine |
| `tls` | `false` | mTLS with client enrollment; `false` = plain h2c, loopback/dev only |
| `secret` | empty | Bearer secret for the operator routes; the only gate when `tls: false` (empty = no authentication) |
| `log_max_size_mb` | `20` | log rotation: safety size ceiling |
| `log_max_backups` | `1` | how many rotated generations (`lxd.log.1…N`) to keep |
| `log_max_age_hours` | `24` | rotation by file age |

The rotation defaults give "about a day of history"; 0/absent key = default,
and there is deliberately no "unlimited" setting. Changing any setting is a
file edit + service restart, never a reinstall.

## 4. Command-line keys

| Key | Meaning |
|---|---|
| `--state-dir <dir>` | the daemon's home: daemon.json, last-good, run-state, client registry, keys (default `lxd-state`) |
| `-c <file>` | seed config (exactly one file; `-C` directories are not supported) |
| `--config-force <file>` | always boot from this file, overriding last-good |
| `--run` | bring the core up regardless of the recorded run state |
| `--service install\|install-user\|uninstall\|print` | service installation (see the OS sections) |
| `--purge` | with `uninstall` — also delete the state directory |
| `client add [--name <label>]` | mint a one-time invite for a new client |
| `client list` / `client remove <name-or-fingerprint>` | list / revoke trusted clients |

The subcommand exists only in builds with the `with_lx_command` tag.

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
sing-box lxd --service=print              # dry run: show the plist, touch nothing
sing-box lxd --service=uninstall          # remove the service; state is kept
sing-box lxd --service=uninstall --purge  # remove the service AND the state (clients, keys, last-good)
sudo sing-box lxd client add --name mac-book   # a fresh invite on a live daemon (state-dir is found automatically)
```

## 8. Linux — setup approaches

`--service` on Linux is still a **stub** — there is no automatic installation
(an "instructions mode" is under discussion: detect the init system and print a
ready-made unit/init script; see "Deferred" in
[SPEC 057](../SPECS/TASKS/057-LXD_MTLS_SERVICE/SPEC.md)). The daemon itself is
fully functional on Linux: mTLS, apply/rollback, log rotation all work; the
`GOOS=linux GOARCH=arm64` cross-build (static binary, musl-compatible) is
verified. Installation is three files by hand.

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

To manage the daemon from another machine set `listen` to a LAN address (e.g.
`192.168.10.1:19091`); mTLS is mandatory, and do not open the port outward in
the firewall without need. Operator commands (`client add`) still run only on
the host itself.

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
- The binary (~50 MB) goes on extroot only — it will not fit the built-in flash.

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

### 8.4. What Linux does not have yet

- `--service=install/uninstall/print` — stubs (an instructions mode is planned);
- auto-discovery of an installed service's state-dir for `client …` — pass
  `--state-dir` explicitly;
- self-update — updating the binary means "deliver the file + restart the
  service".

## 9. Pairing a client (the same on every OS)

1. On the daemon's host: `sing-box lxd client add --name <label>` (with sudo on
   macOS when the service is system-scope). It prints a one-time invite
   `address#fingerprint#code`.
2. Paste the invite into the launcher: it pins the server by the fingerprint,
   registers with the code (`POST /admin/enroll`), and is trusted by its
   certificate from then on. The code burns.
3. Inspect/revoke: `client list`, `client remove <name-or-fingerprint>`.

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

SIGHUP to the daemon = re-read the config file (`--config-force`/`-c`) and
apply it through the same validated, rollback-protected apply pipeline.
