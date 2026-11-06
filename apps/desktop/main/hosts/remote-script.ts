// The POSIX shell program the desktop pipes to `ssh <host> sh -s -- …` to
// install and run prismd on a remote macOS or Linux machine. It only needs
// sh, uname, tar and a sha256 tool; curl or wget is used when the machine can
// download the release itself.
//
// Usage: sh -s -- --version V --port P --base-url URL [--archive PATH --sha256 HEX | --binary PATH]
//
// stdout carries key=value lines for the desktop. Exit 20 means the machine
// could not download the release and needs the archive uploaded. Any other
// failure exits non-zero with one `prism-remote: …` line on stderr.
export const REMOTE_INSTALL_SCRIPT = `set -u
# Braces make sh read the whole script before running any of it, so no
# command below can swallow the rest of the script from stdin.
{
ROOT="$HOME/.prism/remote"
LABEL=sh.prism.remote
UNIT=prism-remote.service
VERSION='' PORT='' BASE='' ARCHIVE='' SHA='' BINARY=''
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION=$2; shift 2 ;;
    --port) PORT=$2; shift 2 ;;
    --base-url) BASE=$2; shift 2 ;;
    --archive) ARCHIVE=$2; shift 2 ;;
    --sha256) SHA=$2; shift 2 ;;
    --binary) BINARY=$2; shift 2 ;;
    *) printf 'prism-remote: unknown argument %s\\n' "$1" >&2; exit 2 ;;
  esac
done
die() { printf 'prism-remote: %s\\n' "$1" >&2; exit "\${2:-1}"; }
say() { printf '%s\\n' "$1"; }
[ -n "$VERSION" ] && [ -n "$PORT" ] || die "--version and --port are required" 2

os=$(uname -s)
arch=$(uname -m)
case "$os" in
  Linux) os=linux ;;
  Darwin)
    os=darwin
    # A shell under Rosetta reports x86_64 on Apple silicon.
    [ "$(sysctl -in hw.optional.arm64 2>/dev/null)" = 1 ] && arch=arm64 ;;
  *) die "prism can be installed on macOS and Linux; this machine runs $os" 10 ;;
esac
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "prism has no build for the $arch processor" 10 ;;
esac
TARGET="\${os}_\${arch}"
say "target=$TARGET"

DIR="$ROOT/versions/$VERSION"
mkdir -p "$ROOT/versions" || die "cannot create $ROOT"

LOCK="$ROOT/install.lock"
waited=0
while ! mkdir "$LOCK" 2>/dev/null; do
  holder=$(cat "$LOCK/pid" 2>/dev/null)
  if [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; then rm -rf "$LOCK"; continue; fi
  [ "$waited" -ge 120 ] && die "another prism install on this machine has held $LOCK for two minutes"
  sleep 1; waited=$((waited + 1))
done
echo $$ > "$LOCK/pid"
STAGE="$ROOT/staging.$$"
trap 'rm -rf "$LOCK" "$STAGE"' EXIT
trap 'exit 1' INT TERM

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d' ' -f1
  elif command -v openssl >/dev/null 2>&1; then openssl dgst -sha256 "$1" | sed 's/.*= *//'
  else return 1; fi
}
fetch() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL --connect-timeout 15 --max-time 300 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then wget -q -T 30 -O "$2" "$1"
  else return 1; fi
}
verify() {
  got=$(sha256_of "$1") || die "this machine needs sha256sum, shasum or openssl to check the download" 12
  [ "$got" = "$2" ] || die "checksum mismatch for prism $VERSION ($TARGET): expected $2, got $got" 12
}

state=current
if ! { [ -x "$DIR/prism" ] && [ "$(cat "$DIR/.install-complete" 2>/dev/null)" = "$VERSION" ]; }; then
  rm -rf "$STAGE"; mkdir -p "$STAGE"
  if [ -n "$BINARY" ]; then
    cp "$BINARY" "$STAGE/prism" && chmod 755 "$STAGE/prism" || die "cannot unpack the uploaded prism"
  else
    name="prism_\${VERSION}_\${TARGET}.tar.gz"
    if [ -z "$ARCHIVE" ]; then
      if [ -z "$BASE" ] || ! fetch "$BASE/v$VERSION/SHA256SUMS" "$STAGE/SHA256SUMS" || ! fetch "$BASE/v$VERSION/$name" "$STAGE/$name"; then
        say "need=archive"
        exit 20
      fi
      SHA=$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1 }' "$STAGE/SHA256SUMS")
      [ -n "$SHA" ] || die "the prism $VERSION release has no build for $TARGET" 11
      ARCHIVE="$STAGE/$name"
    fi
    [ -n "$SHA" ] || die "--archive needs --sha256" 2
    verify "$ARCHIVE" "$SHA"
    tar -xzf "$ARCHIVE" -C "$STAGE" prism || die "cannot unpack $ARCHIVE"
    rm -f "$STAGE/$name" "$STAGE/SHA256SUMS"
  fi
  ran=$("$STAGE/prism" version 2>&1) || die "the prism $TARGET build does not run on this machine: $ran" 13
  printf '%s\\n' "$VERSION" > "$STAGE/.install-complete"
  rm -rf "$DIR" && mv "$STAGE" "$DIR" || die "cannot move prism into $DIR"
  state=installed
fi
previous=$(readlink "$ROOT/current" 2>/dev/null)
ln -sfn "$DIR" "$ROOT/current" || die "cannot point $ROOT/current at $DIR"

probe() {
  url="http://127.0.0.1:$PORT/api/v1/health"
  if command -v curl >/dev/null 2>&1; then curl -fsS --max-time 2 "$url" 2>/dev/null
  elif command -v wget >/dev/null 2>&1; then wget -q -T 2 -O - "$url" 2>/dev/null
  else return 1; fi
}
version_of() { sed -n 's/.*"version":"\\([^"]*\\)".*/\\1/p'; }
owned_pids() { pgrep -f "$ROOT/[^ ]*prism daemon --listen 127.0.0.1:$PORT" 2>/dev/null; }

uid=$(id -u)
service=none
if [ "$os" = linux ] && command -v systemctl >/dev/null 2>&1; then
  if ! systemctl --user show-environment >/dev/null 2>&1 && [ -d "/run/user/$uid" ]; then
    XDG_RUNTIME_DIR="/run/user/$uid"; export XDG_RUNTIME_DIR
  fi
  # The user manager must share this HOME, or it would never read our unit.
  if menv=$(systemctl --user show-environment 2>/dev/null); then
    mconf=$(printf '%s\\n' "$menv" | sed -n 's/^XDG_CONFIG_HOME=//p')
    [ "$(printf '%s\\n' "$menv" | sed -n 's/^HOME=//p')" = "$HOME" ] && service=systemd
  fi
elif [ "$os" = darwin ] && launchctl print "gui/$uid" >/dev/null 2>&1; then
  service=launchd
fi
UNIT_PATH="\${mconf:-$HOME/.config}/systemd/user/$UNIT"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

running=$(probe)
if [ -n "$running" ] && [ -z "$(owned_pids)" ]; then
  say "state=adopted"
  say "version=$(printf '%s' "$running" | version_of)"
  exit 0
fi
if [ "$state" = current ] && [ "$previous" = "$DIR" ] && [ -n "$running" ] &&
   [ "$(printf '%s' "$running" | version_of)" = "$VERSION" ]; then
  case "$service" in
    systemd) [ -f "$UNIT_PATH" ] && systemctl --user is-enabled "$UNIT" >/dev/null 2>&1 && settled=yes ;;
    launchd) [ -f "$PLIST" ] && launchctl print "gui/$uid/$LABEL" >/dev/null 2>&1 && settled=yes ;;
    none) settled=yes ;;
  esac
  if [ "\${settled:-}" = yes ]; then
    say "state=current"; say "service=$service"; say "version=$VERSION"
    exit 0
  fi
fi
[ -n "$running" ] && [ "$state" = current ] && state=updated
[ "$previous" != "$DIR" ] && [ -n "$previous" ] && state=updated

[ "$service" = systemd ] && systemctl --user stop "$UNIT" >/dev/null 2>&1
[ "$os" = darwin ] && launchctl bootout "gui/$uid/$LABEL" >/dev/null 2>&1
for pid in $(owned_pids); do kill "$pid" 2>/dev/null; done
i=0
while [ -n "$(owned_pids)" ] && [ $i -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
for pid in $(owned_pids); do kill -9 "$pid" 2>/dev/null; done
rm -f "$ROOT/daemon.pid" "$ROOT/prism"

BIN="$ROOT/current/prism"
LOG="$ROOT/prism.log"
if [ "$service" = systemd ]; then
  mkdir -p "$(dirname "$UNIT_PATH")"
  cat > "$UNIT_PATH" <<UNIT_EOF
[Unit]
Description=prism daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart="$BIN" daemon --listen 127.0.0.1:$PORT
Restart=always
RestartSec=2
StandardOutput=append:$LOG
StandardError=append:$LOG

[Install]
WantedBy=default.target
UNIT_EOF
  systemctl --user daemon-reload && systemctl --user enable "$UNIT" >/dev/null 2>&1 &&
    systemctl --user restart "$UNIT" || service=none
  if [ "$service" = systemd ]; then
    linger=$(loginctl show-user "$(id -un)" --property=Linger --value 2>/dev/null)
    if [ "$linger" != yes ] && loginctl enable-linger "$(id -un)" >/dev/null 2>&1; then linger=yes; fi
    say "linger=\${linger:-unknown}"
  fi
elif [ "$service" = launchd ]; then
  xml() { printf '%s' "$1" | sed -e 's/&/\\&amp;/g' -e 's/</\\&lt;/g' -e 's/>/\\&gt;/g'; }
  mkdir -p "$(dirname "$PLIST")"
  cat > "$PLIST" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$(xml "$BIN")</string>
    <string>daemon</string>
    <string>--listen</string>
    <string>127.0.0.1:$PORT</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$(xml "$LOG")</string>
  <key>StandardErrorPath</key><string>$(xml "$LOG")</string>
</dict>
</plist>
PLIST_EOF
  launchctl bootstrap "gui/$uid" "$PLIST" >/dev/null 2>&1 || service=none
fi
if [ "$service" = none ]; then
  [ -f "$UNIT_PATH" ] && { systemctl --user disable "$UNIT" >/dev/null 2>&1; rm -f "$UNIT_PATH"; }
  nohup "$BIN" daemon --listen "127.0.0.1:$PORT" >> "$LOG" 2>&1 < /dev/null &
  echo $! > "$ROOT/daemon.pid"
fi

for old in "$ROOT"/versions/*; do
  [ -d "$old" ] || continue
  [ "$old" = "$DIR" ] || [ "$old" = "$previous" ] || rm -rf "$old"
done
rm -rf "$ROOT/incoming"
say "state=$state"
say "service=$service"
say "version=$VERSION"
}
`
