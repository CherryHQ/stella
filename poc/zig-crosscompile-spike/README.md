# CGO cross-compile spike (go/no-go for the native sidecar)

**Question:** can we build the CGO sidecar (onnxruntime_go + daulet/tokenizers)
for all target platforms from a single macOS host, using `zig cc`?

**Verdict: GO for darwin + linux.** windows is dropped (decision).

## Result matrix

| Target | Method | Built | Ran |
| --- | --- | --- | --- |
| darwin/arm64 | native `clang` (no zig) | ✅ | ✅ (host + e5 POC) |
| darwin/amd64 | native `clang`, cross-arch | ✅ | — |
| linux/amd64 | `zig cc` + libstdc++ shim | ✅ | ✅ (docker, real tokenizer call) |
| linux/arm64 | `zig cc` + libstdc++ shim | ✅ | — |
| windows/* | — | dropped | — |

`./build-all.sh` reproduces all four from macOS.

## What the spike proved (and the two gotchas)

The spike binary (`main.go`) imports **both** native deps and calls into each, so
the linker must actually resolve them — it's a faithful stand-in for the sidecar.

1. **onnxruntime_go needs no library at build time.** It `dlopen`s
   `libonnxruntime` at runtime (`SetSharedLibraryPath`), so its only cgo LDFLAG is
   `-ldl`. Cross-compiling never has to find an onnxruntime lib — a big
   simplification. (The OCR-only path therefore cross-compiles trivially.)

2. **daulet/tokenizers links `libtokenizers.a` statically, per target.** Prebuilt
   archives exist for darwin + linux (NOT windows — would need building from Rust
   source). Two snags solved:

   - **darwin:** don't use zig at all. macOS `clang` cross-compiles both arches
     natively (it has the SDK; zig targeting `*-macos` can't find system libs like
     `libresolv` without a sysroot). `CC` is left default.

   - **linux:** the prebuilt `libtokenizers.a` was built against **GNU libstdc++**
     and pulls in exactly two iostream static-init symbols
     (`_ZNSt8ios_base4InitC1Ev` / `D1Ev`) from `esaxx.cpp`. zig ships **libc++**
     (`std::__1::`), whose mangling differs, so it can't satisfy them. `esaxx`
     performs no actual I/O (`nm` shows no `std::cout`/ostream refs), so the
     Init guard is dead weight — `stub.c` provides the two symbols as no-ops.
     The linux/amd64 binary then runs the real tokenizer in docker: `ok 1 false`.

### The zig recipe (linux)

A wrapper script appends the C++ runtime to every invocation so it lands after
`-ltokenizers` on the link line (`cc/zigcc-linux-amd64`):

```sh
#!/bin/sh
exec zig cc -target x86_64-linux-gnu "$@" -lc++ -lc++abi
```

```sh
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  CC="$PWD/cc/zigcc-linux-amd64" \
  CGO_LDFLAGS="-L$PWD/libs/linux-amd64" \
  go build .
# + stub.c in-package supplies the 2 GNU libstdc++ iostream-init symbols
```

## Caveat for production

The no-op libstdc++ shim is pragmatic and correct **for the current prebuilt
`libtokenizers.a`** (esaxx does no I/O). It is brittle against upstream rebuilds.
Two sturdier options for the real `stella-ml-runtime` build:

- **(preferred) build the linux sidecar natively on linux in CI** (GitHub Actions
  linux runners or `docker buildx`, one per arch). The sidecar is a separately
  built, runtime-downloaded artifact, so it never needs to cross-compile from a
  dev's mac — this sidesteps the libstdc++ ABI issue entirely.
- supply a real GNU `libstdc++.a` per arch to zig instead of the shim.

zig-from-mac (this spike) stays the fast path for **local dev** cross-builds.

## Files

| File | Role |
| --- | --- |
| `main.go` | imports + calls both native deps (linker stand-in for the sidecar) |
| `cc/zigcc-linux-*` | zig wrapper appending libc++ for linux targets |
| `stub.c` | no-op GNU libstdc++ iostream-init symbols (linux only) |
| `build-all.sh` | reproduce all 4 darwin+linux builds from macOS |
| `libs/<target>/` | per-target `libtokenizers.a` (gitignored; from daulet release) |
