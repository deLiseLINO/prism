#!/bin/bash
# Focused UI verification for the max effort chip in ModelSettingsModal.
# Sets up the same isolated environment as integration-drive.sh, then drives
# the Providers view: open model settings, toggle the max chip, save, and
# read the persisted reasoningEfforts back from the management API.
set -euo pipefail

fail() { echo "FAIL: $1" >&2; exit 1; }

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
PORT="${PRISM_PORT:-18789}"
CDP_PORT="${PRISM_CDP_PORT:-19223}"
source "$REPO_ROOT/verify/scripts/owned-runtime.sh"
verify_init effort-max "$PORT" "$CDP_PORT"

cat > "$RUNDIR/.prism/prism.json" <<'CONFIG'
{
  "version": 1,
  "generation": 0,
  "config": {
    "version": 1,
    "daemon": {"listen": "127.0.0.1:__PORT__"},
    "providers": {
      "codex": {"wire": "codex", "models": ["gpt-5.6-luna"]}
    },
    "combos": {}, "routes": {}, "aliases": {}
  }
}
CONFIG
node - "$RUNDIR/.prism/prism.json" "$PORT" <<'CONFIG_PORT'
const fs = require('node:fs')
const file = process.argv[2]
const config = JSON.parse(fs.readFileSync(file, 'utf8'))
config.config.daemon.listen = `127.0.0.1:${process.argv[3]}`
fs.writeFileSync(file, JSON.stringify(config) + '\n')
CONFIG_PORT

go build -C "$REPO_ROOT" -o "$RUNDIR/prism" ./cmd/prism > "$RUNDIR/build.log" 2>&1 || fail "go build prism"
npm run build --prefix "$REPO_ROOT" >> "$RUNDIR/build.log" 2>&1 || fail "npm run build"

echo "==> launching isolated Electron app"
verify_start_electron || fail "owned Electron runtime failed"
cdp_eval() { node "$REPO_ROOT/verify/scripts/cdp-eval.mjs" "$WS" "$1" 2>&1 | tee -a "$EVID_WORK/assertions.log"; }

echo "==> opening Providers view"
cdp_eval 'await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const btn = [...document.querySelectorAll("nav button")].find((n) => n.textContent.includes("Providers"))
  if (!btn) throw new Error("Providers nav button not found")
  btn.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector("main h1")?.textContent.trim() === "Providers") break
    await sleep(100)
  }
  if (document.querySelector("main h1")?.textContent.trim() !== "Providers") throw new Error("Providers view did not open")
  return "open"
})()' || fail "Providers view did not open"

echo "==> opening model settings for gpt-5.6-luna"
cdp_eval 'await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  let edit = null
  for (let i = 0; i < 40; i++) {
    edit = [...document.querySelectorAll("button[aria-label]")].find((b) => b.getAttribute("aria-label") === "Edit gpt-5.6-luna")
    if (edit) break
    await sleep(100)
  }
  if (!edit) throw new Error("Edit gpt-5.6-luna button not found")
  edit.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector(".msm-efforts")) break
    await sleep(100)
  }
  const chips = [...document.querySelectorAll(".msm-efforts .msm-effort")].map((c) => c.textContent.trim())
  if (!chips.includes("max")) throw new Error("max chip missing; chips=" + JSON.stringify(chips))
  return {chips}
})()' || fail "model settings modal missing max chip"

echo "==> toggling max effort and saving"
cdp_eval 'await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const maxChip = [...document.querySelectorAll(".msm-efforts .msm-effort")].find((c) => c.textContent.trim() === "max")
  if (!maxChip) throw new Error("max chip not found")
  maxChip.click()
  await sleep(100)
  if (!maxChip.className.includes("msm-effort--on")) throw new Error("max chip did not toggle on: " + maxChip.className)
  const save = [...document.querySelectorAll(".msm-foot button")].find((b) => b.textContent.trim() === "Save")
  if (!save) throw new Error("Save button not found")
  save.click()
  await sleep(500)
  if (document.querySelector(".msm-efforts")) throw new Error("modal still open after save")
  return "saved"
})()' || fail "max effort toggle or save failed"

echo "==> reading persisted settings from management API"
curl -sf "http://127.0.0.1:$PORT/api/v1/providers" > "$EVID_WORK/providers.json" || fail "providers API unreachable"
node -e '
const doc = JSON.parse(require("fs").readFileSync(process.argv[1], "utf8"));
const prov = (doc.providers ?? []).find((p) => p.id === "codex");
const efforts = prov?.modelSettings?.["gpt-5.6-luna"]?.reasoningEfforts;
if (JSON.stringify(efforts) !== JSON.stringify(["max"])) {
  console.error("reasoningEfforts=" + JSON.stringify(efforts) + ", want [\"max\"]");
  process.exit(1);
}
console.log("management API reasoningEfforts=[\"max\"] OK");
' "$EVID_WORK/providers.json" 2>&1 | tee -a "$EVID_WORK/assertions.log" || fail "persisted reasoningEfforts mismatch"

echo "==> re-opening modal to confirm round-trip"
cdp_eval 'await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const edit = [...document.querySelectorAll("button[aria-label]")].find((b) => b.getAttribute("aria-label") === "Edit gpt-5.6-luna")
  if (!edit) throw new Error("Edit button gone after save")
  edit.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector(".msm-efforts")) break
    await sleep(100)
  }
  const maxChip = [...document.querySelectorAll(".msm-efforts .msm-effort")].find((c) => c.textContent.trim() === "max")
  if (!maxChip) throw new Error("max chip missing on reopen")
  if (!maxChip.className.includes("msm-effort--on")) throw new Error("max chip not pressed after reload: " + maxChip.className)
  const overrideRow = [...document.querySelectorAll(".msm-row-name")].some((n) => n.textContent.includes("Override reasoning efforts"))
  const cancel = [...document.querySelectorAll(".msm-foot button")].find((b) => b.textContent.trim() === "Cancel")
  cancel.click()
  return {roundTrip: "ok", overrideRow}
})()' || fail "round-trip verification failed"

echo "==> config file carries the max effort"
node -e '
const doc = JSON.parse(require("fs").readFileSync(process.argv[1], "utf8"));
const efforts = doc.config?.providers?.codex?.modelSettings?.["gpt-5.6-luna"]?.reasoningEfforts;
if (JSON.stringify(efforts) !== JSON.stringify(["max"])) {
  console.error("config file efforts=" + JSON.stringify(efforts));
  process.exit(1);
}
console.log("config file reasoningEfforts=[\"max\"] OK");
' "$RUNDIR/.prism/prism.json" 2>&1 | tee -a "$EVID_WORK/assertions.log" || fail "config file mismatch"

verify_finish 0 || fail "owned runtime cleanup failed"
echo "PASS: max effort chip verified end-to-end"
