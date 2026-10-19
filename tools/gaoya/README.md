# revserp-gaoya

Standalone near-duplicate text matcher for Revserp duplicate detection.
Reads JSONL records, runs gaoya MinHash LSH, writes `pairs.jsonl` and
`metrics.json` to the output directory.

No Rust toolchain is needed on production machines: deploy the prebuilt
static musl binary from the GitHub release (tag `gaoya-v0.1.0`).

## Usage

```sh
revserp-gaoya --input records.jsonl --out-dir out/
revserp-gaoya --input records.jsonl --out-dir out/ --threshold 0.9 --num-hashes 128
revserp-gaoya --version
revserp-gaoya --help
```

Input: one JSON object per line, `{"id": string|number, "text": string}`.

- `id` must be a JSON string or number. Numbers are normalized to their
  decimal string (`1` becomes `"1"`). Ids are always emitted as JSON
  strings, so ids like `"001"` or `"+1"` stay exact and valid.
- `text` must be a JSON string. An empty string is valid and is skipped;
  a missing or non-string `text` is an error.
- A duplicate `id` is an error (exit 1), so bad input cannot silently
  drop records.
- Exit codes: `0` ok, `1` input/IO error, `2` CLI flag error. Errors go to
  stderr with a `revserp-gaoya: error:` prefix.

Output `pairs.jsonl` schema (one object per line):

```json
{"left_id": "1", "right_id": "2", "score": 1.0, "score_kind": "minhash"}
```

`score` is the minhash signature Jaccard estimate; the threshold applies
to that estimate, NOT to exact shingle Jaccard. Identical texts surface
with score 1.0; exact-content labeling is left to evaluation.
`metrics.json` records the effective config, timing, peak RSS of the run,
and counters.

## Release

GitHub tag: `gaoya-v0.1.0`. Downloads:

- `revserp-gaoya-x86_64-unknown-linux-musl`
- `revserp-gaoya-aarch64-unknown-linux-musl`
- `SHA256SUMS`
- `THIRD-PARTY-LICENSES.txt`
- `LICENSE-MIT`

Verify after download:

```sh
sha256sum -c SHA256SUMS
```

Both binaries are fully static (musl) and take the same CLI documented
above. `THIRD-PARTY-LICENSES.txt` lists the licenses of every crate
statically linked into the binaries.

## Build from source

```sh
cd tools/gaoya
cargo build --release
```

Static musl builds (same as the release). Keep build output out of the
repo: `tools/gaoya/.gitignore` excludes `/target`, so point
`CARGO_TARGET_DIR` at a scratch directory outside Git.

```sh
cd tools/gaoya
rustup target add x86_64-unknown-linux-musl aarch64-unknown-linux-musl
CARGO_TARGET_DIR=/tmp/gaoya-build cargo build --release --target x86_64-unknown-linux-musl
# aarch64 cross-link uses the pure-Rust LLD shipped with rustup
# (resolves `rust-lld` from the toolchain sysroot; no gcc, musl-tools,
# or system installs needed):
CARGO_TARGET_DIR=/tmp/gaoya-build RUSTFLAGS="-C linker=rust-lld" \
  cargo build --release --target aarch64-unknown-linux-musl
```

Both targets use the default generic CPU (no `-C target-cpu=native`), so
the binaries run on any x86-64 / ARM64 machine. Both are fully static
(musl): the x86-64 binary reports `static-pie linked`, the ARM64 binary
reports `statically linked`. Confirm with `file` before publishing.

Reproducible builds: `gaoya` is pinned exact in `Cargo.toml`
(`gaoya = "=0.2.2"`); the release build resolves the full crate set into
`Cargo.lock`, which is shipped with the release tag.

## Go integration test (no Rust needed)

Gaoya is an external runtime for the backend: Go developers do not need
a Rust toolchain. Fetch the pinned Linux binary (needs the published
`gaoya-v0.1.0` release assets) and run the Go tests against it:

```sh
scripts/install-gaoya.sh amd64 "$PWD/bin/revserp-gaoya"
GAOYA_PATH="$PWD/bin/revserp-gaoya" go test ./internal/issues/...
```

The Go tests use `GAOYA_PATH` when set, else `revserp-gaoya` on `PATH`;
binary-dependent tests skip with a note when neither is available.

## Upstream source and licenses

- Algorithm library: **gaoya 0.2.2** from crates.io, pinned exact.
  Upstream repo: <https://github.com/serega/gaoya>, license **MIT**.
  Before pinning, the reference upstream tree was checked for local
  modifications: clean `git status`, empty `git diff`, HEAD `9f3fd4b`
  (merge of release 0.2.2), and `0.2.2` confirmed as the published
  crates.io release. No patched or private-path source is used.
- Behavior ported from the reference adapter: same flags (`--input`,
  `--out-dir`, `--threshold`, `--num-hashes`, `--bands`, `--band-width`,
  `--shingle`, `--lowercase`, `--seed`), same `pairs.jsonl` shape. Two
  deliberate changes: `--threshold` defaults to **0.9**, and ids are
  always emitted as JSON strings (see above).
- `revserp-gaoya` itself is MIT-licensed; see `LICENSE-MIT`.
  `gen-third-party-licenses.sh` regenerates the crate sections of
  `THIRD-PARTY-LICENSES.txt` from the locked crate set (`cargo metadata` /
  `cargo tree`, no extra installs), then appends the Rust toolchain
  runtime appendix: Rust std (Apache-2.0 OR MIT), compiler-builtins
  (MIT AND Apache-2.0 WITH LLVM-exception) incl. bundled libm (MIT),
  and musl libc CRT/libc (MIT). The appended texts are fixed provided
  notices, stamped with the invoking rustc version — not derived from
  cargo. Verify them against the toolchain sysroot
  (`rustc --print sysroot`): `share/doc/rust/COPYRIGHT-library.html`,
  `lib/rustlib/src/rust/library/compiler-builtins/LICENSE.txt`, and
  `lib/rustlib/src/rust/library/compiler-builtins/libm/LICENSE.txt`
  (the sysroot ships no separate musl COPYRIGHT file):
  `sh gen-third-party-licenses.sh THIRD-PARTY-LICENSES.txt`.
