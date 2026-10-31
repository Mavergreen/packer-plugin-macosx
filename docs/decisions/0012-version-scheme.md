# 0012 — The version scheme is the family's self-upstream branch

Date: 2026-09-24
Status: accepted

## Context

The family's `mavergreen-conventions` versioning asks first whether a
repository *ports an external upstream* or *is its own upstream*. A port
versions as `<upstream>-mavericks.N`. A self-upstream repository drops the
`-mavericks` suffix and versions itself directly, with `YYYYMMDD.N` as the
family's date form, "precisely because it is not a port";
`mavericks-porthole` is the date instance. (INHERITED from the sibling:
`mavericks-porthole`'s `UPSTREAM_VERSION` held `20260802` and its
`release.yml` computed the version the same way, read 2026-09-22.)

This plugin repackages no one's release. OpenCore, EDK II, QEMU and
Apple's 10.9.5 are ingredients that move independently, and none is *the*
upstream. So it takes the self-upstream branch.

## Decision

**`YYYYMMDD.N`**, as `build/version.sh` computes it. `UPSTREAM_VERSION`
holds a bare eight-digit date, bumped by hand when the plugin itself has
changed enough to ship. The version is `<date>.<n>`, never committed; the
git tag carries it.

Two axes, mirroring `<upstream>-mavericks.N` with this product's own date
line standing in for the upstream release:

| Axis | Moves when | Effect |
|---|---|---|
| The **date** (`UPSTREAM_VERSION`) | a human decides the plugin changed enough to ship. There is no Renovate datasource, because there is nothing external to track | `N` resets to 1 |
| **`N`** | anything else that warrants a release with the plugin's own code unchanged, most importantly an ingredient bump (`INGREDIENTS.md`'s `## Declared state`) | `N+1` on the same date line |

The date is not the release date. It names the version line, and `N`
counts every release cut on it, ingredient-only ones included:
`20260922.4` can mean "the fourth release of the `20260922` line", cut in
November because Renovate moved the OpenCore pin.

## Packer wants semver, and the tag reconciles the two

`packer init` resolves a plugin by a three-component semver tag, and the
SDK's `version.NewPluginVersion` wants the same. So a release tag is
**`v0.<UPSTREAM_VERSION>.<N>`**: `v0.20261005.1` for the family's
`20261005.1`. The tag is `v0.` and the family version, so the two can
never disagree, and `build/version.sh` counts N from those tags alone.
`.goreleaser.yml` reads `{{ .Version }}` off that tag, without the `v`, and
writes it into `version.Version` with `-ldflags`. A plain `go build` says
`0.0.0-dev`; `bin/dev-install.sh` builds a dev build's version, below.

Why a 0 major (decided 2026-10-05, before the first release; the tag was
first specified as `v<UPSTREAM_VERSION>.<N>.0`): a Packer plugin before
1.0 conventionally says so with a 0 major; Go treats a major of 2 or more
as needing a `/vN` module path, and would mark a date-major tag
`+incompatible`; and semver's own reading fits -- a new date line is a
minor bump, which under 0.x may break, and a release that only moves an
ingredient is a patch. A template constrains its plugin to its own line,
`~> 0.<UPSTREAM_VERSION>.1`: any N on that line, never a newer line.

## A dev build is no release's version, past or future

`bin/dev-install.sh` builds **`0.<UPSTREAM_VERSION>.999999-dev`**
(`build/version.sh dev`), and no release is ever cut at an N of 999999 or
above: `build/version.sh` refuses to name one (decided 2026-10-17).

Packer 1.16.1 (read in `packer/plugin-getter/plugins.go`, `command/init.go`
and `hcl2template/plugin.go`) checks a plugin against a template's
constraint by its version without the `-dev`, takes `-dev` as the only
pre-release `plugins install --path` accepts, sorts the installs it finds
in semver order, in which `X-dev` comes before `X`, and runs the last.
`packer init` counts releases alone, so with only a dev build installed
it fetches the newest release the constraint takes. A dev build that
shared a release's version would therefore run that release in its
place, in silence. That happened: built as `0.20261005.8-dev` from 101
commits past `v0.20261005.8`, the build lost to the `v0.20261005.8` that
`packer init` installed beside it, and the run tested the release. A dev
build at the *next* N fails the same way the day that N is released.

At 999999 the dev build is above every release on its line, so `packer
init` may fetch a release beside it but never one that Packer runs over
it, and `~> 0.<UPSTREAM_VERSION>.1` still takes it. The cost is the
reverse case: a dev build left installed is also run over the releases,
so `packer plugins remove github.com/mavergreen/macosx
0.<UPSTREAM_VERSION>.999999` is how a host goes back to them.

**Whatever runs a dev build fails loudly if Packer would run another
plugin.** `bin/dev-install.sh` checks that the binary Packer installed is
byte for byte the one it built, then asks `packer plugins required` for
every template that requires the plugin and fails unless that build is the
last install listed, the one Packer runs. Everything that runs a dev
build installs it with that script: the templates' Go tests, CI's
`template` job and the gate, which also accepts exactly one macosx plugin
in its own plugin directory and compares it with what it built.

## Why not the family's shared scripts

The family's `scripts/version.sh` and `resolve-version.sh` both hardcode
the literal `-mavericks.` of the port shape. `mavericks-porthole` inlines
the equivalent in its `release.yml`; here it is a committed script
instead, because inline YAML cannot be tested and this repository tests it
(`tests/version.bats`).

## Consequences

- **`INGREDIENTS.md`'s `version-scheme:` deviations** name the shape
  taken, the self-upstream branch, and point here. They are scoped to the
  plugin's own globs (`cmd/packer-plugin-macosx/*`, `datasource/*`,
  `internal/*`, `version/*`) so that a deviation on one cannot quietly
  license the rest.
- **A release is a declared state, not an event.** `INGREDIENTS.md`'s
  `## Declared state` lists the inputs whose movement should cut one:
  `UPSTREAM_VERSION` as the single `upstream` entry, the pin registry,
  the OpenSSH component pin and `config.plist`. It is deliberately a
  subset of the full ingredient registry, so that a test tool moving never
  cuts a release.
- **Releases are deliberate, and cut by the family's workflow** (decided
  2026-10-05, before the first release; the first draft cut them by hand
  from a pushed tag, which is not the family's shape).
  `.github/workflows/release.yml` builds and gates on every push and pull
  request, and publishes only when dispatched with `local_release=true` on
  main: `build/version.sh local` names the next N, goreleaser builds, and
  shipyard's `publish-release.yml@v1` mints the tag. Deliberate, because
  this repository is its own upstream; an ingredient bump still cuts N+1,
  through `.github/workflows/repackage-on-ingredient-bump.yml`
  (INGREDIENTS.md).
