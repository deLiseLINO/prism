#!/usr/bin/env python3
import argparse
import http.server
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def fetch_json(url):
    with urllib.request.urlopen(url, timeout=5) as response:
        return json.load(response)


def frame(kind, **fields):
    payload = json.dumps({"type": kind, **fields})
    return f"event: {kind}\ndata: {payload}\n\n".encode()


CASES = {
    "delayed": ({"firstProgressMs": 1000, "idleMs": 100}, "completed"),
    "first-expired": ({"firstProgressMs": 100, "idleMs": 1000}, "upstream_stall"),
    "idle-expired": ({"firstProgressMs": 1000, "idleMs": 100}, "upstream_stall"),
    "heartbeats": ({"firstProgressMs": 100, "idleMs": 1000}, "upstream_stall"),
    "created-only": ({"firstProgressMs": 1000, "idleMs": 100}, "completed"),
    "progressing": ({"firstProgressMs": 1000, "idleMs": 200}, "completed"),
    "disabled": ({"firstProgressMs": 0, "idleMs": 0}, "completed"),
    "first-disabled": ({"firstProgressMs": 0, "idleMs": 100}, "completed"),
    "idle-disabled": ({"firstProgressMs": 1000, "idleMs": 0}, "completed"),
}


class Upstream(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", 0)))
        case = self.path.split("/")[1]
        try:
            if case in {"delayed", "first-expired", "disabled", "first-disabled"}:
                time.sleep(0.35)
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            if case == "heartbeats":
                for _ in range(12):
                    self.wfile.write(b": keep-alive\n\n")
                    self.wfile.flush()
                    time.sleep(0.05)
            if case == "created-only":
                self.wfile.write(frame("response.created", response={"status": "in_progress"}))
                self.wfile.flush()
                time.sleep(0.35)
            item = {"id": "m1", "type": "message", "role": "assistant", "content": []}
            self.wfile.write(frame("response.output_item.added", item=item))
            self.wfile.write(frame("response.output_text.delta", item_id="m1", delta="first "))
            self.wfile.flush()
            if case in {"idle-expired", "idle-disabled"}:
                time.sleep(0.35)
            if case == "progressing":
                for _ in range(8):
                    time.sleep(0.05)
                    self.wfile.write(frame("response.output_text.delta", item_id="m1", delta="next "))
                    self.wfile.flush()
            item["content"] = [{"type": "output_text", "text": "complete"}]
            self.wfile.write(frame("response.output_item.done", item=item))
            self.wfile.write(frame("response.completed", response={"status": "completed", "usage": {"input_tokens": 3, "output_tokens": 2, "total_tokens": 5}}))
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass


class UpstreamServer(http.server.ThreadingHTTPServer):
    daemon_threads = True


def run(binary, evidence):
    evidence.mkdir(parents=True, exist_ok=True)
    upstream = UpstreamServer(("127.0.0.1", 0), Upstream)
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    process = None
    results = []
    try:
        with tempfile.TemporaryDirectory(prefix="prism-stream-wait-") as sandbox:
            root = Path(sandbox)
            port = free_port()
            url = f"http://127.0.0.1:{port}"
            config = {
                "version": 1,
                "generation": 0,
                "config": {
                    "version": 1,
                    "daemon": {"listen": f"127.0.0.1:{port}"},
                    "providers": {name: {"wire": "responses", "baseURL": f"http://127.0.0.1:{upstream.server_port}/{name}/v1", "models": ["model"], "wait": wait} for name, (wait, _expected) in CASES.items()},
                    "combos": {},
                    "routes": {},
                    "aliases": {},
                },
            }
            config_path = root / "prism.json"
            config_path.write_text(json.dumps(config))
            with (evidence / "daemon.log").open("w") as log:
                process = subprocess.Popen([str(binary), "daemon", "--listen", f"127.0.0.1:{port}", "--config", str(config_path), "--credential-store", str(root / "credentials")], env={**os.environ, "HOME": str(root / "home")}, stdout=log, stderr=subprocess.STDOUT)
                for _ in range(50):
                    if process.poll() is not None:
                        raise RuntimeError("isolated daemon exited before readiness")
                    try:
                        fetch_json(url + "/api/v1/health")
                        break
                    except (OSError, ValueError):
                        time.sleep(0.1)
                else:
                    raise RuntimeError("isolated daemon did not become ready")
                for name, (_wait, expected) in CASES.items():
                    body = json.dumps({"model": f"{name}/model", "stream": True, "messages": [{"role": "user", "content": "hi"}]}).encode()
                    request = urllib.request.Request(url + "/v1/chat/completions", data=body, headers={"Content-Type": "application/json"})
                    with urllib.request.urlopen(request, timeout=10) as response:
                        wire = response.read().decode()
                    (evidence / f"{name}.sse").write_text(wire)
                    for _ in range(50):
                        journal = fetch_json(url + "/api/v1/requests")
                        entry = next((r for r in journal["requests"] if r["model"] == f"{name}/model" and r["status"] != "open"), None)
                        if entry:
                            break
                        time.sleep(0.01)
                    if entry is None:
                        raise AssertionError(f"{name} has no terminal request record")
                    (evidence / f"{name}.journal.json").write_text(json.dumps(entry, indent=2))
                    if expected == "completed":
                        assert "upstream_stall" not in wire and "data: [DONE]" in wire, (name, wire)
                        assert entry["status"] == "completed" and not entry.get("reason"), (name, entry)
                    else:
                        assert "upstream_stall" in wire and "data: [DONE]" not in wire, (name, wire)
                        assert entry["status"] == "incomplete" and entry.get("reason") == "upstream_stall", (name, entry)
                        assert entry["attempts"][0]["outcome"] == "upstream_stall", (name, entry)
                    result = {"case": name, "result": expected, "requestId": entry["requestId"]}
                    results.append(result)
                    print(json.dumps(result), flush=True)
                assert fetch_json(url + "/api/v1/health")["status"] == "ok"
    finally:
        if process and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        upstream.shutdown()
        upstream.server_close()
        thread.join(timeout=5)
        (evidence / "result.json").write_text(json.dumps(results, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    run(args.binary.resolve(), args.evidence.resolve())
