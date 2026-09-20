#!/usr/bin/env python3
"""Exercise a CPA binary against local retrieval fixtures, without paid credentials."""

import argparse
import contextlib
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CLIENT_KEY = "retrieval-fixture-client"
MANAGEMENT_KEY = "retrieval-fixture-management"


class Upstream(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def send_json(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/__records":
            self.send_json(200, self.server.records)
        else:
            self.send_json(200, {"data": [{"id": "fixture-vector-v1"}]})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.records.append({
            "path": self.path, "body": body,
            "key": self.headers.get("Authorization"),
            "settings": self.headers.get("X-Retrieval-Fixture"),
        })
        if self.path.endswith("/bad"):
            self.send_json(200, {})
        elif self.path.endswith("/busy"):
            self.send_json(429, {"error": {"message": "fixture busy"}})
        elif self.path.endswith("/nonfinite"):
            self.send_json(200, {"data": [{"index": 0, "embedding": "AACAfw=="}]})
        elif "documents" in body:
            self.send_json(200, {
                "results": [{"index": 0, "relevance_score": 0.95,
                             "document": {"text": "relevant fixture"}}],
                "meta": {"billed_units": {"search_units": 1}},
            })
        else:
            value = body["input"]
            count = len(value) if isinstance(value, list) and not isinstance(value[0], int) else 1
            self.send_json(200, {
                "object": "list", "model": body["model"],
                "data": [{"index": i, "embedding": [0.25, 0.75]} for i in range(count)],
                "usage": {"prompt_tokens": count, "total_tokens": count},
            })


def request(base, path, body=None, key=CLIENT_KEY):
    headers = {"Content-Type": "application/json"}
    if key:
        headers["Authorization"] = "Bearer " + key
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=8) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


@contextlib.contextmanager
def runtime(binary, panel):
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    upstream.records = []
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    with tempfile.TemporaryDirectory(prefix="cpa-retrieval-runtime-") as directory:
        root = Path(directory)
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        provider = {
            "name": "Retrieval fixture", "base-url": f"http://127.0.0.1:{upstream.server_port}/v1",
            "headers": {"X-Retrieval-Fixture": "saved"},
            "api-key-entries": [{"api-key": "fixture-key-one"}, {"api-key": "fixture-key-two"}],
            "models": [
                {"name": "fixture-vector-v1", "alias": "fixture-vector", "type": "embeddings"},
                {"name": "fixture-rank-v1", "alias": "fixture-rank", "type": "rerank"},
            ],
        }
        cfg = {
            "host": "127.0.0.1", "port": port, "auth-dir": str(root / "auths"),
            "api-keys": [CLIENT_KEY], "request-log": True,
            "remote-management": {"secret-key": MANAGEMENT_KEY, "disable-auto-update-panel": True,
                                  "disable-control-panel": panel is None},
            "openai-compatibility": [provider],
        }
        config_path = root / "config.yaml"
        config_path.write_text(json.dumps(cfg))
        env = dict(os.environ, MANAGEMENT_PASSWORD=MANAGEMENT_KEY, HTTP_PROXY="", HTTPS_PROXY="",
                   ALL_PROXY="", http_proxy="", https_proxy="", all_proxy="", NO_PROXY="*")
        if panel:
            panel_path = root / "management.html"
            panel_path.write_bytes(panel.read_bytes())
            env["MANAGEMENT_STATIC_PATH"] = str(panel_path)
        with (root / "server.log").open("w") as log:
            process = subprocess.Popen([str(binary.resolve()), "--config", str(config_path), "--local-model"],
                                       cwd=root, env=env, stdout=log, stderr=log)
            try:
                base = f"http://127.0.0.1:{port}"
                for _ in range(100):
                    if process.poll() is not None:
                        raise RuntimeError((root / "server.log").read_text())
                    try:
                        if request(base, "/v1/models")[0] == 200:
                            break
                    except urllib.error.URLError:
                        pass
                    time.sleep(0.1)
                else:
                    raise RuntimeError("fixture CPA did not start")
                if panel:
                    status, served = request(base, "/management.html")
                    assert status == 200 and served == panel.read_bytes(), "served panel mismatch"
                yield base, provider, upstream, root
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                upstream.shutdown()
                upstream.server_close()
                thread.join()


def smoke(base, provider, upstream):
    assert request(base, "/v1/embeddings", {"model": "fixture-vector", "input": "x"}, key="")[0] == 401
    status, body = request(base, "/v1/embeddings", {"model": "fixture-vector", "input": ["a", "b"], "dimensions": 2})
    assert status == 200 and len(json.loads(body)["data"]) == 2, (status, body)
    status, body = request(base, "/v1/rerank", {"model": "fixture-rank", "query": "a", "documents": ["a", "b"], "top_n": 1})
    assert status == 200 and json.loads(body)["results"][0]["index"] == 0, (status, body)
    assert upstream.records[-2]["body"]["model"] == "fixture-vector-v1"
    for kind, alias, name in [("embeddings", "fixture-vector", "fixture-vector-v1"),
                              ("rerank", "fixture-rank", "fixture-rank-v1")]:
        cases = [("/custom", 200), ("/bad", 502), ("/busy", 429)]
        if kind == "embeddings":
            cases.append(("/nonfinite", 502))
        for path, expected in cases:
            draft = dict(provider, models=[{"name": name, "alias": alias, "type": kind, "upstream-path": path}],
                         headers={"X-Retrieval-Fixture": "draft"})
            before = len(upstream.records)
            status, body = request(base, "/v0/management/provider-connectivity-test", {
                "provider": "openai-compatibility", "model": alias, "api_key": "fixture-key-two",
                "openai_config": draft,
            }, key=MANAGEMENT_KEY)
            assert status == 200 and json.loads(body)["status_code"] == expected, (status, body)
            assert len(upstream.records) == before + 1
            assert upstream.records[-1]["key"] == "Bearer fixture-key-two"
            assert upstream.records[-1]["settings"] == "draft"
    status, body = request(base, "/v0/management/openai-compatibility", key=MANAGEMENT_KEY)
    saved = json.loads(body)["openai-compatibility"][0]
    assert status == 200 and saved["headers"] == provider["headers"]
    assert "upstream-path" not in saved["models"][0]
    print("PASS: native endpoints, authentication, aliases, selected-key probes, draft paths/headers, malformed response and 429; saved configuration unchanged", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--panel", type=Path)
    parser.add_argument("--serve", action="store_true", help="Keep the isolated runtime for browser verification")
    parser.add_argument("--state", type=Path)
    args = parser.parse_args()
    with runtime(args.binary, args.panel) as (base, provider, upstream, root):
        smoke(base, provider, upstream)
        if args.serve:
            state = {"url": base, "upstream": provider["base-url"].removesuffix("/v1"), "directory": str(root)}
            if args.state:
                args.state.write_text(json.dumps(state))
            print(json.dumps(state), flush=True)
            done = threading.Event()
            signal.signal(signal.SIGTERM, lambda *_: done.set())
            signal.signal(signal.SIGINT, lambda *_: done.set())
            done.wait()


if __name__ == "__main__":
    main()
