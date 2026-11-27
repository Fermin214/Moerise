#!/usr/bin/env python3
"""Validate and summarize AndroidX Macrobenchmark measurements and traces."""
import argparse
import json
import math
from pathlib import Path
import sys
import xml.etree.ElementTree as ET

parser = argparse.ArgumentParser()
parser.add_argument("directory", type=Path)
parser.add_argument("--mode", choices=("run", "startup", "stars"))
args = parser.parse_args()
root = args.directory.resolve()
device = json.loads((root / "device.json").read_text(encoding="utf-8"))
mode = args.mode or device.get("mode")
if mode not in ("run", "startup", "stars"):
    parser.error("用 --mode 或 device.json 指定 run、startup 或 stars")

prefix = "io.github.fermin214.moerise.benchmark."
startup_ready_metric = "note_editor_readyMs"
expected = {}
if mode in ("run", "startup"):
    profiles = ("WithoutProfile",) if device.get("profile_mode") == "without" else ("WithoutProfile", "WithProfile")
    for profile in profiles:
        expected[(prefix + "StartupBenchmark", "cold" + profile)] = (
            50, {"timeToInitialDisplayMs", "timeToFullDisplayMs", startup_ready_metric},
            {"timeToFullDisplayMs": 1000})
        expected[(prefix + "StartupBenchmark", "foreground" + profile)] = (
            50, {"note_foreground_readyMs"}, {"note_foreground_readyMs": 300})
if mode in ("run", "stars"):
    for points in (1000, 5000):
        for atmosphere in ("Stars", "Photo"):
            expected[(prefix + "StarBenchmark", f"first{points}{atmosphere}")] = (
                50, {"timeToInitialDisplayMs", "timeToFullDisplayMs", "star_scene_readyMs",
                     "star_scene_parseMs", "frameDurationCpuMs"},
                {"star_scene_readyMs": 2000 if points == 5000 else 1000})
            expected[(prefix + "StarBenchmark", f"gestures{points}{atmosphere}")] = (
                10, {"frameDurationCpuMs"}, {"frameDurationCpuMs": 33})

def percentile(values, fraction):
    values = sorted(values)
    index = (len(values) - 1) * fraction
    low = math.floor(index)
    high = math.ceil(index)
    return values[low] + (values[high] - values[low]) * (index - low)

errors, output, seen = [], [], {}
traces = {}
raw = root / "raw"
for path in raw.rglob("*.perfetto-trace"):
    traces.setdefault(path.name, []).append(path)
for source in sorted(raw.rglob("*.xml")):
    try:
        report = ET.parse(source)
        if report.findall(".//failure") or report.findall(".//error") or report.findall(".//skipped"):
            errors.append(f"{source.relative_to(root)}：存在失败或跳过的设备测试")
    except ET.ParseError as error:
        errors.append(f"{source.relative_to(root)}：XML 损坏：{error}")

for source in sorted(raw.rglob("*benchmarkData.json")):
    try:
        payload = json.loads(source.read_text(encoding="utf-8"))
        benchmarks = payload["benchmarks"]
        if not isinstance(benchmarks, list) or not benchmarks:
            raise ValueError("benchmarks 不能为空")
    except (ValueError, KeyError, TypeError) as error:
        errors.append(f"{source.relative_to(root)}：原始 JSON 无效：{error}")
        continue
    for benchmark in benchmarks:
        if not isinstance(benchmark, dict):
            errors.append(f"{source.relative_to(root)}：无效的场景记录")
            continue
        key = benchmark.get("className"), benchmark.get("name")
        if not all(isinstance(part, str) for part in key):
            errors.append(f"{source.relative_to(root)}：场景身份无效")
            continue
        if key in seen:
            if seen[key] != benchmark:
                errors.append(f"{key}：存在互相冲突的重复结果")
            continue  # AGP 的 reports 和 outputs 可能包含同一份 JSON。
        seen[key] = benchmark
        if key not in expected:
            errors.append(f"{key}：不属于 {mode} 的预期场景")
            continue
        iterations, required, limits = expected[key]
        problems, metrics = [], {}
        for group in ("metrics", "sampledMetrics"):
            values = benchmark.get(group, {})
            if not isinstance(values, dict):
                problems.append(f"{group} 格式无效")
                continue
            for name, metric in values.items():
                source_name = name
                if name in {section + "FirstMs" for section in (
                        "note_editor_ready", "note_foreground_ready", "star_scene_ready", "star_scene_parse")}:
                    name = name.removesuffix("FirstMs") + "Ms"
                runs = metric.get("runs") if isinstance(metric, dict) else None
                if not isinstance(runs, list) or len(runs) != iterations:
                    problems.append(f"{name}：需要 {iterations} 次采样")
                    continue
                samples = []
                for run in runs:
                    entries = run if group == "sampledMetrics" else [run]
                    if not isinstance(entries, list) or not entries or any(
                        isinstance(value, bool) or not isinstance(value, (int, float)) or
                        not math.isfinite(value) for value in entries
                    ):
                        problems.append(f"{name}：存在空值或无效原始采样")
                        break
                    samples.extend(entries)
                else:
                    if name in required and any(value < 0 for value in samples):
                        problems.append(f"{name}：耗时不能为负数")
                        continue
                    if name in metrics:
                        problems.append(f"{name}：指标重复")
                        continue
                    metrics[name] = dict(source_name=source_name, iterations=len(runs), samples=len(samples),
                                         p50=percentile(samples, .5), p95=percentile(samples, .95),
                                         maximum=max(samples))
                    if name == startup_ready_metric:
                        metrics[name]["definition"] = "Activity 创建至草稿恢复后首页写作胶囊的首个可交互帧"
        for name in sorted(required - metrics.keys()):
            problems.append(f"缺少有效指标 {name}")
        profiler_outputs = benchmark.get("profilerOutputs", [])
        if not isinstance(profiler_outputs, list):
            profiler_outputs = []
            problems.append("profilerOutputs 格式无效")
        filenames = [entry.get("filename") for entry in profiler_outputs
                     if isinstance(entry, dict) and entry.get("type") == "PerfettoTrace"]
        if len(filenames) < iterations or any(not isinstance(name, str) or not name for name in filenames):
            problems.append(f"需要至少 {iterations} 份 Perfetto trace 引用")
        else:
            if len(set(filenames)) != len(filenames):
                problems.append("Perfetto trace 引用重复")
            for filename in filenames:
                matches = traces.get(Path(filename).name, [])
                if not matches or any(path.stat().st_size == 0 for path in matches):
                    problems.append(f"trace 缺失或为空：{filename}")
        targets = []
        for name, limit in limits.items():
            value = metrics.get(name, {}).get("p95")
            meets = value is not None and value <= limit
            targets.append(dict(metric=name, p95_limit=limit, measured_p95=value, meets_target=meets))
            if not meets:
                problems.append(f"{name}：P95 {value}，上限 {limit}")
        errors.extend(f"{key[1]}：{problem}" for problem in problems)
        output.append(dict(name=key[1], class_name=key[0], source=str(source.relative_to(root)),
                           metrics=metrics, targets=targets, status="failed" if problems else "succeeded",
                           errors=problems))

for key in sorted(expected.keys() - seen.keys()):
    errors.append(f"缺少场景 {key[0]}.{key[1]}")
if mode in ("run", "startup"):
    for name, value in dict(fixture_notes=100, cached_notes=20, cached_long_note_bytes=65536,
                            cached_long_note_attachments=10).items():
        if device.get(name) != value:
            errors.append(f"device.json 的 {name} 应为 {value}")
if device.get("gradle_succeeded") is not True:
    errors.append("device.json 缺少成功的 Gradle 执行记录")
if device.get("execution") == "adb":
    record_path = root / "instrumentation.json"
    record = json.loads(record_path.read_text()) if record_path.is_file() else {}
    if not all(record.get(key) is True for key in (
            "install_succeeded", "instrumentation_succeeded", "output_collected")):
        errors.append("缺少成功的 ADB 安装、instrumentation 或输出采集记录")
destination = root / "summary.json"
destination.write_text(json.dumps(dict(mode=mode, status="failed" if errors else "succeeded",
                                      errors=errors, benchmarks=output),
                                 ensure_ascii=False, indent=2, allow_nan=False) + "\n", encoding="utf-8")
print(destination)
if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)
