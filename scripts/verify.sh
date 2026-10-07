#!/bin/sh
set -eu
base_ref="${1:-origin/master}"
verify_tmp="$(mktemp -d "${TMPDIR:-/tmp}/lime2-verify.XXXXXX")"
trap 'rm -rf "$verify_tmp"' EXIT HUP INT TERM
mkdir -p "$verify_tmp/v8"
unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
 echo "Go files require formatting: $unformatted"
 exit 1
fi
git diff --check
go vet ./...
go test -race -coverprofile="$verify_tmp/go.cover" ./...
go build ./...
NODE_V8_COVERAGE="$verify_tmp/v8" node --test \
 --experimental-test-coverage --test-coverage-lines=90 \
 --test-coverage-functions=90 --test-coverage-branches=80 \
 --test-coverage-include='examples/lime2-demo/client.js' \
 examples/lime2-demo/client.test.mjs scripts/diff-coverage.test.mjs
node scripts/diff-coverage.mjs --base "$base_ref" --threshold 90 \
 --go-profile "$verify_tmp/go.cover" --v8-directory "$verify_tmp/v8"
