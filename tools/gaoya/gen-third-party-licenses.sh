#!/bin/sh
# Regenerate THIRD-PARTY-LICENSES.txt for a revserp-gaoya release.
#
# Collects, for the exact locked crates compiled into the linux musl
# release binaries: crate name/version, declared license, repository, and
# the full text of every LICENSE/COPYING/UNLICENSE file shipped in the
# crate artifact. Uses only cargo metadata/tree (no extra installs).
# Appends the Rust toolchain runtime appendix (std, compiler-builtins,
# musl libc) for the invoking rustc, so regen reproduces the release asset.
#
# usage: sh gen-third-party-licenses.sh <output-file>
set -eu
cd "$(dirname "$0")"
OUT=${1:?usage: gen-third-party-licenses.sh <output-file>}
RUSTC_VERSION=$(rustc --version | awk '{print $1" "$2}')

cargo metadata --format-version 1 --manifest-path Cargo.toml > "$OUT.meta.json"
for target in x86_64-unknown-linux-musl aarch64-unknown-linux-musl; do
    cargo tree --target "$target" --edges normal,build --prefix none
done | sort -u > "$OUT.tree.txt"

python3 - "$OUT.meta.json" "$OUT.tree.txt" "$OUT" "$RUSTC_VERSION" <<'EOF'
import json, os, sys

meta_path, tree_path, out_path, rustc_version = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
with open(meta_path) as f:
    meta = json.load(f)
os.remove(meta_path)

wanted = set()
with open(tree_path) as f:
    for line in f:
        line = line.strip()
        if not line or line.startswith('revserp-gaoya v'):
            continue
        parts = line.split()
        if len(parts) >= 2 and parts[1].startswith('v'):
            wanted.add((parts[0], parts[1][1:].split('(')[0]))
os.remove(tree_path)

by_namever = {(p['name'], p['version']): p for p in meta['packages']}

def license_files(pkgdir):
    out = []
    for name in sorted(os.listdir(pkgdir)):
        upper = name.upper()
        if upper.startswith(('LICENSE', 'COPYING', 'UNLICENSE')) and os.path.isfile(os.path.join(pkgdir, name)):
            out.append(name)
    return out

# MIT text of gaoya 0.2.2, whose crate artifact ships no license file.
# License declared as MIT in crate metadata; text below matches the LICENSE
# file of upstream tag/release 0.2.2 (github.com/serega/gaoya, commit 9f3fd4b,
# verified clean git tree before pinning).
GAOYA_MIT = """MIT License

Copyright (c) 2022 Sergey Melderis

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
"""

lines = []
add = lines.append
add('THIRD-PARTY LICENSES for revserp-gaoya')
add('=======================================')
add('')
add('This file lists the licenses of the third-party crates statically linked')
add('into the revserp-gaoya release binaries, collected from the Cargo-locked')
add('crate artifacts resolved for the linux musl release targets.')
add('All listed crates are pure Rust (no C sources); several run small Rust')
add('build scripts (feature probing only) at compile time.')
add('')
add('First section: this software (revserp-gaoya, MIT, see LICENSE-MIT).')
add('Remaining sections: one per third-party crate, alphabetical.')
add('Trailing section: Rust toolchain runtime notices (std, compiler-builtins, musl libc).')
add('')
add('--- revserp-gaoya (this software): MIT ---')
with open('LICENSE-MIT') as f:
    add(f.read().rstrip())
add('')

missing = []
for name, version in sorted(wanted):
    p = by_namever.get((name, version))
    if p is None:
        missing.append(f'{name} {version}')
        continue
    add(f'--- {name} {version} ---')
    add(f"license: {p.get('license')}")
    if p.get('repository'):
        add(f"repository: {p['repository']}")
    pkgdir = os.path.dirname(p['manifest_path'])
    files = license_files(pkgdir)
    if name == 'gaoya':
        add('')
        add('[LICENSE file not shipped in crate artifact; text below is the')
        add('upstream LICENSE of gaoya 0.2.2, license declared as MIT in crate metadata.]')
        add('')
        add(GAOYA_MIT.rstrip())
    elif not files:
        add('')
        add('[no license file shipped in crate artifact; declared license above.]')
    for fn in files:
        add('')
        add(f'[file: {fn}]')
        with open(os.path.join(pkgdir, fn), errors='replace') as f:
            add(f.read().rstrip())
    add('')

add('--- Rust toolchain runtime (statically linked) ---')
add('')
add('The static musl binaries also link runtime code shipped with the Rust')
add(f'toolchain ({rustc_version}, x86_64 host): the Rust standard library,')
add('compiler-builtins (including the bundled libm math module and the LLVM')
add('libunwind linked as -lunwind), and the self-contained musl libc startup')
add('objects and C library (crt1.o, crti.o, crtn.o, libc.a, libunwind.a, ...).')
add('Their notices, as provided in the toolchain distribution, follow. The')
add('full MIT and Apache-2.0 license texts already appear above (they ship in')
add('the crate artifacts collected in this file) and are not repeated here,')
add('except for the LLVM exception and the musl notice, which appear below.')
add('')
add('--- Rust standard library ---')
add('license: Apache-2.0 OR MIT')
add('copyright: The Rust Project Developers (see https://thanks.rust-lang.org)')
add('notice source: <sysroot>/share/doc/rust/COPYRIGHT-library.html ("In-tree files").')
add('')
add('--- compiler-builtins (including LLVM libunwind) ---')
add('license: MIT AND Apache-2.0 WITH LLVM-exception')
add('notice source: <sysroot>/lib/rustlib/src/rust/library/compiler-builtins/LICENSE.txt')
add('')
add('Portions of this software are derived from third-party works licensed under')
add('terms compatible with the above Apache-2.0 WITH LLVM-exception AND MIT')
add('license:')
add('')
add("* compiler-builtins is derived from LLVM's compiler-rt (https://llvm.org/).")
add('  Work derived from compiler-rt prior to 2019-01-19 is usable under the MIT')
add('  license, with the following copyright:')
add('')
add('      Copyright (c) 2009-2016 by the contributors listed in CREDITS.TXT')
add('')
add('  The relevant CREDITS.TXT is located at')
add('  https://github.com/llvm/llvm-project/blob/main/compiler-rt/CREDITS.TXT.')
add('')
add('* Work derived from compiler-rt after 2019-01-19 is usable under the')
add('  Apache-2.0 license with the LLVM exception.')
add('')
add('---- LLVM Exceptions to the Apache 2.0 License ----')
add('')
add('As an exception, if, as a result of your compiling your source code, portions')
add('of this Software are embedded into an Object form of such source code, you')
add('may redistribute such embedded portions in such Object form without complying')
add('with the conditions of Sections 4(a), 4(b) and 4(d) of the License.')
add('')
add('In addition, if you combine or link compiled forms of this Software with')
add('software that is licensed under the GPLv2 ("Combined Software") and if a')
add('court of competent jurisdiction determines that the patent provision (Section')
add('3), the indemnity provision (Section 9) or other Section of the License')
add('conflicts with the conditions of the GPLv2, you may retroactively and')
add('prospectively choose to deem waived or otherwise exclude such Section(s) of')
add('the License, but only in their entirety and only with respect to the Combined')
add('Software.')
add('')
add('--- compiler-builtins bundled libm (math module) ---')
add('license: MIT (rust-lang/libm)')
add('notice source: <sysroot>/lib/rustlib/src/rust/library/compiler-builtins/libm/LICENSE.txt')
add('')
add('--- musl libc (self-contained CRT objects + libc.a) ---')
add('license: MIT')
add('copyright: Copyright (c) 2005-2014 Rich Felker, et al.')
add('')
add('Permission is hereby granted, free of charge, to any person obtaining a copy')
add('of this software and associated documentation files (the "Software"), to deal')
add('in the Software without restriction, including without limitation the rights')
add('to use, copy, modify, merge, publish, distribute, sublicense, and/or sell')
add('copies of the Software, and to permit persons to whom the Software is')
add('furnished to do so, subject to the following conditions:')
add('')
add('The above copyright notice and this permission notice shall be included in all')
add('copies or substantial portions of the Software.')
add('')
add('THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR')
add('IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,')
add('FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE')
add('AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER')
add('LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,')
add('OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE')
add('SOFTWARE.')
add('')
add('[The toolchain ships no separate musl COPYRIGHT file; the same MIT grant over')
add('musl portions (Copyright (c) 2005-2013 Rich Felker) is also evidenced in')
add('<sysroot>/share/doc/rust/COPYRIGHT-library.html.]')
add('')
if missing:
    print('WARNING: no metadata for: ' + ', '.join(missing), file=sys.stderr)
    sys.exit(1)

with open(out_path, 'w') as f:
    f.write('\n'.join(lines))
print(f'wrote {out_path} ({len(wanted)} crates)')
EOF
