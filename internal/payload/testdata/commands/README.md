# The commands each environment the payload runs in has

Read on 2026-10-17 from Apple's own images, without booting them, by
listing every entry directly under `/bin`, `/sbin`, `/usr/bin` and
`/usr/sbin` (and, for an installed system, `/usr/libexec`). Symlinks are
included as names. Each file is one path per line, sorted (`LC_ALL=C`).

| File | The environment | Read from |
|---|---|---|
| `snowleopard-installer.txt` | 10.6's install DVD, which the installer boots and runs `rc.cdrom.local` (autoinstall.sh) and postinstall in | the retail DVD's HFS+ volume, the `snowleopard-installer` data source's `snowleopard.hfs` (10A432), listed with `7z l -thfs -slt` |
| `mavericks-installer.txt` | 10.9's "OS X Base System", the installer environment of the media `mavericks-media` builds | a built `installer-media.img`'s HFS+ partition (GPT, at 1 MiB), exposed read-only with `qemu-storage-daemon`'s FUSE export (no root) and listed with `7z l -thfs -slt` |
| `snowleopard-installed.txt` | 10.6.0 as installed, before any update: what firstboot.sh's first boot has | the payloads of the DVD's `BaseSystem.pkg`, `Essentials.pkg`, `BSD.pkg` and `AdditionalEssentials.pkg` (`7z x -txar ... Payload`, gzip'd cpio, `cpio -it`) |
| `mavericks-installed.txt` | 10.9.5 as installed | the same from InstallESD.dmg's `Packages/`: `BaseSystemBinaries.pkg`, `Essentials.pkg`, `BSD.pkg`, `AdditionalEssentials.pkg` (bzip2'd cpio) |

The installed lists are bounded by those four packages: the ones every
install takes, and the ones that put anything in those directories that
the payload could run. The other packages on each medium are printer
drivers, languages, fonts, X11 and applications.

`environment_test.go` holds the payload's scripts to them: postinstall is
run, as each release's media builds it, with PATH holding only its
installer's commands; autoinstall.sh, postinstall and firstboot.sh (with
the scripts it writes) are read for every command they run
(`commandwords_test.go`), which must be in their environment's list.

`absent-on-host.sh` stands in, in that run, for a listed command the host
running the tests lacks.

The other files here are the tests' own: `absent-on-host.sh` (above);
`diskutil-table.sh`, the stand-in diskutil autoinstall.sh's dry runs
under each installer's list use; `reader-plant.sh` and
`unreadable-plant.sh`, never run, the command reader's plants: what it
must read, and what it must refuse, naming the line.
