#!/usr/bin/env python3
import argparse
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
    with tempfile.TemporaryDirectory(prefix='prism-fds-') as directory, socket.socket() as inherited:
        root = Path(directory)
        inherited.bind(('127.0.0.1', 0))
        inherited.listen()
        inode = os.fstat(inherited.fileno()).st_ino
        env = {'HOME': directory, 'PATH': os.environ['PATH'], 'TMPDIR': directory}
        version = subprocess.run([str(binary), 'version'], env=env, pass_fds=(inherited.fileno(),), capture_output=True, timeout=10)
        require(version.returncode == 0 and version.stdout.strip() and not version.stderr, 'real stdout/stderr were not preserved')
        invalid = subprocess.run([str(binary), 'upgrade', 'unexpected'], env=env, pass_fds=(inherited.fileno(),), capture_output=True, timeout=10)
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
                launched = subprocess.Popen(command, env=env, pass_fds=(inherited.fileno(),), stdout=log, stderr=log)
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
                    if mode == 'service':
                        require(launched.wait(timeout=10) == 0, 'service start failed')
                    stopped = subprocess.run([str(binary), 'service', 'stop'], env=env, capture_output=True, timeout=15)
                    require(stopped.returncode == 0, 'owned service stop failed')
                    require(not (home / '.prism/daemon.json').exists(), 'registration survives stop')
                    if mode == 'daemon':
                        require(launched.wait(timeout=10) == 0, 'daemon shutdown failed')
                    with socket.socket() as probe:
                        require(probe.connect_ex(('127.0.0.1', port)) != 0, 'daemon listener survives stop')
                    print(json.dumps({'mode': mode, 'health': 'ok', 'inheritedListener': 'absent', 'parentListener': 'preserved', 'stdio': 'preserved', 'shutdown': 'complete'}))
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
