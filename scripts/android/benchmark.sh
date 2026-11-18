#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
android_benchmark_mode="${1:-run}"
case "$android_benchmark_mode" in profile|run|startup|startup-without-profile|stars) ;; *) echo "用法：benchmark.sh [profile|run|startup|startup-without-profile|stars]" >&2; exit 1 ;; esac
: "${ANDROID_SERIAL:?设置用于性能测量的设备序列号}"
: "${MOERISE_ANDROID_FIXTURE:?设置隔离服务连接档案路径}"
android_benchmark_variant=benchmark
android_benchmark_class="io.github.fermin214.moerise.benchmark.StartupBenchmark,io.github.fermin214.moerise.benchmark.StarBenchmark"
if [[ "$android_benchmark_mode" == profile ]]; then
    android_benchmark_variant=profile
    android_benchmark_class="io.github.fermin214.moerise.benchmark.BaselineProfileGenerator"
elif [[ "$android_benchmark_mode" == startup ]]; then
    android_benchmark_class="io.github.fermin214.moerise.benchmark.StartupBenchmark"
elif [[ "$android_benchmark_mode" == startup-without-profile ]]; then
    android_benchmark_class="io.github.fermin214.moerise.benchmark.StartupBenchmark#coldWithoutProfile,io.github.fermin214.moerise.benchmark.StartupBenchmark#foregroundWithoutProfile"
elif [[ "$android_benchmark_mode" == stars ]]; then
    android_benchmark_class="io.github.fermin214.moerise.benchmark.StarBenchmark"
fi
if [[ "$android_benchmark_variant" == benchmark && "$android_benchmark_mode" != startup-without-profile ]]; then
    test -s mobile/androidApp/src/main/baselineProfiles/baseline-prof.txt || { echo "先运行 benchmark.sh profile 生成设备 profile。" >&2; exit 1; }
fi
[[ "$(adb -s "$ANDROID_SERIAL" get-state)" == device ]]
if [[ "$ANDROID_SERIAL" == emulator-* ]] || adb devices | awk 'NR>1 {print $1}' | grep -q '^emulator-'; then
    echo "性能测量只使用真机；先关闭所有模拟器。" >&2
    exit 1
fi
android_benchmark_min_api="$(sed -n 's/^moerise\.minSdk=//p' mobile/gradle.properties)"
[[ "$android_benchmark_min_api" =~ ^[1-9][0-9]*$ ]]
if [[ "$android_benchmark_mode" == profile && "$android_benchmark_min_api" -lt 33 ]]; then android_benchmark_min_api=33; fi
[[ "$(adb -s "$ANDROID_SERIAL" shell getprop ro.build.version.sdk | tr -d '\r')" -ge "$android_benchmark_min_api" ]]
android_benchmark_output=".local/android/benchmark-$(git rev-parse --short HEAD)-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$android_benchmark_output"
android_benchmark_port="$(python3 -c 'import json,os,urllib.parse; print(urllib.parse.urlparse(json.load(open(os.environ["MOERISE_ANDROID_FIXTURE"]))["origin"]).port or 80)')"
android_benchmark_reverse_added=false
android_benchmark_build_started=false
android_benchmark_gradle_succeeded=false
android_benchmark_credentials=""
cleanup_android_benchmark() {
    local result=$?
    trap - EXIT
    set +e
    adb -s "$ANDROID_SERIAL" shell rm -f /data/local/tmp/moerise-android-fixture.json >/dev/null 2>&1
    if [[ $? != 0 ]]; then result=1; fi
    if [[ -n "$android_benchmark_credentials" ]]; then rm -f -- "$android_benchmark_credentials"; fi
    if [[ "$android_benchmark_reverse_added" == true ]]; then
        adb -s "$ANDROID_SERIAL" reverse --remove "tcp:$android_benchmark_port" >/dev/null 2>&1
        if [[ $? != 0 ]]; then result=1; fi
    fi
    python3 - "$android_benchmark_output" "$result" "$android_benchmark_build_started" "$android_benchmark_gradle_succeeded" <<'PY'
from pathlib import Path
import datetime, json, shutil, sys
output, result, started, succeeded = Path(sys.argv[1]), int(sys.argv[2]), sys.argv[3], sys.argv[4]
build = Path("mobile/benchmark/build")
if result and started == "true":
    directories = [path for path in (build / "outputs").glob("*")
                   if path.is_dir() and (path.name.startswith("connected_android_test") or path.name == "androidTest-results")]
    directories.append(build / "reports/androidTests")
    for directory in directories:
        for path in directory.rglob("*"):
            if path.is_file():
                target = output / "raw" / path.relative_to(build)
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(path, target)
metadata_path = output / "device.json"
metadata = json.loads(metadata_path.read_text()) if metadata_path.exists() else {}
metadata.update(status="failed" if result else "succeeded", gradle_succeeded=succeeded == "true",
                finished_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
metadata_path.write_text(json.dumps(metadata, ensure_ascii=False, indent=2) + "\n")
PY
    if [[ $? != 0 ]]; then result=1; fi
    exit "$result"
}
trap cleanup_android_benchmark EXIT
node scripts/android/seed-benchmark.mjs | tee "$android_benchmark_output/seed.log"
if ! adb -s "$ANDROID_SERIAL" reverse --list | tr -d '\r' | grep -Eq "^[^ ]+ tcp:$android_benchmark_port tcp:$android_benchmark_port$"; then
    android_benchmark_reverse_added=true
fi
adb -s "$ANDROID_SERIAL" reverse "tcp:$android_benchmark_port" "tcp:$android_benchmark_port"
if adb -s "$ANDROID_SERIAL" shell pm list packages io.github.fermin214.moerise.benchmark | tr -d '\r' | grep -qx 'package:io.github.fermin214.moerise.benchmark'; then
    [[ "$(adb -s "$ANDROID_SERIAL" shell pm clear io.github.fermin214.moerise.benchmark | tr -d '\r')" == Success ]]
fi
android_benchmark_credentials="$(mktemp)"
python3 - "$android_benchmark_credentials" <<'PY'
import json, os, pathlib, sys
config = json.loads(pathlib.Path(os.environ["MOERISE_ANDROID_FIXTURE"]).read_text())
pathlib.Path(sys.argv[1]).write_text(json.dumps({key: config[key] for key in ("origin", "username", "password")}))
PY
adb -s "$ANDROID_SERIAL" push "$android_benchmark_credentials" /data/local/tmp/moerise-android-fixture.json >/dev/null
python3 - <<'PY'
from pathlib import Path
import shutil
root = Path("mobile/benchmark/build/outputs")
for child in root.glob("*"):
    if child.is_dir() and (child.name.startswith("connected_android_test") or child.name == "androidTest-results"):
        shutil.rmtree(child)
reports = Path("mobile/benchmark/build/reports/androidTests")
if reports.exists():
    shutil.rmtree(reports)
PY
android_benchmark_cpu="${MOERISE_ANDROID_CPU:-$(python3 -c 'import os; print(min(os.sched_getaffinity(0)))')}"
android_benchmark_build_started=true
taskset -c "$android_benchmark_cpu" nice -n 10 mobile/gradlew -p mobile --no-daemon --max-workers=1 \
    --no-parallel --console=plain \
    ":androidApp:assemble${android_benchmark_variant^}" ":benchmark:assemble${android_benchmark_variant^}" \
    | tee "$android_benchmark_output/run.log"
android_benchmark_gradle_succeeded=true
python3 scripts/android/benchmark-device.py --serial "$ANDROID_SERIAL" --variant "$android_benchmark_variant" \
    --classes "$android_benchmark_class" --output "$android_benchmark_output"
python3 - "$android_benchmark_output" "$android_benchmark_variant" "$android_benchmark_mode" <<'PY'
import hashlib, json, os, pathlib, shutil, subprocess, sys
output, variant, mode = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
build = pathlib.Path("mobile/benchmark/build")
copied = []
directories = [p for p in (build / "outputs").glob("*")
               if p.is_dir() and (p.name.startswith("connected_android_test") or p.name == "androidTest-results")]
directories.append(build / "reports/androidTests")
for directory in directories:
    for source in directory.rglob("*"):
        if source.is_file():
            target = output / "raw" / source.relative_to(build)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, target)
            copied.append(target)
def adb(*command):
    return subprocess.check_output(["adb", "-s", os.environ["ANDROID_SERIAL"], "shell", *command], text=True).strip()
apk = pathlib.Path(f"mobile/androidApp/build/outputs/apk/{variant}/androidApp-{variant}.apk")
shutil.copy2(apk, output / "target.apk")
metadata = dict(commit=subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(),
    dirty=bool(subprocess.check_output(["git", "status", "--porcelain"])), variant=variant, mode=mode,
    serial=os.environ["ANDROID_SERIAL"], gradle_succeeded=True, execution="adb",
    profile_mode="without" if mode == "startup-without-profile" else "compare",
    apk_sha256=hashlib.sha256(apk.read_bytes()).hexdigest(), model=adb("getprop", "ro.product.model"),
    android=adb("getprop", "ro.build.version.release"), api=adb("getprop", "ro.build.version.sdk"),
    abi=adb("getprop", "ro.product.cpu.abi"), page_size=adb("getconf", "PAGESIZE"),
    battery=adb("dumpsys", "battery"), thermal=adb("dumpsys", "thermalservice"),
    memory=adb("dumpsys", "meminfo", "io.github.fermin214.moerise.benchmark"),
    fixture_notes=100, cached_notes=0 if mode == "stars" else 20,
    cached_long_note_bytes=0 if mode == "stars" else 65536,
    cached_long_note_attachments=0 if mode == "stars" else 10, network="adb reverse to isolated Core/Memos")
runner_directory = build / "outputs/apk" / variant
runner_output = json.loads((runner_directory / "output-metadata.json").read_text())
assert len(runner_output["elements"]) == 1, "性能 runner 必须输出单个 APK"
runner = (runner_directory / runner_output["elements"][0]["outputFile"]).resolve()
assert runner.parent == runner_directory.resolve() and runner.suffix == ".apk", "性能 runner APK 路径无效"
shutil.copy2(runner, output / "runner.apk")
metadata["runner_apk_sha256"] = hashlib.sha256(runner.read_bytes()).hexdigest()
(output / "device.json").write_text(json.dumps(metadata, ensure_ascii=False, indent=2) + "\n")
verify = [sys.executable, "scripts/android/verify-apk.py"]
subprocess.run(verify + [str(output / "target.apk"), "--release", "--package", "io.github.fermin214.moerise.benchmark"] +
               (["--require-profile"] if variant == "benchmark" and mode != "startup-without-profile" else []), check=True,
               stdout=(output / "target-verification.txt").open("w"))
subprocess.run(verify + [str(output / "runner.apk"), "--package", "io.github.fermin214.moerise.benchmark.runner",
                        "--same-signer-as", str(output / "target.apk")], check=True,
               stdout=(output / "runner-verification.txt").open("w"))
if variant == "profile":
    for kind in ("baseline", "startup"):
        profiles = list((output / "raw").rglob(f"*-{kind}-prof.txt"))
        if kind == "baseline":
            profiles += list((output / "raw").rglob("*-startup-prof.txt"))
        assert profiles, f"没有设备生成的 {kind} profile"
        rules = sorted({line for p in profiles for line in p.read_text().splitlines() if line.strip()})
        assert rules, "profile 规则为空"
        target = pathlib.Path(f"mobile/androidApp/src/main/baselineProfiles/{kind}-prof.txt")
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text("\n".join(rules) + "\n")
        print(f"{target}: {len(rules)} 条设备规则")
PY
if [[ "$android_benchmark_variant" == benchmark ]]; then
    android_benchmark_report_mode="${android_benchmark_mode/startup-without-profile/startup}"
    python3 scripts/android/benchmark-report.py "$android_benchmark_output" --mode "$android_benchmark_report_mode"
fi
echo "$android_benchmark_output"
