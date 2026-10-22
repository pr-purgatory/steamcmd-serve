#!/usr/bin/env bash
# Picks the tag for a release. Existing tags are read from stdin, one per line.
#
#   release-tag.sh [custom-tag] < existing-tags
#
# With a custom tag: validates it (it is also used as the container image tag) and
# refuses one that already exists. Without: CalVer YYYY.MM.DD in UTC, or
# YYYY.MM.DD.N (one more than the highest N for that date) when the date is already tagged.
# TODAY=YYYY.MM.DD overrides the date (for tests).
set -euo pipefail

custom="${1:-}"
existing="$(cat)"
has() { grep -qxF -- "$1" <<<"$existing"; }

if [[ -n "$custom" ]]; then
  if [[ ! "$custom" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
    echo "invalid tag '$custom': use letters, digits, '.', '_' or '-' (max 128 characters, not starting with '.' or '-')" >&2
    exit 1
  fi
  if has "$custom"; then
    echo "tag '$custom' already exists" >&2
    exit 1
  fi
  echo "$custom"
  exit 0
fi

base="${TODAY:-$(date -u +%Y.%m.%d)}"
if ! has "$base"; then
  echo "$base"
  exit 0
fi
# Continue after the highest existing suffix, so a later release never sorts before
# an earlier one even if a tag in between was deleted.
max=0
while read -r t; do
  n="${t#"$base".}"
  if [[ "$t" == "$base."* && "$n" =~ ^[0-9]+$ ]] && ((10#$n > max)); then max=$((10#$n)); fi
done <<<"$existing"
echo "$base.$((max + 1))"
