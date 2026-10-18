#!/usr/bin/env bash
#
# Print, as a JSON array of absolute directories, the workspace modules that
# a change since BASE can affect:
#
#   ./scripts/changed-modules.sh <base-commit>
#
# A module is selected when a file under it changed (the deepest module
# directory wins), and so is every module that requires a selected one,
# transitively: the workspace builds against local sources, so a change to
# core or config can break any module that depends on it.
#
# Every module is selected when BASE is empty, all zeros, or unknown, or when
# a file that affects every build changed: go.work, go.work.sum,
# .golangci.yml, the Makefile, scripts/, or .github/workflows/.
#
# Markdown files and the docs site sources (docs/src/) build no Go code and
# select nothing.
#
# Run by test.yml to pick which modules to lint, vet and test. release.yml
# checks every module and does not use it.

set -euo pipefail

BASE="${1:-}"

cd "$(dirname "$0")/.."
root="$(pwd)"

all_modules() {
  go list -m -json | jq -sc '[.[].Dir]'
}

if [ -z "$BASE" ] || [ -z "${BASE//0/}" ] || ! git cat-file -e "${BASE}^{commit}" 2>/dev/null; then
  all_modules
  exit 0
fi

# module path<TAB>relative dir, one line per workspace module.
modules="$(go list -m -json | jq -r --arg root "$root/" \
  '"\(.Path)\t\(if .Dir == ($root | rtrimstr("/")) then "." else (.Dir | ltrimstr($root)) end)"')"

selected=()
while IFS= read -r file; do
  [ -z "$file" ] && continue
  case "$file" in
    go.work | go.work.sum | .golangci.yml | Makefile | scripts/* | .github/workflows/*)
      all_modules
      exit 0
      ;;
    *.md | *.mdx | docs/src/*)
      continue
      ;;
  esac
  best=""
  while IFS=$'\t' read -r _ dir; do
    if [ "$dir" = "." ] || [ "$file" = "$dir" ] || [ "${file#"$dir"/}" != "$file" ]; then
      if [ "$dir" = "." ] && [ -n "$best" ]; then
        continue
      fi
      if [ -z "$best" ] || [ "$best" = "." ] || [ "${#dir}" -gt "${#best}" ]; then
        best="$dir"
      fi
    fi
  done <<<"$modules"
  [ -n "$best" ] && selected+=("$best")
done < <(git diff --name-only "$BASE" HEAD)

# chosen holds one selected module directory per line.
chosen="$(printf '%s\n' "${selected[@]+"${selected[@]}"}" | sort -u | sed '/^$/d')"
is_chosen() { grep -qxF -- "$1" <<<"$chosen"; }

# Add every module that requires a chosen one, until nothing changes.
changed=1
while [ "$changed" -eq 1 ]; do
  changed=0
  while IFS=$'\t' read -r _ dir; do
    is_chosen "$dir" && continue
    while IFS= read -r req; do
      dep_dir="$(awk -F'\t' -v p="$req" '$1 == p { print $2 }' <<<"$modules")"
      if [ -n "$dep_dir" ] && is_chosen "$dep_dir"; then
        chosen="$(printf '%s\n%s\n' "$chosen" "$dir" | sed '/^$/d')"
        changed=1
        break
      fi
    done < <(go mod edit -json "$dir/go.mod" | jq -r '.Require // [] | .[].Path')
  done <<<"$modules"
done

printf '%s\n' "$chosen" | sort | jq -Rsc --arg root "$root" \
  'split("\n") | map(select(. != "")) | map(if . == "." then $root else "\($root)/\(.)" end)'
