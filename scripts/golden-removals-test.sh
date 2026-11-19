#!/bin/sh
# The check of scripts/check-golden-removals.sh, against a throwaway repository: a removed line fails from 1.0 on, an added line
# and any change before 1.0 do not, a raised major version allows the removal.
set -e
here=$(cd "$(dirname "$0")" && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
g() { git -C "$t" -c user.name=t -c user.email=t@example.invalid "$@"; }
mkdir -p "$t/scripts" "$t/internal/server/testdata/freeze" "$t/internal/cli/testdata/freeze"
cp "$here/check-golden-removals.sh" "$t/scripts/"
printf 'GET /a\nGET /b\n' > "$t/internal/server/testdata/freeze/routes.golden"
printf '1.0.0\n' > "$t/version.txt"
g init -q
g add -A
g commit -q -m base
run() { (cd "$t" && sh scripts/check-golden-removals.sh HEAD) >/dev/null 2>&1; }
fail=0
expect() { # expect WANT DESCRIPTION
	if run; then got=0; else got=1; fi
	if [ "$got" != "$1" ]; then echo "FAIL: $2 (exit $got, want $1)"; fail=1; else echo "ok: $2"; fi
}
printf 'GET /a\nGET /b\nGET /c\n' > "$t/internal/server/testdata/freeze/routes.golden"
expect 0 "a added route passes"
printf 'GET /a\n' > "$t/internal/server/testdata/freeze/routes.golden"
expect 1 "a removed route fails in 1.x"
printf 'GET /a\nPOST /b\n' > "$t/internal/server/testdata/freeze/routes.golden"
expect 1 "a rewritten route fails in 1.x"
printf '2.0.0\n' > "$t/version.txt"
expect 0 "a raised major version allows it"
printf '1.0.0\n' > "$t/version.txt"
git -C "$t" checkout -q -- internal
printf '0.9.0\n' > "$t/version.txt"
g commit -q -am pre
printf 'GET /a\n' > "$t/internal/server/testdata/freeze/routes.golden"
expect 0 "before 1.0 a removal is only reported"
exit $fail
