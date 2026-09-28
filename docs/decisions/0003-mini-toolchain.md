# 0003. Mini toolchain, targets and float ABI

## Context

Lanscape Mini must be a static C binary for many Linux architectures with a hard size budget
(`lsm-agent` ≤ 128 KiB, `lsm-server` ≤ 512 KiB).

## Decision

- Compiler: `zig cc` from Zig **0.14.1** (musl, static), pinned in CI. One `Makefile` with a
  `TARGET=` table.
- Targets and CPU/float decisions:

  | Target | zig triple and CPU | Float ABI |
  |---|---|---|
  | linux-amd64 | `x86_64-linux-musl` | hard |
  | linux-386 | `x86-linux-musl` | x87 |
  | linux-arm64 | `aarch64-linux-musl` | hard |
  | linux-armv7 | `arm-linux-musleabihf`, `generic+v7a+vfp3d16` | hard (VFPv3-D16, the common subset of Cortex-A7/A9/A15) |
  | linux-armv6 | `arm-linux-musleabihf`, `arm1176jzf_s` | hard (Raspberry Pi 1/Zero) |
  | linux-armv5 | `arm-linux-musleabi`, `generic+v5te+soft_float` | soft |
  | linux-mips | `mips-linux-musleabi`, `mips32r2+soft_float` | soft (ath79 24Kc has no FPU) |
  | linux-mipsle | `mipsel-linux-musleabi`, `mips32r2+soft_float` | soft (MT7621 1004Kc has no FPU) |
  | linux-mips64/mips64le | `mips64{,el}-linux-muslabi64` | hard (n64); `-Wno-option-ignored` because clang ignores `-fno-PIC` with n64 |
  | linux-riscv64 | `riscv64-linux-musl` | lp64d |
  | linux-ppc64le | `powerpc64le-linux-musl` | hard |
  | freebsd-amd64 | `x86_64-freebsd` | **allowed-failure**: Zig 0.14 does not ship a FreeBSD libc |

- No floating point anywhere in Mini: bit rates are integers (bit/s), CPU load in per mille, a
  custom formatter replaces `printf` so the soft-float runtime is not linked.
- `fnmatch` is replaced with a tiny glob matcher (it pulled in wide-character tables).
- After linking, non-allocated sections (`.comment`, `.pdr`, `.mdebug.abi32`) are removed with
  `llvm-objcopy` when available; the size check is valid without this step too.

## Consequences

The FreeBSD Mini build is reported but does not fail CI until Zig gains FreeBSD libc support
(0.15+). All Linux targets are checked against the budget in CI.
