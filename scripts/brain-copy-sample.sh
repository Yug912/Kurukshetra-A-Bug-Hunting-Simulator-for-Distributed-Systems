#!/usr/bin/env bash
# Copy testdata/sample-run into web/timeline/public/sample-run so the viewer works
# with no backend: npm run dev, then open http://localhost:5173/?run=sample
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
dst="$root/web/timeline/public/sample-run"
rm -rf "$dst"
mkdir -p "$(dirname "$dst")"
cp -R "$root/testdata/sample-run" "$dst"
# The viewer links linearizability.html from result.json artifacts; regenerate it if kuru is built.
if [ -x "$root/bin/kuru" ]; then
  "$root/bin/kuru" check "$dst" --checkers linearizable-kv,durability >/dev/null || true
fi
echo "copied sample run to $dst"
