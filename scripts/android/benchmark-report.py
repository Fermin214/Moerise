#!/usr/bin/env python3
"""Summarize AndroidX Macrobenchmark output without replacing raw measurements."""
import argparse
import json
import math
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("directory", type=Path)
args = parser.parse_args()

def numbers(value):
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        return [float(value)]
    if isinstance(value, list):
        return [number for item in value for number in numbers(item)]
    return []

def percentile(values, fraction):
    values = sorted(values)
    index = (len(values) - 1) * fraction
    low = math.floor(index)
    high = math.ceil(index)
    return values[low] + (values[high] - values[low]) * (index - low)

output = []
for source in sorted(args.directory.rglob("*benchmarkData.json")):
    payload = json.loads(source.read_text())
    for benchmark in payload.get("benchmarks", []):
        metrics = {}
        for group in ("metrics", "sampledMetrics"):
            for name, metric in benchmark.get(group, {}).items():
                runs = numbers(metric.get("runs", []))
                if not runs:
                    continue
                metrics[name] = dict(samples=len(runs), p50=percentile(runs, .5),
                                     p95=percentile(runs, .95), maximum=max(runs))
        name = benchmark.get("name", "")
        targets = []
        for metric, limit in (
            ("timeToFullDisplayMs", 1000 if "cold" in name else None),
            ("note_foreground_readyMs", 300 if "foreground" in name else None),
            ("star_scene_readyMs", (2000 if "5000" in name else 1000) if "first" in name else None),
            ("frameDurationCpuMs", 33 if "gestures" in name else None),
        ):
            if limit is not None:
                value = metrics.get(metric, {}).get("p95")
                targets.append(dict(metric=metric, p95_limit=limit, measured_p95=value,
                                    meets_target=value <= limit if value is not None else None))
        output.append(dict(name=name, class_name=benchmark.get("className"),
                           source=str(source.relative_to(args.directory)), metrics=metrics, targets=targets))
assert output, "找不到 Macrobenchmark 原始测量 JSON"
destination = args.directory / "summary.json"
destination.write_text(json.dumps(output, ensure_ascii=False, indent=2) + "\n")
print(destination)
