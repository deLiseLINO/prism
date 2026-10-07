#!/usr/bin/env python3
import errno
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import time
import tempfile
import urllib.request
from urllib.parse import urldefrag, urlparse

os.umask(0o077)


def require(ok, message):
    if not ok:
        raise ValueError(message)


def run(args, env=None, timeout=4):
    return subprocess.run(args, env=env, capture_output=True, text=True, timeout=timeout)


def read(path):
    return json.loads(Path(path).read_text())


def write(path, value):
    path = Path(path)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False) as stream:
            temporary = Path(stream.name)
            json.dump(value, stream, indent=2)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
    fd = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def digest(path):
    result = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            result.update(block)
    return result.hexdigest()


def listeners(port):
    result = run([shutil.which("lsof") or "/usr/sbin/lsof", "-nP", f"-iTCP:{port}", "-sTCP:LISTEN", "-Fp"])
    require(result.returncode == 0 or (result.returncode == 1 and not result.stdout and not result.stderr), "listener inspection failed")
    return {int(line[1:]) for line in result.stdout.splitlines() if re.fullmatch(r"p[1-9][0-9]*", line)}


def unbound(port):
    require(not listeners(port), "TCP listener already bound")
    for family, host in ((socket.AF_INET, "0.0.0.0"), (socket.AF_INET6, "::")):
        try:
            with socket.socket(family) as sock:
                sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                if family == socket.AF_INET6:
                    sock.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
                sock.bind((host, int(port)))
        except OSError as error:
            if family != socket.AF_INET6 or error.errno not in (errno.EAFNOSUPPORT, errno.EPROTONOSUPPORT):
                raise ValueError("TCP port cannot be reserved") from None


def process_absent(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        result = run(["/bin/ps", "-p", str(pid), "-o", "pid="], env={"PATH": "/usr/bin:/bin", "LC_ALL": "C"})
        require(result.returncode == 1 and not result.stdout and not result.stderr, "process identity unreadable")
        return True
    return False


def process(pid):
    require(type(pid) is int and pid > 1, "invalid process identity")
    require(sys.platform in ("darwin", "linux"), "unsupported process platform")
    result = run(["/bin/ps", "-ww", "-p", str(pid), "-o", "lstart=", "-o", "stat=", "-o", "args="], env={"PATH": "/usr/bin:/bin", "LC_ALL": "C"})
    if result.returncode == 1 and not result.stdout and not result.stderr:
        require(process_absent(pid), "process identity unreadable")
        return None
    match = re.fullmatch(r"\s*(\S+\s+\S+\s+\d+\s+\d\d:\d\d:\d\d\s+\d{4})\s+(\S+)\s+(.+)\s*", result.stdout)
    require(result.returncode == 0 and match is not None, "process identity unreadable")
    if match[2].startswith("Z"):
        require(process_absent(pid), "process identity unreadable")
        return None
    if sys.platform == "linux":
        try:
            exe = os.readlink(f"/proc/{pid}/exe")
        except FileNotFoundError:
            require(process_absent(pid), "executable identity unreadable")
            return None
    else:
        result = run(["/bin/ps", "-ww", "-p", str(pid), "-o", "comm="])
        require(result.returncode == 0 or (result.returncode == 1 and not result.stdout and not result.stderr), "executable identity unreadable")
        exe = result.stdout.strip()
    if not (exe.startswith("/") and Path(exe).exists()):
        require(process_absent(pid), "executable identity unreadable")
        return None
    return {"pid": pid, "start": " ".join(match[1].split()), "command": match[3].strip(), "executable": str(Path(exe).resolve())}


def same(record):
    current = process(record["pid"])
    require(current is None or current == record, "process identity changed")
    return current is not None


def running(record):
    try:
        current = process(record["pid"])
    except ValueError as error:
        if str(error) not in ("process identity unreadable", "executable identity unreadable"):
            raise
        result = run(["/bin/ps", "-p", str(record["pid"]), "-o", "lstart=", "-o", "stat="], env={"PATH": "/usr/bin:/bin", "LC_ALL": "C"})
        if result.returncode == 1 and not result.stdout and not result.stderr:
            require(process_absent(record["pid"]), "process identity unreadable")
            return False
        match = re.fullmatch(r"\s*(\S+\s+\S+\s+\d+\s+\d\d:\d\d:\d\d\s+\d{4})\s+(\S+)\s*", result.stdout)
        require(result.returncode == 0 and not result.stderr and match is not None, "process identity unreadable")
        require(" ".join(match[1].split()) == record["start"], "process identity changed")
        if match[2].startswith("Z"):
            return False
        if sys.platform == "darwin" and "E" in match[2]:
            return True
        raise
    require(current is None or current == record, "process identity changed")
    return current is not None


def gone(record, timeout=15):
    deadline = time.monotonic() + timeout
    while running(record):
        require(time.monotonic() < deadline, "process exit timed out")
        time.sleep(0.2)


def http(url):
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(url, timeout=2) as response:
        require(response.status == 200, "HTTP readiness refused")
        return json.load(response)


def attest(root, port):
    reg = read(root / ".prism/daemon.json")
    binary = read(root / ".runtime/binary.json")
    require(reg.get("url") == f"http://127.0.0.1:{port}" and reg.get("version") == binary["version"], "registration endpoint or version changed")
    require(isinstance(reg.get("id"), str) and bool(reg["id"]), "registration identity absent")
    identity = process(reg.get("pid"))
    require(identity is not None and identity["executable"] == str(root / "prism") and identity["command"].startswith(f"{root}/prism daemon "), "daemon executable mismatch")
    require(digest(root / "prism") == binary["sha256"], "daemon binary changed")
    health = http(reg["url"] + "/api/v1/health")
    require(health.get("status") == "ok" and all(type(health.get(k)) is type(reg[k]) and health[k] == reg[k] for k in ("id", "pid", "version")), "health identity mismatch")
    require(listeners(port) == {reg["pid"]}, "daemon listener identity mismatch")
    require(process(reg["pid"]) == identity and read(root / ".prism/daemon.json") == reg, "daemon changed during attestation")
    return {"registration": reg, "process": identity}


def save_daemon(root, record):
    write(root / ".runtime/daemon.json", record)
    public = {key: record["registration"][key] for key in ("id", "pid", "url", "version")}
    write(root / "evidence/runtime-daemon.json", {**public, "start": record["process"]["start"], "binarySHA256": read(root / ".runtime/binary.json")["sha256"]})


def adopt(root, port, timeout=45):
    deadline = time.monotonic() + timeout
    while True:
        try:
            record = attest(root, port)
            previous = root / ".runtime/daemon.json"
            require(not previous.exists() or read(previous) == record, "owned daemon changed")
            save_daemon(root, record)
            return record
        except (OSError, ValueError, subprocess.SubprocessError):
            if (root / ".runtime/daemon.json").exists():
                raise
            if time.monotonic() >= deadline:
                raise ValueError("daemon attestation timed out") from None
            time.sleep(0.2)


def service(root, *args):
    env = read(root / ".runtime/environment.json")
    result = run(["/usr/bin/env", "-i", *[f"{k}={v}" for k, v in env.items()], str(root / "prism"), "service", *args], timeout=20)
    with (root / ".runtime/service.log").open("a") as stream:
        stream.write(result.stdout + result.stderr)
    require(result.returncode == 0, "isolated service command failed")


def tree(path):
    require(not path.is_symlink(), "symlink refused")
    require(path.is_dir() or path.is_file(), "non-regular artifact refused")
    yield path
    if path.is_dir():
        for child in sorted(path.iterdir()):
            yield from tree(child)


def config_paths(value, client=False):
    if isinstance(value, dict):
        for key, item in value.items():
            normalized = key.lower().replace("_", "").replace("-", "")
            require(not (normalized == "accountspath" and item), "external accounts path refused")
            require(not (client and normalized.endswith(("path", "dir", "directory")) and item), "external client config path refused")
            require(not (normalized in ("configpath", "configdir", "settingspath", "settingsdir") and item), "external config path refused")
            config_paths(item, client or normalized in ("clients", "integrations"))
    elif isinstance(value, list):
        for item in value:
            config_paths(item, client)


def stage(root, source, port):
    require(not (root / ".runtime/binary.json").exists(), "state must be staged before launch")
    config, credentials = source / "prism.json", source / "credentials"
    require(config.is_file() and credentials.is_dir(), "source state incomplete")
    for path in (source, config, credentials):
        require(not path.is_symlink(), "source symlink refused")
    list(tree(credentials))
    value = read(config)
    config_paths(value)
    value.get("config", value).setdefault("daemon", {})["listen"] = f"127.0.0.1:{port}"
    require(not (root / ".prism/credentials").exists(), "credential destination already exists")
    shutil.copytree(credentials, root / ".prism/credentials")
    for path in tree(root / ".prism/credentials"):
        path.chmod(0o700 if path.is_dir() else 0o600)
    write(root / ".prism/prism.json", value)
    write(root / ".runtime/source.json", {"sha256": digest(config)})


def sync_directories(destination):
    directories = [p for p in destination.rglob("*") if p.is_dir()] + [destination, *destination.parents]
    for directory in directories:
        fd = os.open(directory, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)


def manifest(destination):
    paths = list(tree(destination))
    target = destination / "manifest.sha256"
    with target.open("w") as stream:
        for path in paths:
            if path.is_file() and path != target:
                stream.write(digest(path) + "  " + path.relative_to(destination).as_posix() + "\n")
        stream.flush()
        os.fsync(stream.fileno())


def publish(root, destination, live):
    destination = Path(os.path.abspath(destination))
    require(not destination.exists() and not destination.is_symlink(), "evidence destination must be new")
    destination = destination.parent.resolve() / destination.name
    require(root not in destination.parents and destination not in root.parents and destination != root, "evidence overlaps sandbox")
    paths = list(tree(root / "evidence"))
    source = read(root / ".runtime/source.json") if (root / ".runtime/source.json").exists() else {}
    private_hashes = {source.get("sha256")}
    if (root / ".prism/prism.json").exists():
        private_hashes.add(digest(root / ".prism/prism.json"))
    if (root / ".prism/credentials").exists():
        private_hashes.update(digest(p) for p in tree(root / ".prism/credentials") if p.is_file())
    for path in paths:
        require(not re.search(r"credential|secret|(?:access|refresh)[-_]token", str(path.relative_to(root / "evidence")), re.I), "private evidence name refused")
        if path.is_file():
            require(digest(path) not in private_hashes, "private state publication refused")
        if live and path.is_file():
            require(not path.name.endswith(".log") and not re.search(r"prism\.json|config(?!.*sanitized)", path.name, re.I), "raw live artifact refused")
    destination.mkdir(mode=0o700, parents=True)
    pending = read(root / "evidence/runtime-teardown.json")
    pending["run"] = root.name
    write(destination / "runtime-teardown.json", pending)
    for path in paths[1:]:
        if path.name == "runtime-teardown.json":
            continue
        target = destination / path.relative_to(root / "evidence")
        if path.is_dir():
            target.mkdir(mode=0o700)
        else:
            with path.open("rb") as src, target.open("xb") as dst:
                shutil.copyfileobj(src, dst)
                dst.flush()
                os.fsync(dst.fileno())
            require(digest(path) == digest(target), "evidence digest mismatch")
    sync_directories(destination)
    return destination


def finalize(root, destination, live, status, cleanup, app_stopped, service_stopped):
    receipt = {"run": root.name, "assertionStatus": status, "cleanupStatus": cleanup,
               "supervisorStopped": app_stopped, "serviceStopped": service_stopped,
               "finalization": "pending", "sandboxRemoved": False}
    receipt_path = root / "evidence/runtime-teardown.json"
    destination = Path(os.path.abspath(destination)).parent.resolve() / Path(destination).name
    created = False
    private_destination = None
    try:
        write(receipt_path, receipt)
        require(not destination.exists() and not destination.is_symlink(), "evidence destination must be new")
        published = publish(root, destination, live)
        created = True
        private_dirs = [root / name for name in ("private-proof", "private-matrix") if (root / name).is_dir()]
        if live and private_dirs:
            private_destination = destination.with_name(destination.name + ".private")
            private_destination.mkdir(mode=0o700)
            for source in private_dirs:
                paths = list(tree(source))
                target_root = private_destination / source.name
                target_root.mkdir(mode=0o700)
                for path in paths[1:]:
                    target = target_root / path.relative_to(source)
                    if path.is_dir():
                        target.mkdir(mode=0o700)
                    else:
                        with path.open("rb") as src, target.open("xb") as dst:
                            shutil.copyfileobj(src, dst)
                            dst.flush()
                            os.fsync(dst.fileno())
                        require(digest(path) == digest(target), "private evidence digest mismatch")
            manifest(private_destination)
            sync_directories(private_destination)
            receipt["privateEvidence"] = str(private_destination)
        if status == 0 and cleanup == 0:
            shutil.rmtree(root)
            receipt["sandboxRemoved"] = True
        receipt["finalization"] = "complete" if cleanup == 0 else "failed"
        write(published / "runtime-teardown.json", receipt)
        manifest(published)
        sync_directories(published)
        print("evidence: " + str(published))
        return cleanup
    except Exception:
        receipt["cleanupStatus"] = 1
        receipt["finalization"] = "failed"
        candidates = [receipt_path]
        external_receipt = destination / "runtime-teardown.json"
        try:
            owned_destination = created or (external_receipt.is_file() and read(external_receipt).get("run") == root.name)
        except OSError:
            owned_destination = False
        if owned_destination:
            candidates.append(external_receipt)
        for failure_path in candidates:
            try:
                write(failure_path, receipt)
                if failure_path == external_receipt:
                    manifest(destination)
                sync_directories(failure_path.parent)
            except OSError:
                print("owned runtime: failed receipt could not be persisted", file=sys.stderr)
        raise


def main():
    command, raw_root, port, *args = sys.argv[1:]
    root = Path(raw_root)
    if command != "init":
        record = read(root / ".runtime/run.json")
        require(record["root"] == str(root) and record["inode"] == root.stat().st_ino and record["device"] == root.stat().st_dev and not root.is_symlink(), "sandbox identity changed")
    if command == "init":
        require(port.isdecimal() and 0 < int(port) < 65536, "invalid daemon port")
        cdp = args[0]
        require(not cdp or (cdp.isdecimal() and 0 < int(cdp) < 65536 and int(cdp) != int(port)), "invalid or equal CDP port")
        require(root.is_dir() and not root.is_symlink() and str(root.resolve()) == str(root), "sandbox must be physical")
        root.chmod(0o700)
        for name in (".runtime", ".prism", "electron", "tmp", "evidence"):
            (root / name).mkdir(mode=0o700)
        write(root / ".runtime/run.json", {"root": str(root), "inode": root.stat().st_ino, "device": root.stat().st_dev, "port": port, "cdp": cdp})
        write(root / "evidence/runtime-run.json", {"run": root.name, "port": int(port), "cdpPort": int(cdp) if cdp else None})
        unbound(port)
        if cdp:
            unbound(cdp)
    elif command == "environment":
        path, shell, headless = args
        user = "verify_" + root.name.replace(".", "_").replace("-", "_")
        try:
            pwd.getpwnam(user)
        except KeyError:
            pass
        else:
            raise ValueError("fixture user exists")
        if shell == str(root / "login-shell"):
            Path(shell).write_text("#!/bin/sh\nprintf '%s\\n' " + shlex.quote(path) + "\n")
            Path(shell).chmod(0o700)
        require(Path(shell).is_file() and os.access(shell, os.X_OK), "probe shell not executable")
        env = dict(HOME=str(root), TMPDIR=str(root / "tmp"), PATH=path, SHELL=shell, USER=user, LOGNAME=user, TERM="dumb", LANG="C", PRISMD_PATH=str(root / "prism"), PRISM_PORT=port, PRISM_DAEMON_CONFIG=str(root / ".prism/prism.json"), PRISM_USER_DATA=str(root / "electron"), PRISM_HEADLESS=headless)
        env.update({k: os.environ[k] for k in ("DISPLAY", "XAUTHORITY") if k in os.environ})
        write(root / ".runtime/environment.json", env)
        for key, value in env.items():
            sys.stdout.buffer.write(f"{key}={value}".encode() + b"\0")
    elif command == "prepare":
        require(not (root / ".runtime/binary.json").exists(), "runtime already launched")
        require(Path(args[0]).resolve() == Path(args[2]).resolve(), "binary checkout differs from desktop checkout")
        require((root / "prism").is_file() and not (root / "prism").is_symlink(), "sandbox binary absent")
        require(not (root / ".prism/daemon.json").exists() and not (root / ".prism/daemon.json").is_symlink(), "registration exists before launch")
        if (root / ".prism/prism.json").exists():
            config_paths(read(root / ".prism/prism.json"))
        unbound(port)
        if args[1]:
            unbound(args[1])
        version = run([str(root / "prism"), "version"], env=read(root / ".runtime/environment.json"))
        require(version.returncode == 0 and bool(version.stdout.strip()), "binary version unavailable")
        sha = run(["git", "-C", args[0], "rev-parse", "HEAD"])
        require(sha.returncode == 0, "checkout identity unavailable")
        binary = {"version": version.stdout.strip(), "sha256": digest(root / "prism"), "commit": sha.stdout.strip()}
        write(root / ".runtime/binary.json", binary)
        write(root / "evidence/runtime-binary.json", binary)
    elif command == "electron":
        repo = Path(args[0]).resolve()
        renderer = repo / "apps/desktop/dist/renderer/index.html"
        require(renderer.is_file(), "built renderer absent")
        exe = repo / "node_modules/electron/dist" / ("Electron.app/Contents/MacOS/Electron" if sys.platform == "darwin" else "electron")
        print(exe.resolve(strict=True))
    elif command == "capture":
        pid, exe = int(args[0]), args[1]
        deadline = time.monotonic() + 2
        while True:
            identity = process(pid)
            if identity and identity["executable"] == exe and identity["command"].startswith(exe + " "):
                write(root / ".runtime/app.json", identity)
                break
            require(time.monotonic() < deadline, "launched app identity unavailable")
            time.sleep(0.05)
    elif command == "start":
        service(root, "start", "--listen", f"127.0.0.1:{port}", "--config", str(root / ".prism/prism.json"), "--credential-store", str(root / ".prism/credentials"))
        adopt(root, port)
    elif command == "attest":
        record = attest(root, port)
        previous = root / ".runtime/daemon.json"
        require(not previous.exists() or read(previous) == record, "owned daemon changed")
        save_daemon(root, record)
    elif command == "ready":
        adopt(root, port)
    elif command == "ws":
        cdp, repo, node = args
        app = read(root / ".runtime/app.json")
        require(same(app) and listeners(cdp) == {app["pid"]}, "CDP listener identity mismatch")
        expected = (Path(repo).resolve() / "apps/desktop/dist/renderer/index.html").as_uri()
        pages = [p for p in http(f"http://127.0.0.1:{cdp}/json") if p.get("type") == "page" and urldefrag(p.get("url", ""))[0] == expected]
        require(len(pages) == 1, "built renderer target ambiguous or absent")
        ws = pages[0].get("webSocketDebuggerUrl", "")
        parsed = urlparse(ws)
        require(parsed.scheme == "ws" and parsed.hostname == "127.0.0.1" and parsed.port == int(cdp) and parsed.path == "/devtools/page/" + pages[0]["id"], "renderer WebSocket identity mismatch")
        result = run([node, str(Path(repo) / "verify/scripts/cdp-ws.mjs"), cdp, expected])
        require(result.returncode == 0 and result.stdout.strip() == ws and same(app) and listeners(cdp) == {app["pid"]}, "CDP discovery mismatch")
        print(ws)
    elif command == "crash":
        old = read(root / ".runtime/daemon.json")
        require(attest(root, port) == old, "crash ownership mismatch")
        os.kill(old["process"]["pid"], signal.SIGTERM)
        deadline = time.monotonic() + 45
        while True:
            require(time.monotonic() < deadline, "replacement daemon timed out")
            if not running(old["process"]):
                try:
                    new = attest(root, port)
                    require(all(new["registration"][k] != old["registration"][k] for k in ("id", "pid")), "replacement identity unchanged")
                    save_daemon(root, new)
                    break
                except (OSError, ValueError, subprocess.SubprocessError):
                    pass
            time.sleep(0.2)
    elif command == "quit":
        record = read(root / ".runtime/app.json")
        if same(record):
            os.kill(record["pid"], signal.SIGTERM)
        gone(record)
        if args[0]:
            unbound(args[0])
    elif command == "stop":
        app = root / ".runtime/app.json"
        require(not app.exists() or not running(read(app)), "supervisor still running")
        if (root / ".prism/daemon.json").exists():
            record = attest(root, port)
            previous = root / ".runtime/daemon.json"
            require(not previous.exists() or record == read(previous), "stop ownership mismatch")
            save_daemon(root, record)
            service(root, "stop")
            gone(record["process"])
        elif (root / ".runtime/daemon.json").exists():
            require(not running(read(root / ".runtime/daemon.json")["process"]), "registration disappeared from live daemon")
        require(not (root / ".prism/daemon.json").exists(), "registration remains after stop")
        unbound(port)
    elif command == "stage":
        stage(root, Path(os.path.abspath(args[0])), port)
    elif command == "logs":
        for source, name in ((root / "app.log", "runtime-app.log"), (root / "build.log", "runtime-build.log"), (root / ".prism/prism.log", "runtime-service.log")):
            if source.exists() or source.is_symlink():
                require(source.is_file() and not source.is_symlink(), "runtime log is not a regular file")
                target = root / "evidence" / name
                require(not target.exists() and not target.is_symlink(), "runtime log destination exists")
                shutil.copyfile(source, target)
                require(digest(source) == digest(target), "runtime log copy differs")
    elif command == "finalize":
        sys.exit(finalize(root, args[0], args[1] == "1", int(args[2]), int(args[3]), args[4] == "1", args[5] == "1"))
    else:
        raise ValueError("unknown runtime operation")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("owned runtime: " + (str(error) if isinstance(error, ValueError) else "boundary operation failed"), file=sys.stderr)
        sys.exit(1)
