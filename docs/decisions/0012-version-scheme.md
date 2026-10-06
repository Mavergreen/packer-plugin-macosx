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
`0.0.0-dev`; `bin/dev-install.sh` builds `0.<UPSTREAM_VERSION>.<N>-dev`,
because Packer matches a `-dev` plugin by its version without the `-dev`
and the templates take `~> 0.<UPSTREAM_VERSION>.1`.

Why a 0 major (decided 2026-10-05, before the first release; the tag was
first specified as `v<UPSTREAM_VERSION>.<N>.0`): a Packer plugin before
1.0 conventionally says so with a 0 major; Go treats a major of 2 or more
as needing a `/vN` module path, and would mark a date-major tag
`+incompatible`; and semver's own reading fits -- a new date line is a
minor bump, which under 0.x may break, and a release that only moves an
ingredient is a patch. A template constrains its plugin to its own line,
`~> 0.<UPSTREAM_VERSION>.1`: any N on that line, never a newer line.

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
- **Releases are cut by hand**, with `goreleaser release --clean` from a
  tag. No workflow publishes them, and there has been no release yet.
