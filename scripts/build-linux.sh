#!/bin/sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
mkdir -p "$ROOT/app/bin" "$ROOT/dist"
mkdir -p "$ROOT/app/ui/web"
cp -R "$ROOT/ui/." "$ROOT/app/ui/web/"

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
  -trimpath \
  -ldflags="-s -w" \
  -o "$ROOT/app/bin/fn-badblocks-victoria" \
  "$ROOT/src"

chmod +x "$ROOT/app/bin/fn-badblocks-victoria" "$ROOT/cmd/main"
chmod +x "$ROOT"/cmd/*

if command -v fnpack >/dev/null 2>&1; then
  fnpack build --directory "$ROOT"
else
  echo "fnpack not found; binary built at app/bin/fn-badblocks-victoria" >&2
  echo "Install fnpack from the fnOS developer toolkit, then rerun this script to create .fpk." >&2
fi
