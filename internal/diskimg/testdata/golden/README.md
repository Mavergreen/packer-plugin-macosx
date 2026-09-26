# Golden files for internal/diskimg

Reference disk images made by the tools diskimg does without -- sgdisk
and mtools 4.0.43 -- so its tests can hold diskimg's output to theirs
without either tool installed. Each `.gz` file is a gzip'd disk image
(`gzip -9`); the tests decompress them at run time.

| File | What it is |
|---|---|
| `efi-40.img.gz` … `efi-512.img.gz` | an empty EFI system partition image of 40, 48, 64, 100, 192, 300 and 512 MiB: a GPT with one partition from sector 2048 to the last usable sector (sgdisk), formatted FAT32 with the volume label `EFI` (`mformat -F`) |
| `mtools-image.img.gz` | the same 48 MiB image, with `/EFI` made by `mmd` and the source files `TestGoReadsAnMtoolsImage` lists copied in by `mcopy` (the bundle recursively): mtools' own long names, short names and lower-case flags |

Used by `TestGeometryMatchesMformat` and `TestGoReadsAnMtoolsImage` in
`tools_test.go`.

## When one changes

These record what sgdisk and mtools do, not what diskimg does, so no
change to diskimg changes them: a test here that fails means diskimg
drifted from the tools. Replace an image only to record a different
tool version's behaviour -- made the way the table says, gzip'd the same
way -- and name the tool versions in the commit message.

## The data sources' recipe

These goldens pin the output of code that shapes a data source's store
entry, so `datasource/firmware` and `datasource/media`
each carry their digest in a `recipe` constant, a
row in every listing. A change here without a recipe bump fails
`TestRecipePinsTheGoldens` there: bump the recipe's number and set its
goldens part to the digest the failure names, so that entries users
built with the old code are rebuilt rather than reused.
