#!/usr/bin/env bash
#
# Rewrite every internal module version, and the version the docs site shows,
# to the given release version.
#
# go.mod requires and the go.work replace block must always agree. The
# workspace resolves internal modules through those versioned replaces, so
# bumping one without the other leaves the workspace unable to build.
#
# The docs site shows the release next to its title, from
# docs/src/lib/version.ts.
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

# The file whose `export const version = '...';` line holds the release the
# docs site shows. Without that line nothing is rewritten, rather than
# bumping the modules and leaving the previous release on the site.
DOCS_VERSION=docs/src/lib/version.ts
if ! grep -qE "^export const version = '[^']*';\$" "$DOCS_VERSION"; then
  echo "$DOCS_VERSION: no \"export const version = '...';\" line to bump" >&2
  exit 1
fi

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

before=$(shasum "$DOCS_VERSION" | cut -d' ' -f1)
perl -pi -e "s{^(export const version = ')[^']*(';)\$}{\${1}$VERSION\${2}}" "$DOCS_VERSION"
after=$(shasum "$DOCS_VERSION" | cut -d' ' -f1)
if [ "$before" != "$after" ]; then
  echo "  bumped $DOCS_VERSION"
  changed=$((changed + 1))
fi

echo "bumped $changed file(s) to $VERSION"
