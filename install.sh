#!/usr/bin/env bash
# spark-mcp installer for macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/vaxann/spark-mcp/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/vaxann/spark-mcp/main/install.sh | bash -s -- --service
#
# Installs the spark-mcp binary to ~/.local/bin and, with --service, a
# LaunchAgent serving MCP over HTTP with a generated bearer token and OAuth
# password. Re-running upgrades the binary and keeps existing secrets.
set -euo pipefail

REPO="vaxann/spark-mcp"
LABEL="io.github.vaxann.spark-mcp"
BIN_DIR="${SPARK_MCP_BIN_DIR:-$HOME/.local/bin}"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
LOG_DIR="$HOME/Library/Logs/spark-mcp"
SPARK_BIN="${SPARK_MCP_BIN:-/usr/local/bin/spark}"
VERSION="latest"
SERVICE=0
PORT=""
LISTEN=""
PUBLIC_URL=""
ROTATE=0
UNINSTALL=0
FROM_SOURCE=0

usage() {
  cat <<'EOF'
Usage: install.sh [options]

  (no options)          install or upgrade the binary for local (stdio) use
  --service             also run spark-mcp as a LaunchAgent over HTTP
  --port N              HTTP port on 127.0.0.1 (default 8766)
  --listen HOST:PORT    HTTP listen address (a token is always generated)
  --public-url URL      public HTTPS URL, e.g. https://spark.example.com
  --rotate-secrets      generate a new token and OAuth password
  --version vX.Y.Z      install a specific release (default: latest)
  --from-source         build with `go install` instead of downloading
  --uninstall           stop and remove the LaunchAgent and the binary
  -h, --help            show this help

With curl, pass options after `bash -s --`:
  curl -fsSL https://raw.githubusercontent.com/vaxann/spark-mcp/main/install.sh | bash -s -- --service --public-url https://spark.example.com
EOF
}

say()  { printf '\033[1m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --service) SERVICE=1 ;;
    --port) PORT="${2:?--port needs a value}"; shift ;;
    --listen) LISTEN="${2:?--listen needs a value}"; shift ;;
    --public-url) PUBLIC_URL="${2:?--public-url needs a value}"; shift ;;
    --rotate-secrets) ROTATE=1 ;;
    --version) VERSION="${2:?--version needs a value}"; shift ;;
    --from-source) FROM_SOURCE=1 ;;
    --uninstall) UNINSTALL=1 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "unknown option: $1" ;;
  esac
  shift
done

[ "$(uname -s)" = "Darwin" ] || die "spark-mcp runs on macOS, next to Spark Desktop"

if [ "$UNINSTALL" = 1 ]; then
  launchctl bootout "gui/$(id -u)" "$PLIST" 2>/dev/null || true
  rm -f "$PLIST" "$BIN_DIR/spark-mcp"
  say "removed the LaunchAgent and $BIN_DIR/spark-mcp"
  say "kept OAuth state and logs: ~/Library/Application Support/spark-mcp, $LOG_DIR"
  exit 0
fi

# ---- binary ----

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

install_release() {
  local arch asset base
  case "$(uname -m)" in
    arm64) arch=arm64 ;;
    x86_64) arch=amd64 ;;
    *) die "unsupported CPU: $(uname -m)" ;;
  esac
  asset="spark-mcp_darwin_${arch}.tar.gz"
  if [ -n "${SPARK_MCP_RELEASE_URL:-}" ]; then
    base="${SPARK_MCP_RELEASE_URL%/}"   # mirror or local test server
  elif [ "$VERSION" = latest ]; then
    base="https://github.com/$REPO/releases/latest/download"
  else
    base="https://github.com/$REPO/releases/download/$VERSION"
  fi
  say "downloading $asset ($VERSION)"
  curl -fsSL "$base/$asset" -o "$TMP/$asset" || return 1
  curl -fsSL "$base/checksums.txt" -o "$TMP/checksums.txt" || return 1
  (cd "$TMP" && grep " $asset\$" checksums.txt | shasum -a 256 -c - >/dev/null) || die "checksum mismatch for $asset"
  tar -xzf "$TMP/$asset" -C "$TMP"
  mkdir -p "$BIN_DIR"
  install -m 0755 "$TMP/spark-mcp" "$BIN_DIR/spark-mcp"
}

install_source() {
  command -v go >/dev/null 2>&1 || die "Go is not installed (brew install go) and no release could be downloaded"
  say "building from source with go install (@$VERSION)"
  mkdir -p "$BIN_DIR"
  GOBIN="$BIN_DIR" go install "github.com/$REPO/cmd/spark-mcp@$VERSION"
}

if [ "$FROM_SOURCE" = 1 ]; then
  install_source
elif ! install_release; then
  warn "no release binary available, falling back to building from source"
  install_source
fi
say "installed $("$BIN_DIR/spark-mcp" -version) to $BIN_DIR/spark-mcp"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) warn "$BIN_DIR is not on PATH; add it: echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> ~/.zshrc" ;;
esac

# ---- Spark ----

if [ ! -x "$SPARK_BIN" ]; then
  warn "spark CLI not found at $SPARK_BIN: open Spark Desktop > Settings > AI Agents > Spark CLI Setup"
elif "$BIN_DIR/spark-mcp" -check 2>"$TMP/check"; then
  say "$(cat "$TMP/check")"
else
  warn "Spark is not reachable yet: $(cat "$TMP/check")"
  warn "start Spark Desktop and enable the CLI; the server picks it up automatically"
fi

if [ "$SERVICE" = 0 ]; then
  cat <<EOF

Local use (stdio) on this Mac:
  claude mcp add spark -- $BIN_DIR/spark-mcp

Claude Desktop (claude_desktop_config.json):
  { "mcpServers": { "spark": { "command": "$BIN_DIR/spark-mcp" } } }

To reach Spark from the Claude apps or other computers, run the installer with --service.
EOF
  exit 0
fi

# ---- LaunchAgent ----

existing() {
  if [ -f "$PLIST" ]; then
    plutil -extract "EnvironmentVariables.$1" raw "$PLIST" 2>/dev/null || true
  fi
}
xml_escape() { printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'; }

TOKEN="$(existing SPARK_MCP_HTTP_TOKEN)"
PASSWORD="$(existing SPARK_MCP_OAUTH_PASSWORD)"
if [ "$ROTATE" = 1 ] || [ -z "$TOKEN" ]; then TOKEN="$(openssl rand -hex 32)"; fi
if [ "$ROTATE" = 1 ] || [ -z "$PASSWORD" ]; then PASSWORD="$(openssl rand -base64 32 | tr -dc 'A-Za-z0-9' | cut -c1-24)"; fi
if [ -z "$LISTEN" ]; then
  if [ -n "$PORT" ]; then
    LISTEN="127.0.0.1:$PORT"
  else
    LISTEN="$(existing SPARK_MCP_HTTP_LISTEN)"
    LISTEN="${LISTEN:-127.0.0.1:8766}"
  fi
fi
[ -n "$PUBLIC_URL" ] || PUBLIC_URL="$(existing SPARK_MCP_PUBLIC_URL)"
case "$PUBLIC_URL" in
  ""|https://*|http://*) PUBLIC_URL="${PUBLIC_URL%/}" ;;
  *) die "--public-url must start with https://" ;;
esac

mkdir -p "$(dirname "$PLIST")" "$LOG_DIR"
umask 077
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$(xml_escape "$BIN_DIR/spark-mcp")</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>SPARK_MCP_HTTP_LISTEN</key>
    <string>$(xml_escape "$LISTEN")</string>
    <key>SPARK_MCP_HTTP_TOKEN</key>
    <string>$TOKEN</string>
    <key>SPARK_MCP_OAUTH_PASSWORD</key>
    <string>$PASSWORD</string>
    <key>SPARK_MCP_PUBLIC_URL</key>
    <string>$(xml_escape "$PUBLIC_URL")</string>
    <key>SPARK_MCP_BIN</key>
    <string>$(xml_escape "$SPARK_BIN")</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ProcessType</key>
  <string>Interactive</string>
  <key>StandardOutPath</key>
  <string>$(xml_escape "$LOG_DIR/spark-mcp.log")</string>
  <key>StandardErrorPath</key>
  <string>$(xml_escape "$LOG_DIR/spark-mcp.log")</string>
</dict>
</plist>
EOF
chmod 600 "$PLIST"
plutil -lint "$PLIST" >/dev/null || die "generated plist is invalid: $PLIST"

launchctl bootout "gui/$(id -u)" "$PLIST" 2>/dev/null || true
# A just-stopped agent can briefly refuse a new bootstrap: retry.
for attempt in 1 2 3 4 5; do
  launchctl bootstrap "gui/$(id -u)" "$PLIST" 2>/dev/null && break
  if [ "$attempt" = 5 ]; then die "launchctl bootstrap failed; try: launchctl bootstrap gui/$(id -u) $PLIST"; fi
  sleep 1
done
HEALTH_HOST="${LISTEN%:*}"
case "$HEALTH_HOST" in ""|0.0.0.0|"[::]") HEALTH_HOST=127.0.0.1 ;; esac
HEALTH="http://$HEALTH_HOST:${LISTEN##*:}/healthz"
for _ in $(seq 1 30); do
  curl -fsS "$HEALTH" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -fsS "$HEALTH" >/dev/null 2>&1 || die "service did not start; see $LOG_DIR/spark-mcp.log"
say "LaunchAgent $LABEL is running on $LISTEN"

ENDPOINT="${PUBLIC_URL:-http://$LISTEN}/mcp"
cat <<EOF

MCP endpoint:  $ENDPOINT
Logs:          $LOG_DIR/spark-mcp.log
Settings:      $PLIST  (mode 0600)

Show the secrets on this Mac:
  plutil -extract EnvironmentVariables.SPARK_MCP_OAUTH_PASSWORD raw "$PLIST"   # Claude app sign-in
  plutil -extract EnvironmentVariables.SPARK_MCP_HTTP_TOKEN raw "$PLIST"       # bearer token

Claude Code on another computer:
  claude mcp add --transport http spark $ENDPOINT --header "Authorization: Bearer <token>"

Claude apps (web, desktop, iOS, Android): Settings > Connectors > Add custom connector,
URL $ENDPOINT, then sign in with the OAuth password.
EOF
if [ -z "$PUBLIC_URL" ]; then
  cat <<EOF

Next: publish http://$LISTEN over HTTPS (for example a Cloudflare Tunnel) and re-run
  install.sh --service --public-url https://your-hostname
EOF
fi
