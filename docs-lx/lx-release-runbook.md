# lx release runbook

> 🌐 Русская версия: **[lx-release-runbook.ru.md](lx-release-runbook.ru.md)**.

The procedure for cutting an lx release or pre-release. The rule that matters most comes first:
**before any tag, check whether upstream moved ahead — and normally, take its changes.**

Repository context:

- `upstream` = `https://github.com/SagerNet/sing-box.git`; we track the **`upstream/stable`** branch.
  **Measure drift ONLY against `upstream/stable`.** `upstream/testing` is upstream's development
  line; it runs hundreds of commits ahead at all times (Go bumps, immature refactors). Measuring
  drift against it is meaningless — it will NEVER read zero, and a red gate on testing blocks
  releases forever. Zero against `upstream/stable` = no drift, the tag can be cut.
- Our working branch is **`lx`** (also the GitHub default); upstream integration is a manual
  **`git merge upstream/stable`** (NOT rebase; see `wg-1.14-migration` in memory and
  [BUILD_CI_CD](../SPECS/FEATURES/001-BUILD_CI_CD/FEATURE.md)).
  `lx-rebase.yml` describes the old auto-rebase onto stable upstream tags; it never force-pushes
  `lx` — it only opens a PR/issue.
- **`lx-1.14` is the historical 1.14 migration branch.** The migration is finished, the branch was
  merged into `lx` (both pointed at the same commit at `v1.14.0-lx.16`) and is kept in origin only
  as an anchor. New work and releases happen on `lx`.
- **Prerelease is decided by the tag suffix**, not by the branch: `lx-release.yml` passes
  `--prerelease` for `-rc.N` / `-alpha.N` / `-beta.N`. A tag without a suffix publishes as "Latest"
  (for the current list run `git tag -l 'v*lx*' --sort=-creatordate | head`; this document
  deliberately does not name a version — that is the fastest thing to go stale). The old
  restriction "while upstream is in alpha, rc line only" is lifted: upstream moved to beta, and the
  fork cuts stable tags again.
- Release notes are assembled by `lx-release.yml` from two sources, in this order of priority:
  1. **`docs-lx/releases/v<tag-without-v>.md`** — hand-written bilingual notes in the LxBox format
     (EN+RU TL;DR, 🇬🇧/🇷🇺 blocks with 🆕/🔧/🐛/🧰 sections; rules and skeleton in
     [docs-lx/releases/TEMPLATE.md](releases/TEMPLATE.md)). **Required for a stable tag** (without
     it CI warns and ships the raw changelog).
  2. Fallback for rc/alpha/beta: the `#### v<tag-without-v>` section of
     [docs-lx/lx-changelog.md](lx-changelog.md).
  The boilerplate (upstream base, collapsed `<details>` about binaries/AAR/build tags, link to the
  previous release) is generated — do not write it into the files. The changelog is still kept per
  tag as an engineering log; it must be correct BEFORE the tag.

---

## 0. Pre-release gate — do NOT cut the tag until every item is green

```
[ ] 1. drift in the fork SUBMODULES checked and closed BEFORE merging the core (section 1)
[ ] 2. upstream drift checked (section 2)
[ ] 3. if upstream is ahead — taken/merged/built (section 3), OR deliberately deferred with a reason
[ ] 4. go build ./... and build -tags with_lx_command are green; full set — make -f Makefile.lx lx-build
       (⚠️ SPEC 049: the toolchain version lives in a SINGLE file `go.version` at the root — every
        `setup-go` step across all `lx-*.yml` reads it (currently go1.26.5). NOT
        `go-version-file: go.mod`: that yields 1.24.x, which is the SPEC 044 regression — a
        go1.24 AAR kills quic-go outbounds on vendor Android kernels, and badtls is a stub there.
        The Win7 job has its own patched toolchain; neither it nor the upstream workflow is touched
        by this pin. Raise the pin's minor only after running an AAR on a real device)
[ ] 5. gofmt -l over lx-owned files — empty
[ ] 6. docs-lx/lx-changelog.md contains a #### v<this-tag> section with correct content
       (verify with the SAME awk as CI — see section 4);
       for a stable tag ALSO: docs-lx/releases/v<this-tag>.md written per TEMPLATE.md,
       proofread in rendered form and stripped of the draft comment
[ ] 7. branch lx pushed to origin BEFORE the tag (push branch → push tag)
```

---

## 1. Submodules first, core second (THE ORDER IS MANDATORY)

**Drift in the child fork repositories is resolved BEFORE merging the core, not after.**
Our `replace` directives in `go.mod` substitute upstream modules with fork submodules
(`wireguard-go`, `sing-tun`, `gvisor`). Merging the core raises the versions in `require`, but
`replace` keeps substituting OUR branch — so the build silently runs on code upstream no longer
expects.

### 1.1 Machine check: the fork's tip against `go.mod`

The question is **not** "does upstream have new commits" but **"does our branch contain exactly
the commit `go.mod` requires"**. Checking by eye is useless: `go build` passes, the tests pass,
and the race shows up at runtime.

```bash
for m in wireguard-go sing-tun gvisor; do
  req=$(grep -E "sagernet/$m v" go.mod | grep -oE "[0-9a-f]{12}$")
  echo "=== $m (go.mod requires: ${req:-snapshot without a hash}) ==="
  [ -z "$req" ] && continue
  git -C submodules/$m fetch sagernet 2>/dev/null
  if ! git -C submodules/$m cat-file -e "$req" 2>/dev/null; then
    echo "  ❌ commit absent from the fork — DRIFT"; continue
  fi
  if git -C submodules/$m merge-base --is-ancestor "$req" HEAD 2>/dev/null; then
    echo "  ✅ contained in our branch"
  else
    echo "  ❌ DRIFT: missing $(git -C submodules/$m rev-list --count HEAD..$req) commits"
    git -C submodules/$m log --oneline HEAD..$req | head -10
  fi
done
```

For `gvisor` the `require` version carries no hash (it is a snapshot) — compare the version string
against the snapshot date in the submodule's history.

### 1.2 Take the whole upstream line, not selected commits

⚠️ **Do not cherry-pick "the commits the compiler complains about."** An upstream line is meant to
work as a whole: among the ones you skip there will almost certainly be race fixes and locking
refactors that produce no compile errors but do produce **intermittent** runtime failures.

That is exactly how `v1.14.0-lx.20-rc.5` broke: of 14 missing `wireguard-go` commits, 3 were taken
(picked by `undefined: device.PeerLookupFunc`), while `15b912c device: fix TOCTOU race during
session state update` and `2ad9837 device: refactor container locking for lock-order clarity` were
skipped. The result was a state that never existed upstream: green build, green unit tests, and a
nil panic in `udpNat.Start()` on the device when a WireGuard endpoint started — reproducing every
other time.

⚠️ **A fast-forward of the fork is IMPOSSIBLE** — upstream branches contain zero of our lx commits,
and updating would wipe the AWG2 obfuscation and SPEC 041. The correct path is to carry OUR patches
on top of the full upstream line (re-graft), not to pull upstream fragments onto our old base.

⚠️ **After a re-graft, bump `upstream.version`** (at the root, next to `go.version`) — it is the
base for `LX_VERSION` in a local `make -f Makefile.lx lx-build`. Release CI takes the version from
the tag name and never reads this file, so a forgotten bump will not surface in the release — it
only breaks builds for users following the README.

The check is **manual only; there is no CI gate and there cannot be one**: there is nothing to
compare the pin against. The upstream version is not recorded in the tree (`constant/version.go` is
`"unknown"`, the version comes from ldflags — that is our zero-diff), and upstream tags in the fork
are incomplete: `origin` carries only old pure-upstream ones (≤ `v1.13.11`), while `v1.13.13` and
`v1.14.0-beta.*` exist solely in local clones where someone ran `git fetch upstream --tags`. A
tag-based gate was tried (commit `5c621c089`, reverted in `d7fa017a8`): green locally, red in CI,
where it honestly reported `1.13.11` — exactly what a user with a clean clone sees. So after a
re-graft, verify this by eye.

### 1.3 The second class of drift: API absent from the fork's base

A merge can pull in an external dependency that needs API from a newer submodule. The core builds,
the AAR does not, and you find out from CI.

A real case (SPEC 051): upstream raised `tailscale` 1.92 → 1.102, which required
`device.PeerLookupFunc`/`NewPeerConfig`/`SetPeerLookupFunc` from a newer `wireguard-go`. The chain
was `libbox/native_shell_session.go` → `protocol/tailscale/tailssh` → `wgengine/wgcfg` →
`wireguard-go/device`. Build tags do not avoid it: `tailssh` is gated on `with_gvisor`, which is
always on.

### 1.4 After closing the drift — a device run is mandatory

Builds and unit tests **do not catch** this class of bug (proven on rc.5). If a submodule was
touched, a live run is required before the tag: the tunnel comes up, DNS resolves, URL-test
measures, WG/AWG nodes are alive — and all of it several times in a row, because races float.

## 2. Check whether upstream moved ahead (MANDATORY before every release)

**The baseline is `upstream/stable`, and only that.** Drift is not measured against
`upstream/testing`: it is upstream's development branch, it runs hundreds of commits ahead and will
never read zero (measured 2026-08-14: stable — ahead=0, testing — ahead=233 from its own
merge-base; the number grows on its own and is quoted only as an order of magnitude). A gate on
testing is permanently red and blocks releases for no reason.

```bash
git fetch upstream --tags
# THE honest check: if merge-base == tip of upstream/stable, there is no drift
git merge-base lx upstream/stable
git rev-parse upstream/stable
# what exactly arrived (empty = nothing):
git --no-pager log --oneline $(git merge-base lx upstream/stable)..upstream/stable
# whether a new upstream tag appeared past our base:
git tag -l 'v1.14.0*' --sort=-creatordate | grep -iv lx | head
```

⚠️ **Do not measure drift from `<our-merge-commit>^2`.** Upstream branches are regularly
**force-pushed**, so the second parent of our merge commit points at rewritten history, and
`git log/diff <merge>^2..upstream/stable` shows garbage — including OUR delta in reverse
("upstream deleted `option/platform.go`"), which never happened. On release `v1.14.0-lx.16`
(2026-07-26) this looked like "8 new upstream commits" although all 8 were already merged and
merge-base matched the tip. The only reliable signal is merge-base; compare commits by subject
(`git log --format=%s`), not by hash — after a force-push the hashes differ.

**The trick when merge-base has moved** (a force-push landed after your merge — as on 2026-07-30:
right after merging 235 commits, drift reported "210 ahead"). Compare by subject, not by hash:

```bash
comm -23 <(git log --format=%s $(git merge-base lx upstream/stable)..upstream/stable | sort) \
         <(git log --format=%s -260 lx | sort)
```

Whatever remains in the output is genuinely new. Back then, 5 of the 210 "new" ones were real.
**Take such a tail with `cherry-pick`, not with a second merge**: a second merge re-raises the
already-resolved conflicts (49 of them in that case) against a stale base.

- **merge-base == tip / 0 commits ahead** → upstream is in sync, move on to build/tag (section 4).
- **>0 commits** → by default, **take and merge** (section 3). Deferring is allowed only
  deliberately and with a recorded reason (e.g. an upstream commit breaks our layer and needs its
  own investigation) — record it in the release's changelog entry so the known drift is visible.

Why "normally take it": the longer drift accumulates, the more expensive and risky the merge
(conflicts in `.pb.go`, the wireguard-go and sing-tun fork submodules, adapter interface changes).
Small frequent merges are cheaper than one big one right before a release.

## 3. Take upstream's changes (merge, then build) — and ONLY then release

```bash
git checkout lx
git merge upstream/stable             # manual merge, NOT rebase
```

On conflicts, these are the zones we touch most often (keep lx semantics, accept upstream logic):

- `daemon/*.pb.go` / `*.proto` — our fields are additive (`detourList=23`, DnsQueryEvent 1..12). If
  upstream regenerated the descriptors, regenerate via `make -f Makefile.lx lx-proto` and re-apply
  the lx fields, or do it by hand: see `lx-commandclient-extensions` in memory (pinned protoc
  toolchain).
- `submodules/wireguard-go`, `submodules/sing-tun` and `submodules/gvisor` — our fork submodules.
  Never accept an upstream bump blindly (including a commit like "Update sing-tun" or a
  `sagernet/gvisor` bump in `go.mod`): it silently moves `replace` off the fork and reverts our
  patches (AWG obfuscation, SPEC 040 acceptLoop self-heal, SPEC 041 rebind, SPEC 048 nil-guard in
  gvisor's `handleConnecting`); see `wg-1.14-migration` and the 2026-08-01 sync in the changelog.
  The revert is silent: everything builds, package tests are green, and the bug returns in the
  field — so after any merge that touched `go.mod`, verify all three with `go list -m`:

  ```bash
  go list -m github.com/sagernet/wireguard-go github.com/sagernet/sing-tun github.com/sagernet/gvisor
  # each must resolve to => ./submodules/<name>
  ```

  `submodules/gvisor` is maintained as a **snapshot of the pin without history** (upstream's full
  history is 1.45 GB per CI clone): a new pin lands as a new snapshot commit, the patch is applied
  on top, and the red/green test travels with it. Details in SPEC 048 §6.
- `cmd/internal/build_libbox/main.go` — the only upstream-file edit in the CI zone, marked `// lx`.
- `box.go`, `dns/client*.go`, `route/route.go`, `common/trafficcontrol/tracker.go` — these carry lx
  observability on top of upstream logic; on conflict keep upstream resolve/routing behaviour, our
  emit/Detour bits are additive (see the purity audit, commit 3505beb6).

After the merge, build and exercise both paths before tagging:

```bash
go build ./...
go build -tags with_lx_command ./...
gofmt -l box.go common/dnstrack/manager.go dns/client.go dns/client_log.go \
        dns/transport_adapter.go route/route.go common/trafficcontrol/tracker.go \
        daemon/started_service_command_lx.go experimental/libbox/command_client_command_lx.go
make -f Makefile.lx lx-check     # build the lx binary + check a minimal config
```

If the merge brought noticeable upstream changes, add a line about the upstream base to the
release's changelog section (as in `b8ff5c78`: "rc.6 also carries the upstream alpha.35 merge").

## 4. Update the changelog and release notes, then cut the tag

1. Append a section to [docs-lx/lx-changelog.md](lx-changelog.md). The heading must be **exactly**
   `#### v<tag-without-v>` (e.g. `#### v1.14.0-lx.16`) — for rc/alpha/beta `lx-release.yml`
   extracts precisely that section into the release notes via `awk` (when the file from step 1b is
   absent). A wrong or missing heading silently yields empty or foreign notes.
   **Promoting an rc line to stable needs its own section**, not a reuse of the last rc's: tag
   `v1.14.0-lx.16` needs a `#### v1.14.0-lx.16` section summarizing rc.1–rc.N.

   1b. **For a stable tag, write `docs-lx/releases/v<tag-without-v>.md`** per the rules and skeleton
   in [TEMPLATE.md](releases/TEMPLATE.md): a bilingual TL;DR plus 🇬🇧/🇷🇺 blocks with
   🆕/🔧/🐛/🧰 sections, item headings phrased as the symptom or benefit as the user sees it,
   rc.1–rc.N folded into one story per topic. The file can (and should) be drafted along the rc
   line; before the tag, proofread it rendered and remove the draft comment. When the file exists it
   **fully replaces** the changelog section in the release body (for rc tags too, if written).

   Verify the fallback extraction BEFORE tagging, with the same code CI uses:

   ```bash
   VERSION=1.14.0-lx.16   # the tag without the leading v
   awk -v v="#### v${VERSION}" '$0==v {f=1; next} /^#### / {f=0} f' docs-lx/lx-changelog.md
   ```

   Empty output → the notes ship as a stub. Capturing neighbouring `####` → foreign sections land in
   the notes.
2. Commit the branch → **push the branch to origin BEFORE the tag** (otherwise the tag ends up
   ahead of the branch — this happened on rc.1; see `git-push-auth-gh-token` in memory about the
   inline token).
3. Create and push the tag. CI `lx-release.yml` builds the desktop archives + AAR (`libbox` +
   `libbox-legacy`) and publishes the release with generated notes; **prerelease or stable is
   decided by the tag suffix** (`-rc./-alpha./-beta.` → prerelease, no suffix → stable "Latest").
4. Check the run: `gh run list --workflow lx-release.yml`, wait for `completed success`, confirm the
   release has the AAR, that the notes match their source (the release file, or the changelog
   section for rc), that the `<details>` blocks rendered, and that the "Previous release" footer
   points at the right tag.

## 5. Post-release sanity

- `gh release view v<tag>` — assets in place, the prerelease flag matches the tag suffix, notes
  correct. For a stable tag additionally:
  `gh api repos/Leadaxe/sing-box-lx/releases/latest -q .tag_name` must return this tag.
- **Links in the notes point at branch `lx`** (`/blob/lx/...` is hardcoded in `lx-release.yml`). If
  `lx` lags behind the released commit, links to new files return **404** — at `v1.14.0-lx.16` the
  branch was 28 commits behind and the links to `SPECS/FEATURES/013-DNS_GROUP` would have been
  broken. To check:

  ```bash
  grep -ohE 'https://github.com/Leadaxe/sing-box-lx/blob/[^)"]+' <(gh release view v<tag> --json body -q .body) \
    | sort -u | while read -r u; do echo "$(curl -s -o /dev/null -w '%{http_code}' -L "$u")  $u"; done
  ```

  Now that work happens directly on `lx`, a divergence is only possible if the tag was cut from
  another branch.
- Download one archive, verify its checksum against `SHA256SUMS`, and run the binary:
  `sing-box version` must show the tag's version, the same revision, and the full build-tag set (in
  desktop archives **`with_clash_api` must be present** — only the AAR drops it; see
  `desktop-keeps-clash-api-aar-drops`).
- For observability/attribution features (the DNS stream, Detour) a **device verification is
  mandatory**: builds and proto round-trips do NOT catch registry-key / fast-path-hijack / ctx-timing
  bugs (the §180/§180-2 history). See `lx-spec018-dns-query-stream` in memory.

---

### In one line

`fetch upstream → compare merge-base with tip → if ahead, merge it in → build+gofmt+lx-check →
changelog (+verify the awk; for stable also docs-lx/releases/v<tag>.md) → push branch lx → tag →
verify notes/assets/checksums`.
Drift is checked **every** time and **only via merge-base**; merging is the default behaviour, and
skipping it is a deliberate exception with a recorded reason.
