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

## The registry

| Ingredient | Pinned in | Renovate | On bump |
|---|---|---|---|
| **ocbuild `efibuild.sh`** (Tier 0, build script) | `assets/pins/sources.tsv` `ocbuild-efibuild` | ❌ **untrackable.** A `raw.githubusercontent.com` URL at a commit on `master`; there are no releases and no tags to track. Upstream's `build_oc.tool` fetches it off `master` and `eval`s it. **Compensates:** pinned commit and checksum, and `assets/firmware/patches/0001-*` makes `build_oc.tool` read the pinned copy rather than fetch one | Deliberate, alongside OpenCore |
| **Lilu** (SMC injection, Tier 1 release binary) | `assets/pins/sources.tsv` `lilu-release` | ✅ `github-releases` on `acidanthera/Lilu`, with an `autoReplaceStringTemplate` — the version appears **twice** in one URL and a manager that rewrote only the captured occurrence would leave a half-updated URL that 404s. **Automerge off** | A new kext in every future guest. Re-pin the checksum; re-read `assets/firmware/README.md`, which records how each version's 10.9 (Darwin 13) support was checked |
| **VirtualSMC** (Tier 1 release binary) | `assets/pins/sources.tsv` `virtualsmc-release` | ✅ as Lilu | as Lilu |
| **`assets/firmware/config.plist`** (OpenCore's configuration) | ours, in-tree | n/a — we author it | Changes how every future guest boots. Its sha256 is a row of the firmware's input listing, so a change rebuilds the EFI image |
| **Apple's `InstallESD.dmg`, 10.9.5** | `assets/pins/sources.tsv` `apple-installesd-10.9.5` | ❌ **untrackable, and deliberately so.** Apple publishes no feed, and the pin names one immutable build: 10.9.5 is 10.9.5 forever. **Compensates:** the transfer is plain HTTP after an `osrecovery.apple.com` AssetToken handshake, so the checksum is the only integrity there is, and `mavericks-installesd` enforces it | Never bumps. **Never enters the repository, in any form** — `bin/no-apple-bytes.sh` is the gate |
| **Safari 9.1.3** and **iTunes 12.6.2** (`updates = "all"`; iTunes is one softwareupdate product made of five flat packages, so six pins) | `assets/pins/sources.tsv` `apple-safari-9.1.3`, `apple-itunes-12.6.2-*` | ❌ **no datasource, as above, and for the same reason.** Frozen 2016/2017 artifacts for an OS that stopped receiving them. Opt-in, because they are applications rather than the operating system | Never bumps. The five iTunes pins move together or not at all — they are one product, installed in the order `internal/fetch/updates.go` gives |
| **`iBooksDelta-1.0.1`, `RemoteDesktopClient-3.8.4`** — offered to a live 10.9.5 guest by Apple, **not pinned, not installed, not findable** | **nowhere.** Deliberately, and this row is why | ❌ **untrackable AND unfetchable.** A 10.9.5 guest's own `softwareupdate -l` offered five items on 2026-09-21; three are in `index-10.9.merged-1.sucatalog` and these two are not — not in its package URLs, and not in any of its 333 distribution files, all of which were fetched and searched. There is no URL to pin, so there is nothing for a checksum to be the identity of. **Recorded here rather than omitted**, because "we could not find it" is a reason and "we did not mention it" is not. **Compensates:** nothing, and that is the honest answer; `docs/open-questions.md` Q1 says to ask the guest again rather than guess | Nothing. If one is ever located, it becomes an `assets/pins/sources.tsv` row like its three siblings |
| **`utm-bundle`, `opencore-legacy-img`** (Tier 2 reference blobs) | `assets/pins/sources.tsv` | ❌ **deliberately untracked.** Evidence, not ingredients: the UTM bundle that booted 10.9 before this project built its own boot stack, and khronokernel's OpenCore image inside it. Nothing the plugin builds or boots reads either one. A blob nothing ships cannot go stale in anything | Never bumped. If one ever needed to be, that is a sign it stopped being Tier 2 |
| **shipyard** (`check-family-conventions.sh`, `check-ingredient-pins.sh`, `deviations.sh`, `templates/msc.sh`) | `@v1` in `.github/workflows/*.yml` | ✅ tracked by Renovate's native `github-actions` manager, as the family intends — `@v1` is a moving major tag | Gate behaviour changes; `build/msc.sh` must still match `$SHIPYARD_SCRIPTS/templates/msc.sh` byte for byte. **Never edit our copy** — fix the template upstream |
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
