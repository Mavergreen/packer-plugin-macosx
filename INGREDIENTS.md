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

## The registry

| Ingredient | Pinned in | Renovate | On bump |
|---|---|---|---|
| **Lilu** (SMC injection, Tier 1 release binary) | `assets/pins/sources.tsv` `lilu-release` | ✅ `github-releases` on `acidanthera/Lilu`, with an `autoReplaceStringTemplate` — the version appears **twice** in one URL and a manager that rewrote only the captured occurrence would leave a half-updated URL that 404s. **Automerge off** | A new kext in every future guest. Re-pin the checksum; re-read `assets/firmware/README.md`, which records how each version's 10.9 (Darwin 13) support was checked |
| **VirtualSMC** (Tier 1 release binary) | `assets/pins/sources.tsv` `virtualsmc-release` | ✅ as Lilu | as Lilu |
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
