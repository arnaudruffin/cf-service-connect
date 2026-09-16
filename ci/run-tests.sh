#!/bin/bash

# Fail the build on any error rather than reporting success after a failed step.
set -euo pipefail

echo "--- gofmt ---"
unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  echo "These files are not gofmt'd:"
  echo "$unformatted"
  exit 1
fi

echo "--- go vet ---"
go vet ./...

echo "--- go build (all release targets) ---"
# Cross-compiling here catches platform-specific breakage before release rather
# than during it.
for target in darwin/amd64 darwin/arm64 linux/amd64 linux/386 windows/amd64 windows/386; do
  GOOS="${target%/*}" GOARCH="${target#*/}" go build -o /dev/null .
done

echo "--- go test (race detector) ---"
go test -race ./...
