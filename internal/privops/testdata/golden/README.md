# Golden files for internal/privops

Where the privops microVM looks for the host's kernel, and which one it
boots, for three fixture roots. `TestKernelCandidatesMatchTheirGolden`
in `requirements_test.go` builds each fixture in a tempdir `$root` (boot
directory `$root/boot`, modules directory `$root/modules`, kernel version
`6.1.0-test`) and compares `KernelCandidates` and `Kernel` with the file.

| File | Fixture: the files under `$root` |
|---|---|
| `kernel-nothing.txt` | none |
| `kernel-glob-expands.txt` | `boot/kernel-other`, `boot/kernel-zzz` |
| `kernel-keyed-and-generic.txt` | `boot/vmlinuz-linux`, `boot/kernel-6.1.0-test`, `boot/vmlinuz` |

Each file holds two blocks: `CANDIDATES`, the candidate paths in the
order they are tried, then `KERNEL`, the one chosen (empty when there is
none to choose). `@ROOT@` stands for the fixture's root, which is unique
per run; the test substitutes its own back in.

## When one changes

Only a deliberate change to where the kernel is looked for, or in what
order, changes these: edit the blocks by hand in the same commit, and
say why in its message.
