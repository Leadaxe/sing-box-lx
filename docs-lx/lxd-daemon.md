# The lxd daemon: what it is and how to set it up

> 🌐 Русская версия: **[lxd-daemon.ru.md](lxd-daemon.ru.md)**.

Operator's guide: what `sing-box lxd` is, why it exists, how it is installed on
macOS and Windows, and the setup approaches on Linux.

## Table of contents

- [1. What it is and why](#1-what-it-is-and-why)
- [2. Quick start (dev, no installation)](#2-quick-start-dev-no-installation)
- [3. daemon.json — the daemon's settings](#3-daemonjson--the-daemons-settings)
  - [3.1. listen: one address or several](#31-listen-one-address-or-several)
- [4. Command-line keys](#4-command-line-keys)
- [5. Security: who authenticates with what](#5-security-who-authenticates-with-what)
- [6. Logs](#6-logs)
- [7. macOS — automatic installation](#7-macos--automatic-installation)
  - [7.1. The root-owned copy of the binary](#71-the-root-owned-copy-of-the-binary)
- [7a. Windows — automatic installation](#7a-windows--automatic-installation)
  - [7a.1. The protected copy of the binary set](#7a1-the-protected-copy-of-the-binary-set)
  - [7a.2. Checking by hand](#7a2-checking-by-hand)
- [8. Linux — setup approaches](#8-linux--setup-approaches)
  - [8.1. Common part (any init)](#81-common-part-any-init)
  - [8.2. systemd (a regular server/desktop)](#82-systemd-a-regular-serverdesktop)
  - [8.3. OpenWrt / procd (routers)](#83-openwrt--procd-routers)
  - [8.4. What Linux does not have](#84-what-linux-does-not-have)
- [9. Pairing a client (the same on every OS)](#9-pairing-a-client-the-same-on-every-os)
  - [9.1 Enrollment on the wire — building your own client](#91-enrollment-on-the-wire--building-your-own-client)
- [10. Admin REST (reference)](#10-admin-rest-reference)
- [10a. Naming the devices on the LAN](#10a-naming-the-devices-on-the-lan)
- [10b. Host telemetry](#10b-host-telemetry)
- [11. Diagnosing a misbehaving daemon](#11-diagnosing-a-misbehaving-daemon)

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
and Windows or by the operator's editor).

| Key | Default | Meaning |
|---|---|---|
| `listen` | `127.0.0.1:9091` | channel address (both planes); a `"host:port"` string, or `{"address": [...], "port": N}` to bind several addresses — see below |
| `tls` | `false` | mTLS with client enrollment; `false` = plain h2c, loopback/dev only |
| `secret` | empty | Bearer secret for the operator routes; the only gate when `tls: false` (empty = no authentication) |
| `log_file` | `<parent of state-dir>/lxd.log` | rotated-log path override (absolute; e.g. `/tmp/lxd.log` for tmpfs on a router; on Windows install writes `<ProgramData>\sing-box-lxd\logs\lxd.log` when the key is absent) — `/admin/info` advertises the actual path |
| `log_max_size_mb` | `1` | log rotation: safety size ceiling |
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
| `--service install\|install-user\|copy\|uninstall\|status` | service installation, a protected copy without a service, removal, a status report (see the OS sections, [7.1](#71-the-root-owned-copy-of-the-binary) and [7a](#7a-windows--automatic-installation)); on Windows `install`/`copy`/`uninstall` need an elevated token and `install-user` is refused |
| `--exec-dir <dir>` | with `install`/`copy`/`uninstall`/`status` — directory of the protected copy and its sidecar. macOS: `sing-box-lxd`, default `/Library/PrivilegedHelperTools`, which must exist; a directory given here is created. Windows: `sing-box-lxd.exe` (with `libcronet.dll` when it lies beside the source), default `<ProgramFiles>\sing-box-lxd`, created if missing |
| `--allow-unsafe-exec` | debug only: let the service (root launchd job, Windows SCM service) start from a binary that is not a protected copy (WARN instead of a refusal) |
| `--purge` | with `uninstall` — also delete the state directory (Windows: all of `<ProgramData>\sing-box-lxd`, state and logs) |
| `--keep-copy` | with `uninstall` — remove the service but keep the protected copy for non-service use ([7.1](#71-the-root-owned-copy-of-the-binary), [7a](#7a-windows--automatic-installation)) |
| `--dry-run` | with `--service` (except `status`) — show what would be done, change nothing |
| `--invite-out <file>` | with `--service=install` (macOS, Windows) — write the pairing invite into this new file instead of stdout; an existing file refuses the command, a failed mint exits 1 ([9](#9-pairing-a-client-the-same-on-every-os)) |
| `--invite-name <name>` | with `--service=install` — the name of the client the invite pairs (default `singbox-launcher` with `--invite-out`, no name without it) |
| `client add [--name <label>] [--invite-out <file>]` | mint a one-time invite for a new client; `--invite-out` writes it into a new file instead of printing it |
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
Implemented on macOS, Linux and Windows.

**Windows.** The service's log is `<ProgramData>\sing-box-lxd\logs\lxd.log` (install
writes that `log_file` into daemon.json), beside the launcher's `classic.log`. A live file
cannot be renamed there (Go opens files without `FILE_SHARE_DELETE`), and a writer holding
it would stay on the old file, so the daemon keeps one handle for its whole life: the
process's standard output and error handles point at it, runtime panics included, and
rotation copies the content to `lxd.log.1` and truncates `lxd.log`. Lines written between
the copy and the truncation are lost. The file cannot be deleted while the daemon runs.
`/admin/logs` reads `lxd.log` and `lxd.log.1` as on the other platforms.

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

macOS and Windows ([7a](#7a-windows--automatic-installation)) have a full `--service`; this
section is macOS. Two scopes:

```bash
sudo sing-box lxd --service=install    # system LaunchDaemon: root, starts before login, TUN
sing-box lxd --service=install-user    # LaunchAgent: no sudo, starts at login, desktop UX
```

`install-user` runs as the logged-in user and therefore **cannot own TUN**:
creating the interface on macOS needs root, so a config with a `tun` inbound
fails with a permission error under the user scope. This has nothing to do with
`tls` in daemon.json — for TUN install the system scope
(`sudo … --service=install`).

Install does everything itself:

1. system scope: copies the binary to
   `/Library/PrivilegedHelperTools/sing-box-lxd`, root-owned, and
   the service runs that copy, not the file you installed from
   ([7.1](#71-the-root-owned-copy-of-the-binary));
2. creates `…/Application Support/sing-box-lxd/` (0700; system scope — `root:wheel`)
   with `state/` inside;
3. **materializes daemon.json**: an existing address is kept (a reinstall never
   moves the channel out from under enrolled clients), otherwise the first free
   loopback port from 19091 up; `tls` — always; the secret — kept or generated;
4. writes the plist (`com.leadaxe.sing-box-lxd`) and bootstraps the service — after a bootout it waits for the old job to disappear (up to 10 s, `waiting for the old service to unload (Ns)`) and retries a bootstrap that answers "already in progress", because a live core takes seconds to exit;
   the plist degenerates to `sing-box lxd --state-dir <dir>` — every setting
   lives in daemon.json;
5. prints the status report ([7.1](#71-the-root-owned-copy-of-the-binary)) and the
   summary: channel address, admin secret, daemon.json path, the restart command —
   and a **one-time invite** to pair the launcher (or, with `--invite-out`, the invite
   goes into a file — [9](#9-pairing-a-client-the-same-on-every-os)).

Paths: system — `/Library/Application Support/sing-box-lxd/`, user —
`~/Library/Application Support/sing-box-lxd/`. The log is `lxd.log` beside
`state/`.

Other actions:

```bash
sing-box lxd --service=install --dry-run  # show the copy plan, the plist and what would happen, touch nothing
sing-box lxd --service=status             # what is installed; exit 0/2/3/4 (see 7.1), no root needed
sudo sing-box lxd --service=copy          # only the root-owned copy, no service (7.1)
sing-box lxd --service=uninstall          # remove the service; state is kept
sing-box lxd --service=uninstall --purge  # remove the service AND the state (clients, keys, last-good)
sing-box lxd --service=uninstall --dry-run --purge   # show what would be removed
sudo sing-box lxd client add --name mac-book   # a fresh invite on a live daemon (state-dir is found automatically)
```

### 7.1. The root-owned copy of the binary

A LaunchDaemon runs as root at every boot and every KeepAlive restart. If its plist
pointed at the binary inside the launcher bundle — a file the logged-in user can
replace — any process of that user could get code run as root. So the system service
never runs the file it was installed from; `--service=install` copies it first:

| What | Path | Owner / mode |
|---|---|---|
| binary | `/Library/PrivilegedHelperTools/sing-box-lxd` | `root:wheel 0755` |
| sidecar | `/Library/PrivilegedHelperTools/sing-box-lxd.install.json` | `root:wheel 0644` |

Apple's convention for privileged helpers: one flat file, no directory of its own. It is
named `sing-box-lxd`, not by the label: macOS truncates a process name to 16 characters,
and `sing-box-lxd` fits whole and contains `sing-box`, so `pgrep sing-box`, `pkill` and
`ps -c` find the daemon without `-f`. The service label, the plist and
`XPC_SERVICE_NAME` stay `com.leadaxe.sing-box-lxd`.
`/Library/PrivilegedHelperTools` ships with macOS; install never creates it, and a
missing one is an error with the remedy.

- **The invariant.** Every path component from `/` down to the binary is a real
  directory or file (not a symlink), owned by uid 0, with no write bit for group or
  other. `/Applications` itself is `root:admin 0775` — that is why a bundle binary fails
  it.
- **How the copy lands.** A temporary file with a unique name in the same directory,
  fsync, `chown root:wheel`, `chmod 0755`, sha256 compared with the source, then
  `rename` over the old copy. Never rewritten in place (a running daemon keeps the old
  file, and macOS kills a process whose signed pages change), no xattrs, no re-signing.
  An identical copy is left alone: `lxd: binary unchanged (sha256 …), copy skipped`.
- **The plist** changes only in `ProgramArguments[0]`; daemon.json, the address, the
  secret and the enrolled clients stay as they were.
- **The sidecar** `sing-box-lxd.install.json` is readable without root:
  ```json
  {
    "source": "/Applications/singbox-launcher.app/Contents/MacOS/bin/sing-box",
    "sha256": "…",
    "version": "1.14.1-lx.11",
    "installed_at": "2026-09-24T12:00:00Z",
    "plist_path": "/Library/LaunchDaemons/com.leadaxe.sing-box-lxd.plist",
    "label": "com.leadaxe.sing-box-lxd"
  }
  ```
  `version` is the copy's core version, readable without running it; `plist_path` is
  empty for a copy without a service.
- **`--exec-dir <dir>`** puts both files, under the same names, into another directory;
  a missing one is created `root:wheel 0755`. The same invariant covers every component
  of `<dir>` and the file itself; otherwise install refuses:
  `<path>: owned by uid N, mode NNNN, must be root-owned and not group/world-writable`.
- **A directory where the file belongs** stops install and copy:
  `target is a directory (legacy layout); remove it: sudo rm -rf <path>`. Nothing deletes
  it automatically; status reports it (exit 2), uninstall leaves it with the same hint.
- **Files of earlier builds are not this core's.** `v1.14.1-lx.11` named the copy by the
  label (`/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd` and its
  `.install.json`); early pre-releases left a directory of that name. The current core
  never reads, rewrites or deletes them; a plist running such a file shows as `MISMATCH`
  (exit 2) until `sudo sing-box lxd --service=install` moves the service to
  `sing-box-lxd`. Remove the leftovers by hand:
  `sudo rm -rf /Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd*`.

**A copy without a service.** `sudo sing-box lxd --service=copy` lays down the same copy
and sidecar and nothing else — no plist, no launchd. It serves a launcher that runs the
core as root by itself (classic TUN mode). Repeating it with the same binary changes
nothing (`lxd: already up to date <sha256>`); a later `--service=install` binds this copy
to the plist without copying again. Updating the core means running `--service=copy`
(and `--service=install`, if the service exists) from the new binary — the core does not
watch for updates.

**Status** needs no root and changes nothing:

```bash
sing-box lxd --service=status; echo "exit $?"
```

For the LaunchDaemon (or, without one, the copy) and for a user agent it prints the
plist, the program it runs, owner and mode, whether the invariant holds, the program's
sha256 against this binary's, the sidecar, and `launchctl print`'s state and pid. The
last line is the verdict:

| Verdict | Meaning | Exit |
|---|---|---|
| `OK` | the service runs a root-owned copy of exactly this binary | 0 |
| `MISMATCH` | reinstall needed: another binary, a missing or foreign sidecar, a copy changed behind its sidecar, a sidecar without its copy | 2 |
| `UNSAFE` | the program, or a directory above it, fails the invariant | 2 |
| `NOT INSTALLED` | neither a plist nor a copy | 3 |
| `COPY ONLY` | a good copy of this binary, no service | 4 |
| `NOT RUNNING` | installed and consistent on disk, but launchd has no running job for the label (a failed bootstrap, a bootout without bootstrap); the reason carries the `launchctl bootstrap` command | 5 |

**Uninstall** removes the copy and its sidecar only when the sidecar belongs to this
service (or to no plist) and the file's sha256 still equals the sidecar's; otherwise the
file stays and the reason is printed (`lxd: copy left in place: …`). It never deletes an
arbitrary `ProgramArguments[0]`.

`sudo sing-box lxd --service=uninstall --keep-copy` removes the plist and the launchd job
but keeps the copy and its sidecar, whose `plist_path` is cleared — the copy-only state
(status `COPY ONLY`, exit 4):
`lxd: copy kept for non-service use: <path>; remove with --service=uninstall without --keep-copy`.
`--purge` still concerns only the state. With no service installed and a copy already
unbound it changes nothing, prints the same line and exits 0.

**Self-check at start.** A core started as root (`lxd` or `run`) checks its own binary
against the invariant. The launchd job — parent pid 1 and
`XPC_SERVICE_NAME=com.leadaxe.sing-box-lxd` — refuses to start and says why in
`lxd.log`:

```
lxd: refusing to run as a root service from /Applications/…/sing-box (uid 501, mode 0755): /Applications: owned by uid 0, mode 0775, must be root-owned and not group/world-writable; run `sing-box lxd --service=install` to reinstall from a root-owned copy
```

Any other root run — sudo from a terminal, nohup, a launcher elevating the core — only
logs a WARN with the same path, owner and mode. `lxd --allow-unsafe-exec` turns the
refusal into a WARN for debugging. A passed check in the launchd job logs one INFO line:
`lxd: self-check ok: root-owned /Library/PrivilegedHelperTools/sing-box-lxd, launchd service com.leadaxe.sing-box-lxd`.

> ⚠️ **Upgrading a system install made by an older core.** Its plist runs the bundle
> binary. Once that binary is updated to this version, the next restart of the service
> refuses to start (above). Reinstall once: `sudo sing-box lxd --service=install` —
> daemon.json, clients and keys are kept.

**Checking by hand:**

```bash
ls -ld / /Library /Library/PrivilegedHelperTools
ls -l /Library/PrivilegedHelperTools/sing-box-lxd*                         # root wheel -rwxr-xr-x the binary, -rw-r--r-- .install.json
plutil -p /Library/LaunchDaemons/com.leadaxe.sing-box-lxd.plist            # ProgramArguments[0] = the copy
shasum -a 256 /Library/PrivilegedHelperTools/sing-box-lxd
cat /Library/PrivilegedHelperTools/sing-box-lxd.install.json               # the same sha256
pgrep -l sing-box                                                          # the daemon shows as sing-box-lxd
launchctl print system/com.leadaxe.sing-box-lxd | grep -E '^[[:space:]](state|pid|program) ='
sing-box lxd --service=status
```

## 7a. Windows — automatic installation

On Windows `--service` installs a service of the Service Control Manager (SCM) named
`sing-box-lxd`: account `LocalSystem`, automatic start at boot (before anyone logs on),
dependency on `Tcpip`. The model is the macOS one ([7.1](#71-the-root-owned-copy-of-the-binary)):
the service never runs the file it was installed from, only a protected copy.

```powershell
# PowerShell "Run as administrator" for everything but status and --dry-run
sing-box lxd --service=install                  # the service, the protected copy, daemon.json, an invite
sing-box lxd --service=install --dry-run        # the plan: data dir, copy, BinaryPathName; touches nothing
sing-box lxd --service=status                   # no elevation; exit 0/2/3/4/5, 1 on an error (see below)
sing-box lxd --service=copy                     # the protected copy only, the SCM untouched (7a.1)
sing-box lxd --service=uninstall                # remove the service and its copy; state and logs are kept
sing-box lxd --service=uninstall --keep-copy    # remove the service, keep the copy (status COPY ONLY)
sing-box lxd --service=uninstall --purge        # also delete <ProgramData>\sing-box-lxd (state and logs)
sing-box lxd --service=install --invite-out C:\Users\me\invite.txt   # the invite into a new file (9)
sing-box lxd client add --name office-pc        # a fresh invite on a live daemon (state dir is found automatically)
```

`install`, `copy` and `uninstall` need an elevated token; without it they refuse before
touching anything (`--service=install needs an elevated token (run as administrator)`).
`status` and `--dry-run` work from a regular prompt. `--service=install-user` does not
exist here: `--service=install-user is not supported on Windows; use --service=install`.
`--exec-dir <dir>` moves the copy elsewhere, as on macOS; `status` and `uninstall` of such
an install need the same flag.

Paths (the roots come from the Known Folders `ProgramFiles` and `ProgramData`; `C:\` is
only the usual value):

| What | Path | Owner / DACL |
|---|---|---|
| copy directory | `<ProgramFiles>\sing-box-lxd\` | Administrators; `D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FRFX;;;AU)` |
| binary | `…\sing-box-lxd\sing-box-lxd.exe` | Administrators; `D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FRFX;;;AU)` |
| library | `…\sing-box-lxd\libcronet.dll` — only if it lies beside the source binary | as the binary |
| sidecar | `…\sing-box-lxd\sing-box-lxd.install.json` | as the binary |
| data directory | `<ProgramData>\sing-box-lxd\` | Administrators; `D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)` |
| state | `<ProgramData>\sing-box-lxd\state\` — daemon.json, TLS pair, clients, `resources`, `tailscale` | SYSTEM or Administrators, SYSTEM and Administrators only |
| logs | `<ProgramData>\sing-box-lxd\logs\` — `lxd.log`, `lxd.log.1`; the launcher writes its `classic.log` there | as state |

Authenticated Users may read and execute the copy (the launcher hashes it and reads the
sidecar without elevation) but not write it. The data directory is closed to everyone
except SYSTEM and Administrators. `wintun.dll` is not part of the copy: `sing-tun` embeds
it and loads it from memory.

What `--service=install` does, in order:

1. Checks the token; with `--invite-out`, an existing file refuses the whole command
   before any change.
2. **Takes the data directory over** before anything is written into it — daemon.json
   carries the secret, and a file created under a foreign owner would inherit a foreign
   DACL. With `SeTakeOwnershipPrivilege` and `SeRestorePrivilege` enabled it walks
   `<ProgramData>\sing-box-lxd` top-down, every node through a handle opened without
   following links: the root gets owner Administrators and the protected DACL above; a node
   below it that is out of the norm gets owner Administrators and an explicit protected DACL
   of its own (directories — inheritable, files — `D:P(A;;FA;;;SY)(A;;FA;;;BA)`). Files the
   daemon created as SYSTEM with the inherited entries are in the norm and stay as they
   are. One line per node that changed — `lxd: took ownership of <path> (was <name>
   (<SID>))`, `lxd: replaced DACL on <path>` — or a single `lxd: data dir <path> is
   protected`. `state\` and `logs\` are created if missing. The launcher's `classic.log`
   gets the same DACL; the launcher restores its own read access when it next starts
   elevated. A reparse point or a file with more than one hard link inside refuses
   (`<path> is a reparse point; remove it: Remove-Item <path>`): changing an ACL through a
   link would change someone else's object. Install never deletes them itself.
3. **daemon.json**, as on macOS: an existing address is kept, otherwise the first free
   loopback port from 19091; `tls: true`; the secret is kept or generated. If the file has
   no `log_file`, install writes `<ProgramData>\sing-box-lxd\logs\lxd.log`.
4. A running service is stopped, waiting up to 30 s for `STOPPED`
   (`lxd: stopping service sing-box-lxd (Ns)`); if it does not stop, install fails and
   nothing is changed.
5. The binary set and the sidecar go into the copy directory ([7a.1](#7a1-the-protected-copy-of-the-binary-set)).
6. The service is created or updated: `BinaryPathName` is the quoted copy path plus
   `lxd --state-dir <abs>` (and `-c`/`--config-force`/`--run` if given, as on macOS);
   `LocalSystem`, automatic start, dependency `Tcpip`; recovery — restart three times
   after 5 s, counter reset after 86400 s, also when the service stops with a non-zero exit
   code. The service DACL is `D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x2008d;;;AU)`: Authenticated
   Users may read the configuration and the status, but not start, stop, reconfigure or
   take the service over.
7. Start, waiting up to 30 s for `RUNNING`. A service that stops while starting is an
   error with its exit codes; the reason is in `lxd.log`.
8. The status report; a verdict other than `OK` makes install fail (exit 1).
9. The summary: channel address, admin secret, daemon.json path,
   `restart command: Restart-Service sing-box-lxd (PowerShell as administrator)` and a
   one-time invite — printed, or written to the `--invite-out` file.

If the copy or the service configuration fails after the stop in step 4, install puts the
previous image back, starts the service on it and only then returns the error
(`lxd: install failed, previous service image restarted`): a failed update does not leave
the host without its VPN. A repeated install from the same binary leaves the files alone
(`lxd: binary set unchanged (…), copy skipped`), keeps daemon.json, the secret and the
clients, and restarts the service — a short VPN blink.

`Restart-Service` rather than `sc.exe stop` + `sc.exe start`: `sc.exe stop` does not wait,
and `start` fails with error 1056 while the service is still `STOP_PENDING`. An install
right after an uninstall can hit `ERROR_SERVICE_MARKED_FOR_DELETE` (1072) while the
Services console (`services.msc`) is open; close it, or reboot, and install again.

If install took ownership from an account outside SYSTEM, Administrators and
TrustedInstaller, or such an account could read `state\`, and a daemon.json was already
there, the secret and the server key pair are not regenerated (the launcher keeps the
secret, and a new pair would unpair every client). Install prints
`WARN: <path> was readable by <name> (<SID>) before this install; rotate the admin secret
in daemon.json and re-pair clients if the host is shared` and records the same text in the
sidecar (`warnings`), because a launcher running install through `runas` has no console to
show it.

**Status** needs no elevation and changes nothing. It opens the SCM with
`SC_MANAGER_CONNECT` and the service with query rights only, and prints three blocks —
`[service]` (name, `BinaryPathName` and its argv[0], start type, account, state, pid, whether
the service DACL is protected), `[copy]` (directory, the invariant of the chain, owner and
sha256 of each file against the caller's set, extra files, leftovers, the sidecar with its
warnings) and `[data dir]` (owner and whether the DACL is protected; without administrator
rights it is usually unreadable, which is expected — informational only). The last line is
the verdict:

| Verdict | Meaning | Exit |
|---|---|---|
| `OK` | the service runs the canonical copy, the service DACL and the invariant hold, the sidecar is bound to `sing-box-lxd`, files = sidecar = the calling binary's set, no extra files, state `RUNNING` | 0 |
| `MISMATCH` | the invariant holds, but: another set than the caller's (including `libcronet.dll` present on one side only); no sidecar or a foreign one; a file differs from the sidecar; a sidecar names a missing file; the sidecar is bound to a service that does not exist; an extra file in the copy directory | 2 |
| `UNSAFE` | the service configuration cannot be read (access denied included); `BinaryPathName` does not parse; argv[0] is not the canonical copy; an unquoted path with a space; the service DACL gives another account `SERVICE_CHANGE_CONFIG`, `WRITE_DAC`, `WRITE_OWNER`, `DELETE`, `GENERIC_WRITE` or `GENERIC_ALL`; the chain or the set fails the invariant | 2 |
| `NOT INSTALLED` | no service, no copy, no sidecar | 3 |
| `COPY ONLY` | no service; the copy passes the invariant, its sidecar has `service: ""`, files = sidecar = the caller's set | 4 |
| `NOT RUNNING` | everything as for `OK`, but the state is not `RUNNING` (`START_PENDING` included); the reason carries `sc.exe start sing-box-lxd` (as administrator) or `--service=install` | 5 |
| error | the SCM does not open, the calling binary cannot be read | 1 |

Unlike macOS, a service whose argv[0] is not the canonical copy is `UNSAFE`, not
`MISMATCH`. The caller's set is its own `.exe` plus `libcronet.dll` beside it, if any; paths
compare case-insensitively.

**Uninstall** stops and deletes the service (`lxd: uninstalled service sing-box-lxd`), then
looks at two places: the directory of the service's argv[0] (when that file is named
`sing-box-lxd.exe`) and the copy directory. The set there is removed only if the sidecar
exists, its `service` is `sing-box-lxd` or empty, and the sha256 of every file equals the
sidecar's; one difference and nothing is removed, with the reason
(`lxd: copy left in place: …`). Leftovers go with the set; the default
`<ProgramFiles>\sing-box-lxd` is removed once empty, a directory given by `--exec-dir`
stays. `--keep-copy` removes only the service and rewrites the sidecar with `service: ""`
(`lxd: copy kept for non-service use: <path>; remove with --service=uninstall without --keep-copy`).
`--purge` also deletes the whole `<ProgramData>\sing-box-lxd`, the launcher's `classic.log`
included. `--dry-run` prints the same decisions with `would`.

**Logs.** The daemon writes `logs\lxd.log`; rotation is by copying (see [6](#6-logs)).
Events of the service itself (start, stop, exit code) are in the System event log, written
by the SCM; the daemon does not use the Event Log. An error before the log is open (an
unreadable daemon.json) is visible only as exit code 1 there.

**Under the SCM** the daemon reports `START_PENDING`, reads daemon.json, takes the log over,
runs the self-check, changes its working directory to the state directory (relative config
paths such as `cache.db` resolve there, not in `System32`) and reports `RUNNING` right away:
the control channel comes up first anyway, and the core's bootstrap may take longer than
the SCM start timeout. A stop or shutdown cancels the daemon and waits for it for 10 s;
past that the process logs a line and exits with code 1. The channel is the same TCP
loopback + mTLS as elsewhere; no named pipe.

Windows 7 builds (`windows-386-legacy-windows-7`) ship without `with_lxd`, so there is no
`lxd` and no service there.

### 7a.1. The protected copy of the binary set

- **The invariant.** Every component from the volume root down to the parent of the copy
  directory is owned by SYSTEM, Administrators or TrustedInstaller, and no other account
  holds `DELETE`, `WRITE_DAC`, `WRITE_OWNER`, `GENERIC_WRITE`, `GENERIC_ALL` or
  `FILE_DELETE_CHILD` on it (creating folders in `C:\` is allowed — it cannot replace our
  path). The copy directory and every file of the set are owned by SYSTEM or
  Administrators, and no other account holds any write right on them either. Each component
  is opened without following links, and its owner and DACL are read from that handle; a
  reparse point anywhere, a NULL DACL, or an allow entry of an unknown type (object,
  callback) is a violation. The volume must be a fixed NTFS drive. Messages name the path
  and the account: `<path>: owner <name> (<SID>) is not SYSTEM, Administrators or
  TrustedInstaller` (for the copy itself: `is not SYSTEM or Administrators`), `<path>: <name> (<SID>) is granted <rights>, must not be writable by a
  non-administrative principal`, `<path>: is a reparse point, must be a real file or
  directory`, `<path>: not on a fixed NTFS volume`.
- **The set.** The resolved `os.Executable()` (a regular file, at most 512 MiB) plus
  `libcronet.dll` from the same directory, if present. The copy is always named
  `sing-box-lxd.exe`.
- **How the set lands.** The ancestors are checked first (a violation refuses before any
  change); the copy directory is created with its DACL or brought back to it. Leftovers of
  a previous run (`<member>.old`, `.<member>.tmp-<hex>`) are removed; one still in use stays
  for the next install (`lxd: <path> is still in use, left for the next install`). A file
  named in the previous sidecar that is no longer part of the set (a `libcronet.dll` of an
  earlier build) is removed; an unknown file refuses:
  `<path>: unknown file in the copy directory; remove it: Remove-Item <path>`. Each changed
  member goes through a temporary file created in the same directory, flushed, given its
  DACL, and compared by sha256 with the source; then the old file is renamed to
  `<name>.old`, the temporary file is renamed into place and `.old` is deleted. There is no
  rewrite in place: a running image cannot be opened for writing, but it can be renamed. An
  identical set is left alone:
  `lxd: binary set unchanged (sha256 <exe>[, libcronet.dll <hex>]), copy skipped`.
- **The sidecar** `sing-box-lxd.install.json` is written after the set, through a temporary
  file and a rename, and is readable without elevation. It is a different structure from
  the macOS one:
  ```json
  {
    "source": "C:\\Users\\u\\AppData\\Local\\singbox-launcher\\bin\\sing-box.exe",
    "version": "1.14.2-lx.2",
    "installed_at": "2026-09-24T12:00:00Z",
    "service": "sing-box-lxd",
    "files": [
      {"name": "sing-box-lxd.exe", "sha256": "…"},
      {"name": "libcronet.dll", "sha256": "…"}
    ],
    "warnings": [
      {"code": "state_dir_foreign_before_install", "text": "WARN: … before this install; …"}
    ]
  }
  ```
  `service` is the SCM service the copy is bound to, `""` for a copy without a service.
  `files` is the whole set with the sha256 of each copy. `warnings` holds what the last
  install or copy warned about; absent or empty means nothing, and every run rewrites it.
  The only code so far is `state_dir_foreign_before_install`. If the files, the binding and
  the warnings are all unchanged, the sidecar is not rewritten
  (`lxd: already up to date <sha256>`). Installing from the copy itself keeps the previous
  `source`.
- **A copy without a service.** `--service=copy` takes the data directory over and lays down
  the set and the sidecar; the SCM is not touched. The sidecar gets `service: ""`, unless it
  was bound to an existing `sing-box-lxd` service — then the binding stays. A running service
  keeps executing the old image until it is restarted
  (`lxd: the service still runs the previous image until restarted`). Repeating it with the
  same binary changes nothing. This is for a launcher that runs the core elevated by
  itself.
- **Self-check at start.** An elevated core (`lxd` or `run`) checks its own binary with the
  invariant: the chain above its directory, the directory, the binary and `libcronet.dll`
  beside it. `lxd` started by the SCM refuses to start on a violation, with the reason in
  `lxd.log` and exit code 1:
  ```
  lxd: refusing to run as a Windows service from <binary> (owner <name> (<SID>)): <violation>; run `sing-box lxd --service=install` to reinstall from a protected copy
  ```
  Any other elevated run — `lxd` outside the SCM, `run` (the launcher's classic mode
  included) — and `lxd --allow-unsafe-exec` only log a WARN; a non-elevated process is not
  checked. A passed check under the SCM logs one INFO line:
  `lxd: self-check ok: protected <path>, windows service sing-box-lxd`.
- **DLL search.** Every `with_lxd` build on Windows restricts the search for DLLs loaded
  without a full path to the application directory and `System32`
  (`SetDefaultDllDirectories`), so a privileged core does not pick up a library planted in
  its working directory or in a user-writable `PATH` entry. `libcronet.dll` itself is loaded
  lazily by full path from the executable's directory and then from `PATH`; when the set
  carries no `libcronet.dll`, the service and an elevated `run` pin the load to the
  executable's directory at start, so a naive outbound gets a load error instead of a `PATH`
  search under SYSTEM.

Not covered: Authenticode signatures of the copy are neither checked nor applied; two
concurrent install/copy runs with different binaries are not locked against each other
(status then shows `MISMATCH`, repeating the command fixes it); the ancestors of
`<ProgramData>` are not checked by the core.

### 7a.2. Checking by hand

From PowerShell (the `icacls` and `Get-Acl` of the data directory need administrator
rights):

```powershell
sc.exe qc sing-box-lxd                     # BINARY_PATH_NAME = "C:\Program Files\sing-box-lxd\sing-box-lxd.exe" lxd --state-dir …, AUTO_START, LocalSystem, DEPENDENCIES Tcpip
sc.exe qfailure sing-box-lxd               # RESTART -- Delay = 5000 milliseconds (three times), RESET_PERIOD 86400
sc.exe sdshow sing-box-lxd                 # D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x2008d;;;AU)
sc.exe query sing-box-lxd                  # STATE 4 RUNNING
icacls "C:\Program Files\sing-box-lxd"     # SYSTEM:(OI)(CI)(F), Administrators:(OI)(CI)(F), Authenticated Users:(OI)(CI)(RX); no inherited entries
icacls "C:\Program Files\sing-box-lxd\sing-box-lxd.exe"
(Get-Acl "C:\Program Files\sing-box-lxd\sing-box-lxd.exe").Owner   # BUILTIN\Administrators
icacls C:\ProgramData\sing-box-lxd /t      # SYSTEM and Administrators only (plus the launcher's own entry on classic.log)
Get-FileHash "C:\Program Files\sing-box-lxd\sing-box-lxd.exe"      # SHA256, compare with the sidecar
Get-Content "C:\Program Files\sing-box-lxd\sing-box-lxd.install.json"
Get-Content C:\ProgramData\sing-box-lxd\logs\lxd.log -Tail 50
sing-box lxd --service=status; "exit $LASTEXITCODE"
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
- A log beside the state dir would write into the NAND overlay — flash wear.
  The installer therefore sets `"log_file": "/tmp/lxd.log"` in daemon.json:
  the log lives on tmpfs (lost on reboot — for a router that is the right
  trade), and the rotation limits (1 MB × 2 files) bound the RAM it can take.

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
   macOS when the service is system-scope, elevated on Windows — there an
   unelevated run says `cannot read <path>: access denied — run as administrator`).
   It prints a one-time invite `address#fingerprint#code`.
2. Paste the invite into the launcher: it pins the server by the fingerprint,
   registers with the code (`POST /admin/enroll`), and is trusted by its
   certificate from then on. The code burns.
3. Inspect/revoke: `client list`, `client remove <name-or-fingerprint>`.

**The client name.** `--name`, `--invite-name` and the `name` field of
`POST /admin/client-code` are trimmed of surrounding spaces and must then be empty (no
name) or 1 to 64 printable characters; otherwise the command refuses and the route answers
400 `client name: …`. A named invite **replaces** an enrolled client of the same name when
it is redeemed: the old certificate is revoked and the new one takes its place, so a
launcher that re-pairs on every install does not pile up entries. An invite without a name
adds a client, as before.

**The invite into a file.** `client add --invite-out <file>` and
`--service=install --invite-out <file>` write the invite line (`address#fingerprint#code`
and a newline) into a new file instead of printing it, and print
`lxd: invite written to <file>`. The file is created before the mint, exclusively: on
Unix with `O_CREAT|O_EXCL|O_NOFOLLOW` and mode 0600; on Windows with `CREATE_NEW`, after
which the final path of the opened file must be the requested one (an 8.3 short name in
the request is accepted; a junction on the path refuses and the file is deleted). An
existing file refuses the command before anything changes. Install waits up to 15 s for
the daemon to mint the invite; if it does not, with `--invite-out` install exits 1 — the
service stays installed, the file is removed — because the launcher judges by the exit
code. With `--invite-out` install names the client `singbox-launcher` unless
`--invite-name` says otherwise; without `--invite-out` the name is `--invite-name` or none.
`--service=install --invite-out` needs a platform where install really installs (macOS,
Windows) and is refused on Linux, where install prints a recipe; `client add --invite-out`
works everywhere.

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

### 9.1 Enrollment on the wire — building your own client

The launcher performs all of this internally; this section is the contract for
third-party clients and for debugging with curl.

The invite is three `#`-separated segments — `address#fingerprint#code`:

| Segment | Meaning |
|---|---|
| `address` | where to connect (`host:port`) |
| `fingerprint` | SHA-256 of the server certificate DER, lowercase hex — the client pins the server by it |
| `code` | the one-time code (`XXXX-XXXX-XXXX`) — the **only** segment that goes into the `code` field |

1. **Generate a client identity.** Any parseable X.509 certificate works: the
   daemon pins its SHA-256 fingerprint and never validates a chain, so
   self-signed is the norm. The private key never leaves the client — the model
   mirrors WireGuard peers: only public certs (fingerprints) are exchanged. The
   daemon's own identities are ECDSA P-256 valid for 10 years:

   ```bash
   openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
     -keyout client_key.pem -out client_cert.pem -days 3650 \
     -subj "/CN=my-client" -addext "extendedKeyUsage=clientAuth"
   ```

2. **Connect over TLS pinning the server.** The server certificate is
   self-signed, so a real client replaces chain validation with a pin:
   sha256 of the leaf certificate DER must equal the invite's `fingerprint`
   segment. curl cannot express this pin (`--pinnedpubkey` hashes the public
   key, not the certificate), so for curl experiments use `-k` and compare the
   fingerprint by hand:

   ```bash
   openssl s_client -connect 127.0.0.1:19091 </dev/null 2>/dev/null \
     | openssl x509 -outform DER | openssl dgst -sha256
   ```

3. **`POST /admin/enroll`** — **no** `Authorization` header: the code is the
   only guard, and this is the single route reachable before trust. The body is
   JSON with three fields; `cert_pem` is the client certificate generated in
   step 1 (the daemon answers `invalid client certificate PEM` when it is
   missing or is not a PEM `CERTIFICATE` block):

   ```bash
   curl -sk https://127.0.0.1:19091/admin/enroll \
     -H 'Content-Type: application/json' \
     -d "$(jq -n --rawfile cert client_cert.pem --arg code XXXX-XXXX-XXXX \
           '{code: $code, name: "my-client", cert_pem: $cert}')"
   ```

   Success is `{"enrolled":true,"name":…,"fingerprint":…}`. The label given to
   `client add --name` wins over the `name` in the request, and such a named code
   replaces an enrolled client of that name (see [9](#9-pairing-a-client-the-same-on-every-os)). The code burns on
   success; only one code is active at a time (a new mint replaces the old).

4. **Everything after enrollment is mTLS with that certificate** — it is the
   full credential for both planes (admin REST and gRPC), no Bearer on top:

   ```bash
   curl -sk --cert client_cert.pem --key client_key.pem \
     https://127.0.0.1:19091/admin/status
   ```

Error map:

| Answer | Cause |
|---|---|
| `invalid client certificate PEM` | `cert_pem` missing, or not a PEM `CERTIFICATE` block |
| `invalid enrollment code` | typo, an already-burnt code, or the whole invite pasted into `code` instead of the last segment |
| `no active enrollment code` | the daemon restarted after `client add` (the code lives in process memory), or the code was already redeemed |
| `client certificate not trusted` (any other route) | the certificate was never enrolled, or the daemon state was purged |

## 10. Admin REST (reference)

| Route | What it does |
|---|---|
| `POST /admin/apply` | body = config; 200 applied / 422 invalid / 500 failure (+`rolled_back`) |
| `POST /admin/rollback` | roll back to last-good (404 — nothing recorded) |
| `POST /admin/start` · `POST /admin/stop` | core lifecycle apart from the config (stop is remembered) |
| `GET /admin/config` | the active config |
| `GET /admin/status` | `idle\|started\|fatal`, active/last-good sha, `last_error`, `interrupted_apply` |
| `GET /admin/info` | identity card: version, state_dir, listen, tls, fingerprint, pid, uptime, log_path, `executable` and `executable_sha256` (the running binary — compare cores by hash, not by path; the hash is computed once at start and reads `""` for the first moments) |
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

**Operator routes** — `GET /admin/clients`, `POST /admin/client-code`,
`POST /admin/client-remove`. These are the wire behind the `client list / add /
remove` subcommands and are deliberately **not** part of the remote surface:
they are served on loopback only, require the Bearer secret, and — uniquely —
do **not** require a client certificate, because the operator standing on the
host has none. Minting an invite code is handing out trust, so it must not be
reachable from the network.

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

**Cached for 500 ms.** You can poll these up to twice a second and get a fresh
answer every time; polling harder just returns the same snapshot with the same
`updated_unix`. Remember that percentages are deltas between two samples: a
shorter window makes the number **noisier**, not more accurate, so average on
the client rather than polling faster.

**Counters and rates both.** Interfaces report raw `rx_bytes` alongside
`rx_bytes_per_second`: a counter survives restarts and gaps, a rate is
convenient but lies across them. Graph the counter, read the rate.

Every interface is listed, `lo` and down ones included — "wan went down" is
exactly what you want to see. Filtering is the UI's job.

**Read the platform from the machine-readable fields**, not from the `os`
string, which is human-facing and formatted by the distribution:

| Field | Example | Answers |
|---|---|---|
| `os_family` | `linux` | is there a `/proc` (kernel, syscalls) |
| `os_id` | `openwrt` | is there a `ubus` (distribution) |
| `os_id_like` | `["lede","openwrt"]` | a fork of something known? |

The distinction is not academic: a RouteRich router calls itself `RouteRich`
but reports `ID="openwrt"` in `/etc/os-release`, so matching the human string
would fail to recognise the platform. `os_id_like` covers forks that do invent
their own `ID` and name their base only there. Off Linux `os_id` is a constant
(`macos`, `windows`) and `os_id_like` is an empty array, not `null`.

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
