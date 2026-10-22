#!/usr/bin/env bash
# Tests for release-tag.sh. Run: .github/scripts/release-tag_test.sh
set -uo pipefail
cd "$(dirname "$0")" || exit 1
fail=0
check() { # name, want ("ERR" for failure), existing tags, args...
  local name=$1 want=$2 existing=$3; shift 3
  local got
  if got=$(printf '%b' "$existing" | TODAY=2026.10.22 ./release-tag.sh "$@" 2>/dev/null); then :; else got=ERR; fi
  if [[ "$got" == "$want" ]]; then echo "ok   $name"; else echo "FAIL $name: got '$got' want '$want'"; fail=1; fi
}
check "first of the day"        2026.10.22   ""
check "second of the day"       2026.10.22.1 "2026.10.22\n"
check "third of the day"        2026.10.22.3 "2026.10.22\n2026.10.22.1\n2026.10.22.2\nv1.0.0\n"
check "continues after highest"  2026.10.22.3 "2026.10.22\n2026.10.22.2\n"
check "numeric not lexical"     2026.10.22.11 "2026.10.22\n2026.10.22.9\n2026.10.22.10\n"
check "non-numeric suffix"      2026.10.22.1 "2026.10.22\n2026.10.22.rc\n"
check "other days ignored"      2026.10.22   "2026.10.21\n2026.10.21.1\n"
check "custom tag"              v1.2.3       "2026.10.22\n" v1.2.3
check "custom existing"         ERR          "v1.2.3\n" v1.2.3
check "custom with space"       ERR          "" "v1 2"
check "custom leading dot"      ERR          "" .hidden
check "custom leading dash"     ERR          "" -x
check "custom slash"            ERR          "" feature/x
check "custom too long"         ERR          "" "$(printf 'a%.0s' $(seq 129))"
check "prefix is not a match"   2026.10.22   "2026.10.22-rc1\n"
exit $fail
