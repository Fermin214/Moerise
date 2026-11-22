#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
mkdir -p .local/android
export ANDROID_SERIAL="${ANDROID_SERIAL:-emulator-5554}"
if [[ ! "$ANDROID_SERIAL" =~ ^emulator-[0-9]+$ ]]; then
    echo "此入口只用于隔离模拟器；真机使用 scripts/android/phone-session.py。" >&2
    exit 1
fi
export MOERISE_ANDROID_SUITE="${MOERISE_ANDROID_SUITE:-all}"
android_ci_selection="$(python3 scripts/android/device-suites.py "$MOERISE_ANDROID_SUITE")"
mapfile -t android_ci_suites <<< "$android_ci_selection"
run_device_check() (
    export MOERISE_ANDROID_FIXTURE_ROOT
    export MOERISE_ANDROID_FIXTURE_DISPOSE_ON_EXIT=1
    MOERISE_ANDROID_FIXTURE_ROOT="$(mktemp -d "$android_repo_root/.local/android/device-XXXXXXXX")" || exit 1
    export MOERISE_ANDROID_FIXTURE="$MOERISE_ANDROID_FIXTURE_ROOT/connection.json"
    setsid scripts/android/start-fixture.sh > "$MOERISE_ANDROID_FIXTURE_ROOT/fixture.log" 2>&1 &
    fixture_pid=$!
    cleanup_fixture() {
        local result=$?
        trap - EXIT
        kill -TERM -- "-$fixture_pid" 2>/dev/null || true
        if ! wait "$fixture_pid"; then result=1; fi
        for log in fixture core; do
            if [[ -f "$MOERISE_ANDROID_FIXTURE_ROOT/$log.log" ]]; then
                cp "$MOERISE_ANDROID_FIXTURE_ROOT/$log.log" ".local/android/device-fixture-$log.log" || result=1
                cp "$MOERISE_ANDROID_FIXTURE_ROOT/$log.log" ".local/android/device-fixture-$MOERISE_ANDROID_SUITE-$log.log" || result=1
            fi
        done
        exit "$result"
    }
    trap cleanup_fixture EXIT
    python3 - "$MOERISE_ANDROID_FIXTURE" "$fixture_pid" <<'PY' || exit 1
from pathlib import Path
import os, sys, time
path, pid = Path(sys.argv[1]), int(sys.argv[2])
end = time.monotonic() + 180
while not path.exists():
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        raise SystemExit("fixture 启动失败")
    if time.monotonic() >= end:
        raise SystemExit("fixture 启动超时")
    time.sleep(1)
PY
    scripts/android/acceptance.sh
)

wait_for_device() {
    local ready_since=-1 deadline=$((SECONDS + 30))
    while (( SECONDS < deadline )); do
        if [[ $(timeout 2s adb -s "$ANDROID_SERIAL" get-state 2>/dev/null) == device ]]; then
            if (( ready_since < 0 )); then ready_since=$SECONDS; fi
            if (( SECONDS - ready_since >= 5 )); then return 0; fi
        else
            ready_since=-1
        fi
        sleep 1
    done
    return 1
}

check_device_suite() (
export MOERISE_ANDROID_SUITE="$1"
if run_device_check; then exit 0; fi
if [[ "${MOERISE_ANDROID_RETRY_TRANSPORT:-0}" != 1 ]]; then exit 1; fi
python3 - "$MOERISE_ANDROID_SUITE" <<'PY' || exit 1
import json
from pathlib import Path
import subprocess
import sys

suite = sys.argv[1]
root = Path('.local/android')
groups = json.loads(Path('scripts/android/maestro/catalog.json').read_text())['groups']
try:
    selected = subprocess.check_output([sys.executable, 'scripts/android/device-suites.py', suite], text=True).splitlines()
    rows = [row.split('\t') for row in (root / 'device-results.tsv').read_text().splitlines()[1:]]
    if not rows or [row[0] for row in rows] != selected[:len(rows)] or rows[-1][1] != 'environment_blocked' or any(row[1] != 'passed' for row in rows[:-1]):
        raise SystemExit(1)
    failed = rows[-1][0]
    if failed not in groups:
        raise SystemExit(1)
    group = root / 'maestro' / failed
    results = json.loads((group / 'results.json').read_text())
    transport = ('environment_blocked', 'maestro_driver_died')
    retries = [flow for flow, result in results.items()
               if (result['status'], result.get('cause')) == transport]
    if any((result['status'], result.get('cause')) not in {transport, ('passed', None)}
           for result in results.values()):
        raise SystemExit(1)
    for flow in retries:
        if flow not in groups[failed]:
            raise SystemExit(1)
        reports = sorted((group / flow / 'artifacts').rglob('commands.json'))
        if not reports:
            raise SystemExit(1)
        # Maestro's completion screenshot can replace an earlier assertion in JUnit.
        for report in reports:
            for command in json.loads(report.read_text()):
                metadata = command.get('metadata', {})
                if metadata.get('status') not in ('FAILED', 'ERROR'):
                    continue
                message = metadata.get('error', {}).get('message', '')
                if 'Device server died during ' not in message and not (
                    '(host:transport:' in message and (': device offline' in message or ' not found' in message)
                ):
                    raise SystemExit(1)
except (OSError, ValueError, AttributeError, KeyError, IndexError, TypeError, subprocess.CalledProcessError):
    raise SystemExit(1)
if not retries:
    raise SystemExit(1)
PY
echo "Maestro 设备连接中断，等待 ADB 恢复后重跑 $MOERISE_ANDROID_SUITE。"
if ! wait_for_device; then
    echo 'ADB 未恢复稳定连接。' >&2
    exit 1
fi
first_attempt=$(mktemp -d .local/android/device-transport-XXXXXXXX)
for path in .local/android/device-results.tsv .local/android/device-acceptance.log \
    .local/android/device-diagnostics.tar .local/android/device-fixture-*.log \
    ".local/android/maestro/$MOERISE_ANDROID_SUITE"; do
    if [[ -e "$path" ]]; then mv "$path" "$first_attempt/"; fi
done
printf 'group\tstatus\tseconds\n' > .local/android/device-results.tsv
: > .local/android/device-acceptance.log
run_device_check

)
android_ci_summary=.local/android/device-results-combined.tsv
printf 'group\tstatus\tseconds\n' > "$android_ci_summary"
android_ci_failed=0
for android_ci_suite in "${android_ci_suites[@]}"; do
    android_ci_started=$SECONDS
    : > .local/android/device-results.tsv
    : > .local/android/device-acceptance.log
    android_ci_result=0
    if ! check_device_suite "$android_ci_suite"; then
        android_ci_result=1
        android_ci_failed=1
    fi
    android_ci_output=".local/android/device-flows/$android_ci_suite"
    mkdir -p "$android_ci_output"
    cp .local/android/device-results.tsv .local/android/device-acceptance.log "$android_ci_output/"
    if [[ -f .local/android/device-diagnostics.tar ]]; then
        mv .local/android/device-diagnostics.tar "$android_ci_output/"
    fi
    if [[ "$android_ci_result" == 1 ]] && awk -F '\t' 'NR > 1 && $2 != "passed" { exit 1 }' .local/android/device-results.tsv; then
        printf '%s\tharness_error\t%s\n' "$android_ci_suite" "$((SECONDS - android_ci_started))" >> "$android_ci_summary"
    elif [[ "$(wc -l < .local/android/device-results.tsv)" -ge 2 ]]; then
        tail -n +2 .local/android/device-results.tsv >> "$android_ci_summary"
    else
        printf '%s\tharness_error\t%s\n' "$android_ci_suite" "$((SECONDS - android_ci_started))" >> "$android_ci_summary"
    fi
done
mv "$android_ci_summary" .local/android/device-results.tsv
cat .local/android/device-results.tsv
exit "$android_ci_failed"
