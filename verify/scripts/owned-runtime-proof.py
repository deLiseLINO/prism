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
spec = importlib.util.spec_from_file_location("owned_runtime", repo / "verify/scripts/owned-runtime.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def observation(stdout="", returncode=0, stderr=""):
    return subprocess.CompletedProcess([], returncode, stdout, stderr)


pid = 4242
start = "Tue Oct 6 00:49:38 2026"
executable = str(Path(sys.executable).resolve())
command = f"{executable} daemon"
record = {"pid": pid, "start": start, "command": command, "executable": executable}
live = observation(f"{start} S {command}\n")
exiting = observation(f"{start} ?Es (prism)\n")
missing = observation(returncode=1)
zombie = observation(f"{start} Z (prism)\n")
monitor_zombie = observation(f"{start} Z\n")
monitor_exiting = observation(f"{start} ?Es\n")
changed_start = "Tue Oct 6 00:49:39 2026"
for name, probe, platform, observations, kill_error, link_error, expected in (
    ("exited-between-reads", "process", "darwin", [exiting, missing, missing], ProcessLookupError(), None, None),
    ("alive-missing-comm", "process", "darwin", [exiting, missing], None, None, ValueError),
    ("permission-missing-comm", "process", "darwin", [exiting, missing], PermissionError(), None, PermissionError),
    ("probe-error", "process", "darwin", [exiting, missing], OSError("probe unavailable"), None, OSError),
    ("reused-during-confirmation", "process", "darwin", [exiting, missing, observation(f"{pid}\n")], ProcessLookupError(), None, ValueError),
    ("unreadable-confirmation", "process", "darwin", [exiting, missing, observation(returncode=2, stderr="inspection failed")], ProcessLookupError(), None, ValueError),
    ("exited-before-inspection", "process", "darwin", [missing, missing], ProcessLookupError(), None, None),
    ("alive-unreadable-inspection", "process", "darwin", [missing], None, None, ValueError),
    ("alive-zombie", "process", "darwin", [zombie], None, None, ValueError),
    ("linux-exited-exe", "process", "linux", [live, missing], ProcessLookupError(), FileNotFoundError(), None),
    ("linux-alive-missing-exe", "process", "linux", [live], None, FileNotFoundError(), ValueError),
    ("linux-permission-exe", "process", "linux", [live], ProcessLookupError(), PermissionError(), PermissionError),
    ("linux-exe-error", "process", "linux", [live], ProcessLookupError(), OSError("readlink unavailable"), OSError),
    ("owned-record", "process", "darwin", [live, observation(executable + "\n")], None, None, record),
    ("changed-start-record", "same", "darwin", [observation(f"{changed_start} S {command}\n"), observation(executable + "\n")], None, None, "changed"),
    ("owned-zombie-still-strict", "same", "darwin", [zombie], None, None, ValueError),
    ("owned-exiting-still-strict", "same", "darwin", [exiting, missing], None, None, ValueError),
    ("monitor-owned-live", "running", "darwin", [live, observation(executable + "\n")], None, None, True),
    ("monitor-changed-command", "running", "darwin", [observation(f"{start} S {command} changed\n"), observation(executable + "\n")], None, None, "changed"),
    ("monitor-owned-zombie", "running", "darwin", [zombie, monitor_zombie], None, None, False),
    ("monitor-linux-zombie", "running", "linux", [zombie, monitor_zombie], None, None, False),
    ("monitor-owned-exiting", "running", "darwin", [exiting, missing, monitor_exiting], None, None, True),
    ("monitor-exiting-to-zombie", "gone", "darwin", [exiting, missing, monitor_exiting, zombie, monitor_zombie], None, None, None),
    ("monitor-owned-exited", "running", "darwin", [missing, missing], ProcessLookupError(), None, False),
    ("monitor-exited-after-unreadable", "running", "darwin", [exiting, missing, missing, missing], [None, ProcessLookupError()], None, False),
    ("monitor-changed-live", "running", "darwin", [observation(f"{changed_start} S {command}\n"), observation(executable + "\n")], None, None, "changed"),
    ("monitor-reused-zombie", "running", "darwin", [zombie, observation(f"{changed_start} Z\n")], None, None, "changed"),
    ("monitor-reused-exiting", "running", "darwin", [exiting, missing, observation(f"{changed_start} ?Es\n")], None, None, "changed"),
    ("monitor-alive-unreadable", "running", "darwin", [live, missing, observation(f"{start} S\n")], None, None, ValueError),
    ("monitor-linux-exiting-refused", "running", "linux", [exiting, monitor_exiting], None, FileNotFoundError(), ValueError),
    ("monitor-malformed-inspection", "running", "darwin", [zombie, observation("Z\n")], None, None, ValueError),
    ("monitor-failed-inspection", "running", "darwin", [zombie, observation(returncode=2, stderr="inspection failed")], None, None, ValueError),
    ("monitor-inspection-diagnostic", "running", "darwin", [zombie, observation(f"{start} Z\n", stderr="inspection denied")], None, None, ValueError),
    ("monitor-inspection-permission", "running", "darwin", [zombie, PermissionError()], None, None, PermissionError),
    ("monitor-signal-permission", "running", "darwin", [exiting, missing], PermissionError(), None, PermissionError),
    ("monitor-missing-still-alive", "running", "darwin", [zombie, missing], None, None, ValueError),
    ("monitor-reused-at-absence", "running", "darwin", [zombie, missing, observation(f"{pid}\n")], [None, ProcessLookupError()], None, ValueError),
):
    with patch.object(module.sys, "platform", platform), \
         patch.object(module.subprocess, "run", side_effect=observations), \
         patch.object(module.os, "kill", side_effect=kill_error) as signal_probe, \
         patch.object(module.os, "readlink", return_value=executable, side_effect=link_error):
        try:
            current = getattr(module, probe)(pid if probe == "process" else record)
        except (OSError, ValueError) as error:
            if expected == "changed":
                assert isinstance(error, ValueError) and str(error) == "process identity changed", (name, error)
            else:
                assert isinstance(expected, type) and isinstance(error, expected), (name, error)
        else:
            assert not isinstance(expected, type) and expected != "changed", f"{name} accepted an unverified process"
            assert current == expected, (name, current)
        assert all(call.args == (pid, 0) for call in signal_probe.call_args_list), f"{name} sent a process signal"
print("owned process proof passed: confirmed exit, unreadable live process, permission refusal, replaced identity, owned zombie and exiting monitor")

if sys.platform == "linux":
    child = subprocess.Popen([sys.executable, "-c", "import sys; sys.stdin.buffer.read(1)"],
                             stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        child_record = module.process(child.pid)
        assert child_record is not None and module.same(child_record), "child identity unavailable"
        child.stdin.close()
        module.gone(child_record)
        os.kill(child.pid, 0)
        try:
            module.same(child_record)
        except ValueError as error:
            assert str(error) == "process identity unreadable", error
        else:
            raise AssertionError("unreaped child authorized as a strict process")
        assert not module.running(child_record), "unreaped child still executing"
    finally:
        child.stdin.close()
        child.wait(timeout=4)
    assert not module.running(child_record), "reaped child still present"
    print("owned child proof passed: live identity, unreaped zombie, reaped exit")


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
