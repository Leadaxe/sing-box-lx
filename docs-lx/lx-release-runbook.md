# lx release runbook

> 🌐 Русская версия: **[lx-release-runbook.ru.md](lx-release-runbook.ru.md)**.

The procedure for cutting an lx release or pre-release. The rule that matters most comes first:
**before any tag, check whether upstream moved ahead — and normally, take its changes.**

Repository context:

- `upstream` = `https://github.com/SagerNet/sing-box.git`; we track the **`upstream/stable`** branch.
  **Measure drift ONLY against `upstream/stable`.** `upstream/testing` is upstream's development
  line (the next minor version in alpha); it is always ahead — by tens or hundreds of commits (Go
  bumps, immature refactors) — and gets rebuilt on top of stable with a force-push. Measuring
  drift against it is meaningless — it will NEVER read zero, and a red gate on testing blocks
  releases forever. Zero against `upstream/stable` = no drift, the tag can be cut.
  The banner on the fork's page, "N commits behind SagerNet/sing-box:testing", counts from testing
  (upstream's default branch) — it is not drift. Do not press **Sync fork**: it merges testing into
  `lx`, and on conflicts it offers "Discard commits", which resets `lx` to upstream.
- Our working branch is **`lx`** (also the GitHub default); upstream integration is a manual
  **`git merge upstream/stable`** (NOT rebase; see `wg-1.14-migration` in memory and
  [BUILD_CI_CD](../SPECS/FEATURES/001-BUILD_CI_CD/FEATURE.md)).
  `lx-rebase.yml` from [BUILD_CI_CD](../SPECS/FEATURES/001-BUILD_CI_CD/FEATURE.md) describes the
  old auto-rebase onto stable upstream tags; it never force-pushes `lx` — it only opens a PR/issue.
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

## Table of contents

- [0. Pre-release gate — do NOT cut the tag until every item is green](#0-pre-release-gate--do-not-cut-the-tag-until-every-item-is-green)
- [1. Submodules first, core second (THE ORDER IS MANDATORY)](#1-submodules-first-core-second-the-order-is-mandatory)
  - [1.1 Machine check: the fork's tip against `go.mod`](#11-machine-check-the-forks-tip-against-gomod)
  - [1.2 Take the whole upstream line, not selected commits](#12-take-the-whole-upstream-line-not-selected-commits)
  - [1.3 The second class of drift: API absent from the fork's base](#13-the-second-class-of-drift-api-absent-from-the-forks-base)
  - [1.4 After closing the drift — a device run is mandatory](#14-after-closing-the-drift--a-device-run-is-mandatory)
- [2. Check whether upstream moved ahead (MANDATORY before every release)](#2-check-whether-upstream-moved-ahead-mandatory-before-every-release)
- [2a. Check dependency versions — not just commits (MANDATORY before every release)](#2a-check-dependency-versions--not-just-commits-mandatory-before-every-release)
- [2b. Full release build without publishing (dry run)](#2b-full-release-build-without-publishing-dry-run)
- [3. Take upstream's changes (merge, then build) — and ONLY then release](#3-take-upstreams-changes-merge-then-build--and-only-then-release)
- [4. Update the changelog and release notes, then cut the tag](#4-update-the-changelog-and-release-notes-then-cut-the-tag)
- [5. Post-release sanity](#5-post-release-sanity)
  - [In one line](#in-one-line)

---

## 0. Pre-release gate — do NOT cut the tag until every item is green

```
[ ] 1. drift in the fork SUBMODULES checked and closed BEFORE merging the core (section 1)
[ ] 2. upstream drift checked (section 2)
[ ] 2a. dependency versions checked against upstream/stable, not just commits (section 2a): Go
        toolchain, go.mod, CRONET_GO_VERSION, NDK/JDK, protoc plugins, upstream.version, GitHub
        Actions; every mismatch fixed OR marked `lx:` with a reason and recorded in the changelog
[ ] 3. if upstream is ahead — taken/merged/built (section 3), OR deliberately deferred with a reason
[ ] 4. go build ./... and build -tags with_lx_command are green; full set — make -f Makefile.lx lx-build
       (⚠️ SPEC 049: the toolchain version lives in a SINGLE file `go.version` at the root — every
        `setup-go` step across all `lx-*.yml` reads it. NOT `go-version-file: go.mod`: that yields
        the language floor, not the toolchain pin (while it said 1.24.x that was the SPEC 044
        regression — a go1.24 AAR kills quic-go outbounds on vendor Android kernels, and badtls is
        a stub there). The Win7 job has its own patched toolchain; neither it nor the upstream
        workflow is touched by this pin. Raise the pin's minor only after running an AAR on a real
        device)
[ ] 4a. if the toolchain or any section 2a dependency version changed since the last release — the
        full release build without publishing (`lx-release.yml` dry run) is green (section 2b)
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
(`wireguard-go`, `sing-tun`, `gvisor`, `utls`). Merging the core raises the versions in `require`, but
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

For `utls` the pin is a `metacubex/utls vX.Y.Z` tag (currently `v1.8.7`), also without a hash: the
fork's `lx` branch must sit **on that tag** and carry exactly three commits ported from refraction on
top (Firefox 148 + key share reuse, SPEC 086; Safari 26.3, SPEC 087):

```bash
req=$(grep -oE 'metacubex/utls v[0-9.]+' go.mod | awk '{print $2}')
git -C submodules/utls fetch metacubex --tags 2>/dev/null
git -C submodules/utls merge-base --is-ancestor "$req" HEAD && echo "✅ lx sits on $req" || echo "❌ DRIFT: go.mod requires $req"
git -C submodules/utls log --oneline "$req..HEAD"   # expect exactly 3 lines (cherry-picks of fc716b2, ddebe39, aa6edf4)
```

An upstream `metacubex/utls` bump means moving the fork's `lx` branch onto the new tag with the
same three commits on top (metacubex accepts no external PRs — the sync is ours alone); the
condition for dropping the fork is in SPEC 086/087.

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
and updating would wipe the AWG obfuscation and SPEC 041. The correct path is to carry OUR patches
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
where it reported `1.13.11` — exactly what a user with a clean clone sees. So after a
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
`upstream/testing`: it is upstream's development branch, it is always ahead and will never read zero
(measured 2026-08-14: stable — ahead=0, testing — ahead=233 from its own merge-base; 2026-09-14,
after testing was rebuilt on top of stable, — 21; the number moves on its own and is quoted only as
an order of magnitude). A gate on testing is permanently red and blocks releases for no reason.

```bash
git fetch upstream --tags
# The reliable check: if merge-base == tip of upstream/stable, there is no drift
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

## 2a. Check dependency versions — not just commits (MANDATORY before every release)

Zero drift by merge-base does not yet mean we build with what upstream builds with. A merge brings
upstream's own files (`build.yml`, `go.mod`, `.github/CRONET_GO_VERSION`, the toolchain scripts),
but our pins — `go.version`, NDK and JDK in `lx-*.yml`, the protoc plugins in `Makefile.lx` — stay
as they were, and the mismatch accumulates silently. That is how, after the 2026-09-05 stable merge,
upstream built with Go 1.26.7 while we stayed on 1.26.6 until 2026-09-14.

**The baseline is `upstream/stable`.** Every mismatch is either fixed before the tag or kept
deliberately: the reason goes into an `lx:` comment next to the pin (like `ndk-version: r28c` in
`lx-release.yml`) and into a line of the release's changelog section.

| Dependency | Our pin | Compare with |
|---|---|---|
| Language: Go toolchain | `go.version` | `go-version:` in `upstream/stable:.github/workflows/build.yml`; `VERSION=` in `.github/setup_go_for_windows7.sh` and `setup_go_for_macos1013.sh` — upstream files that arrive with the merge and must match `go.version` |
| Language: `go.mod` | `go` / `toolchain` directives | `upstream/stable:go.mod` — identical |
| Go modules | the `require` blocks in `go.mod` | `upstream/stable:go.mod` — the same set; `replace` onto fork submodules — section 1.1 |
| Fork submodules | `submodules/{wireguard-go,sing-tun,gvisor,utls}` | section 1.1 |
| naive / cronet-go | `.github/CRONET_GO_VERSION` | the same file in `upstream/stable`; after a bump run `lx-musl-toolchain-mirror.yml` by hand (SPEC 023) |
| Android | `ndk-version`, OpenJDK in `lx-*.yml` | `ndk-version` / `java-version` in upstream `build.yml` |
| Code generation | `LX_PROTOC_GEN_GO_VERSION`, `LX_PROTOC_GEN_GO_GRPC_VERSION` in `Makefile.lx` | `google.golang.org/protobuf` / `grpc` in `go.mod` and the style of upstream's generated code |
| Version base | `upstream.version` | `git describe --tags --abbrev=0 upstream/stable` |
| GitHub Actions | `uses:` majors in `lx-*.yml` | the same actions in upstream workflows — ours are not older |

```bash
git fetch upstream --tags
S=upstream/stable
modlist() { awk '/^require \($/{r=1;next} r&&/^\)$/{r=0;next} r&&NF>=2&&$1!~/^\/\//{print $1, $2} /^require [^(]/{print $2, $3}'; }
acts() { grep -hoE 'uses: [A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[^ ]+( # v[0-9.]+)?' \
  | sed -E 's/@[0-9a-f]{40} # /@/; s/^uses: //; s/@/ /' | sort -u \
  | awk '{v[$1]=(v[$1]?v[$1]",":"")$2} END{for(k in v)print k, v[k]}' | sort; }
echo "Go:     lx=$(cat go.version)  stable=$(git show $S:.github/workflows/build.yml | grep -oE 'go-version: *[0-9.]+' | awk '{print $2}' | sort -u | xargs)  scripts: $(grep -hoE 'VERSION="[0-9.]+"' .github/setup_go_for_*.sh | sort -u | xargs)"
echo "go.mod: lx=$(grep -E '^(go|toolchain) ' go.mod | xargs)  stable=$(git show $S:go.mod | grep -E '^(go|toolchain) ' | xargs)"
diff <(git show $S:go.mod | modlist | sort) <(modlist < go.mod | sort) && echo "require: == stable"
echo "cronet: lx=$(cat .github/CRONET_GO_VERSION)  stable=$(git show $S:.github/CRONET_GO_VERSION)"
echo "NDK:    lx=$(grep -hoE 'ndk-version: *[a-z0-9]+' .github/workflows/lx-*.yml | awk '{print $2}' | sort -u | xargs)  stable=$(git show $S:.github/workflows/build.yml | grep -oE 'ndk-version: *[a-z0-9]+' | awk '{print $2}' | sort -u | xargs)"
echo "JDK:    lx=$(grep -hoE 'openjdk-[0-9]+' .github/workflows/lx-*.yml | sort -u | xargs)  stable=$(git show $S:.github/workflows/build.yml | grep -oE 'java-version: *[0-9]+' | awk '{print $2}' | sort -u | xargs)"
echo "protoc: $(grep -E '^LX_PROTOC' Makefile.lx | tr -s ' ' | xargs)  go.mod: $(grep -E 'google.golang.org/(protobuf|grpc) ' go.mod | xargs)"
echo "base:   upstream.version=$(cat upstream.version)  stable=$(git describe --tags --abbrev=0 $S)"
join -a2 -e '—' -o 0,1.2,2.2 <(git show $S:.github/workflows/build.yml | acts) <(cat .github/workflows/lx-*.yml | acts) | column -t
# for reference — current Go releases (the baseline is still stable):
curl -s 'https://go.dev/dl/?mode=json' | grep -oE '"version": *"go[0-9.]+"' | grep -oE 'go[0-9.]+' | sort -u
```

- Everything matches, or the mismatch is already marked `lx:` with a reason → move on.
- Upstream raised a version → raise ours before the tag; a toolchain or dependency version change
  requires the full release build (section 2b).
- Go shipped a patch that stable is not on yet → by default stay with stable and record the known
  lag in the release's changelog section.
- `require` differs from stable → either an unmerged upstream change (section 3) or a new dependency
  of ours — name it in the changelog.

## 2b. Full release build without publishing (dry run)

`lx-ci` on push builds only native linux; a manual `lx-ci` run adds cross, AAR and musl, but not
Win7, mips or darwin on the macOS runner. Only `lx-release.yml` builds the whole release matrix —
windows including Win7 on the patched toolchain, linux mips, darwin, linux-musl, `libbox` +
`libbox-legacy`. The `dry_run` flag runs it without publishing:

```bash
gh workflow run lx-release.yml --ref lx -f tag=v<next-version>-dryrun -f dry_run=true
gh run list --workflow lx-release.yml --limit 1      # run id
gh run watch <id> --exit-status
```

- The `publish release` job is skipped: no GitHub Release is created and no tag appears on origin
  (the AAR job tags only locally inside the runner, for `git describe`). Artifacts stay on the run.
- In a dry run `tag` only stamps the version into the binaries — use a `-dryrun` suffix so an
  artifact cannot be mistaken for a release.
- **Mandatory** if the toolchain or any section 2a dependency version changed since the last
  release: otherwise the first full build on the new toolchain is the release itself.
- Check which toolchain built each job. The aggregate `gh run view <id> --log` does not return the
  logs of every job, so go job by job:

  ```bash
  gh run view <id> --json jobs --jq '.jobs[] | select(.conclusion=="success") | "\(.databaseId) \(.name)"' |
    while read -r jid name; do
      echo "$name: $(gh api repos/Leadaxe/sing-box-lx/actions/jobs/$jid/logs | grep -oE 'Successfully set up Go version [0-9.]+|Environment: go[0-9.]+' | sort -u | xargs)"
    done
  ```

  `build windows/386` prints an empty line — that is Win7 on its own patched toolchain from the
  cache, whose version is `VERSION=` in `.github/setup_go_for_windows7.sh` (section 2a).
- Make sure nothing was published: `gh release view v<version>-dryrun` → `release not found`, and
  `gh api repos/Leadaxe/sing-box-lx/releases/latest -q .tag_name` still returns the previous tag.

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
- `submodules/wireguard-go`, `submodules/sing-tun`, `submodules/gvisor` and `submodules/utls` — our
  fork submodules. Never accept an upstream bump blindly (including a commit like "Update sing-tun"
  or a `sagernet/gvisor` / `metacubex/utls` bump in `go.mod`): it silently moves `replace` off the
  fork and reverts our patches (AWG obfuscation, SPEC 040 acceptLoop self-heal, SPEC 041 rebind,
  SPEC 048 nil-guard in gvisor's `handleConnecting`, SPEC 086/087 Firefox 148 + key share reuse + Safari 26.3 in
  utls); see `wg-1.14-migration` and the 2026-08-01 sync in the changelog.
  The revert is silent: everything builds, package tests are green, and the bug returns in the
  field — so after any merge that touched `go.mod`, verify all four with `go list -m`:

  ```bash
  go list -m github.com/sagernet/wireguard-go github.com/sagernet/sing-tun github.com/sagernet/gvisor github.com/metacubex/utls
  # each must resolve to => ./submodules/<name>
  ```

  For `utls` the tests guard it too: `go test -tags with_utls ./common/tls/` (`TestLxFirefox…`,
  `TestLxRealityFingerprints…`) fails if `HelloFirefox_Auto` stops being Firefox 148, `HelloSafari_Auto`
  stops being Safari 26.3, or the hybrid share leaves `chrome`/`firefox`/`safari` — i.e. if `replace` slid onto bare metacubex.

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

`fetch upstream → compare merge-base with tip → check dependency versions against stable → if ahead,
merge it in → build+gofmt+lx-check → toolchain or a dependency changed — lx-release dry run →
changelog (+verify the awk; for stable also docs-lx/releases/v<tag>.md) → push branch lx → tag →
verify notes/assets/checksums`.
Drift and dependency versions are checked **every** time, drift **only via merge-base**; merging and
following stable's versions are the default behaviour, and skipping either is a deliberate exception
with a recorded reason.
