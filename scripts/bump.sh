#!/usr/bin/env bash
#
# Rewrite every internal module version to the given release version.
#
# go.mod requires and the go.work replace block must always agree. The
# workspace resolves internal modules through those versioned replaces, so
# bumping one without the other leaves the workspace unable to build.
#
#   ./scripts/bump.sh v0.5.0
#
# Does not run `go mod tidy`: the new version has no tag yet, so tidy cannot
# resolve it. Tidy after the release is published.

set -euo pipefail

VERSION="${1:-}"
if ! printf '%s' "$VERSION" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$'; then
  echo "usage: ${0##*/} vX.Y.Z" >&2
  exit 2
fi

cd "$(dirname "$0")/.."

# Matches an internal module path followed by a semver, in both go.mod
# requires ("<path> v1.2.3 // indirect") and go.work replaces
# ("<path> v1.2.3 => ./dir"). Module lines carry no version and are untouched.
PATTERN='(github\.com/gasmod/gas(?:/[A-Za-z0-9._-]+)*)[ \t]+v[0-9]+\.[0-9]+\.[0-9]+[^ \t\n]*'

changed=0
for f in $(find . -name go.mod -not -path './.git/*' | sort) ./go.work; do
  before=$(shasum "$f" | cut -d' ' -f1)
  perl -pi -e "s{$PATTERN}{\$1 $VERSION}g" "$f"
  after=$(shasum "$f" | cut -d' ' -f1)
  if [ "$before" != "$after" ]; then
    echo "  bumped ${f#./}"
    changed=$((changed + 1))
  fi
done

echo "bumped $changed file(s) to $VERSION"
