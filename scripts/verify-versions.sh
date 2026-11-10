#!/usr/bin/env bash
#
# Assert every internal module version, and the version the docs site shows
# (docs/src/lib/version.ts), equals the given release version.
#
#   ./scripts/verify-versions.sh v0.5.0
#
# Run by release.yml before tagging. A forgotten bump fails loudly here rather
# than publishing modules that silently point at the previous release, which is
# how the v0.3.1/v0.3.4/v0.3.5 spread accumulated, or leaving the previous
# release in the docs site's header, as v0.6.1 did.

set -euo pipefail

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  echo "usage: ${0##*/} vX.Y.Z" >&2
  exit 2
fi

cd "$(dirname "$0")/.."

PATTERN='github\.com/gasmod/gas(/[A-Za-z0-9._-]+)*[[:space:]]+v[0-9]+\.[0-9]+\.[0-9]+[^[:space:]]*'

mismatches=$(
  for f in $(find . -name go.mod -not -path './.git/*' | sort) ./go.work; do
    grep -oE "$PATTERN" "$f" 2>/dev/null |
      awk -v want="$VERSION" -v file="${f#./}" '$2 != want { printf "  %s: %s %s\n", file, $1, $2 }'
  done

  docs_version=docs/src/lib/version.ts
  if ! grep -qxF "export const version = '$VERSION';" "$docs_version"; then
    found=$(sed -n "s/^export const version = '\(.*\)';\$/\1/p" "$docs_version")
    printf '  %s: docs site version %s\n' "$docs_version" "${found:-not found}"
  fi
)

if [ -n "$mismatches" ]; then
  echo "versions do not match $VERSION:" >&2
  echo "$mismatches" >&2
  echo >&2
  echo "run ./scripts/bump.sh $VERSION and commit before tagging" >&2
  exit 1
fi

echo "all internal module versions and the docs site version are $VERSION"
