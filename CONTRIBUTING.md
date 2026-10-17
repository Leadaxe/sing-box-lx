# Contributing to sing-box-lx

> 🌐 Русская версия: **[CONTRIBUTING.ru.md](CONTRIBUTING.ru.md)**.

Thank you for your interest in the project. This document explains which proposals are
accepted here, the path every feature takes, and what a pull request must contain to be
considered. Read it before writing code: most closed PRs were closed not for code quality
but for skipped steps.

## 1. What this project is and how it differs

sing-box-lx is a **thin downstream** of upstream [SagerNet/sing-box](https://github.com/SagerNet/sing-box):
upstream plus a small, pre-agreed set of features, and nothing else. The full index is
[SPECS/FEATURES/](SPECS/FEATURES/README.md).

The project's main value is **consistency with upstream**. Every change is judged by how
cheaply it survives the next merge of `upstream/stable`. Three things follow from this and
set us apart from forks with a "take everything" philosophy:

- **Security.** The core ships in [LxBox](https://github.com/Leadaxe/LxBox), which is
  published on [F-Droid](https://f-droid.org/en/packages/com.leadaxe.lxbox/) and audited
  there. The audience is promised full transparency: a dependency whose behaviour cannot be
  read and verified in this repository cannot live here.
- **Reliability.** A feature is done not when the code compiles, but when it has passed a
  test stand against the real protocol implementation, negative scenarios included.
- **Controllability.** The feature set is bounded, each feature is described as a black box
  in its `FEATURE.md`, and every edit to an upstream file is marked and carried over by hand
  on each merge.

The immutable principles are in [SPECS/CONSTITUTION.md](SPECS/CONSTITUTION.md). This
document does not replace them; it retells them for an outside contributor.

## 2. The path of a feature: from idea to release

Every feature goes through the same steps, in this order. Code is second to last.

```
issue → spec (FEATURE + TASKS) → stand → code → acceptance on the stand → release
```

1. **An issue with the case.** Who needs the feature, why upstream does not provide it, and
   why carrying it here is cheaper than the alternatives. These are the criteria of
   Constitution §3.1, all mandatory. "Would be nice" does not pass.
2. **A spec.** `SPECS/FEATURES/NNN-NAME/FEATURE.md` is written before the code and describes
   the feature as a black box: purpose, parameters, inputs, outputs, data flow, guarantees,
   boundaries. The scope is then cut into tasks `SPECS/TASKS/NNN-NAME` with
   `SPEC.md → PLAN.md → TASKS.md`. The methodology is in [SPECS/README.md](SPECS/README.md).
3. **A stand.** A reproducible setup in `lx-test/<name>` against the official protocol
   implementation: test scenarios, negative cases, acceptance criteria. The stand is
   described before the code, because it is the definition of "works".
4. **Code.** Following the rules in section 4 below.
5. **Acceptance.** The stand passes, the DoD from
   [SPECS/IMPLEMENTATION_PROMPT.md](SPECS/IMPLEMENTATION_PROMPT.md) is met,
   `IMPLEMENTATION_REPORT.md` is filled in, the task status is set.

The reference example of the whole path is TrustTunnel:
discussion [#46](https://github.com/Leadaxe/sing-box-lx/issues/46),
feature [018](SPECS/FEATURES/018-TRUSTTUNNEL/FEATURE.md),
tasks [133](SPECS/TASKS/133-TRUSTTUNNEL_OUTBOUND/SPEC.md) and
[134](SPECS/TASKS/134-TRUSTTUNNEL_INBOUND/SPEC.md),
stand [lx-test/trusttunnel](lx-test/trusttunnel/README.md),
protocol write-up [docs-lx/trusttunnel-protocols-explained.md](docs-lx/trusttunnel-protocols-explained.md).

## 3. Where to start

### I want a new protocol or feature

Open an issue and answer these questions in it:

- **Audience.** Who needs it: client or server side, which apps and servers it is used
  with. A field request from a user or a task from one of the core's consumers (the
  launcher, LxBox, routers) weighs more than a general "it is popular".
- **Upstream.** Why the capability is not in upstream and is not expected. A link to the
  upstream issue, if there is one.
- **Known problems.** What is known about the protocol: its authors' issues, behaviour
  under DPI, incompatibilities between versions and implementations.
- **Stand.** Which server we will test against and how we will accept. Are there public
  servers, or are you ready to bring up a test one.
- **Feedback.** Who will test it and where after release, so the feature is not built for
  the drawer.
- **Config continuity.** How it is configured in other implementations (mihomo, husi,
  sing-box-extended, the official client) and how a config will migrate.

The project owner decides in that issue whether the feature joins the set. A PR without an
agreed issue is closed regardless of code quality.

### I want to fix a bug

Open an issue using the bug report template, with the config, version and log. If the bug
is in upstream code, a local patch is acceptable only when reproduced in real practice (a
device, a router, a stand with a live server), with a test and a **removal condition**: the
upstream event after which the patch is dropped. The registry is
[HOTFIXES](SPECS/FEATURES/004-HOTFIXES/FEATURE.md).

### I want to help with acceptance

This is the most valuable help. Every feature has a stand and a list of what is not yet
verified: run it on your servers and clients and report in the feature's issue. Report
fields: platform, core version, config without secrets, what was expected, what happened.

## 4. Code rules

In short; details and exceptions are in [CONSTITUTION §3.2–3.3](SPECS/CONSTITUTION.md).

- **Everything new goes in new files and packages.** Upstream files are not extended;
  neighbours are added next to them: `*_lx.go` or a package of its own.
- **A build tag** wherever a feature adds a subsystem, a dependency or changes default
  behaviour. The pattern is `include/<feat>.go` under the tag and `include/<feat>_stub.go`
  with a clear error without it, like upstream's `wireguard`. The canonical tag set is
  `LX_TAGS` in `Makefile.lx`. The invariant: a build without our tags and a config without
  our fields behave exactly like upstream, byte for byte.
- **Edits to upstream files** only where there is no other way (dispatchers, option
  structs, constants, `go.mod`), each wrapped in a marker:
  ```go
  // lx:begin <feature>
  ...
  // lx:end <feature>
  ```
  Such edits go in **separate atomic commits**, apart from the new code.
- **A protocol is a package of its own, not a wrapper over someone else's library.** The
  protocol logic must be readable and testable here. A dependency is acceptable only if it
  is already in upstream or is one of the project's four fork submodules.
- **Lifecycle per the upstream contract:** `Start(stage, scope)`, no connections started in
  the constructor; `Close` releases everything `Start` opened.
- **Server responses and errors are surfaced** with the code and the cause, not swallowed.
  A wrong password must be a readable dial error, not silence.
- **The config schema** is checked against the established implementations, and a key
  mapping table goes into the docs. Empty or clearly invalid mandatory fields are rejected
  at start.
- **Tests** for every new package. `gofmt` clean, `go vet ./...` green, `go.sum` without
  unrelated changes.
- **The Go module path stays `github.com/sagernet/sing-box`**, the binary is named
  `sing-box`. The fork's identity lives only in the version.

## 5. What a pull request must contain

A PR is considered when:

- it references an agreed issue and a task `SPECS/TASKS/NNN-NAME`;
- it carries one feature or one fix, not several protocols at once;
- it builds without tags (`go build ./...`) and with tags (`make -f Makefile.lx lx-build`);
- `go vet ./...` and `go test` of the affected packages are green;
- `./sing-box check -c <config>` accepts the sample config from `lx-test/config`;
- edits to upstream files are marked `// lx:` and sit in separate commits;
- the stand result is attached: what was run, against what, what came out;
- `FEATURE.md`, `SPEC.md`, `TASKS.md` and the documentation in `docs-lx/` are updated.

Commit format: `lx(<feat>): <what was done>`, in English, in upstream style. Commit order
in the delta: new files → `// lx:` edits → `go.mod`/submodules → tag wiring and CI.

## 6. Language

- Issues, discussions and replies: Russian or English, whichever suits the author.
- Code, comments, commits: English, in upstream sing-box style.
- The fork's specs and documentation: Russian. Features for an outside audience (example:
  TrustTunnel) are written in English with a Russian `.ru.md` copy next to them; both are
  edited together.

## 7. What will not happen here

To save everyone's time:

- features without an agreed issue and spec;
- wrappers over third-party protocol libraries;
- several protocols in one PR;
- edits to upstream files without markers or outside separate commits;
- a server side where the client feature is complete without it (the server side is in
  scope as a second priority and yields to the client side);
- "improvements" to upstream behaviour that upstream did not ask for and that break the
  "no tags = upstream" invariant.

If you are not sure whether an idea fits, open an issue and ask before writing code. It is
cheaper for everyone.
