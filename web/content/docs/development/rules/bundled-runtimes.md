---
title: Bundled runtimes
description: Rules for third-party CLIs embedded in the stellad binary and installed into $STELLA_HOME/bin.
---

A **bundled runtime** is a third-party CLI compiled into `stellad` with `go:embed`
and extracted to `$STELLA_HOME/bin` on startup. Today there are two: `mise`, which
bootstraps every other tool, and `xberg`, which extracts text from documents for
Library and for Vision's OCR fallback.

Bundling is the exception, not the default. Read "Choosing how to ship a tool"
below before adding one.

## Choosing how to ship a tool

Climb this list and stop at the first option that works:

| Option              | Use when                                                                  | Where it lives                       |
| ------------------- | ------------------------------------------------------------------------- | ------------------------------------ |
| **mise shim**       | The default. Any tool an agent runs in a sandbox (`gh`, `fd`, `rg`, ...). | `$STELLA_HOME/.mise-tools/shims`     |
| **Plugin binary**   | The tool belongs to one plugin and not to the platform.                   | Installed by the plugin's reconciler |
| **Bundled runtime** | `stellad` itself calls it, or it must exist before mise does.             | `$STELLA_HOME/bin`                   |

Bundling costs binary size on every platform and pins the version to the Stella
release. Only bundle when the daemon's own code paths depend on the tool being
present — Library refuses PDFs without `xberg`, and nothing can install tools
before `mise` exists.

## The permission contract

Everything under `$STELLA_HOME/bin` must stay readable — and executables
runnable — by **any UID that has `bin` on PATH**, not just the UID that installed
it. The sandbox image takes its UID as a build argument and is routinely run
under a different one, so an owner-only path is a deployment that starts cleanly
and then fails at the first use.

`resources/binaries/embedded.go` states the modes once, in `toolDirMode`,
`toolExecMode`, and `toolDataMode`. Both install paths use them, `VerifyTools`
enforces them, and `repairToolPermissions` reasserts them on every startup.

Two syscalls will silently violate the contract if you trust their defaults:

- `os.MkdirTemp` **always** creates `0700` and ignores both its mode argument and
  umask. Any staging directory published by rename must be chmodded first.
- `os.OpenFile`'s mode is masked by umask, so `0755` becomes `0700` under
  `umask 077`. Chmod extracted files explicitly.

A bundle directory published at `0700` shipped in a release once: extraction
succeeded, the executable's own bit was `0755`, and every process not owned by
the installer got `permission denied` from the launcher symlink's target
directory, while `mise` — a plain file in `bin` — kept working.

## The two shapes

**Single file.** The upstream release is one static executable. Extract it
straight into `bin` with mode `toolExecMode`. This is `mise`.

**Bundle directory.** The upstream release carries files the executable needs at
runtime — `xberg` ships six shared libraries the dynamic linker resolves from the
executable's own directory. Extract into `bin/<name>-v<version>/` and add a
relative symlink `bin/<name>` pointing at it.

Do not flatten a bundle into `bin` to make it look like the single-file case.
`bin` is on PATH; filling it with shared libraries invites name collisions
between tools. The versioned directory also makes upgrades atomic: extract to a
temporary sibling, then rename.

## Adding a bundled runtime

1. **`resources/binaries/gen.go`** — add the version constant, a per-platform
   asset table with a **SHA-256 for every asset**, and the sync function that
   downloads and verifies it. Downloads land in
   `resources/binaries/binaries/<platform>/`, which is gitignored; only
   `PLACEHOLDER` is committed.
2. **`resources/binaries/embedded.go`** — declare the same version (the two
   constants are kept in sync by hand; the archive filename is the contract
   between them), then extend `embeddedToolName` and the extraction switch in
   `extractTools`.
3. **Modes** — use `toolDirMode` / `toolExecMode` / `toolDataMode`. Never a
   literal.
4. **`plugins/sandbox/docker/Dockerfile`** — add the tool to the cross-UID smoke
   test. Running `<tool> --version` as the installing user proves nothing; the
   check must run under an unrelated UID.
5. **Tests** — `resources/binaries/embedded_test.go` verifies the contract across
   the whole install tree, so a new runtime is covered automatically. Add a
   version-probe test only if the tool needs one.

Run `mise run generate` (or `go generate ./resources/binaries/`) after step 1;
`go build ./...` alone does not download anything, and a build without it embeds
only `PLACEHOLDER`.

## Windows

`gen.go` skips any platform missing from a tool's asset table, and POSIX mode
bits do not exist on Windows, so both `repairToolPermissions` and the
`VerifyTools` mode check return early there. A bundled runtime with no Windows
asset must degrade explicitly: `VerifyTools` deliberately skips verification when
nothing is embedded, so the caller — not the extraction layer — decides whether a
missing runtime is fatal.
