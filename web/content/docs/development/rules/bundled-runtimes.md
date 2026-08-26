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

### The admission test

A candidate must pass **one** of these two. They are different arguments, and a
proposal has to say which one it is making.

1. **Core dependency with no network at use time.** The daemon needs the tool on
   a request path that must work in an air-gapped deployment. `xberg` qualifies:
   every Library upload and every Vision OCR fallback calls it, and a deployment
   without egress still has to parse documents.
2. **Bootstrap.** Nothing can install the tool because the thing that installs
   tools _is_ the tool. `mise` qualifies, and only `mise` can.

Note what argument 2 is **not**. Bundling `mise` does not buy offline operation —
`runScopeInstall` in `internal/manifestplugins/mise_config.go` shells `mise
install`, which downloads over the network. It buys a deterministic, pinned
bootstrap with no chicken-and-egg. Anyone proposing "just fetch mise on first
run" is answering an argument nobody made; the real objection is that first run
would then depend on an unpinned fetch of the most privileged binary in the
chain.

Anything that passes neither test goes to a mise shim, even if bundling would be
more convenient.

### The size budget

Measured on `main` at 2026-08 (darwin-arm64):

| Part                                | Size   |
| ----------------------------------- | ------ |
| Bundled runtimes (`mise` + `xberg`) | 72 MB  |
| Web UI (`web/static`)               | 21 MB  |
| Compiled Go code (`__text`)         | 30 MB  |
| Symbol and line tables              | ~39 MB |
| `stellad` total                     | 206 MB |

The runtimes are about a third of the binary, and the deployment pays for those
bytes twice: `EnsureTools` extracts ~185 MB into `$STELLA_HOME/bin`, and the
extraction is synchronous on the startup path, so first boot writes all of it
before serving.

### Upgrade trigger

Move to fetch-on-first-run when **either** holds:

- Bundled runtimes exceed half the binary, or
- A third runtime is admitted.

At that point the extraction, permission-repair, and verification code does not
change — only the source of the bytes does. Keep the SHA-256 pin and the version
stamp; they are what make a fetched artifact as trustworthy as an embedded one.

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

- `os.MkdirTemp` takes no mode argument at all: it **always** creates `0700`.
  Any staging directory published by rename must be chmodded first.
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

1. **`internal/cmd/syncembeddedbinaries/main.go`** — add the version constant, a per-platform
   asset table with a **SHA-256 for every asset**, and the sync function that
   downloads and verifies it. Write the artifact under a **fixed filename** and
   stamp the version into its gzip header comment; do not put the version in the
   filename. Downloads land in `resources/binaries/binaries/<platform>/`, which
   is gitignored; only `PLACEHOLDER` is committed.
2. **`resources/binaries/embed_<os>_<arch>.go`** — add the new archive to the
   `//go:embed` line **by exact name**, on every platform that has an asset. This
   is what makes a build with no generated artifact fail to compile.
3. **`resources/binaries/embedded.go`** — add one entry to `knownRuntimes()` with
   its installed name, archive name, and extract function. There is no version
   constant to keep in sync: `archiveVersion` reads it back from the artifact.
4. **Modes** — use `toolDirMode` / `toolExecMode` / `toolDataMode`. Never a
   literal.
5. **`plugins/sandbox/docker/Dockerfile`** — add the tool to the cross-UID smoke
   test. Running `<tool> --version` as the installing user proves nothing; the
   check must run under an unrelated UID.
6. **Tests** — `TestExtractedToolsShareOnePermissionContract` walks the whole
   install tree, so the permission contract covers a new runtime automatically.
   The repair and verification tests name Xberg explicitly; extend them if the
   new runtime is a bundle.

Run `mise run generate` after step 1; `setup`, `build`, and `test` all depend on
it. To fetch for another platform, set the target explicitly:

```bash
TARGET_GOOS=windows TARGET_GOARCH=amd64 go run ./internal/cmd/syncembeddedbinaries
```

**The generator lives outside `resources/binaries` and must stay there.** Exact
`//go:embed` names mean the package does not compile until its artifacts exist, so
a generator inside it could never run — `go generate` loads the package first.
Any CI job that compiles the package has to sync before it builds.

## Windows and missing assets

`syncXberg` skips a platform absent from its asset table; `syncMise` treats the
same case as fatal, because a platform Stella supports must have mise. Follow
whichever rule matches the new tool, and say which in a comment.

POSIX mode bits do not exist on Windows, so `repairToolPermissions` and the
`VerifyTools` mode check return early there.

**Missing runtimes are visible, not silent.** `VerifyTools` no longer tolerates an
empty embedded FS: exact-name embeds mean a binary that compiled has its
runtimes. What remains legitimate is a platform with no asset for one tool —
Xberg on Windows. `ToolNames` then omits it, and the decision about whether that
is fatal belongs to the consumer:

- Library registers no Xberg parser routes and logs a warning naming the affected
  media types and `stellad system-bundle install` as the remedy
  (`cmd/stellad/commands.go`).
- Uploads of those types fail closed at the API with
  `library.ErrParserUnavailable`, surfaced as `503 "this deployment cannot
process this file type"` — deliberately not the generic "temporarily
  unavailable", which would invite a retry that can never succeed.

## Calling a bundled runtime

Installation and invocation are separate concerns; `resources/binaries` owns only
the first. Anything that parses untrusted input must cross the process boundary
through a package that owns the hardening — for Xberg that is `internal/xberg`,
which scrubs the environment to a whitelist, disables configuration discovery,
and bounds output.

Do not build an `exec.Cmd` for a bundled runtime by hand. Vision did, and it
inherited the daemon's full environment — provider credentials included — into a
process parsing user-supplied images.

Two details worth knowing:

- **The daemon's own PATH does not include `$STELLA_HOME/bin`.** Resolve through
  `binaries.ToolPath`, not `exec.LookPath`. Sandboxes are different: both the
  Docker image and `pkg/sandbox.HostEnvBuildPath` put `bin` on PATH.
- **Adjacent shared libraries resolve through `@loader_path` / `$ORIGIN`, not the
  working directory.** Setting `cmd.Dir` to the binary's directory is about
  configuration discovery, not linking.
