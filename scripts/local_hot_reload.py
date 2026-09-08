#!/usr/bin/env python3
"""Local development supervisor: compile before replacing the running CPA child."""
import hashlib
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

SOURCE = Path(os.environ.get('CPA_SOURCE_DIR', '/workspace')).resolve()
BUILD = Path(os.environ.get('CPA_BUILD_DIR', '/tmp/cpa-hot-reload')).resolve()
RUNTIME = Path(os.environ.get('CPA_RUNTIME_DIR', '/CLIProxyAPI')).resolve()
INTERVAL = max(.1, float(os.environ.get('CPA_POLL_INTERVAL', '1')))
STOP_GRACE = max(0, float(os.environ.get('CPA_STOP_GRACE_SECONDS', '30')))
stopping = False


def log(message):
    print('[hot-reload] ' + message, flush=True)


def stop_requested(_signum, _frame):
    global stopping
    stopping = True


def fingerprint():
    digest = hashlib.sha256()
    commit = subprocess.check_output(['git', '-c', f'safe.directory={SOURCE}', '-C', str(SOURCE),
                                      'rev-parse', 'HEAD'], stderr=subprocess.DEVNULL)
    digest.update(commit)
    paths = [SOURCE / name for name in ('go.mod', 'go.sum', 'scripts/local_hot_build.sh')]
    for directory in ('cmd', 'internal', 'sdk'):
        for parent, dirs, files in os.walk(SOURCE / directory):
            dirs[:] = sorted(d for d in dirs if not d.startswith('.') and d not in ('vendor', '__pycache__'))
            paths.extend(Path(parent) / name for name in sorted(files)
                         if Path(name).suffix in ('.go', '.json', '.txt', '.tmpl', '.html'))
    for path in sorted(paths):
        if path.is_file():
            digest.update(str(path.relative_to(SOURCE)).encode())
            digest.update(b'\0')
            digest.update(path.read_bytes())
    return digest.digest()


def stop_process(process):
    if process is None:
        return
    # The shell/compiler/server share a private process group, never the container's.
    try:
        os.killpg(process.pid, signal.SIGINT)
    except ProcessLookupError:
        process.wait()
        return
    deadline = time.monotonic() + STOP_GRACE
    while process.poll() is None and time.monotonic() < deadline:
        time.sleep(.1)
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait()


def compile_candidate():
    log('source or commit changed; building while current process stays available')
    process = subprocess.Popen(['sh', str(SOURCE / 'scripts/local_hot_build.sh')],
                               cwd=SOURCE, start_new_session=True)
    while process.poll() is None:
        if stopping:
            stop_process(process)
            return False
        time.sleep(.1)
    return process.returncode == 0


def main():
    signal.signal(signal.SIGTERM, stop_requested)
    signal.signal(signal.SIGINT, stop_requested)
    BUILD.mkdir(parents=True, exist_ok=True)
    child = None
    pending = None
    attempted = None
    try:
        while not stopping:
            try:
                current = fingerprint()
            except (OSError, subprocess.CalledProcessError):
                log('source snapshot not ready; keeping current process')
                time.sleep(INTERVAL)
                continue
            if current != pending:
                pending = current
                time.sleep(INTERVAL)
                continue
            if current != attempted:
                attempted = current
                if not compile_candidate():
                    if not stopping:
                        log('build failed; keeping current process')
                    continue
                if stopping:
                    break
                # A save during compilation needs another stable build, not mixed source.
                try:
                    if fingerprint() != current:
                        log('source changed during build; deferring process replacement')
                        continue
                except (OSError, subprocess.CalledProcessError):
                    continue
                stop_process(child)
                child = None
                if stopping:
                    break
                child = subprocess.Popen([str(BUILD / 'CLIProxyAPI'), *sys.argv[1:]],
                                         cwd=RUNTIME, start_new_session=True)
                log(f'process replaced; pid={child.pid}')
            if child is not None and child.poll() is not None:
                log(f'CPA exited unexpectedly ({child.returncode}); exiting supervisor for container restart policy')
                return 1
            time.sleep(INTERVAL)
    finally:
        stop_process(child)
    return 0


if __name__ == '__main__':
    sys.exit(main())
