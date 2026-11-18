#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
mkdir -p .local/android
export ANDROID_SERIAL="${ANDROID_SERIAL:-emulator-5554}"
if [[ ! "$ANDROID_SERIAL" =~ ^emulator-[0-9]+$ ]]; then
    echo "此入口只用于隔离模拟器；真机使用 scripts/android/phone.ps1。" >&2
    exit 1
fi
export MOERISE_ANDROID_SUITE="${MOERISE_ANDROID_SUITE:-all}"
python3 - <<'PY'
import json, os
from pathlib import Path
groups = json.loads(Path('scripts/android/maestro/catalog.json').read_text())['groups']
if os.environ['MOERISE_ANDROID_SUITE'] not in [*groups, 'all', 'ui', 'lifecycle', 'ime', 'recovery', 'stars']:
    raise SystemExit('未知设备检查组：' + os.environ['MOERISE_ANDROID_SUITE'])
PY
run_device_check() (
    export MOERISE_ANDROID_FIXTURE_ROOT
    MOERISE_ANDROID_FIXTURE_ROOT="$(mktemp -d "$android_repo_root/.local/android/device-XXXXXXXX")" || exit 1
    export MOERISE_ANDROID_FIXTURE="$MOERISE_ANDROID_FIXTURE_ROOT/connection.json"
    setsid scripts/android/start-fixture.sh > "$MOERISE_ANDROID_FIXTURE_ROOT/fixture.log" 2>&1 &
    fixture_pid=$!
    cleanup_fixture() {
        kill -TERM -- "-$fixture_pid" 2>/dev/null || true
        wait "$fixture_pid" 2>/dev/null || true
        for log in fixture core; do
            if [[ -f "$MOERISE_ANDROID_FIXTURE_ROOT/$log.log" ]]; then
                cp "$MOERISE_ANDROID_FIXTURE_ROOT/$log.log" ".local/android/device-fixture-$log.log"
            fi
        done
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

if run_device_check; then exit 0; fi
if [[ "${MOERISE_ANDROID_RETRY_TRANSPORT:-0}" != 1 ]]; then exit 1; fi
python3 - "$MOERISE_ANDROID_SUITE" <<'PY' || exit 1
import json
from pathlib import Path
import sys

suite = sys.argv[1]
root = Path('.local/android')
groups = json.loads(Path('scripts/android/maestro/catalog.json').read_text())['groups']
if suite not in groups:
    raise SystemExit(1)
try:
    rows = (root / 'device-results.tsv').read_text().splitlines()
    group = root / 'maestro' / suite
    results = json.loads((group / 'results.json').read_text())
    transport = ('environment_blocked', 'maestro_driver_died')
    retries = [flow for flow, result in results.items()
               if (result['status'], result.get('cause')) == transport]
    if any((result['status'], result.get('cause')) not in {transport, ('passed', None)}
           for result in results.values()):
        raise SystemExit(1)
    for flow in retries:
        if flow not in groups[suite]:
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
except (OSError, ValueError, AttributeError, KeyError, TypeError):
    raise SystemExit(1)
if len(rows) != 2 or rows[1].split('\t')[:2] != [suite, 'environment_blocked']:
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
run_device_check
