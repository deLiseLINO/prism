import http.server
import importlib.util
from unittest.mock import patch
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import sys
import threading
import shutil

sys.dont_write_bytecode = True
repo, work = map(Path, sys.argv[1:])
runtime = repo / "verify/scripts/owned-runtime.sh"


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def drive(name, body, port=None, evidence=None):
    port = port or free_port()
    evidence = evidence or work / name
    env = dict(os.environ, PRISM_VERIFY_EVIDENCE_DIR=str(evidence))
    script = f"""set -euo pipefail
REPO_ROOT={shlex.quote(str(repo))}
source {shlex.quote(str(runtime))}
verify_init {shlex.quote(name)} {port}
printf '%s\\n' "$RUNDIR" > {shlex.quote(str(work / (name + '.sandbox')))}
cp {shlex.quote(str(work / 'prism'))} "$RUNDIR/prism"
printf '%s\\n' '{{"generation":0,"config":{{"version":1,"daemon":{{"listen":"127.0.0.1:{port}"}},"providers":{{}},"combos":{{}},"routes":{{}},"aliases":{{}}}}}}' > "$RUNDIR/.prism/prism.json"
{body}
"""
    result = subprocess.run(["/bin/bash", "-c", script], env=env, text=True, capture_output=True, timeout=90)
    (work / f"{name}.log").write_text(result.stdout + result.stderr)
    return result


class QuietHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"foreign listener")

    def log_message(self, *args):
        pass


with http.server.HTTPServer(("127.0.0.1", 0), QuietHandler) as server:
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    result = drive("foreign", "exit 0", port=server.server_port)
    assert result.returncode != 0, "occupied foreign listener accepted"
    with socket.create_connection(server.server_address, timeout=2):
        pass
    server.shutdown()
    thread.join()

result = drive("daemon", """verify_start_daemon
printf 'durable evidence\\n' > "$EVID_WORK/probe.txt"
verify_attest_daemon
verify_stop_daemon
verify_stop_daemon
""")
assert result.returncode == 0, result.stderr
sandbox = Path((work / "daemon.sandbox").read_text().strip())
assert not sandbox.exists(), "successful cleanup retained sandbox"
assert (work / "daemon/probe.txt").read_text() == "durable evidence\n"

result = drive("identity", """verify_start_daemon
cp "$VERIFY_HOME/.prism/daemon.json" "$RUNDIR/registration.saved"
python3 - "$VERIFY_HOME/.prism/daemon.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p));d['id']='foreign-registration'
with open(p,'w') as f:json.dump(d,f)
PY
if verify_stop_daemon; then echo 'mismatched identity accepted' >&2; exit 1; fi
curl -sf "http://127.0.0.1:$PORT/api/v1/health" > "$EVID_WORK/preserved.json"
cp "$RUNDIR/registration.saved" "$VERIFY_HOME/.prism/daemon.json"
verify_stop_daemon
""")
assert result.returncode != 0, "ownership refusal must remain a failed run"
assert json.loads((work / "identity/preserved.json").read_text())["status"] == "ok"
identity_sandbox = Path((work / "identity.sandbox").read_text().strip())
assert not (identity_sandbox / ".prism/daemon.json").exists(), "restored owner was not stopped"
shutil.rmtree(identity_sandbox)

bad_destination = work / "not-a-directory"
bad_destination.write_text("preserve this file\n")
result = drive("publish-failure", "verify_start_daemon", evidence=bad_destination)
assert result.returncode != 0, "evidence publication failure was hidden"
sandbox = Path((work / "publish-failure.sandbox").read_text().strip())
assert sandbox.is_dir(), "sandbox deleted after evidence failure"
assert bad_destination.read_text() == "preserve this file\n"
assert not (sandbox / ".prism/daemon.json").exists(), "daemon registration left after copy failure"
shutil.rmtree(sandbox)
spec = importlib.util.spec_from_file_location("owned_runtime", repo / "verify/scripts/owned-runtime.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
for name, operation in (("partial-copy", "copyfileobj"), ("remove-failure", "rmtree")):
    root = work / name
    (root / "evidence").mkdir(parents=True)
    (root / ".runtime").mkdir()
    (root / "evidence/probe.txt").write_text("retained proof\n")
    destination = work / (name + "-published")
    with patch.object(module.shutil, operation, side_effect=OSError("filesystem failure")):
        try:
            module.finalize(root, str(destination), False, 0, 0, True, True)
        except OSError:
            pass
        else:
            raise AssertionError(f"{name} accepted a filesystem failure")
    for receipt_path in (root / "evidence/runtime-teardown.json", destination / "runtime-teardown.json"):
        receipt = json.loads(receipt_path.read_text())
        assert receipt["cleanupStatus"] == 1 and receipt["finalization"] == "failed", receipt
    assert root.is_dir(), f"{name} deleted remaining evidence"

for name in ("replace-once", "private-receipt-failure"):
    root = work / name
    (root / "evidence").mkdir(parents=True)
    (root / ".runtime").mkdir()
    (root / "evidence/probe.txt").write_text("retained proof\n")
    destination = work / (name + "-published")
    if name == "replace-once":
        replace = module.os.replace
        failed = [False]
        def fail_complete_once(source, target):
            if Path(target) == destination / "runtime-teardown.json" and json.loads(Path(source).read_text())["finalization"] == "complete" and not failed[0]:
                failed[0] = True
                raise OSError("one-shot receipt replacement failure")
            return replace(source, target)
        fault = patch.object(module.os, "replace", side_effect=fail_complete_once)
    else:
        copy = module.shutil.copyfileobj
        write = module.write
        failed = [False]
        def fail_copy(source, target):
            failed[0] = True
            raise OSError("copy failure")
        def fail_private_receipt(path, value):
            if failed[0] and Path(path) == root / "evidence/runtime-teardown.json":
                raise OSError("private filesystem unavailable")
            return write(path, value)
        fault = patch.object(module.shutil, "copyfileobj", side_effect=fail_copy)
    with fault:
        with patch.object(module, "write", side_effect=fail_private_receipt) if name == "private-receipt-failure" else patch.object(module, "write", wraps=module.write):
            try:
                module.finalize(root, str(destination), False, 0, 0, True, True)
            except OSError:
                pass
            else:
                raise AssertionError(f"{name} hid receipt failure")
    receipt = json.loads((destination / "runtime-teardown.json").read_text())
    assert receipt["cleanupStatus"] == 1 and receipt["finalization"] == "failed", receipt

occupied = work / "occupied-output"
occupied.mkdir()
(occupied / "prism").write_text("user bytes\n")
result = subprocess.run(["/bin/bash", str(repo / "verify/scripts/owned-runtime-proof.sh")],
                        env=dict(os.environ, PRISM_VERIFY_EVIDENCE_DIR=str(occupied)), capture_output=True, timeout=10)
assert result.returncode != 0 and (occupied / "prism").read_text() == "user bytes\n"
print("owned runtime proof passed: foreign listener, service identity, repeated stop, durable evidence, finalization failures, occupied output")
