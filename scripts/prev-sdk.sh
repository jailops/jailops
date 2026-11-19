#!/bin/sh
# Writes the baseline of the previous-client test: the client library (sdk/ and sdk/api/, without tests and without the
# fake) of a release, as a module of its own under internal/server/testdata/prevsdk. The test builds a small program
# with that old library and runs it against the current server (internal/server TestPreviousSDKWorksAgainstTheServer):
# a change of the server that the old client cannot follow fails there.
#
#   sh scripts/prev-sdk.sh         the sdk of the working tree (what `just freeze sdk` uses: a meant break of the client
#                                  protocol makes today's client the baseline)
#   sh scripts/prev-sdk.sh vX.Y.Z  the sdk of a tag (how the baseline was made)
set -eu
cd "$(dirname "$0")/.."
dest=internal/server/testdata/prevsdk
ref=${1:-}
src=$(mktemp -d)
trap 'rm -rf "$src"' EXIT
if [ -n "$ref" ]; then
	git archive "$ref" sdk | tar -x -C "$src"
else
	mkdir "$src/sdk" && cp -R sdk/. "$src/sdk/"
fi
rm -rf "$dest"
mkdir -p "$dest/api"
for f in "$src"/sdk/*.go; do
	case $f in *_test.go) continue ;; esac
	cp "$f" "$dest/"
done
for f in "$src"/sdk/api/*.go; do
	case $f in *_test.go) continue ;; esac
	cp "$f" "$dest/api/"
done
cp "$src/sdk/go.mod" "$dest/go.mod"
cat > "$dest/README" <<README
The client library of ${ref:-the working tree} (sdk and sdk/api without tests). Made by scripts/prev-sdk.sh; do not edit.
internal/server prevsdk_test.go builds a program with it (../prevsdk_driver) and runs it against the current server.
README
echo "baseline: $(ls "$dest"/*.go "$dest"/api/*.go | wc -l) files from ${ref:-the working tree}"
