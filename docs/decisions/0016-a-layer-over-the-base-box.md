# 0016 — A layer over the base box, Aqua Transport first

Date: 2026-10-11
Status: accepted, on measurement

## Decision

A second Mavericks template, `templates/mavericks-aquatransport/`, starts
from a finished base box and adds to it. Its first, and so far only,
content is Wowfunhappy's Aqua Transport with its ModernRootCertificates.

1. **It starts from the base box, and never rebuilds it.** `base_box` is
   the directory a base box unpacks to. The qemu builder converts the
   box's `box_0.img` into its own output directory, and boots it with the
   box's own OVMF and `opencore.img`, read-only. The base box is never
   written to. The variables that boot it are the base template's,
   defaults and validations included. Validation refuses a directory
   short of any of the five files, and a box whose Vagrantfile
   `mavericks.pkr.hcl` did not render, such as a Snow Leopard one.
2. **The build installs, restarts, verifies, then packages.**
   - It uploads the pinned DMG.
   - It installs `AquaTransport.pkg` with `installer`, with
     `-applyChoiceChangesXML` deselecting `installer_choice_3`, the tap
     kext.
   - It restarts the guest, because Aqua Transport patches
     `Security.framework`.
   - `verify.sh` runs in the new boot. It requires 10.9.5 (13F1911), the
     pinned Aqua Transport's receipt, ModernRootCertificates' receipt, no
     tap by receipt, by `kextstat` or on disk, and three fetches with the
     system's own `/usr/bin/curl`: `https://github.com/`,
     `https://release-assets.githubusercontent.com/` and
     `https://downloads.claude.ai/claude-code-releases/latest`. A failing
     verdict fails the build before anything is packaged.
   - It zeroes free space and packages a standalone box,
     `output/mavericks-10.9.5-aquatransport-libvirt.box`, carrying the base
     box's own Vagrantfile and firmware files.
3. **The pin lives in the template's directory**, which ships alone:
   `aquatransport.pkr.hcl` holds the URL, version and SHA-256. The data
   source `macosx-download` fetches the DMG into the builder's Packer
   cache and refuses any other bytes before anything reaches the guest.
   The DMG is never committed, mirrored or put in a release.
4. **The download is the source of truth, and Renovate moves the pin.**
   `aquatransport-observer.yml` records the live download's version and
   digest on the `aquatransport-observed` branch. A Renovate regex manager
   reads that and moves the version and the SHA-256 in one PR. A person
   lands it, as with the boot stack (`INGREDIENTS.md`).
5. **A release ships the layer as its own template zip**, beside the base
   templates, with the base recipe (`bin/base-recipe.sh`;
   `docs/decisions/0007`). A release that changes only the layer leaves
   the base recipe as it was, so a cache of the base image keyed on it
   stays good.
6. **An old release stops building the layer once the download moves.**
   The URL is overwritten in place. Once it serves a newer Aqua Transport,
   the data source fails, naming the pinned and the served SHA-256. There
   is no fallback to an older layer or to the bare base. The README says
   so.

## Context

A stock 10.9.5 guest cannot verify most of today's HTTPS. Its System
keychain lacks the roots that github.com (USERTrust ECC, through Sectigo
Public Server Authentication Root E46) and GitHub's release downloads
(ISRG Root X1, through Let's Encrypt's Root YR) chain to.
Mavergreen/claude-code's first end-to-end run on `mavericks-vm@v1` (run
38093897958, 2026-10-10) failed at its first fetch with "SSL certificate
problem: Invalid certificate chain".

Real Mavericks users fix this with Aqua Transport and its
ModernRootCertificates. The owner's position is that the guest should
match a real user's Mac, rather than each consumer patching trust in its
own job. Other choices were the user's and the owner's:

- **A layer, not the base template.** Aqua Transport changes often, so a
  new version must rebuild the layer without rebuilding the base. (Owner,
  2026-10-11.)
- **Baked once.** Installing it wants a restart, and no CI job may pay for
  an install and a restart at boot time. (Owner.)
- **Vagrant gets it too.** `mavericks-vm` is this plugin and its templates
  projected onto GitHub Actions, so the layer lives with the plugin and
  `mavericks-vm` consumes it. (User.)
- **10.9 only.** 10.6's `/usr/bin/curl` links OpenSSL 0.9.8, which Aqua
  Transport does not reach. (User.)
- **Pin to the live download, and let Renovate bump it.** Nothing is
  mirrored, and it must keep working when GitHub lags the DMG. (User,
  2026-10-11.)

Aqua Transport is the first of what may become several things that make a
10.9 guest useful and safe. The template is named for it, and its
mechanism can carry later items.

The download is `https://mavericksforever.com/downloads/AquaTransport.dmg`,
overwritten in place, with no versioned URLs, releases or appcast. On
2026-10-11 it is 10,698,314 bytes, SHA-256 `7b91d3e4…85bc36`. It holds
AquaTransport 2026.10.09, ModernRootCertificates 2026.09.03 and tap
20150118. The OWL binaries it bundles are GPLv3, which is one more reason
never to redistribute it.

## Measurements

All 2026-10-11 on `pet-power-plant` (i7-8700B, KVM, host kernel 7.2.9-2,
QEMU 8.2.2, packer 1.16.1), one guest at a time. The base box is
`mavericks-1`'s from 0015, built 2026-10-10 with the RTC patch:
6,739,191,383 bytes, 13F1911.

**The verify step fails the bare base.** The base box was booted with the
template's own qemuargs, on a qcow2 overlay, and `verify.sh` was run with
`AQUATRANSPORT_VERSION=2026.10.09`. SSH answered after 61 and 66 s. It
exited 1, both times, with:

```
verify: FAILED: pkgutil has no Wowfunhappy.AquaTransport receipt: Aqua Transport is not installed (see /var/log/install.log in the guest)
verify: FAILED: pkgutil --pkgs has no Wowfunhappy.AquaTransport.ModernRootCertificates receipt: the modern roots are not installed (see /var/log/install.log in the guest)
verify: FAILED: /usr/bin/curl -sS -o /dev/null https://github.com/ fails: curl: (60) SSL certificate problem: Invalid certificate chain
verify: FAILED: /usr/bin/curl -sS -o /dev/null https://release-assets.githubusercontent.com/ fails: curl: (60) SSL certificate problem: Invalid certificate chain
verify: 4 check(s) failed; this guest will not be packaged as a box
```

`downloads.claude.ai` passed on the bare base. It serves GTS Root R1
cross-signed by GlobalSign Root CA, which 10.9 already trusts. The first
run printed curl 7.30's paragraph of CA-bundle advice after each error,
so `verify.sh` now keeps curl's first line.

**The build.** `packer build -var base_box=… templates/mavericks-aquatransport/`
passed in 5 min 57 s, with `verify: ok: 10.9.5 (13F1911), Aqua Transport
2026.10.09 with ModernRootCertificates, no tap, and /usr/bin/curl reaches 3
hosts`.

| Step | Time |
|---|---|
| Download the DMG, convert the base disk | 5 s |
| Boot until SSH answered | 55 s |
| Upload and install, with `installer_choice_3` deselected | 12 s |
| `shutdown -r` until the new boot's kernel started (`BOOT_TIME`) | 31 s |
| `shutdown -r` until SSH answered again | 48 s |
| `verify.sh` | 4 s |
| Zero-fill free space | 3 min 3 s |
| Shut down, convert, package | 39 s |

The install needed no fix: `sudo hdiutil attach -nobrowse -readonly
-mountpoint` over SSH mounted the DMG, `installer` applied the choice
changes and installed headless, and no tap receipt or kext appeared.
After the restart the guest had `Security.original` beside the patched
`Security`, and the receipts `Wowfunhappy.AquaTransport` (2026.10.09) and
`Wowfunhappy.AquaTransport.ModernRootCertificates` (2026.09.03).

The box is 6,406,545,188 bytes, 333 MB smaller than the base box it came
from. Its disk is standalone, with no backing file: 7.74 GiB allocated,
against the base's 8.49 GiB.

**A fresh boot.** The box was added with `vagrant box add` and brought up
with `vagrant up --provider qemu` (vagrant 2.4.9, vagrant-qemu 0.6.3)
three times, each a new VM. The first came up in 55 s. In the first and
the third, `/usr/bin/curl -sS -o /dev/null` exited 0 for all three URLs.
`github.com` and `downloads.claude.ai` answered 200, and
`release-assets.githubusercontent.com/` 404, past a verified handshake.
The first `vagrant destroy -f` printed a Ruby backtrace and left the
machine stopped, not destroyed; the second destroyed it. Two more
up-and-destroy runs did not repeat it.

## Alternatives considered

- **Installing Aqua Transport in the base template.** Every Aqua Transport
  bump would rebuild the base, 1470 s here (0015), and re-key every cache
  of it.
- **Installing it when a CI job boots.** An install and a restart on every
  boot, about a minute each time, where the layer costs that once.
- **The family's own ca-certs as the trust source.** It is unreleased, and
  a real user's Mac does not have it. Roots alone would likely pass the
  three fetches, since 10.9's curl speaks TLS 1.2. Aqua Transport adds
  TLS 1.3 and modern ciphers for every Secure Transport client, and is
  what real users install.
- **A box that keeps the base as its backing file.** It would be smaller,
  but a Vagrant box must stand alone. A consumer that wants a thin delta,
  such as `mavericks-vm`'s cache, can make one from the two disks.
- **Mirroring the DMG**, so that old releases keep building. It is
  redistribution, with a GPLv3 source offer to go with it.
- **Watching the TLSFix repository** instead of the download. Its version
  can lag or lead the uploaded DMG, and most of its commits do not change
  the download.

## What would change this

- **Versioned download URLs upstream.** A pin could name one, and an older
  release would keep building its layer.
- **A second item in the layer**, such as another Mavericks Forever
  recommendation or ca-certs once it ships. It joins this template, which
  might then want a name for the layer rather than for Aqua Transport.
- **A tap choice with another ID.** Upstream could renumber its choices.
  `verify.sh` would then fail the build on a tap receipt or kext, rather
  than ship it.
- **A CI job that builds, boots and verifies the layer** when its pin
  moves. That is what would let the Renovate bump automerge.
- **10.6.** It needs another way to its `curl`'s OpenSSL 0.9.8, which is
  tracked in ca-certs' and openssl-35's backlogs.
