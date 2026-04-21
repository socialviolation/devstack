#!/usr/bin/env python3
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
COLLECTOR_LOG = ROOT / "logs" / "collector.jsonl"
COLLECTOR_LOG.parent.mkdir(parents=True, exist_ok=True)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, _fmt, *_args):
        return

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        payload = {
            "path": self.path,
            "received_at": time.time(),
            "body": json.loads(body.decode("utf-8")),
        }
        with COLLECTOR_LOG.open("a", encoding="utf-8") as fh:
            fh.write(json.dumps(payload) + "\n")
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")


def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 4318
    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    server.serve_forever()


if __name__ == "__main__":
    main()
