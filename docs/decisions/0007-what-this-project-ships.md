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

## The sibling boundary

`mavericks-vm-host` (publishing as `Mavergreen/vm-host`) ships
Hypervisor.framework for 10.9, and a QEMU to go with it, so that modern
QEMU can use hardware acceleration *on* Mavericks. That is Mavericks as a
**host**; this repository is Mavericks as a **guest**. They are orthogonal
and stay separate repositories. Nothing in the guest stack depends on the
host project.
