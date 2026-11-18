#!/bin/sh
# Prose that tells the history of the code, the history of a test run, or sells, does not belong in the docs, the
# help texts or the comments. This script lists such phrases and exits 1 when it finds one. A line that has a reason to
# keep a phrase ends with the marker `lint-prose: allow`. Not scanned: CHANGELOG.md, docs/upgrade.md (upgrade notes name
# versions), docs/design.md (the status register), tests, and the state migrations.
set -eu
cd "$(dirname "$0")/.."

FILES=$(git ls-files 'README.md' 'docs/*.md' 'sdk/*.md' 'examples/*/README.md' 'packaging/*.md' 'packaging/*/*.md' \
	'internal/cli/*.go' 'internal/fleet/cli/*.go' 'internal/service/doctor*.go' 'cmd/*/*.go' 'web/*/src/i18n/*.ts' |
	grep -vE '_test\.go$|^docs/upgrade\.md$|^docs/design\.md$')

HISTORY='(no longer (exists|supported|accepted)|used to (be|make|fall|say|write|have|keep|run|accept)|formerly|previously|former (meaning|spelling|name|behaviou?r)|backwards?[- ]compat|shims?\b|deprecated|kept for (a |the )?transition|removable once|(earlier|older|previous) (version|release|daemon|helper) of jailops|left by an earlier version|before 0\.[0-9]+|new in 0\.[0-9]+|since 0\.[0-9]+|as before)'
DIARY='(^|[^a-zA-Z])(verified on|tested on|tested with fakes|was tested|we tested|end-to-end block|tested how)'
FILLER='(seamless|powerful|robust|comprehensive|leverag(e|es|ing)|simply|easily|effortless|blazing|world-class|enterprise-grade|best-in-class|cutting-edge|out of the box|under the hood|perfectly good|provides a way to|it is worth noting|basically|essentially)'
CLAIM='(unlike (bastille|iocage|cbsd|pot|appjail|ezjail)|lightweight|lightning)'

rc=0
for group in HISTORY DIARY FILLER CLAIM; do
	eval "pattern=\$$group"
	hits=$(echo "$FILES" | xargs grep -nIiE "$pattern" | grep -v 'lint-prose: allow' |
		grep -vE 'compat/linux|mountpoint[=: ]*legacy' || true)
	if [ -n "$hits" ]; then
		echo "lint-prose: $group" >&2
		echo "$hits" >&2
		rc=1
	fi
done
exit $rc
