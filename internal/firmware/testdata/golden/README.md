# Golden files for internal/firmware

Expected outputs, known answers and reference data for this package's
tests. Each is compared byte for byte (or line by line, where a test
lists its cases) with what the code produces; the test named beside each
file says how.

| File | What it is | Test |
|---|---|---|
| `opencore-pins.txt` | every OpenCore build input and the commit it is pinned to, `source<TAB>commit` | `TestPinsAndListsMatchTheirGoldens` (`pins_test.go`) |
| `opencore-artifacts.txt` | the OpenCore build's shipped artifacts, in order | the same |
| `opencore-udk-commit.txt` | the audk (UDK) commit the OpenCore build uses | the same |
| `opencore-build-options.txt` | the extra compiler options the OpenCore build passes | the same |
| `ovmf-artifacts.txt` | the OVMF build's firmware files | the same |
| `ovmf-build.txt` | the OVMF build's platform, arch, toolchain and target, tab-separated | the same |
| `kexts.txt` | the kexts the EFI image carries | the same |
| `opencorepkg-version.txt` | the OpenCorePkg release built | the same |
| `efi-image-contents.txt` | the EFI image's top-level contents, in image order | the same |
| `edk2-sources.txt` | the pinned sources an EDK II build unpacks, in order | the same |
| `efi-image-mib.txt` | the EFI image's size in MiB | `TestEFIImageSizeMatchesItsGolden` (`pins_test.go`) |
| `compiler-range-verdict.txt` | `RangeVerdict`'s verdict and detail for each case, in order | `TestRangeVerdictMatchesItsGolden` (`toolchain_test.go`) |
| `compiler-parse.txt` | `ParseCompiler`'s family and version for each banner, then the banner | `TestParseCompilerMatchesItsGolden` (`toolchain_test.go`) |
| `compiler-range-text.txt` | the supported compiler range, as the build states it | `TestTheDeclaredRangeIsGcc13Through16` (`toolchain_test.go`) |
| `ccache-verdict.txt` | `CcacheVerdict`'s verdict and detail for each case, in order | `TestCcacheVerdictMatchesItsGolden` (`toolchain_test.go`) |
| `smbios-models.txt` | the SMBIOS tested-options table, `model<TAB>status<TAB>evidence` | `TestTheTableMatchesItsGoldenWordForWord` (`smbios_test.go`) |
| `smbios-verdict.txt` | `SMBIOSVerdict` for `iMac14,2`, `MacPro5,1` and `Macmini6,2`, in order | `TestVerdictsMatchTheirGoldens` (`smbios_test.go`) |
| `smbios-status-text.txt` | `SMBIOSStatusText` for `VERIFIED`, `BOOTED`, `PANICKED`, `NOT-TESTED`, `UNLISTED` and `WEIRD`, in order | the same |
| `smbios-plist-set-MacPro5-1.plist`, `smbios-plist-set-iMac14-2.plist` | `assets/firmware/config.plist` with SystemProductName set to that model | `TestSetProductNameMatchesItsGoldenByteForByte` (`smbios_test.go`) |
| `debug-plist-set-MacPro5-1.plist`, `debug-plist-set-iMac14-2.plist` | that model's `smbios-plist-set-*.plist` with the debug switch on (`SetDebug`): `AppleDebug` true, `DisplayLevel` 2147483714, `boot-args` `-v keepsyms=1 debug=0x100` | `TestSetDebugMatchesItsGoldenByteForByte`, `TestEFIImageWithDebugOn` (`debug_test.go`) |
| `smbios-comment-product-name.txt`, `smbios-comment-plist-set.plist` | `ProductName`, and `SetProductName` to `MacPro5,1`, on the test's fixture whose `<string>` follows a comment | `TestProductNameAndSetProductNameWhenACommentPrecedesTheStringTag` (`smbios_test.go`) |
| `efi-fits.txt` | whether each of `TestFitsDemandsHeadroom`'s cases fits: 0 if it does, 1 if not | `TestFitsDemandsHeadroom` (`efi_test.go`) |
| `efi-image/` | the EFI image built from `newFixture`+`shipped` at the default SMBIOS, as sgdisk and mtools lay it out, reduced to its structural facts: `meta.json` (partition, masked FAT geometry, volume label, sorted paths), `files.tar.gz` (every non-directory path's bytes, tarred and gzip'd rather than tracked as loose files -- two of those paths are shaped like a kext bundle, `*.kext/Contents/...`, which bin/no-apple-bytes.sh's path check flags regardless of content) and `sidecar.sha256` (the image's sidecar) | `TestEFIImageMatchesItsGolden` (`efi_test.go`) |

The image in `efi-image/` is not reproducible byte for byte -- sgdisk's
GPT and partition GUIDs and mformat's volume serial are random -- so
`TestEFIImageMatchesItsGolden` checks the sidecar's shape (bare hex and a
newline) rather than recomputing it.

## When one changes

A failing comparison is a regression until shown otherwise. When the
change is deliberate -- a pin bump, a new artifact, new evidence in the
SMBIOS table, a reworded verdict -- edit the golden file in the same
commit as the code, so that the diff shows the new expected output next
to the change that produced it, and say in the commit message why it
changed. An edit to the SMBIOS table's evidence changes
`smbios-models.txt` and `smbios-verdict.txt` together.
`efi-image/` records sgdisk's and mtools' layout of the image, so it
changes only when the image's contents or geometry do.

An edit to `assets/firmware/config.plist`, even to its `#Comment`,
changes both `smbios-plist-set-*.plist`, both `debug-plist-set-*.plist`,
the `EFI/OC/config.plist` in `efi-image/files.tar.gz`, and
`goldenEFIImage` in `efi_test.go` (the fixture image's sha256): carry the
same edit into each.

## The data sources' recipe

These goldens pin the output of code that shapes a data source's store
entry, so `datasource/firmware` carries their digest in a `recipe` constant, a
row in every listing. A change here without a recipe bump fails
`TestRecipePinsTheGoldens` there: bump the recipe's number and set its
goldens part to the digest the failure names, so that entries users
built with the old code are rebuilt rather than reused.
