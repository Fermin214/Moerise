"""Private layout worker; scientific processes exit after each job to release memory."""

import argparse
import base64
import hashlib
import json
import os
import re
import resource
import subprocess
import sys
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from tempfile import TemporaryDirectory

for variable in ("NUMBA_NUM_THREADS", "OPENBLAS_NUM_THREADS", "OMP_NUM_THREADS", "MKL_NUM_THREADS"):
    os.environ.setdefault(variable, "2")

ALGORITHM = "umap-balanced-v1"
PARAMETERS = {"n_neighbors": 15, "min_dist": .12, "spread": 1.0, "metric": "cosine",
              "n_components": 2, "random_state": 42, "transform_seed": 42, "n_jobs": 1, "init": "spectral"}
STATE = Path(os.environ.get("MOERISE_LAYOUT_STATE", "/data"))
GATE = threading.Semaphore(1)
LAST_JOB = {}


def compute(request):
    import joblib
    import numpy as np
    from sklearn.decomposition import PCA
    from threadpoolctl import threadpool_limits
    from umap import UMAP
    from semantic import describe

    started = time.perf_counter()
    scope = request["scope"]
    if not re.fullmatch(r"[a-f0-9]{64}", scope):
        raise ValueError("invalid scope")
    documents = request["documents"]
    count, dimensions = len(documents), request["dimensions"]
    matrix = np.frombuffer(base64.b64decode(request["matrix"], validate=True), dtype="<f4").reshape(count, dimensions).copy()
    if not np.isfinite(matrix).all() or count and np.any(np.linalg.norm(matrix, axis=1) < 1e-12):
        raise ValueError("invalid vectors")
    if count:
        matrix /= np.linalg.norm(matrix, axis=1, keepdims=True)
    root = STATE / "models" / scope
    root.mkdir(parents=True, exist_ok=True)
    frame = request.get("frame", "")
    if frame and not re.fullmatch(r"[a-f0-9]{32}", frame):
        raise ValueError("invalid frame")
    path = root / (frame + ".joblib")
    state = joblib.load(path) if frame and path.exists() else None
    if state and (state["dimensions"] != dimensions or state["algorithm"] != ALGORITHM
                  or state.get("fit_count", 0) < 2 and count > state.get("fit_count", 0)):
        state = None
    with threadpool_limits(limits=2):
        if state:
            old = {point["name"]: point for point in request.get("previous_points", [])}
            positions = np.zeros((count, 2))
            changed = []
            for i, document in enumerate(documents):
                point = old.get(document["name"])
                document["unchanged"] = bool(point and point["vector_hash"] == document["vector_hash"])
                if document["unchanged"]:
                    positions[i] = [point["x"], point["y"]]
                    document["stellar"] = point.get("stellar")
                else:
                    changed.append(i)
            if changed:
                model = state["model"]
                if model is None:
                    projected = np.zeros((len(changed), 2))
                else:
                    projected = model.transform(matrix[changed])
                    if projected.shape[1] < 2:
                        projected = np.pad(projected, ((0, 0), (0, 2 - projected.shape[1])))
                positions[changed] = (projected - state["center"]) / state["radius"]
            previous = request.get("previous_topics", [])
        else:
            frame = uuid.uuid4().hex
            model = None
            if count < 2:
                positions = np.zeros((count, 2))
            elif count < 12 or np.max(np.std(matrix, axis=0)) < 1e-7:
                model = PCA(n_components=min(2, dimensions, count), svd_solver="full")
                positions = model.fit_transform(matrix)
                if positions.shape[1] < 2:
                    positions = np.pad(positions, ((0, 0), (0, 2 - positions.shape[1])))
            else:
                model = UMAP(**dict(PARAMETERS, n_neighbors=min(15, count - 1)))
                positions = model.fit_transform(matrix)
            center = positions.mean(axis=0) if count else np.zeros(2)
            radius = max(1e-5, float(np.percentile(np.linalg.norm(positions - center, axis=1), 90))) if count else 1.
            positions = (positions - center) / radius
            state = {"algorithm": ALGORITHM, "dimensions": dimensions, "model": model, "fit_count": count,
                     "center": center, "radius": radius, "parameters": PARAMETERS}
            path = root / (frame + ".joblib")
            temporary = path.with_suffix(".partial")
            joblib.dump(state, temporary, compress=0)
            temporary.replace(path)
            previous = None
        if not np.isfinite(positions).all():
            raise ValueError("invalid coordinates")
        topics, attributes, links = describe(matrix, documents, positions, previous) if count else ([], [], [])
    path.touch()
    for obsolete in sorted(root.glob("*.joblib"), key=lambda file: file.stat().st_mtime, reverse=True)[3:]:
        obsolete.unlink(missing_ok=True)
    points = [{"name": document["name"], "x": round(float(positions[i, 0]), 7),
               "y": round(float(positions[i, 1]), 7), "stellar": attributes[i]}
              for i, document in enumerate(documents)]
    usage = resource.getrusage(resource.RUSAGE_SELF)
    return {"algorithm": ALGORITHM, "frame": frame, "points": points, "topics": topics, "links": links,
            "seconds": round(time.perf_counter() - started, 3),
            "peak_rss_bytes": usage.ru_maxrss * 1024,
            "cpu_seconds": round(usage.ru_utime + usage.ru_stime, 3)}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, code, value):
        content = json.dumps(value, ensure_ascii=False, allow_nan=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(content)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        try:
            self.wfile.write(content)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def do_GET(self):
        self.reply(200 if self.path == "/health" else 404,
                   {"status": "ready", "last_job": LAST_JOB})

    def do_POST(self):
        global LAST_JOB
        if self.path != "/layout":
            return self.reply(404, {"error": "NOT_FOUND"})
        size = int(self.headers.get("Content-Length", "0"))
        if size <= 0 or size > 256 * 1024 * 1024:
            return self.reply(413, {"error": "LAYOUT_INPUT_LIMIT"})
        if not GATE.acquire(blocking=False):
            return self.reply(429, {"error": "LAYOUT_BUSY"})
        try:
            with TemporaryDirectory(dir=STATE / "jobs", prefix="layout-") as temporary:
                source, output = Path(temporary) / "input.json", Path(temporary) / "result.json"
                source.write_bytes(self.rfile.read(size))
                process = subprocess.run([sys.executable, str(Path(__file__).resolve()), "compute",
                                          str(source), str(output)], timeout=900, capture_output=True)
                if process.returncode:
                    sys.stderr.write(process.stderr.decode(errors="replace")[-4000:] + "\n")
                    return self.reply(503, {"error": "LAYOUT_FAILED"})
                result = json.loads(output.read_text())
                LAST_JOB = {key: result[key] for key in ("seconds", "cpu_seconds", "peak_rss_bytes")}
                LAST_JOB["notes"] = len(result["points"])
                self.reply(200, result)
        except (ValueError, TimeoutError, subprocess.TimeoutExpired):
            self.reply(503, {"error": "LAYOUT_FAILED"})
        finally:
            GATE.release()


def main():
    if sys.argv[1:2] == ["compute"]:
        Path(sys.argv[3]).write_text(json.dumps(compute(json.loads(Path(sys.argv[2]).read_text())),
                                               ensure_ascii=False, allow_nan=False))
        return
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=["serve"])
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8766)
    args = parser.parse_args()
    for name in ("models", "jobs", "cache"):
        (STATE / name).mkdir(parents=True, exist_ok=True)
    os.environ.setdefault("NUMBA_CACHE_DIR", str(STATE / "cache"))
    ThreadingHTTPServer((args.host, args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
