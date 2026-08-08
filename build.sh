#!/usr/bin/env bash
# Build + sign + package Cli Times for local use and sharing.
# Private key stays in keys/ (gitignored) and is never bundled.
set -euo pipefail
export PATH="/opt/homebrew/bin:$PATH"
cd "$(dirname "$0")"

KEYDIR="keys"
KID="k1"
DIST="dist"
BUNDLE="${1:-feed-bundles/2026-08-08.json}"

# 1. Keygen once (offline). Never overwrite an existing key.
if [ ! -f "$KEYDIR/priv.key" ]; then
  go run ./cmd/sign keygen -out "$KEYDIR" -kid "$KID" >/dev/null
  echo "[keygen] new offline keypair in $KEYDIR/ (gitignored)"
else
  echo "[keygen] reusing existing $KEYDIR/priv.key"
fi
PUB="$(cat "$KEYDIR/pub.key")"
echo "[pubkey] $PUB (pinned into binaries)"

# 2. Sign the feed bundle → cache.json (the artifact the renderer reads).
mkdir -p "$DIST"
go run ./cmd/sign bundle -key "$KEYDIR/priv.key" -kid "$KID" -in "$BUNDLE" -out "$DIST/cache.json" -expires-hours "${CLI_TIMES_EXPIRES_HOURS:-120}"
echo "[sign] $BUNDLE -> $DIST/cache.json ($(wc -c < "$DIST/cache.json") bytes)"

# 3. Cross-compile the renderer with the pubkey PINNED in (no env needed).
LDFLAGS="-s -w -X main.pinnedKeyB64=$PUB -X main.pinnedKeyID=$KID -X main.version=0.1.0"
build() { # os arch out
  GOOS="$1" GOARCH="$2" go build -trimpath -ldflags "$LDFLAGS" -o "$DIST/$3" ./cmd/renderer
  echo "[build] $3"
}
build darwin arm64  cli-times-darwin-arm64
build darwin amd64  cli-times-darwin-amd64
build linux  amd64  cli-times-linux-amd64
build linux  arm64  cli-times-linux-arm64

# 4. Cross-compile the updater with pubkey + FEED URL pinned.
#    Set CLI_TIMES_FEED_URL to your Cloudflare Pages/R2 URL before running, e.g.:
#      CLI_TIMES_FEED_URL=https://cli-times-feed.pages.dev/feed.json bash build.sh <bundle>
#    (Left empty, the updater falls back to the CLI_TIMES_FEED_URL env var at runtime.)
: "${CLI_TIMES_FEED_URL:?set CLI_TIMES_FEED_URL before building updaters (e.g. https://cli-times-feed.pages.dev/feed.json) — a URL-less updater can never fetch, so every install would stay frozen}"
FEED_URL="$CLI_TIMES_FEED_URL"
LDU="-s -w -X main.pinnedKeyB64=$PUB -X main.pinnedKeyID=$KID -X main.feedURL=$FEED_URL"
buildu() { # os arch out
  GOOS="$1" GOARCH="$2" go build -trimpath -ldflags "$LDU" -o "$DIST/$3" ./cmd/updater
  echo "[build] $3"
}
buildu darwin arm64  cli-times-update-darwin-arm64
buildu darwin amd64  cli-times-update-darwin-amd64
buildu linux  amd64  cli-times-update-linux-amd64
buildu linux  arm64  cli-times-update-linux-arm64
echo "[updater] feed URL pinned: $FEED_URL"

# 5. Package the trial tarball so it ALWAYS matches the freshly built + signed
#    artifacts (no more manual, drifting tarball). Includes BOTH renderer and
#    updater binaries for all 4 targets, the seed cache, the installer, and the
#    launchd/systemd timer templates that install.sh needs to schedule updates.
tar czf "$DIST/cli-times-trial.tar.gz" \
  -C "$DIST" install.sh README.txt cache.json \
    cli-times-darwin-arm64 cli-times-darwin-amd64 cli-times-linux-amd64 cli-times-linux-arm64 \
    cli-times-update-darwin-arm64 cli-times-update-darwin-amd64 cli-times-update-linux-amd64 cli-times-update-linux-arm64 \
  -C "$(pwd)" packaging/launchd/com.clitimes.updater.plist \
              packaging/systemd/cli-times-update.service packaging/systemd/cli-times-update.timer
echo "[package] $DIST/cli-times-trial.tar.gz ($(wc -c < "$DIST/cli-times-trial.tar.gz") bytes)"

echo "[done] artifacts in $DIST/"
