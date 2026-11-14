#!/usr/bin/env python3
"""CLI and loopback server for the independent stars layout experiment."""

import argparse
import gzip
import html
import json
import os
import subprocess
import sys
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit

# Set limits before importing numpy/numba in a child computation process.
for variable in ("NUMBA_NUM_THREADS", "OPENBLAS_NUM_THREADS", "OMP_NUM_THREADS", "MKL_NUM_THREADS"):
    os.environ[variable] = "2"

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
DEFAULT_OUTPUT = ROOT / ".local/stars"


def compute(args):
    from engine import build_limited, read_export, read_index, write_json

    output = Path(args.out)
    output.mkdir(parents=True, exist_ok=True)
    if (output / "bundle.json").exists():
        raise ValueError("输出目录已包含布局；请指定新的 --out 目录")

    def progress(message):
        print(message, flush=True)
        write_json(output / "progress.json", {"status": "building", "message": message})

    progress("读取笔记向量")
    if args.index:
        notes, vectors, source = read_index(args.index, args.instance, args.user, args.space)
        if args.label:
            source["label"] = args.label
    else:
        notes, vectors, source = read_export(args.input, args.vectors_f32, args.dimensions, args.label, args.kind)
    result = build_limited(notes, vectors, source, output, tuple(args.sizes), args.seed, progress)
    write_json(output / "progress.json", {"status": "ready", "message": f"{result['source']['vector_notes']} 条布局已生成"})
    print(str(output / "bundle.json"), flush=True)


class Experiment:
    def __init__(self, output, bundle=None):
        self.output = Path(output).resolve()
        self.output.mkdir(parents=True, exist_ok=True)
        self.bundle = Path(bundle).resolve() if bundle else None
        self.job = None
        self.process = None
        self.error = None
        self.lock = threading.Lock()

    def start(self, input_path=None, index=None, options=None):
        options = options or {}
        with self.lock:
            if self.process and self.process.poll() is None:
                raise ValueError("已有布局计算任务，请等待完成")
            job = self.output / (time.strftime("%Y%m%d-%H%M%S") + "-" + uuid.uuid4().hex[:8])
            job.mkdir()
            command = [sys.executable, str(HERE / "lab.py"), "build", "--out", str(job)]
            if index:
                command += ["--index", str(index)]
            else:
                command += ["--input", str(input_path)]
            for key in ("instance", "user", "space", "label", "kind"):
                if options.get(key):
                    command += ["--" + key, options[key]]
            self.job, self.error = job, None
            log = (job / "compute.log").open("w", encoding="utf-8")
            self.process = subprocess.Popen(command, stdout=log, stderr=subprocess.STDOUT, cwd=ROOT)
            log.close()

    def status(self):
        with self.lock:
            if self.process is not None:
                code = self.process.poll()
                if code is not None:
                    if code == 0:
                        self.bundle = self.job / "bundle.json"
                    else:
                        self.error = (self.job / "error.txt").read_text(encoding="utf-8") if (self.job / "error.txt").exists() else "布局计算失败，请查看实验目录 compute.log"
                    self.process = None
                else:
                    path = self.job / "progress.json"
                    progress = json.loads(path.read_text()) if path.exists() else {"status": "building", "message": "准备计算"}
                    return {**progress, "has_bundle": self.bundle is not None}
            return {"status": "error" if self.error else "ready" if self.bundle else "empty",
                    "message": self.error or "", "has_bundle": self.bundle is not None,
                    "revision": f"{self.bundle.parent.name}:{self.bundle.stat().st_mtime_ns}" if self.bundle else ""}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, message, *args):
        if len(args) > 1 and str(args[1]).startswith("4"):
            super().log_message(message, *args)

    def reply(self, code, content, mime="application/json; charset=utf-8", download=None):
        if isinstance(content, dict):
            content = json.dumps(content, ensure_ascii=False).encode()
        compressed = len(content) > 1024 and "gzip" in self.headers.get("Accept-Encoding", "")
        if compressed:
            content = gzip.compress(content, compresslevel=4)
        self.send_response(code)
        self.send_header("Content-Type", mime)
        self.send_header("Content-Length", str(len(content)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("Vary", "Accept-Encoding")
        if compressed:
            self.send_header("Content-Encoding", "gzip")
        if download:
            self.send_header("Content-Disposition", f'attachment; filename="{download}"')
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; object-src 'none'; frame-ancestors 'none'")
        self.end_headers()
        self.wfile.write(content)

    def do_GET(self):
        state = self.server.experiment
        path = urlsplit(self.path).path
        if path == "/api/status":
            return self.reply(200, state.status())
        if path in ("/api/bundle", "/api/report", "/api/offline"):
            state.status()
            if state.bundle is None:
                return self.reply(404, {"error": "请先导入笔记向量"})
            target = state.bundle if path == "/api/bundle" else state.bundle.with_name("stars.html" if path == "/api/offline" else "comparison.md")
            if not target.exists():
                return self.reply(404, {"error": "实验文件不存在"})
            mime = "application/json; charset=utf-8" if path.endswith("bundle") else "text/html; charset=utf-8" if path.endswith("offline") else "text/markdown; charset=utf-8"
            return self.reply(200, target.read_bytes(), mime, "stars.html" if path.endswith("offline") else None)
        static = {"/": ("index.html", "text/html"), "/app.js": ("app.js", "text/javascript"),
                  "/scene.js": ("scene.js", "text/javascript"), "/style.css": ("style.css", "text/css")}
        if path not in static:
            return self.reply(404, {"error": "路径不存在"})
        name, mime = static[path]
        content = (HERE / name).read_bytes()
        if path == "/" and state.bundle:
            state.status()
            serialized = state.bundle.read_text(encoding="utf-8").replace("<", "\\u003c")
            revision = html.escape(state.status()["revision"], quote=True)
            preload = f'<script id="initial-data" type="application/json" data-revision="{revision}">{serialized}</script>'
            content = content.replace(b"</head>", preload.encode() + b"</head>")
        self.reply(200, content, mime + "; charset=utf-8")

    def do_POST(self):
        if urlsplit(self.path).path != "/api/import":
            return self.reply(404, {"error": "路径不存在"})
        origin = self.headers.get("Origin")
        if (origin and urlsplit(origin).netloc != self.headers.get("Host")
                or self.headers.get("Sec-Fetch-Site") == "cross-site"):
            return self.reply(403, {"error": "请从实验页面导入"})
        state = self.server.experiment
        if state.status()["status"] == "building":
            return self.reply(409, {"error": "已有布局计算任务，请等待完成"})
        try:
            size = int(self.headers.get("Content-Length", "0"))
            if size <= 0 or size > 256 * 1024 * 1024:
                raise ValueError("导入文件需在 1 字节至 256 MiB 之间；更大的索引请通过命令行读取")
            content = self.rfile.read(size)
            if len(content) != size:
                raise ValueError("文件传输不完整")
            sqlite = content.startswith(b"SQLite format 3\x00")
            if not sqlite:
                value = json.loads(content)
                if not isinstance(value, (dict, list)):
                    raise ValueError("需要笔记向量 JSON 或 index.sqlite3 副本")
            source_dir = state.output / "imports"
            source_dir.mkdir(exist_ok=True)
            path = source_dir / (uuid.uuid4().hex + (".sqlite3" if sqlite else ".json"))
            path.write_bytes(content)
            state.start(index=path if sqlite else None, input_path=None if sqlite else path)
            self.reply(202, {"status": "building"})
        except (ValueError, json.JSONDecodeError) as error:
            self.reply(400, {"error": str(error)})


def parser():
    result = argparse.ArgumentParser(description="Moerise 群星语义布局实验")
    commands = result.add_subparsers(dest="command", required=True)
    build = commands.add_parser("build", help="离线计算三种布局及评估")
    inputs = build.add_mutually_exclusive_group(required=True)
    inputs.add_argument("--index", help="只读 index.sqlite3 路径")
    inputs.add_argument("--input", help="含 notes 数组和向量的 JSON，或配合 --vectors-f32 的笔记 JSON")
    build.add_argument("--vectors-f32", help="按笔记顺序存放的 little-endian float32 文件")
    build.add_argument("--dimensions", type=int)
    build.add_argument("--instance")
    build.add_argument("--user")
    build.add_argument("--space")
    build.add_argument("--label")
    build.add_argument("--kind", choices=("personal", "demo", "evaluation"))
    build.add_argument("--seed", type=int, default=42)
    build.add_argument("--sizes", nargs="+", type=int, default=[50, 300])
    build.add_argument("--out", required=True)
    serve = commands.add_parser("serve", help="启动独立浏览器实验页面")
    serve.add_argument("--port", type=int, default=4317)
    serve.add_argument("--bundle", help="预先计算的 bundle.json")
    serve.add_argument("--index", help="启动后自动读取索引")
    serve.add_argument("--instance")
    serve.add_argument("--user")
    serve.add_argument("--space")
    serve.add_argument("--out", default=str(DEFAULT_OUTPUT))
    return result


if __name__ == "__main__":
    os.umask(0o077)
    args = parser().parse_args()
    try:
        if args.command == "build":
            if any(size < 1 for size in args.sizes):
                raise ValueError("--sizes 必须为正整数")
            compute(args)
        else:
            default_bundle = args.bundle
            if not default_bundle and not args.index:
                default_bundle = next((str(path) for path in
                    [DEFAULT_OUTPUT / "visual-v3/bundle.json", DEFAULT_OUTPUT / "visual-v2/bundle.json",
                     DEFAULT_OUTPUT / "demo-1000/bundle.json"]
                    if path.exists()), None)
            experiment = Experiment(args.out, default_bundle)
            if args.index:
                experiment.start(index=Path(args.index).resolve(), options=vars(args))
            server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
            server.experiment = experiment
            print(f"群星实验页面：http://127.0.0.1:{args.port}", flush=True)
            try:
                server.serve_forever()
            finally:
                server.server_close()
                if experiment.process and experiment.process.poll() is None:
                    experiment.process.terminate()
                    experiment.process.wait()
    except KeyboardInterrupt:
        pass
    except Exception as error:
        if args.command == "build":
            output = Path(args.out)
            output.mkdir(parents=True, exist_ok=True)
            (output / "error.txt").write_text(str(error), encoding="utf-8")
        print(f"错误：{error}", file=sys.stderr, flush=True)
        raise
