# Golden files for internal/media

Expected outputs for the installer-media code. `.gz` files are gzip'd
disk images (`gzip -9`); the tests decompress them at run time.

| File | What it is | Test |
|---|---|---|
| `required-files.txt` | the files installer media must hold, in order: the bootloader, BaseSystem.dmg and its chunklist, and the sixteen files of Packages | `TestRequiredFilesMatchTheirGolden` (`apple_test.go`) |
| `check-sums-good.sums`, `check-sums-bad.sums` | the checksums `sumFixtures` generates: Apple's pinned package sums, and the same with `BSD.pkg`'s value changed and `X11redirect.pkg`'s line removed | `TestCheckAppleSumsMatchesItsGolden` (`apple_test.go`) |
| `check-sums-good.problems`, `check-sums-bad.problems` | what `CheckAppleSums` reports for each, one problem per line (none for the good sums) | the same |
| `hfs-create-gpt.img.gz` | a 40 MiB HFS+ volume named `OS X Base System` in a GPT disk, the partition at 1 MiB and one MiB of GPT behind it, as sgdisk and mkfs.hfsplus lay it out | `TestCreateHFSGPTMatchesItsGolden` (`hfs_test.go`) |
| `mark-clean-bare.img.gz` | the "bare" synthetic volume `TestMarkCleanMatchesItsGolden` builds (size 1 MiB, start 0, attributes 0x800/0x800), marked clean | `TestMarkCleanMatchesItsGolden` (`hfs_test.go`) |
| `mark-clean-at-1mib.img.gz` | the "at 1 MiB" synthetic volume (size 3 MiB, start 1 MiB, attributes 0x80000800/0x00000900), marked clean | the same |
| `mark-clean-one-header-clean.img.gz` | the "one header clean" synthetic volume (size 1 MiB, start 0, attributes 0x100/0x2800), marked clean | the same |

"Marked clean" is `kHFSVolumeUnmountedBit` set and
`kHFSVolumeInconsistentBit` cleared in both the volume header and the
alternate header, every other byte unchanged.

## When one changes

A failing comparison is a regression until shown otherwise. A
deliberate change -- a file added to the required list, a reworded
problem, a new pin in `assets/pins/apple-packages.sha256` (which changes
the `.sums` fixtures) -- edits the golden in the same commit as the code,
with the reason in the commit message. `hfs-create-gpt.img.gz` records
sgdisk's and mkfs.hfsplus's layout: `CreateHFSGPT` is compared with it by
sgdisk's reading of both, so it changes only if that layout does. The
`mark-clean-*` images change only if what "clean" means does.

## The data sources' recipe

These goldens pin the output of code that shapes a data source's store
entry, so `datasource/media` carries their digest in a `recipe` constant, a
row in every listing. A change here without a recipe bump fails
`TestRecipePinsTheGoldens` there: bump the recipe's number and set its
goldens part to the digest the failure names, so that entries users
built with the old code are rebuilt rather than reused.
