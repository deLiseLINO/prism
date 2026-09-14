#!/usr/bin/env bash
# Host driver for the prism agent-install container matrix.
#
#   build          compile prismd/prismctl (static) and build the cell images
#   cell <name>    run one cell (see the table below)
#   all            run every cell sequentially
#   summary        aggregate outcome.txt files into $EVID/summary.tsv
#
# Evidence lands in $PRISM_EVID (default ~/prism-agenttest-evidence), one
# directory per cell. Containers are disposable: repo and binaries mount
# read-only, all package-manager state lives and dies with the container.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/../.." && pwd)
DIST=$REPO/verify/scripts/dist
EVID=${PRISM_EVID:-$HOME/prism-agenttest-evidence}
NODE_IMAGE=prism-test/node
UBUNTU_IMAGE=prism-test/ubuntu
BREW_IMAGE=prism-test/brew
FULL_PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
build() {
  mkdir -p "$DIST"
  CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) go build -o "$DIST/prismd" ./cmd/prismd
  CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) go build -o "$DIST/prismctl" ./cmd/prismctl
  docker build -t "$NODE_IMAGE" -f "$REPO"/verify/scripts/Dockerfile.node "$REPO"/verify/scripts
  docker build -t "$UBUNTU_IMAGE" -f "$REPO"/verify/scripts/Dockerfile.ubuntu "$REPO"/verify/scripts
  docker build --build-arg "CELL_UID=$(id -u)" -t "$BREW_IMAGE" -f "$REPO"/verify/scripts/Dockerfile.brew "$REPO"/verify/scripts
}

# name image port daemon-path pre mid agent update-agent
cells() {
  cat <<'EOF'
node-npm-codex|node|17201||||codex|codex
node-npm-claude|node|17202||||claude|claude
node-npm-grok|node|17203|/opt/nobash:/usr/local/bin|mkdir -p /opt/nobash && for f in /usr/bin/*; do case ${f##*/} in bash) ;; *) ln -sf "$f" /opt/nobash/ ;; esac; done||grok|grok
node-npm-pi|node|17204||||pi|pi
node-npm-opencode|node|17205||||opencode|opencode
node-npm-opencode2|node|17206||||opencode2|opencode2
node-npm-hermes|node|17207||||hermes|hermes
node-pnpm-classify|node|17208|/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/root/.local/share/pnpm:/root/.local/share/pnpm/bin|npm install -g pnpm && export PNPM_HOME=/root/.local/share/pnpm && export PATH=/usr/local/bin:$PNPM_HOME:$PNPM_HOME/bin:$PATH && pnpm add -g @openai/codex|||codex
node-bun-omp|node|17209|/usr/local/bin:/root/.bun/bin|npm install -g bun||omp|omp
ubuntu-clean|ubuntu|17210|||apt-get update && apt-get install -y nodejs npm|codex|codex
script-codex|ubuntu|17211||||codex|codex
script-claude|ubuntu|17212||||claude|claude
script-grok|ubuntu|17213||||grok|grok
script-pi|ubuntu|17214||||pi|
script-opencode|ubuntu|17215||||opencode|opencode
brew-opencode|brew|17216||||opencode|opencode
EOF
}

image_for() {
  case $1 in
    node) echo "$NODE_IMAGE" ;;
    ubuntu) echo "$UBUNTU_IMAGE" ;;
    brew) echo "$BREW_IMAGE" ;;
  esac
}

run_cell() {
  local name=$1 image=$2 port=$3 daemon_path=${4:-} pre=${5:-} mid=${6:-} agent=${7:-} upd=${8:-}
  local img out rc user_args=()
  img=$(image_for "$image")
  if [ "$image" = brew ]; then
    user_args=(--user "$(id -u):$(id -g)")
    daemon_path=/home/linuxbrew/.linuxbrew/bin:$FULL_PATH
  fi
  out=$EVID/$name
  rm -rf "$out"
  mkdir -p "$out"
  echo "== cell $name (image $image, port $port)"
  rc=0
  /opt/homebrew/opt/coreutils/libexec/gnubin/timeout 1800 docker run --rm \
    --name "prism-agenttest-$name" \
    -v "$REPO":/repo:ro -v "$DIST":/dist:ro -v "$out":/evidence \
    "${user_args[@]}" \
    -e CELL="$name" -e PORT="$port" -e DAEMON_PATH="$daemon_path" \
    -e PRE="$pre" -e MID="$mid" -e AGENT="$agent" -e UPDATE_AGENT="$upd" \
    "$img" bash /repo/verify/scripts/cell.sh || rc=$?
  if [ $rc -ne 0 ] && ! [ -s "$out/outcome.txt" ]; then
    printf 'cell=%s verdict=fail reason=container-exit-%s\n' "$name" "$rc" > "$out/outcome.txt"
  fi
  cat "$out/outcome.txt"
}

find_cell() {
  local want=$1 line
  while IFS='|' read -r name image port rest; do
    [ "$name" = "$want" ] && { echo "$name|$image|$port|$rest"; return 0; }
  done < <(cells)
  echo "unknown cell: $want (see cells table)" >&2
  return 1
}

summary() {
  printf 'cell\tinstall\tupdate\tsource\tverdict\n' > "$EVID/summary.tsv"
  for out in "$EVID"/*/outcome.txt; do
    [ -f "$out" ] || continue
    sed 's/ /\t/g' "$out" >> "$EVID/summary.tsv"
  done
  column -t -s $'\t' "$EVID/summary.tsv" 2>/dev/null || cat "$EVID/summary.tsv"
}

case ${1:-} in
  build) build ;;
  cell)
    [ $# -eq 2 ] || { echo "usage: $0 cell <name>" >&2; exit 2; }
    IFS='|' read -r name image port daemon_path pre mid agent upd < <(find_cell "$2")
    run_cell "$name" "$image" "$port" "$daemon_path" "$pre" "$mid" "$agent" "$upd"
    ;;
  all)
    while IFS='|' read -r name image port daemon_path pre mid agent upd; do
      run_cell "$name" "$image" "$port" "$daemon_path" "$pre" "$mid" "$agent" "$upd"
    done < <(cells)
    summary
    ;;
  summary) summary ;;
  *)
    echo "usage: $0 build|cell <name>|all|summary" >&2
    exit 2
    ;;
esac
