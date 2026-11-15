#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
android_benchmark_mode="${1:-run}"
case "$android_benchmark_mode" in profile|run|startup|stars) ;; *) echo "用法：benchmark.sh [profile|run|startup|stars]" >&2; exit 1 ;; esac
: "${ANDROID_SERIAL:?设置用于性能测量的设备序列号}"
: "${MOERISE_ANDROID_FIXTURE:?设置隔离服务连接档案路径}"
adb -s "$ANDROID_SERIAL" get-state >/dev/null
node scripts/android/seed-benchmark.mjs
android_benchmark_port="$(python3 -c 'import json,os,urllib.parse; print(urllib.parse.urlparse(json.load(open(os.environ["MOERISE_ANDROID_FIXTURE"]))["origin"]).port)')"
adb -s "$ANDROID_SERIAL" reverse "tcp:$android_benchmark_port" "tcp:$android_benchmark_port"
adb -s "$ANDROID_SERIAL" shell pm clear io.github.fermin214.moerise.benchmark >/dev/null 2>&1 || true
adb -s "$ANDROID_SERIAL" push "$MOERISE_ANDROID_FIXTURE" /data/local/tmp/moerise-android-fixture.json >/dev/null
cleanup_android_benchmark() {
    adb -s "$ANDROID_SERIAL" shell rm -f /data/local/tmp/moerise-android-fixture.json >/dev/null 2>&1 || true
}
trap cleanup_android_benchmark EXIT
android_benchmark_output=".local/android/benchmark-$(git rev-parse --short HEAD)-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$android_benchmark_output"
android_benchmark_variant=benchmark
android_benchmark_class="io.github.fermin214.moerise.benchmark.StartupBenchmark,io.github.fermin214.moerise.benchmark.StarBenchmark"
if [[ "$android_benchmark_mode" == profile ]]; then
    android_benchmark_variant=profile
    android_benchmark_class="io.github.fermin214.moerise.benchmark.BaselineProfileGenerator"
elif [[ "$android_benchmark_mode" == startup ]]; then
    android_benchmark_class="io.github.fermin214.moerise.benchmark.StartupBenchmark"
elif [[ "$android_benchmark_mode" == stars ]]; then
    android_benchmark_class="io.github.fermin214.moerise.benchmark.StarBenchmark"
fi
if [[ "$android_benchmark_variant" == benchmark ]]; then
    test -s mobile/androidApp/src/main/baselineProfiles/baseline-prof.txt || { echo "先运行 benchmark.sh profile 生成设备 profile。" >&2; exit 1; }
fi
python3 - <<'PY'
from pathlib import Path
import shutil
root = Path("mobile/benchmark/build/outputs")
for child in root.glob("*"):
    if child.is_dir() and (child.name.startswith("connected_android_test") or child.name == "androidTest-results"):
        shutil.rmtree(child)
PY
android_benchmark_cpu="${MOERISE_ANDROID_CPU:-$(python3 -c 'import os; print(min(os.sched_getaffinity(0)))')}"
taskset -c "$android_benchmark_cpu" nice -n 10 mobile/gradlew -p mobile --no-daemon --max-workers=1 \
    ":benchmark:connected${android_benchmark_variant^}AndroidTest" \
    "-Pandroid.testInstrumentationRunnerArguments.class=$android_benchmark_class" \
    | tee "$android_benchmark_output/run.log"
python3 - "$android_benchmark_output" "$android_benchmark_variant" <<'PY'
import hashlib, json, os, pathlib, shutil, subprocess, sys
output, variant = pathlib.Path(sys.argv[1]), sys.argv[2]
roots = pathlib.Path("mobile/benchmark/build/outputs")
copied = []
for source in roots.rglob("*"):
    if source.is_file() and any(part.startswith("connected_android_test") or part == "androidTest-results" for part in source.parts):
        target = output / "raw" / source.relative_to(roots)
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, target)
        copied.append(target)
def adb(*command):
    return subprocess.check_output(["adb", "-s", os.environ["ANDROID_SERIAL"], "shell", *command], text=True).strip()
apk = pathlib.Path(f"mobile/androidApp/build/outputs/apk/{variant}/androidApp-{variant}.apk")
shutil.copy2(apk, output / "target.apk")
metadata = dict(commit=subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(),
    dirty=bool(subprocess.check_output(["git", "status", "--porcelain"])), variant=variant,
    apk_sha256=hashlib.sha256(apk.read_bytes()).hexdigest(), model=adb("getprop", "ro.product.model"),
    android=adb("getprop", "ro.build.version.release"), api=adb("getprop", "ro.build.version.sdk"),
    abi=adb("getprop", "ro.product.cpu.abi"), page_size=adb("getconf", "PAGESIZE"),
    battery=adb("dumpsys", "battery"), thermal=adb("dumpsys", "thermalservice"),
    memory=adb("dumpsys", "meminfo", "io.github.fermin214.moerise.benchmark"),
    fixture_notes=100, network="adb reverse to isolated Core/Memos")
(output / "device.json").write_text(json.dumps(metadata, ensure_ascii=False, indent=2) + "\n")
if variant == "profile":
    for kind in ("baseline", "startup"):
        profiles = [p for p in copied if p.name.endswith(f"-{kind}-prof.txt")]
        assert profiles, f"没有设备生成的 {kind} profile"
        rules = sorted({line for p in profiles for line in p.read_text().splitlines() if line.strip()})
        assert rules, "profile 规则为空"
        target = pathlib.Path(f"mobile/androidApp/src/main/baselineProfiles/{kind}-prof.txt")
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text("\n".join(rules) + "\n")
        print(f"{target}: {len(rules)} 条设备规则")
PY
if [[ "$android_benchmark_variant" == benchmark ]]; then
    python3 scripts/android/benchmark-report.py "$android_benchmark_output"
fi
echo "$android_benchmark_output"
