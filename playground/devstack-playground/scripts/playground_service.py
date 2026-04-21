#!/usr/bin/env python3
import atexit
import json
import os
import signal
import socketserver
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse

ROOT = Path(__file__).resolve().parent.parent
STATE_DIR = ROOT / "state"
LOG_DIR = ROOT / "logs"
RUNTIME_DIR = STATE_DIR / "runtime"
STATE_DIR.mkdir(parents=True, exist_ok=True)
LOG_DIR.mkdir(parents=True, exist_ok=True)
RUNTIME_DIR.mkdir(parents=True, exist_ok=True)


def state_file(service: str) -> Path:
    return STATE_DIR / f"{service}.mode"


def runtime_file(service: str) -> Path:
    return RUNTIME_DIR / f"{service}.json"


def service_log(service: str) -> Path:
    return LOG_DIR / f"{service}.log"


def read_mode(service: str, default: str = "healthy") -> str:
    path = state_file(service)
    if path.exists():
        return path.read_text().strip() or default
    return default


def append_log(service: str, message: str) -> None:
    line = f"[{time.strftime('%Y-%m-%dT%H:%M:%S')}] {message}\n"
    with service_log(service).open("a", encoding="utf-8") as fh:
        fh.write(line)
    print(message, flush=True)


def write_runtime(service: str, state: str, **extra) -> None:
    payload = {
        "service": service,
        "state": state,
        "pid": os.getpid(),
        "timestamp": time.time(),
    }
    payload.update(extra)
    runtime_file(service).write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")


class ServiceContext:
    def __init__(self, service: str):
        self.service = service
        self.mode = read_mode(service)
        self.stopped = False
        self.exit_code = 0
        self._cleanup_registered = False

    def register_cleanup(self):
        if self._cleanup_registered:
            return
        self._cleanup_registered = True
        atexit.register(self._on_exit)
        signal.signal(signal.SIGTERM, self._handle_signal)
        signal.signal(signal.SIGINT, self._handle_signal)

    def _handle_signal(self, signum, _frame):
        self.exit_code = 0
        self.stopped = True
        append_log(self.service, f"received signal {signum}; shutting down")
        raise SystemExit(0)

    def _on_exit(self):
        final_state = "stopped" if self.exit_code == 0 else "crashed"
        write_runtime(self.service, final_state, mode=self.mode, exit_code=self.exit_code)


class ReusableTCPServer(socketserver.TCPServer):
    allow_reuse_address = True


class ThreadedHTTPServer(ThreadingHTTPServer):
    allow_reuse_address = True


def env_port(service: str, default: int) -> int:
    key = f"PLAYGROUND_{service.upper().replace('-', '_')}_PORT"
    return int(os.environ.get(key, str(default)))


def run_http_service(service: str, port: int, handler_factory):
    ctx = ServiceContext(service)
    ctx.register_cleanup()
    port = env_port(service, port)
    write_runtime(service, "running", mode=ctx.mode, port=port)
    append_log(service, f"starting HTTP service on :{port} (mode={ctx.mode})")
    server = ThreadedHTTPServer(("127.0.0.1", port), handler_factory(ctx))
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


def json_response(handler, status: int, payload):
    data = json.dumps(payload).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json")
    handler.send_header("Content-Length", str(len(data)))
    handler.end_headers()
    handler.wfile.write(data)


def frontend_handler(ctx: ServiceContext):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            append_log(ctx.service, fmt % args)

        def do_GET(self):
            parsed = urlparse(self.path)
            if parsed.path == "/health":
                json_response(self, 200, {"service": ctx.service, "status": "ok"})
                return
            if parsed.path == "/chain":
                api_base = os.environ.get("PLAYGROUND_API_URL", "http://127.0.0.1:8080")
                with urllib.request.urlopen(api_base + "/chain", timeout=2) as resp:
                    body = json.loads(resp.read().decode("utf-8"))
                json_response(self, 200, {"service": ctx.service, "api": body})
                return
            json_response(self, 404, {"error": "not found"})

    return Handler


def api_handler(ctx: ServiceContext):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            append_log(ctx.service, fmt % args)

        def do_GET(self):
            parsed = urlparse(self.path)
            if parsed.path == "/health":
                json_response(self, 200, {"service": ctx.service, "status": "ok"})
                return
            if parsed.path == "/chain":
                worker_base = os.environ.get("PLAYGROUND_WORKER_URL", "http://127.0.0.1:9090")
                with urllib.request.urlopen(worker_base + "/job?kind=chain", timeout=2) as resp:
                    worker = json.loads(resp.read().decode("utf-8"))
                json_response(self, 200, {"service": ctx.service, "worker": worker})
                return
            json_response(self, 404, {"error": "not found"})

    return Handler


def worker_handler(ctx: ServiceContext):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            append_log(ctx.service, fmt % args)

        def do_GET(self):
            parsed = urlparse(self.path)
            if parsed.path == "/health":
                json_response(self, 200, {"service": ctx.service, "status": "ok"})
                return
            if parsed.path == "/job":
                kind = parse_qs(parsed.query).get("kind", ["default"])[0]
                append_log(ctx.service, f"processed job kind={kind}")
                json_response(self, 200, {"service": ctx.service, "job": kind, "status": "done"})
                return
            json_response(self, 404, {"error": "not found"})

    return Handler


def post_trace(collector_url: str, service_name: str, mode: str) -> bool:
    payload = json.dumps({
        "service": service_name,
        "mode": mode,
        "kind": "trace",
        "timestamp": time.time(),
    }).encode("utf-8")
    req = urllib.request.Request(
        collector_url.rstrip("/") + "/v1/traces",
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=2) as resp:
        resp.read()
    return True


def telemetry_loop(service: str, default_collector: str = "http://127.0.0.1:4318"):
    ctx = ServiceContext(service)
    ctx.register_cleanup()
    write_runtime(service, "running", mode=ctx.mode)
    collector_url = os.environ.get("OTEL_EXPORTER_OTLP_ENDPOINT", default_collector)
    service_name = service if ctx.mode != "wrong-service-name" else f"{service}-mismatch"
    append_log(service, f"starting telemetry loop mode={ctx.mode} collector={collector_url}")
    while True:
        append_log(service, f"emitting log heartbeat service={service_name} mode={ctx.mode}")
        try:
            if ctx.mode not in {"no-traces", "logs-only"}:
                target = collector_url
                if ctx.mode == "collector-down":
                    target = "http://127.0.0.1:65530"
                post_trace(target, service_name, ctx.mode)
                append_log(service, f"trace export succeeded service={service_name}")
            else:
                append_log(service, f"trace export skipped mode={ctx.mode}")
        except Exception as exc:  # noqa: BLE001
            append_log(service, f"trace export failed: {exc}")
        time.sleep(1)


def run_crashy(service: str):
    ctx = ServiceContext(service)
    ctx.register_cleanup()
    write_runtime(service, "running", mode=ctx.mode)
    append_log(service, "crashy starting and will exit with status 1")
    time.sleep(0.5)
    ctx.exit_code = 1
    raise SystemExit(1)


def main(argv) -> int:
    if len(argv) != 2:
        print("usage: playground_service.py <service>", file=sys.stderr)
        return 1
    service = argv[1]
    if service == "frontend":
        run_http_service(service, 3000, frontend_handler)
        return 0
    if service == "api":
        run_http_service(service, 8080, api_handler)
        return 0
    if service == "worker":
        run_http_service(service, 9090, worker_handler)
        return 0
    if service in {"telemetry-good", "telemetry-bad"}:
        telemetry_loop(service)
        return 0
    if service == "crashy":
        run_crashy(service)
        return 1
    print(f"unknown service: {service}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
