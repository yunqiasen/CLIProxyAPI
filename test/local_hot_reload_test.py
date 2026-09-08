"""Public-process tests for the local Docker code-reload contract."""
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import time
import unittest
import urllib.error
import urllib.request

REPO = Path(__file__).resolve().parents[1]


class LocalHotReloadTest(unittest.TestCase):
    def test_source_changes_and_commits_reload_without_stopping_on_build_error(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'source'
            (source / 'cmd/server').mkdir(parents=True)
            (source / 'scripts').mkdir()
            (source / 'go.mod').write_text('module fixture\n\ngo 1.26.0\n')
            program = '''package main
import ("encoding/json"; "net/http"; "os")
var Version, Commit, BuildDate string
func main() { http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
json.NewEncoder(w).Encode(map[string]any{"text":"first", "commit":Commit, "pid":os.Getpid()})
}); http.ListenAndServe(os.Getenv("FIXTURE_ADDRESS"), nil) }
'''
            main = source / 'cmd/server/main.go'
            main.write_text(program)
            build_script = REPO / 'scripts/local_hot_build.sh'
            self.assertTrue(build_script.is_file(), 'local hot-build command is missing')
            (source / 'scripts/local_hot_build.sh').write_bytes(build_script.read_bytes())
            git = ['git', '-C', str(source), '-c', 'core.hooksPath=/dev/null']
            def run_git(*args):
                return subprocess.check_output(git + list(args), stderr=subprocess.STDOUT, text=True).strip()
            run_git('init', '-q')
            run_git('config', 'user.email', 'fixture@example.invalid')
            run_git('config', 'user.name', 'Fixture')
            run_git('add', '.')
            run_git('commit', '-qm', 'initial')
            first_commit = run_git('rev-parse', 'HEAD')
            with socket.socket() as reservation:
                reservation.bind(('127.0.0.1', 0))
                port = reservation.getsockname()[1]
            env = dict(os.environ, CPA_SOURCE_DIR=str(source), CPA_BUILD_DIR=str(root / 'build'),
                       CPA_RUNTIME_DIR=str(source), CPA_POLL_INTERVAL='0.1', CPA_STOP_GRACE_SECONDS='1',
                       FIXTURE_ADDRESS=f'127.0.0.1:{port}')
            url = f'http://127.0.0.1:{port}/'
            log = root / 'runner.log'
            with log.open('w') as output:
                runner = subprocess.Popen(['python3', str(REPO / 'scripts/local_hot_reload.py')],
                                          env=env, stdout=output, stderr=output)
                try:
                    def wait_for(predicate):
                        deadline = time.monotonic() + 45
                        while time.monotonic() < deadline:
                            self.assertIsNone(runner.poll(), log.read_text())
                            try:
                                with urllib.request.urlopen(url, timeout=1) as response:
                                    data = json.load(response)
                                if predicate(data):
                                    return data
                            except (urllib.error.URLError, TimeoutError, ConnectionError):
                                pass
                            time.sleep(.1)
                        self.fail('reload contract timed out:\n' + log.read_text())
                    first = wait_for(lambda x: x['text'] == 'first' and x['commit'] == first_commit)
                    main.write_text(program.replace('"first"', '"second"'))
                    second = wait_for(lambda x: x['text'] == 'second' and x['commit'].endswith('-dirty'))
                    self.assertNotEqual(first['pid'], second['pid'])
                    self.assertEqual(run_git('rev-parse', 'HEAD'), first_commit, 'watcher must not auto-stage/commit files')
                    main.write_text('package main\nTHIS IS NOT GO\n')
                    deadline = time.monotonic() + 20
                    while 'build failed; keeping current process' not in log.read_text():
                        self.assertLess(time.monotonic(), deadline, log.read_text())
                        time.sleep(.1)
                    for _ in range(5):
                        with urllib.request.urlopen(url, timeout=1) as response:
                            self.assertEqual(json.load(response)['pid'], second['pid'])
                        time.sleep(.1)
                    main.write_text(program.replace('"first"', '"third"'))
                    wait_for(lambda x: x['text'] == 'third' and x['commit'].endswith('-dirty'))
                    run_git('add', '.')
                    run_git('commit', '-qm', 'change')
                    final_commit = run_git('rev-parse', 'HEAD')
                    wait_for(lambda x: x['text'] == 'third' and x['commit'] == final_commit)
                    self.assertIsNone(runner.poll(), 'supervisor itself restarted')
                finally:
                    runner.send_signal(signal.SIGTERM)
                    try:
                        runner.wait(timeout=8)
                    except subprocess.TimeoutExpired:
                        runner.kill()
                        runner.wait()
                self.assertEqual(runner.returncode, 0, log.read_text())


if __name__ == '__main__':
    unittest.main()
