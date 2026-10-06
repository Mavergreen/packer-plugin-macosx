# Ingredients

Every input baked into what this project produces: where it is pinned, its
Renovate status, and what a bump does.

An ingredient with neither a tracker nor a written reason is a silent
staleness hole, so every row below has one or the other. Where no clean
datasource exists, the row says so and says what compensates — a fragile
tracker invented to fill a cell (scraping a download page for a date) is
worse than an honest blank.

The pin registry is `assets/pins/sources.tsv`: name, URL, sha256. The
checksum is the identity; the URL is only how to get it. The plugin embeds
the registry, so a plugin binary carries the pins it was built with.

## Declared state

A release is the realisation of a declared state, not the side effect of a
push. These are the inputs whose movement should cut one. Deliberately a
SUBSET of the full ingredient registry (`## The registry`, below): `bats`
moving must never cut a release.

- upstream: UPSTREAM_VERSION
- pins: assets/pins/sources.tsv
- openssh: components/openssh/version
- opencore-config: assets/firmware/config.plist

## What a bump does here is not what it does in a sibling

**This is the one place this repository genuinely differs from the family,
and it is worth its own section.**

Every sibling ships a **built artifact**. An ingredient moves, a release
is cut, the rebuilt `.pkg` supersedes the old one, and what is published
is never stale for long.

We ship a **recipe**: the plugin and the template. Our release contains no
image, and it cannot, because the image is made of Apple's operating
system and this project never publishes that (`README.md`,
`docs/decisions/0007`, and `bin/no-apple-bytes.sh`, which enforces it).

A moved pin changes what the recipe builds. What it does to things already
built is quieter: **every guest and box already on disk stops being what
the repository would build.** Renovate moves the OpenCore pin; a box built
last week still boots, and simply no longer means what a build from this
commit would mean. Nothing fails and nothing goes red.

### What we chose

**Every output is filed under the inputs that made it, so a moved pin is
a rebuild, never a silent reuse.**

1. Each data source keys its output in Packer's cache by the digest of a
   **listing** of its inputs, one row per pin, patch, embedded asset and
   setting (`internal/inputs`, `internal/store`; `docs/decisions/0006`).
   `mavericks-firmware`'s listing names every boot-stack pin, each patch,
   `config.plist` and the compiler; `mavericks-media`'s names the ESD pin,
   the OpenSSH release and every update package's pin. A bumped OpenCore
   pin therefore changes the firmware's listing, and the next build
   rebuilds the firmware instead of reusing the one in the cache.
2. The stored output keeps its listing as `inputs`, so **what an output
   was made of is written down beside it**, row by row, while the cache
   holds it. Diffing two outputs' `inputs` names the ingredient that
   differs.
3. Each listing carries a `recipe` row for the plugin's own code, bumped
   whenever that code changes what an output holds, so a newer plugin does
   not reuse an older plugin's output either.

What this does not do: a finished box does not carry the listings, so
"is this box still made of what the repository is made of?" has no answer
from the box alone. Rebuilding answers it.

### `repackage-on-ingredient-bump`: no caller declared

The family's caller exists to cut a release when an ingredient moves, so
that what is published picks up the bump. What gets published here is the
recipe, and a moved pin changes what that recipe builds: someone who
installs last month's plugin builds last month's OpenCore from then on.
That is exactly the staleness the caller exists to catch, so it applies in
principle.

**No caller is declared, because no release workflow exists.** Releases
are cut by hand (`docs/decisions/0012`). `check-ingredient-pins.sh` runs in
CI anyway (`.github/workflows/ci.yml`); it passes trivially for a
repository declaring no caller, and is wired so that the day a caller
lands, the gate is already watching it.

The `sparkle-updater:` deviations in `## Conformance deviations` below are
untouched by any of this: they were never an argument about publishing.

## The registry

| Ingredient | Pinned in | Renovate | On bump |
|---|---|---|---|
| **OpenSSH for the guest** (`Mavergreen/openssh`, two product archives per release) | `components/openssh/version` (tag `<upstream>-mavericks.N`) | ✅ `github-releases` on `Mavergreen/openssh`, with a `regex:` versioning that captures `N` — default versioning coerces `-mavericks.N` away, every release then compares equal and the pin never moves again | Future guests install the new OpenSSH. No checksum to update: `mavericks-media` reads the asset names *and* their checksums out of the pinned release's own `SHA256SUMS`, and lists both in its inputs, so the bump is self-verifying and a renamed asset prefix cannot 404 across it |
| **OpenCore** (`acidanthera/OpenCorePkg`, built from source — Tier 0) | `assets/pins/sources.tsv` `opencorepkg-src` | ✅ `github-tags` on `acidanthera/OpenCorePkg`. **Automerge off** — see below | Rebuilds the boot stack. The `sha256` column must be re-pinned in the same commit, which `bin/verify-changed-sources.sh` enforces. `docs/decisions/0003`'s build-path limits must be re-measured |
| **EDK II / audk** and its twelve submodules (Tier 0) | `assets/pins/sources.tsv` `audk-*` | ❌ **untrackable as pinned.** Each is a GitHub archive tarball of a bare commit with no ref beside it, so there is no `currentValue` for a `git-refs` manager to move, and a digest-only manager would rewrite the URL while leaving a checksum it cannot compute. **Compensates:** every one is checksummed, `mavericks-firmware` refuses a URL naming a commit other than the one `internal/firmware/pins.go` declares, and they move only when the OpenCore pin does — `acidanthera/audk` is OpenCore's own build tree, not an independent upstream | Only ever bumped deliberately, together with OpenCore |
| **ocbuild `efibuild.sh`** (Tier 0, build script) | `assets/pins/sources.tsv` `ocbuild-efibuild` | ❌ **untrackable.** A `raw.githubusercontent.com` URL at a commit on `master`; there are no releases and no tags to track. Upstream's `build_oc.tool` fetches it off `master` and `eval`s it. **Compensates:** pinned commit and checksum, and `assets/firmware/patches/0001-*` makes `build_oc.tool` read the pinned copy rather than fetch one | Deliberate, alongside OpenCore |
| **Lilu** (SMC injection, Tier 1 release binary) | `assets/pins/sources.tsv` `lilu-release` | ✅ `github-releases` on `acidanthera/Lilu`, with an `autoReplaceStringTemplate` — the version appears **twice** in one URL and a manager that rewrote only the captured occurrence would leave a half-updated URL that 404s. **Automerge off** | A new kext in every future guest. Re-pin the checksum; re-read `assets/firmware/README.md`, which records how each version's 10.9 (Darwin 13) support was checked |
| **VirtualSMC** (Tier 1 release binary) | `assets/pins/sources.tsv` `virtualsmc-release` | ✅ as Lilu | as Lilu |
| **`assets/firmware/config.plist`** (OpenCore's configuration) | ours, in-tree | n/a — we author it | Changes how every future guest boots. Its sha256 is a row of the firmware's input listing, so a change rebuilds the EFI image |
| **Apple's `InstallESD.dmg`, 10.9.5** | `assets/pins/sources.tsv` `apple-installesd-10.9.5` | ❌ **untrackable, and deliberately so.** Apple publishes no feed, and the pin names one immutable build: 10.9.5 is 10.9.5 forever. **Compensates:** the transfer is plain HTTP after an `osrecovery.apple.com` AssetToken handshake, so the checksum is the only integrity there is, and `mavericks-installesd` enforces it | Never bumps. **Never enters the repository, in any form** — `bin/no-apple-bytes.sh` is the gate |
| **Apple's 10.6.8 combo update and Security Update 2013-004**, the last ones shipped for 10.6 (`snowleopard-media`'s `updates = "security"`, the default) | `assets/pins/sources.tsv` `apple-*-combo-10.6.8`, `apple-secupd-2013-004-snowleopard` | ❌ **untrackable, and deliberately so**, as for 10.9's: frozen artifacts of a discontinued product line. **Compensates:** each sha256 is the identity, verified on every fetch, and the pins are rows of the media's input listing | Never bumps. **Never enters the repository** -- fetched at build time from Apple's CDN on the user's machine |
| **A retail Mac OS X 10.6 install disc's packages**, build 10A432 (the user's own disc image; `snowleopard-installer`) | `assets/pins/snowleopard-packages.sha256` | ❌ **untrackable: a fixed retail disc.** Apple pressed 10A432 once; there is nothing to watch. **Compensates:** the plugin never fetches a disc -- the user supplies one -- and `internal/disc.Verify` holds every package on it to these sums, so any faithful copy passes and anything else is refused by name | Never bumps. A disc of another build (10D573, the 10.6.3 retail) is a new section of the file, read the same way. **Never enters the repository** -- only its checksums do |
| **Apple's Security Update 2016-004**, the last one shipped for 10.9 (`updates = "security"`, the default) | `assets/pins/sources.tsv` `apple-secupd-2016-004` | ❌ **no datasource, and none is needed.** A frozen artifact from a discontinued product line: Apple shipped 10.9's last security update in 2016 and is not going to ship another. A `regex` manager scraping a CDN path for a date would be a fragile tracker invented to fill a cell. **Compensates:** the sha256 is the identity and `mavericks-media` verifies it on every fetch; `bin/verify-changed-sources.sh` re-verifies any row whose URL moves; the pin is a row of the media's input listing. **What would actually go stale is watched by hand:** the claim "2016-004 is the last one" was wrong once (the briefs said 2016-001) and was corrected by asking a live guest; `docs/open-questions.md` Q1 records how, so the next person re-checks rather than inherits | Never bumps. **Never enters the repository** — fetched at build time, from Apple, on the user's machine; `bin/no-apple-bytes.sh` is the gate. **Never by `softwareupdate`**, which would reach Apple's servers during the build |
| **Safari 9.1.3** and **iTunes 12.6.2** (`updates = "all"`; iTunes is one softwareupdate product made of five flat packages, so six pins) | `assets/pins/sources.tsv` `apple-safari-9.1.3`, `apple-itunes-12.6.2-*` | ❌ **no datasource, as above, and for the same reason.** Frozen 2016/2017 artifacts for an OS that stopped receiving them. Opt-in, because they are applications rather than the operating system | Never bumps. The five iTunes pins move together or not at all — they are one product, installed in the order `internal/fetch/updates.go` gives |
| **`iBooksDelta-1.0.1`, `RemoteDesktopClient-3.8.4`** — offered to a live 10.9.5 guest by Apple, **not pinned, not installed, not findable** | **nowhere.** Deliberately, and this row is why | ❌ **untrackable AND unfetchable.** A 10.9.5 guest's own `softwareupdate -l` offered five items on 2026-09-21; three are in `index-10.9.merged-1.sucatalog` and these two are not — not in its package URLs, and not in any of its 333 distribution files, all of which were fetched and searched. There is no URL to pin, so there is nothing for a checksum to be the identity of. **Recorded here rather than omitted**, because "we could not find it" is a reason and "we did not mention it" is not. **Compensates:** nothing, and that is the honest answer; `docs/open-questions.md` Q1 says to ask the guest again rather than guess | Nothing. If one is ever located, it becomes an `assets/pins/sources.tsv` row like its three siblings |
| **Packer's `qemu` and `vagrant` plugins** | `templates/mavericks/mavericks.pkr.hcl` `required_plugins` (`~> 1` each) | ✅ a `regex` manager on the template's `source`/`version` pairs, `github-releases` on `hashicorp/packer-plugin-*`. CI's `template` job validates the template against them, so a bump that breaks it is red on its PR | `packer init` resolves the newest release inside the constraint. They build and package the guest; none of their bytes reaches the guest disk |
| **Go modules**, `packer-plugin-sdk` above all | `go.mod`, `go.sum` | ✅ Renovate's native `gomod` manager | A new plugin binary. CI runs the tests, staticcheck and every cross-build |
| **QEMU, on the host** | not pinned | ❌ **unpinnable by us.** It is the user's, from their platform. Pinning a QEMU would mean shipping one. **Compensates:** the device findings are measured on QEMU 8.2.2, 11.0.2 and 11.1.1 (`docs/host-profile.md` G6, G13, G16), and `docs/test-hosts.md` records each host's version | Nothing automatic |
| **The host C compiler** (builds OpenCore and OVMF — the one Tier 0 input that is not pinned) | not pinned. **Range-checked** in `internal/firmware/compiler.go`: **gcc 13 through 16, verified at gcc 13.3.0, 14.2.0 and 16.2.1**, enforced by `mavericks-firmware` before it builds | ❌ **unpinnable by us**, like QEMU — it is the host's, from its distribution. Tracking a version we do not choose would produce PRs nobody can act on. **But unlike QEMU its output is baked into shipped bytes**, which is why it is a row here: OpenCorePkg 1.0.7 does not compile *at all* under a C23-default gcc, and OvmfPkg compiles clean and emits a *different* `OVMF_CODE.fd`. **Compensates — deliberately not a pin:** the C dialect is stated rather than inherited (`-std=gnu17`, `assets/firmware/patches/0002-*`); upstream's `-Werror` is not inherited either (`-Wno-error`, `assets/firmware/patches/0003-*`), because a warning a newer compiler invents in code we pin and cannot patch should not stop our build — the warnings are still printed, and our own code keeps every gate it has; the range check refuses below the floor and warns above the ceiling; the compiler line is a row of the firmware's input listing, so a changed compiler rebuilds the firmware. `docs/decisions/0004` has the reasoning and why (b), a declared range, and not (c), a pinned toolchain | Nothing automatic — the host bumps it, not us. A host outside the range hears about it at build time instead of in a checksum diff. **The range moves only on evidence**: gcc 15 is inside it by interpolation between two verified neighbours and is marked as never seen |
| **`utm-bundle`, `opencore-legacy-img`** (Tier 2 reference blobs) | `assets/pins/sources.tsv` | ❌ **deliberately untracked.** Evidence, not ingredients: the UTM bundle that booted 10.9 before this project built its own boot stack, and khronokernel's OpenCore image inside it. Nothing the plugin builds or boots reads either one. A blob nothing ships cannot go stale in anything | Never bumped. If one ever needed to be, that is a sign it stopped being Tier 2 |
| **shipyard** (`check-family-conventions.sh`, `check-ingredient-pins.sh`, `deviations.sh`, `templates/msc.sh`) | `@v1` in `.github/workflows/*.yml` | ✅ tracked by Renovate's native `github-actions` manager, as the family intends — `@v1` is a moving major tag | Gate behaviour changes; `build/msc.sh` must still match `$SHIPYARD_SCRIPTS/templates/msc.sh` byte for byte. **Never edit our copy** — fix the template upstream |
| **`bats`, `shellcheck`, `dmg2img`, `mkfs.hfsplus`, `busybox`, `zstd`, `nasm`, `iasl`** | not pinned | ❌ **not ingredients.** Build-host and test tools: none of their bytes reaches a guest, which is exactly what separates them from the compiler row above. `bin/run-tests.sh` checks for what the tests need, and the data sources name any tool they need and cannot find before they build | n/a |
| **`ccache`, optional, for the firmware builds** (`mavericks-firmware`'s `ccache` option, `firmware.CcacheDefault`) | not pinned | ❌ **unpinnable by us**, like QEMU and the host compiler — it is the host's, if it is there at all, and this project installs nothing. **And it is not an ingredient — claimed.** It stands between `gcc` and its output, so it is *positioned* to change shipped bytes, and the claim that it does not is the whole reason it is allowed near this build. **What is measured, on the primary host, gcc 13.3.0, 2026-09-21:** two cold boot-stack builds **at the same build directory**, one straight and one through a `PATH` wrapper ahead of the real compiler — the mechanism the `ccache` option uses — produced **identical checksums for all eight artifacts**. That is evidence about the *mechanism*, and about the compiler's identity surviving it. **What is NOT measured:** ccache itself, which no host here has installed, so nobody has compared a cache hit against a cold compile. **Therefore it is off by default** (`firmware.CcacheDefault`), and it is not a row of the input listing — installing ccache must not rebuild the firmware. The cache lives under the firmware workspace in Packer's cache directory, **never the repository** | Nothing automatic; it is the host's. **A complete comparison on a host that has ccache is what moves the default** — see `internal/firmware/ccache.go` and its tests |

### Why the boot stack does not automerge

The shared Renovate policy is "if it builds and passes, it ships", and asks
a repo to state a reason wherever it restricts that. Ours:

- **A bad bump here builds fine and is wrong.** A green suite here means
  the Go tests and the repository's tooling pass — it does not mean the
  guest boots. Nothing in CI boots a guest; nothing can, without nested
  virtualization and Apple's media.
- **The checksum arrives stale.** Renovate can move a URL in
  `assets/pins/sources.tsv`; it cannot compute the `sha256` beside it. The
  bump PR is therefore internally inconsistent by construction, and
  `bin/verify-changed-sources.sh` is what turns that from a half-hour
  failure in someone's build into a red PR.
- **It changes what every future guest boots**, per the section above — a
  human should see that happen.

## Conformance deviations

Transcribed from `docs/decisions/0007-what-this-project-ships.md`, which is
where the sparkle-updater reasoning lives; the version-scheme reasoning
lives in `docs/decisions/0012-version-scheme.md`. Scoped to filename
globs, each with a reason — `deviations.sh` rejects an entry that has no
reason, because an exception without one is indistinguishable from drift.

- version-scheme:cmd/packer-plugin-macosx/*: this product is its own upstream, not a repackage of somebody else's release, so it takes the family's SELF-UPSTREAM shape (`YYYYMMDD.N`, as `mavericks-porthole` does) rather than `<upstream>-mavericks.N`. The suffix means "our Nth repackage of someone else's thing" and there is no such thing here; `docs/decisions/0012-version-scheme.md` has the reasoning. The plugin's main package is at `cmd/packer-plugin-macosx/` (the repository root is the package that embeds `assets/`). **The semver Packer wants and the two-axis scheme are reconciled at the release tag, not by changing either:** `packer init` needs a full three-component semver (`hashicorp/packer-plugin-sdk`'s `version.NewPluginVersion`), so a release tag is `v0.<UPSTREAM_VERSION>.<N>` — `v0.20261005.1` for the family's `20261005.1` — the family version behind `v0.`, which `build/version.sh` reports as its TAG; `.goreleaser.yml` reads `{{ .Version }}` off that tag with the leading `v` stripped, unchanged from what `build/version.sh` computes
- version-scheme:datasource/*: same product, same SELF-UPSTREAM `YYYYMMDD.N` shape, scoped the same way so a deviation on one glob cannot quietly license the rest to drift
- version-scheme:internal/*: same product, same SELF-UPSTREAM `YYYYMMDD.N` shape, scoped the same way so a deviation on one glob cannot quietly license the rest to drift
- version-scheme:version/*: same product, same SELF-UPSTREAM `YYYYMMDD.N` shape, scoped the same way; this is the package that carries the version (`version.Version`, written from the release tag by `.goreleaser.yml`'s -ldflags), so it is the last glob to leave out
- sparkle-updater:cmd/packer-plugin-macosx/*: Sparkle is a macOS framework, and the plugin runs on the build host, whose primary OS is Linux; Packer installs and updates plugins itself (`packer init`). The guest-side payload, which IS a 10.9 .pkg, takes the family's Sparkle shape unchanged. Scoped to the plugin's main package the same way as the version-scheme deviation above
- sparkle-updater:datasource/*: same product, same reason, scoped the same way
- sparkle-updater:internal/*: same product, same reason, scoped the same way
- sparkle-updater:version/*: same product, same reason, scoped the same way

## No upstream release notes

No upstream release notes: this repository has no single upstream whose
notes a release could link. Each ingredient's notes live with its own
project — `acidanthera/OpenCorePkg`, `acidanthera/Lilu`,
`acidanthera/VirtualSMC`, `Mavergreen/openssh` — and the registry row
above names each one, which is the closest thing to "what changed" that a
product with a dozen upstreams can honestly offer.

## Provenance

`docs/decisions/0004-boot-stack-provenance.md` is the tier scheme these
rows are an instance of: **Tier 0** built from pinned source, **Tier 1**
vanilla upstream binaries pinned and checksummed, **Tier 2** reference
blobs that never reach the boot path (see the
`utm-bundle`/`opencore-legacy-img` row above).
