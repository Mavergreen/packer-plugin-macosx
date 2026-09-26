# Golden files for internal/inputs

Each part's input listing: what `RepoRows` and `Listing` produce from
the embedded registry and the repository's own assets. Each is compared
byte for byte with what the code produces.

| File | What it is | Test |
|---|---|---|
| `listing-esd.txt`, `listing-payload.txt` | that part's input listing, from the embedded registry | `TestListingsMatchTheirGoldens` (`inputs_test.go`) |
| `listing-opencore.txt`, `listing-ovmf.txt` | the same, with the compiler row this golden itself carries | the same |
| `listing-efi.txt` | the same, plus `smbios=iMac14,2` and `opencore-artifacts=absent` | the same |
| `listing-media.txt` | the same (its `privops:` rows are `assets/privops/*.sh`), plus `payload=absent`, `openssh-enabled=1` and the stamp for updates `security` | the same |
| `listing-esd-no-row.txt`, `listing-esd-empty-url.txt` | the esd listing from `assets/pins/sources.tsv` with the `apple-installesd-10.9.5` row removed, and with that row's URL emptied | `TestESDListingWithoutAURLMatchesItsGolden` (`inputs_test.go`) |

## When one changes

A failing comparison is a regression until shown otherwise. Deliberate
changes that move these, each edited into the goldens in the same commit
as the change, with the reason in its message:

- a pin, a guest asset, an autoinstall hook or a firmware patch changes
  the rows of the listings that hash it (`listing-payload.txt` carries
  the digests of `assets/guest/firstboot.sh`, `postinstall` and
  `com.mqg.firstboot.plist`; `listing-media.txt` those of the autoinstall
  hooks and of the privops microVM's scripts, `assets/privops/*.sh`;
  `listing-opencore.txt` and `listing-ovmf.txt` those of the firmware
  patches; `listing-efi.txt` that of
  `assets/firmware/config.plist`) -- a changed row is exactly what makes
  that data source build its output again.
