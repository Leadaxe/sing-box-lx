# lx release runbook

> 🌐 Русская версия: **[lx-release-runbook.ru.md](lx-release-runbook.ru.md)**.

The procedure for cutting an lx release or pre-release. The first rule: before any tag, check
whether upstream moved ahead, and by default take its changes.

Repository context:

- `upstream` = `https://github.com/SagerNet/sing-box.git`; we track the `upstream/stable` branch.
  Drift is measured only against `upstream/stable`. `upstream/testing` is upstream's development
  line: the next minor version in alpha. It is always ahead by tens or hundreds of commits (Go
  bumps, immature refactors) and gets rebuilt on top of stable with a force-push. Drift against it
  never reads zero, and a red gate on testing would block releases forever. Zero against
  `upstream/stable` = no drift, the tag can be cut.
  The banner on the fork's page, "N commits behind SagerNet/sing-box:testing", counts from testing,
  upstream's default branch; it is not drift. Do not press **Sync fork**: it merges testing into
  `lx`, and on conflicts it offers "Discard commits", which resets `lx` to upstream.
- Our working branch is `lx`, also the GitHub default. Upstream is integrated with a manual
  `git merge upstream/stable`, not a rebase; see `wg-1.14-migration` in memory and
  [BUILD_CI_CD](../SPECS/FEATURES/001-BUILD_CI_CD/FEATURE.md).
  `lx-rebase.yml` from the same feature describes the old auto-rebase onto stable upstream tags.
  It never force-pushes `lx`; it only opens a PR or an issue.
- `lx-1.14` is the historical 1.14 migration branch. The migration is finished, the branch was
  merged into `lx` (both pointed at the same commit at `v1.14.0-lx.16`) and is kept in origin only
  as an anchor. New work and releases happen on `lx`.
- Prerelease is decided by the tag suffix, not by the branch: `lx-release.yml` passes
  `--prerelease` for `-rc.N` / `-alpha.N` / `-beta.N`. A tag without a suffix publishes as "Latest".
  For the current list of tags run `git tag -l 'v*lx*' --sort=-creatordate | head`; this document
  does not name a version, since that goes stale fastest. The fork cuts stable tags: the
  restriction "while upstream is in alpha, rc line only" was lifted when upstream moved to beta.
- Release notes are assembled by `lx-release.yml` from two sources, in this order of priority:
  1. **`docs-lx/releases/v<tag-without-v>.md`** — hand-written bilingual notes in the LxBox format:
     EN+RU TL;DR, 🇬🇧/🇷🇺 blocks with 🆕/🔧/🐛/🧰 sections. Rules and skeleton are in
     [docs-lx/releases/TEMPLATE.md](releases/TEMPLATE.md). A stable tag requires this file:
     without it CI warns and ships the raw changelog.
  2. Fallback for rc/alpha/beta: the `#### v<tag-without-v>` section of
     [docs-lx/lx-changelog.md](lx-changelog.md).
  The boilerplate (upstream base, collapsed `<details>` about binaries/AAR/build tags, link to the
  previous release) is generated automatically; do not write it into the files. The changelog is
  still kept per tag as an engineering log and must be correct before the tag.

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

Drift in the child fork repositories is resolved before merging the core, not after.
Our `replace` directives in `go.mod` substitute upstream modules with fork submodules
(`wireguard-go`, `sing-tun`, `gvisor`, `utls`). Merging the core raises the versions in `require`, but
`replace` keeps substituting our branch. The build silently runs on code upstream no longer
expects.

### 1.1 Machine check: the fork's tip against `go.mod`

The question is not whether upstream has new commits, but whether our branch contains the commit
`go.mod` requires. You cannot check this by eye: `go build` passes, the tests pass, and the race
shows up at runtime.

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

For `gvisor` the `require` version carries no hash; it is a snapshot. Compare the version string
against the snapshot date in the submodule's history.

For `utls` the pin is a `metacubex/utls vX.Y.Z` tag (currently `v1.9.0-alpha.1`), also without a
hash. The fork's `lx` branch must contain that tag and differ from it by five commits:

- the cherry-pick of refraction 6ebdceb — the Chrome 155 preset, SPEC 118;
- three of ours around it. Before it: ML-DSA signature scheme codepoints only. After it: the
  dropped ML-DSA handshake test and `HelloChrome_Auto` kept at Chrome 133; Chrome 155 is opt-in
  as `chrome_155`;
- ours on top of all: the new file `u_conn_release_lx.go` with `UConn.ReleaseHandshakeState` (SPEC 122).

Firefox 148 with key share reuse and Safari 26.3 (SPEC 086/087) come from metacubex itself since
`v1.9.0-alpha.1`.

```bash
req=$(grep -oE 'metacubex/utls v[0-9a-z.-]+' go.mod | head -1 | awk '{print $2}')
git -C submodules/utls fetch metacubex --tags 2>/dev/null
git -C submodules/utls merge-base --is-ancestor "$req" HEAD && echo "✅ lx contains $req" || echo "❌ DRIFT: go.mod requires $req"
git -C submodules/utls diff --stat "$req" HEAD   # only the Chrome 155 / ML-DSA codepoint / trust_anchors files and u_conn_release_lx*.go
```

metacubex rebuilds its history between tags: `v1.9.0-alpha.1` does not descend from `v1.8.7`. So
`git log "$req..HEAD"` also lists the fork's older line and does not work as a check — compare
trees. An upstream `metacubex/utls` bump means re-grafting:

1. a side branch from the new tag;
2. the same commits cherry-picked on top, dropping those the tag already carries;
3. one commit on the fork's `lx` whose tree is that side branch and whose parents are the old
   `lx` and the side branch.

The fork's `lx` is not force-pushed, and old gitlinks stay reachable. metacubex accepts no
external PRs, so the sync is ours alone. The condition for dropping the fork is in SPEC 086/087/118.

### 1.2 Take the whole upstream line, not selected commits

⚠️ Do not cherry-pick "the commits the compiler complains about." An upstream line is meant to
work as a whole. The commits you skip will almost certainly include race fixes and locking
refactors: they produce no compile errors, but they do produce intermittent runtime failures.

That is how `v1.14.0-lx.20-rc.5` broke: of 14 missing `wireguard-go` commits, 3 were taken, picked
by `undefined: device.PeerLookupFunc`. Skipped were `15b912c device: fix TOCTOU race during
session state update` and `2ad9837 device: refactor container locking for lock-order clarity`.
The result was a state that never existed upstream: green build, green unit tests, and on the
device a nil panic in `udpNat.Start()` when a WireGuard endpoint started, every other time.

⚠️ A fast-forward of the fork is impossible: upstream branches contain none of our lx commits,
and updating would wipe the AWG obfuscation and SPEC 041. The correct path is to carry our patches
on top of the full upstream line (re-graft), not to pull upstream fragments onto our old base.

The `wireguard-go` fork's branches mirror the superproject: `lx` is the main line, `lx-1.15` the
1.15 line; the submodule pin is always the tip of its branch. A re-graft produces a history
unrelated to `lx`, so it lands in `lx` through a `-s ours --allow-unrelated-histories` merge made
from the new line (the tree is the new line, the old commits stay in history) rather than in a new
branch. The old `lx-awg2-v003`/`v005`/`v007` are an archive; do not commit there.

⚠️ After a re-graft, bump `upstream.version` (at the root, next to `go.version`). It is the base
for `LX_VERSION` in a local `make -f Makefile.lx lx-build`. Release CI takes the version from the
tag name and never reads this file. So a forgotten bump will not surface in the release; it only
breaks builds for users following the README.

The check is manual only: there is no CI gate and there cannot be one, because there is nothing
to compare the pin against. The upstream version is not recorded in the tree: `constant/version.go`
is `"unknown"`, and the version comes from ldflags — that is our zero-diff. Upstream tags in the
fork are incomplete: `origin` carries only old pure-upstream ones (≤ `v1.13.11`), while `v1.13.13`
and `v1.14.0-beta.*` exist solely in local clones where someone ran `git fetch upstream --tags`.
A tag-based gate was tried (commit `5c621c089`, reverted in `d7fa017a8`): green locally, red in
CI. CI reported `1.13.11`, the version a user with a clean clone sees. So after a re-graft, verify
this item by eye.

### 1.3 The second class of drift: API absent from the fork's base

A merge can pull in an external dependency that needs API from a newer submodule. The core builds,
the AAR does not, and you find out from CI.

The case from SPEC 051: upstream raised `tailscale` 1.92 → 1.102, which required
`device.PeerLookupFunc`/`NewPeerConfig`/`SetPeerLookupFunc` from a newer `wireguard-go`. The chain:
`libbox/native_shell_session.go` → `protocol/tailscale/tailssh` → `wgengine/wgcfg` →
`wireguard-go/device`. Build tags do not avoid it: `tailssh` is gated on `with_gvisor`, which is
always on.

### 1.4 After closing the drift — a device run is mandatory

Builds and unit tests do not catch this class of bug (proven on rc.5). If a submodule was
touched, a live run is required before the tag: the tunnel comes up, DNS resolves, URL-test
measures, WG/AWG nodes are alive. Repeat the run several times in a row, because races float.

## 2. Check whether upstream moved ahead (MANDATORY before every release)

The baseline is `upstream/stable`, and only that. Drift is not measured against
`upstream/testing`: it is upstream's development branch, always ahead, and will never read zero.
Measured 2026-08-14: stable — ahead=0, testing — ahead=233 from its own merge-base; 2026-09-14,
after testing was rebuilt on top of stable, — 21. The number moves on its own and is quoted only
as an order of magnitude. A gate on testing is permanently red and blocks releases for no reason.

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

⚠️ Do not measure drift from `<our-merge-commit>^2`. Upstream branches are regularly
force-pushed, so the second parent of our merge commit points at rewritten history.
`git log/diff <merge>^2..upstream/stable` shows garbage, including our delta in reverse
("upstream deleted `option/platform.go`"), which never happened. On release `v1.14.0-lx.16`
(2026-07-26) this looked like "8 new upstream commits" although all 8 were already merged and
merge-base matched the tip. The only reliable signal is merge-base. Compare commits by subject
(`git log --format=%s`), not by hash: after a force-push the hashes differ.

If merge-base has moved because a force-push landed after your merge, compare by subject, not by
hash. That happened on 2026-07-30: right after merging 235 commits, drift reported "210 ahead".

```bash
comm -23 <(git log --format=%s $(git merge-base lx upstream/stable)..upstream/stable | sort) \
         <(git log --format=%s -260 lx | sort)
```

Whatever remains in the output is new. Back then, 5 of the 210 "new" ones remained.
Take such a tail with `cherry-pick`, not with a second merge: a second merge re-raises the
already-resolved conflicts (49 of them in that case) against a stale base.

- **merge-base == tip / 0 commits ahead** → upstream is in sync, move on to build and tag (section 4).
- **>0 commits** → by default, take and merge (section 3). Deferring is allowed only deliberately
  and with a recorded reason: for example, an upstream commit breaks our layer and needs its own
  investigation. Then record it in the release's changelog section so the drift is visibly known.

The longer drift accumulates, the more expensive and risky the merge: conflicts in `.pb.go`, in
the wireguard-go and sing-tun fork submodules, adapter interface changes. That is why upstream's
changes are taken by default: small frequent merges are cheaper than one big one before a release.

## 2a. Check dependency versions — not just commits (MANDATORY before every release)

Zero drift by merge-base does not yet mean we build with what upstream builds with. A merge brings
upstream's own files (`build.yml`, `go.mod`, `.github/CRONET_GO_VERSION`, the toolchain scripts).
Our pins stay as they were: `go.version`, NDK and JDK in `lx-*.yml`, the protoc plugins in
`Makefile.lx`. The mismatch accumulates silently: after the 2026-09-05 stable merge, upstream built
with Go 1.26.7 while we stayed on 1.26.6 until 2026-09-14.

The baseline is `upstream/stable`. Every mismatch is either fixed before the tag or kept
deliberately. In that case the reason goes into an `lx:` comment next to the pin (like
`ndk-version: r28c` in `lx-release.yml`) and into a line of the release's changelog section.

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

`lx-ci` on push builds only native linux. A manual `lx-ci` run adds cross, AAR and musl, but not
Win7, mips or darwin on the macOS runner. Only `lx-release.yml` builds the whole release matrix:
windows including Win7 on the patched toolchain, linux mips, darwin, linux-musl, `libbox` +
`libbox-legacy`. The `dry_run` flag runs it without publishing:

```bash
gh workflow run lx-release.yml --ref lx -f tag=v<next-version>-dryrun -f dry_run=true
gh run list --workflow lx-release.yml --limit 1      # run id
gh run watch <id> --exit-status
```

- The `publish release` job is skipped: no GitHub Release is created and no tag appears on origin.
  The AAR job tags only locally inside the runner, for `git describe`. Artifacts stay on the run.
- In a dry run `tag` only stamps the version into the binaries. The `-dryrun` suffix keeps an
  artifact from being mistaken for a release.
- The dry run is mandatory if the toolchain or any section 2a dependency version changed since the
  last release. Otherwise the first full build on the new toolchain is the release itself.
- Check which toolchain built each job. The aggregate `gh run view <id> --log` does not return the
  logs of every job, so go job by job:

  ```bash
  gh run view <id> --json jobs --jq '.jobs[] | select(.conclusion=="success") | "\(.databaseId) \(.name)"' |
    while read -r jid name; do
      echo "$name: $(gh api repos/Leadaxe/sing-box-lx/actions/jobs/$jid/logs | grep -oE 'Successfully set up Go version [0-9.]+|Environment: go[0-9.]+' | sort -u | xargs)"
    done
  ```

  `build windows/386` prints an empty line: that is Win7 on its own patched toolchain from the
  cache. Its version is `VERSION=` in `.github/setup_go_for_windows7.sh` (section 2a).
- Make sure nothing was published: `gh release view v<version>-dryrun` → `release not found`, and
  `gh api repos/Leadaxe/sing-box-lx/releases/latest -q .tag_name` still returns the previous tag.

## 3. Take upstream's changes (merge, then build) — and ONLY then release

```bash
git checkout lx
git merge upstream/stable             # manual merge, NOT rebase
```

On conflicts, these are the zones we touch most often. The rule for all of them: keep lx
semantics, accept upstream logic.

- `daemon/*.pb.go` / `*.proto` — our fields are additive (`detourList=23`, DnsQueryEvent 1..12). If
  upstream regenerated the descriptors, regenerate via `make -f Makefile.lx lx-proto` and re-apply
  the lx fields. Or do it by hand: see `lx-commandclient-extensions` in memory (pinned protoc
  toolchain).
- `submodules/wireguard-go`, `submodules/sing-tun`, `submodules/gvisor` and `submodules/utls` — our
  fork submodules. Never accept an upstream bump blindly, including a commit like "Update sing-tun"
  and a `sagernet/gvisor` or `metacubex/utls` bump in `go.mod`. Such a bump silently moves `replace`
  off the fork and reverts our patches: AWG obfuscation, SPEC 040 acceptLoop self-heal, SPEC 041 rebind,
  SPEC 048 nil-guard in gvisor's `handleConnecting`, SPEC 086/087 Firefox 148 + key share reuse + Safari 26.3 in
  utls. See `wg-1.14-migration` and the 2026-08-01 sync in the changelog.
  The revert is silent: everything builds, package tests are green, and the bug returns in the
  field. So after any merge that touched `go.mod`, verify all four with `go list -m`:

  ```bash
  go list -m github.com/sagernet/wireguard-go github.com/sagernet/sing-tun github.com/sagernet/gvisor github.com/metacubex/utls
  # each must resolve to => ./submodules/<name>
  ```

  For `utls` the tests guard it too: `go test -tags with_utls ./common/tls/` (`TestLxFirefox…`,
  `TestLxRealityFingerprints…`) fails if `replace` slid onto bare metacubex. The signs:
  `HelloFirefox_Auto` stops being Firefox 148, `HelloSafari_Auto` stops being Safari 26.3, or the
  hybrid key share leaves `chrome`/`firefox`/`safari`.

  `submodules/gvisor` is maintained as a snapshot of the pin without history: upstream's full
  history is 1.45 GB per CI clone. A new pin lands as a new snapshot commit, the patch is applied
  on top, and the red/green test travels with it. Details in SPEC 048 §6.
- `cmd/internal/build_libbox/main.go` — the only upstream-file edit in the CI zone, marked `// lx`.
- `common/dialer/dialer.go` / `default.go` — if upstream changed which dial fields `NewWithOptions`
  applies under `detour`, update the list of SPEC 165 Д1 (`box_lx_detour.go`,
  `ignoredUnderDetour`, `readByProtocol`). A stale list only misreports `GetOutboundConfig`;
  behaviour stays upstream's.
- `box.go`, `dns/client*.go`, `route/route.go`, `common/trafficcontrol/tracker.go` — these carry lx
  observability on top of upstream logic. On conflict keep upstream resolve and routing behaviour;
  our emit/Detour bits are additive (see the purity audit, commit 3505beb6).

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
release's changelog section, as in `b8ff5c78`: "rc.6 also carries the upstream alpha.35 merge".

## 4. Update the changelog and release notes, then cut the tag

1. Append a section to [docs-lx/lx-changelog.md](lx-changelog.md). The heading must be exactly
   `#### v<tag-without-v>`, e.g. `#### v1.14.0-lx.16`. For rc/alpha/beta `lx-release.yml`
   extracts that section into the release notes via `awk` when the file from step 1b is absent.
   A wrong or missing heading silently yields empty or foreign notes.
   Promoting an rc line to stable needs its own section, not a reuse of the last rc's: tag
   `v1.14.0-lx.16` needs a `#### v1.14.0-lx.16` section summarizing rc.1–rc.N.

   1b. For a stable tag, write `docs-lx/releases/v<tag-without-v>.md` per the rules and skeleton
   in [TEMPLATE.md](releases/TEMPLATE.md): a bilingual TL;DR plus 🇬🇧/🇷🇺 blocks with
   🆕/🔧/🐛/🧰 sections. Item headings are the symptom or benefit as the user sees it; rc.1–rc.N
   fold into one story per topic. Draft the file along the rc line; before the tag, proofread it
   rendered and remove the draft comment. When the file exists it fully replaces the changelog
   section in the release body, for rc tags too.

   Verify the fallback extraction before tagging, with the same code CI uses:

   ```bash
   VERSION=1.14.0-lx.16   # the tag without the leading v
   awk -v v="#### v${VERSION}" '$0==v {f=1; next} /^#### / {f=0} f' docs-lx/lx-changelog.md
   ```

   Empty output → the notes ship as a stub. Capturing neighbouring `####` → foreign sections land in
   the notes.
2. Commit the branch and push it to origin before the tag. Otherwise the tag ends up ahead of the
   branch, as happened on rc.1. For the inline token see `git-push-auth-gh-token` in memory.
3. Create and push the tag. CI `lx-release.yml` builds the desktop archives and the AAR (`libbox` +
   `libbox-legacy`) and publishes the release with generated notes. The tag suffix decides
   prerelease or stable: `-rc./-alpha./-beta.` → prerelease, no suffix → stable "Latest".
4. Check the run: `gh run list --workflow lx-release.yml`, wait for `completed success`. Then
   confirm the release has the AAR, the notes match their source (the release file, or the
   changelog section for rc), the `<details>` blocks rendered, and the "Previous release" footer
   points at the right tag.

## 5. Post-release sanity

- `gh release view v<tag>`: assets in place, the prerelease flag matches the tag suffix, notes
  correct. For a stable tag additionally:
  `gh api repos/Leadaxe/sing-box-lx/releases/latest -q .tag_name` must return this tag.
- Links in the notes point at branch `lx`: `/blob/lx/...` is hardcoded in `lx-release.yml`. If
  `lx` lags behind the released commit, links to new files return 404. At `v1.14.0-lx.16` the
  branch was 28 commits behind, and the links to `SPECS/FEATURES/013-DNS_GROUP` would have been
  broken. To check:

  ```bash
  grep -ohE 'https://github.com/Leadaxe/sing-box-lx/blob/[^)"]+' <(gh release view v<tag> --json body -q .body) \
    | sort -u | while read -r u; do echo "$(curl -s -o /dev/null -w '%{http_code}' -L "$u")  $u"; done
  ```

  Work happens directly on `lx`, so a divergence is only possible if the tag was cut from
  another branch.
- Download one archive, verify its checksum against `SHA256SUMS`, and run the binary.
  `sing-box version` must show the tag's version, the same revision, and the full build-tag set.
  In desktop archives `with_clash_api` must be present; only the AAR drops it. See
  `desktop-keeps-clash-api-aar-drops`.
- For observability and attribution features (the DNS stream, Detour) a device verification is
  mandatory. Builds and proto round-trips do not catch registry-key, fast-path-hijack or ctx-timing
  bugs (the §180/§180-2 history). See `lx-spec018-dns-query-stream` in memory.

---

### In one line

`fetch upstream → compare merge-base with tip → check dependency versions against stable → if ahead,
merge it in → build+gofmt+lx-check → toolchain or a dependency changed — lx-release dry run →
changelog (+verify the awk; for stable also docs-lx/releases/v<tag>.md) → push branch lx → tag →
verify notes/assets/checksums`.
Drift and dependency versions are checked every time, drift only via merge-base. Merging and
following stable's versions are the default behaviour; skipping either is a deliberate exception
with a recorded reason.
