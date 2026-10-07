#!/bin/bash
set -euo pipefail
REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
if [ -n "${PRISM_VERIFY_EVIDENCE_DIR:-}" ]; then
  WORK=$PRISM_VERIFY_EVIDENCE_DIR
  [ ! -e "$WORK" ] && [ ! -L "$WORK" ] || { printf 'proof evidence destination must be new\n' >&2; exit 1; }
  mkdir -p "$(dirname "$WORK")"
  mkdir -m 700 "$WORK"
else
  WORK=$(mktemp -d /tmp/prism-runtime-proof.XXXXXX)
fi
WORK=$(cd "$WORK" && pwd -P)
(cd "$REPO_ROOT" && go build -o "$WORK/prism" ./cmd/prism)
python3 "$REPO_ROOT/verify/scripts/owned-runtime-proof.py" "$REPO_ROOT" "$WORK" | tee "$WORK/proof.log"
if [ "$(uname -s)" = Linux ]; then
  python3 "$REPO_ROOT/verify/scripts/inherited-fds-proof.py" "$WORK/prism" | tee "$WORK/inherited-fds-proof.log"
fi
rm "$WORK/prism"
printf 'evidence: %s\n' "$WORK"
