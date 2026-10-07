#!/usr/bin/env python3
import argparse
from contextlib import ExitStack
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request


def require(value, message):
    if not value:
        raise RuntimeError(message)


def health(url):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(url + '/api/v1/health', timeout=1) as response:
        return json.load(response)


def holds(pid, inode):
    for entry in (Path('/proc') / str(pid) / 'fd').iterdir():
        try:
            if os.readlink(entry) == f'socket:[{inode}]':
                return True
        except FileNotFoundError:
            continue
    return False


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('binary')
    args = parser.parse_args()
    require(sys.platform == 'linux', 'inherited descriptor proof requires Linux')
    binary = Path(args.binary).resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix='prism-fds-') as directory, socket.socket() as inherited, ExitStack() as handles:
        root = Path(directory)
        inherited.bind(('127.0.0.1', 0))
        inherited.listen()
        inode = os.fstat(inherited.fileno()).st_ino
        inherited_file = handles.enter_context((root / 'inherited-file').open('w+'))
        inherited_file.write('parent-file')
        inherited_file.flush()
        file_identity = os.fstat(inherited_file.fileno())
        connected, peer = socket.socketpair()
        handles.enter_context(connected)
        handles.enter_context(peer)
        connected_inode = os.fstat(connected.fileno()).st_ino
        inherited_fds = (inherited.fileno(), inherited_file.fileno(), connected.fileno())
        env = {'HOME': directory, 'PATH': os.environ['PATH'], 'TMPDIR': directory}
        version = subprocess.run([str(binary), 'version'], env=env, pass_fds=inherited_fds, capture_output=True, timeout=10)
        require(version.returncode == 0 and version.stdout.strip() and not version.stderr, 'real stdout/stderr were not preserved')
        invalid = subprocess.run([str(binary), 'upgrade', 'unexpected'], env=env, pass_fds=inherited_fds, capture_output=True, timeout=10)
        require(invalid.returncode == 2 and b'additional arguments' in invalid.stderr and not invalid.stdout, 'real stderr was not preserved')
        for mode in ('service', 'daemon'):
            with socket.socket() as reservation:
                reservation.bind(('127.0.0.1', 0))
                port = reservation.getsockname()[1]
            home = root / mode
            home.mkdir()
            config = home / 'prism.json'
            config.write_text(json.dumps({'generation': 0, 'config': {'version': 1, 'providers': {}, 'combos': {}, 'routes': {}, 'aliases': {}}}))
            env = {'HOME': str(home), 'PATH': os.environ['PATH'], 'TMPDIR': directory}
            command = [str(binary), 'service', 'start'] if mode == 'service' else [str(binary), 'daemon', '--register']
            command += ['--listen', f'127.0.0.1:{port}', '--config', str(config), '--credential-store', str(home / 'credentials')]
            with (home / 'output.log').open('w') as log:
                launched = subprocess.Popen(command, env=env, pass_fds=inherited_fds, stdout=log, stderr=log)
                owned = None
                try:
                    deadline = time.monotonic() + 10
                    while time.monotonic() < deadline:
                        try:
                            record = json.loads((home / '.prism/daemon.json').read_text())
                            observed = health(record['url'])
                            require(record['pid'] == observed['pid'] and record['id'] == observed['id'], 'daemon identity mismatch')
                            require(Path(f"/proc/{record['pid']}/exe").resolve() == binary, 'wrong daemon executable')
                            owned = record
                            break
                        except (OSError, ValueError):
                            time.sleep(0.05)
                    require(owned is not None, f'{mode} did not register a healthy daemon')
                    require(not holds(owned['pid'], inode), f'{mode} daemon retained the parent listener')
                    require(holds(os.getpid(), inode), 'parent listener was unexpectedly closed')
                    require(not holds(owned['pid'], connected_inode), f'{mode} daemon retained the connected socket')
                    child_fds = Path('/proc') / str(owned['pid']) / 'fd'
                    for child_fd in child_fds.iterdir():
                        try:
                            target = child_fd.stat()
                        except FileNotFoundError:
                            continue
                        require((target.st_dev, target.st_ino) != (file_identity.st_dev, file_identity.st_ino), f'{mode} daemon retained the inherited file')
                    connected.sendall(b'parent-socket')
                    require(peer.recv(13) == b'parent-socket', 'parent connected socket was changed')
                    inherited_file.seek(0)
                    require(inherited_file.read() == 'parent-file', 'parent file was changed')
                    stdin_env = dict(env, PRISM_URL=owned['url'])
                    stdin_result = subprocess.run([str(binary), 'providers', 'add', 'stdin-proof', '--wire', 'chat', '--endpoint', 'http://127.0.0.1:1/v1', '--model', 'fixture', '--stdin', '--json'], input=b'synthetic-stdin-credential\n', env=stdin_env, capture_output=True, timeout=10)
                    require(stdin_result.returncode == 0, f'stdin credential was not accepted: {stdin_result.stderr!r}')
                    listing = subprocess.run([str(binary), 'providers', 'list', '--json'], env=stdin_env, capture_output=True, timeout=10)
                    require(listing.returncode == 0 and b'stdin-proof' in listing.stdout, 'stdin command did not create the provider')
                    if mode == 'service':
                        require(launched.wait(timeout=10) == 0, 'service start failed')
                    stopped = subprocess.run([str(binary), 'service', 'stop'], env=env, capture_output=True, timeout=15)
                    require(stopped.returncode == 0, 'owned service stop failed')
                    require(not (home / '.prism/daemon.json').exists(), 'registration survives stop')
                    if mode == 'daemon':
                        require(launched.wait(timeout=10) == 0, 'daemon shutdown failed')
                    with socket.socket() as probe:
                        require(probe.connect_ex(('127.0.0.1', port)) != 0, 'daemon listener survives stop')
                    print(json.dumps({'mode': mode, 'health': 'ok', 'inheritedListener': 'absent', 'inheritedFile': 'absent', 'inheritedSocket': 'absent', 'parentListener': 'preserved', 'parentFile': 'preserved', 'parentSocket': 'preserved', 'stdio': 'preserved', 'shutdown': 'complete'}))
                finally:
                    try:
                        if owned is not None:
                            try:
                                current = json.loads((home / '.prism/daemon.json').read_text())
                            except FileNotFoundError:
                                current = None
                            if current == owned and Path(f"/proc/{owned['pid']}/exe").exists() and Path(f"/proc/{owned['pid']}/exe").resolve() == binary:
                                stopped = subprocess.run([str(binary), 'service', 'stop'], env=env, capture_output=True, timeout=15)
                                require(stopped.returncode == 0, 'owned cleanup failed')
                    finally:
                        if launched.poll() is None:
                            launched.terminate()
                            launched.wait(timeout=10)
    print('inherited descriptor proof passed')


if __name__ == '__main__':
    main()
