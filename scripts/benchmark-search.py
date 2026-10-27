#!/usr/bin/env python3
"""Measure synthetic 10k x 1024 indexing and search with disposable databases."""

import argparse
import datetime
import hashlib
import json
import re
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path


PHASES = {
    "index-batch5": ("BenchmarkVectorIndex10000x1024/batch5", "1x"),
    "index-batch20": ("BenchmarkVectorIndex10000x1024/batch20", "1x"),
    "cold": ("BenchmarkSemanticCold10000x1024", "10x"),
    "warm": ("BenchmarkSemanticCache10000x1024", "100x"),
    "service": ("BenchmarkSearch10000x1024", "50x"),
}


def benchmark_rows(output):
    rows = []
    for line in output.splitlines():
        match = re.match(r"^(Benchmark\S+)\s+(\d+)\s+(.+)$", line)
        if not match:
            continue
        fields = match[3].split()
        if len(fields) % 2:
            raise ValueError("invalid Go benchmark metrics: " + line)
        metrics = {fields[i + 1]: float(fields[i]) for i in range(0, len(fields), 2)}
        rows.append({"name": match[1], "iterations": int(match[2]), "metrics": metrics})
    return rows


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--docker-image", help="Linux Go image, e.g. golang:1.27.3-bookworm")
    parser.add_argument("--go", default="go", help="Go executable for native Linux runs")
    parser.add_argument("--cpu", type=int, default=2, help="Go GOMAXPROCS during measurement")
    parser.add_argument("--phase", action="append", choices=PHASES, help="Run selected phases; defaults to all")
    parser.add_argument("--output", default=".local/search-benchmark.json")
    args = parser.parse_args()
    if args.cpu < 1:
        parser.error("--cpu must be positive")
    if not args.docker_image and not sys.platform.startswith("linux"):
        parser.error("use --docker-image outside Linux")
    root = Path(__file__).resolve().parents[1]
    output = Path(args.output).resolve()
    if output.exists():
        parser.error("output exists; select a new path to retain earlier measurements")
    output.parent.mkdir(parents=True, exist_ok=True)
    local = root / ".local"
    local.mkdir(exist_ok=True)
    report = {
        "schema_version": 1,
        "created_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "status": "running",
        "environment": {
            "runner": "docker-linux" if args.docker_image else "native-linux",
            "docker_image": args.docker_image,
            "gomaxprocs": args.cpu,
        },
        "workload": {
            "notes": 10000,
            "chunks": 10000,
            "dimensions": 1024,
            "vector_type": "dense positive float32, deterministic per unique short note",
            "embedding": "in-process synthetic; no network, quota, retry or provider semaphore",
            "source": "in-process Memos HTTP transport fixture; no network latency",
            "index": "projection-to-publication, empty vector cache; projection import excluded",
            "cold": "new application matrix cache per query; OS filesystem pages may be warm",
            "warm": "candidate scan with one shared published matrix",
            "service": "semantic/hybrid, ten results, live source rereads, 1/2/4 workers",
            "latency": "per-operation p50/p95; service worker queue wait excluded",
            "memory": "RSS/HWM cover process and fixture; HWM accumulates within each phase process",
        },
        "benchmark_sha256": hashlib.sha256(
            (root / "internal" / "search_benchmark_test.go").read_bytes()
        ).hexdigest(),
        "phases": [],
    }
    if shutil.which("git"):
        head = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=root, text=True, capture_output=True, check=False
        )
        if head.returncode == 0:
            report["source_head"] = head.stdout.strip()

    def run(command, name):
        print(name, flush=True)
        start = time.perf_counter()
        result = subprocess.run(
            command, cwd=root, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, encoding="utf-8", errors="replace", check=False,
        )
        phase = {
            "name": name,
            "wall_seconds_including_setup": round(time.perf_counter() - start, 3),
            "exit_code": result.returncode,
            "stdout": result.stdout,
        }
        report["phases"].append(phase)
        print(result.stdout, end="", flush=True)
        if result.returncode:
            raise RuntimeError(name + " failed")
        return phase

    def docker(command, network=False):
        flags = ["docker", "run", "--rm"]
        if not network:
            flags += ["--network", "none"]
        return flags + [
            "--mount", "type=bind,source=" + str(root) + ",target=/src",
            "-e", "GOCACHE=/src/.local/linux-gocache",
            "-e", "GOMODCACHE=/src/.local/gomodcache",
            "-e", "GOPATH=/src/.local/gopath",
            "-w", "/src", args.docker_image,
        ] + command

    try:
        with tempfile.TemporaryDirectory(prefix="search-benchmark-", dir=local) as directory:
            binary = Path(directory) / "search.test"
            container_binary = "/src/" + binary.relative_to(root).as_posix()
            build = [args.go, "test", "-c", "./internal", "-o", str(binary)]
            if args.docker_image:
                build = docker(["go", "test", "-c", "./internal", "-o", container_binary], network=True)
            run(build, "compile")
            for name in args.phase or PHASES:
                benchmark, duration = PHASES[name]
                pattern = "/".join("^" + component + "$" for component in benchmark.split("/"))
                command = [
                    container_binary if args.docker_image else str(binary),
                    "-test.run=^$", "-test.bench=" + pattern,
                    "-test.benchtime=" + duration, "-test.count=1",
                    "-test.cpu=" + str(args.cpu), "-test.benchmem", "-test.timeout=30m",
                ]
                phase = run(docker(command) if args.docker_image else command, name)
                phase["rows"] = benchmark_rows(phase["stdout"])
                expected = 6 if name == "service" else 1
                if len(phase["rows"]) != expected:
                    raise ValueError(f"{name}: expected {expected} benchmark rows")
        report["status"] = "passed"
    except (OSError, RuntimeError, ValueError) as error:
        report["status"] = "failed"
        report["error"] = str(error)
    with output.open("x", encoding="utf-8") as stream:
        json.dump(report, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    print(str(output), flush=True)
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
