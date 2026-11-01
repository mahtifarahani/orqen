#!/usr/bin/env bash
# Fails when web/openapi.json or web/src/api/generated differ from what `make openapi` produces.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
SNAP=$(mktemp -d)
trap 'rm -rf "$SNAP"' EXIT
cp "$ROOT/web/openapi.json" "$SNAP/openapi.json"
cp -R "$ROOT/web/src/api/generated" "$SNAP/generated"
make -C "$ROOT" openapi >/dev/null
if ! diff -ru "$SNAP/openapi.json" "$ROOT/web/openapi.json" \
  || ! diff -ru "$SNAP/generated" "$ROOT/web/src/api/generated"; then
  echo "OpenAPI drift: run 'make openapi' and commit web/openapi.json and web/src/api/generated" >&2
  exit 1
fi
echo "openapi: no drift"
