# 0007 — What this project ships

Date: 2026-09-19
Status: accepted

## Context

This repository is one of about forty `mavericks-*` projects in the
Mavergreen family. The family has conventions, and its governing rule is:
**match the family unless the product genuinely differs, and when you
deviate, say so.** A silent deviation reads as a mistake; a documented one
reads as a decision.

Every sibling cross-builds one upstream thing into a **Mac OS X 10.9**
compatible `.pkg`, on a modern runner, with no 10.9 build machine
anywhere. This project inverts that: what it makes *runs* Mavericks, as a
guest, on a Linux host.

## Decision 2 — never publish Apple's bytes

**The built disk and the box both contain Apple's operating system. They
are never published**: not as a release asset, not as a package, not
anywhere reachable without authentication. The same holds for everything
the data sources fetch: Apple's installer and update packages are
downloaded on the user's own machine, at build time, from Apple, and never
leave it.

This is what separates the product from a Vagrant box someone could
download. What ships is the **recipe** (the plugin and the template); the
box is made, and stays, on the machine that runs the recipe. So the
template has **no upload post-processor**, and must never gain one.

`bin/no-apple-bytes.sh` is the gate:
- on the tree a release is built from (CI runs it on every push), nothing
  may look like Apple's bytes: names (`.dmg`, `.pkg`, `InstallESD`, ...),
  magic numbers (`xar!`, `H+`/`HX`, `koly`, Mach-O) and size;
- with `--archives`, on goreleaser's output before anything is uploaded,
  every file and every member of every zip is judged the same way, and a
  disk image, box or installer package by name is refused. The plugin
  binary is the one exemption.

It passes by construction, because nothing here ever holds Apple's bytes.
That is the reason to assert it rather than assume it: a 6 GB `.dmg`
committed "just for a minute" is one `git add -A` away and looks like
nothing in a large diff.

## The sibling boundary

`mavericks-vm-host` (publishing as `Mavergreen/vm-host`) ships
Hypervisor.framework for 10.9, and a QEMU to go with it, so that modern
QEMU can use hardware acceleration *on* Mavericks. That is Mavericks as a
**host**; this repository is Mavericks as a **guest**. They are orthogonal
and stay separate repositories. Nothing in the guest stack depends on the
host project.
