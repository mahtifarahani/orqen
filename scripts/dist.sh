#!/usr/bin/env bash
# make dist: the release binary plus SHA256SUMS and THIRD_PARTY_LICENSES.txt in bin/.
# Offline: the Go module list comes from the binary (go version -m) and the license files from the local module cache;
# the web licenses come from the local node_modules (pnpm licenses list). Nothing is downloaded, signed or published.
set -euo pipefail
cd "$(dirname "$0")/.."
bin=bin/orqen-release
test -x "$bin" || { echo "build $bin first (make release)" >&2; exit 1; }

python3 - "$bin" <<'PY'
import json, os, subprocess, sys

binary = sys.argv[1]
out = ["THIRD PARTY LICENSES", "", "Generated offline from the release binary and the local caches. Review before distribution.", ""]

mods = []
for line in subprocess.run(["go", "version", "-m", binary], check=True, capture_output=True, text=True).stdout.splitlines():
    p = line.split()
    if len(p) >= 3 and p[0] == "dep":
        mods.append((p[1], p[2]))
out.append("== Go modules (%d) ==" % len(mods))
for path, ver in sorted(mods):
    out.append("")
    out.append("--- %s %s ---" % (path, ver))
    d = subprocess.run(["go", "list", "-m", "-f", "{{.Dir}}", "%s@%s" % (path, ver)], capture_output=True, text=True).stdout.strip()
    found = False
    if d and os.path.isdir(d):
        for name in sorted(os.listdir(d)):
            if name.upper().startswith(("LICENSE", "COPYING", "NOTICE")) and os.path.isfile(os.path.join(d, name)):
                out.append("[%s]" % name)
                out.append(open(os.path.join(d, name), errors="replace").read().rstrip())
                found = True
    if not found:
        out.append("(no license file in the local module cache)")

web = json.loads(subprocess.run(["pnpm", "-C", "web", "licenses", "list", "--prod", "--json"], check=True, capture_output=True, text=True).stdout)
out += ["", "== Web packages (production) =="]
for lic in sorted(web):
    for pkg in sorted(web[lic], key=lambda x: x["name"]):
        out.append("%s %s - %s - %s" % (pkg["name"], ",".join(pkg.get("versions", [])), lic, pkg.get("homepage", "")))
open("bin/THIRD_PARTY_LICENSES.txt", "w").write("\n".join(out) + "\n")
PY

(cd bin && shasum -a 256 orqen-release THIRD_PARTY_LICENSES.txt > SHA256SUMS)
echo "dist: bin/orqen-release bin/SHA256SUMS bin/THIRD_PARTY_LICENSES.txt"
