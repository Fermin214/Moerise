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
export MOERISE_ANDROID_FIXTURE_ROOT
MOERISE_ANDROID_FIXTURE_ROOT="$(mktemp -d "$android_repo_root/.local/android/device-XXXXXXXX")"
export MOERISE_ANDROID_FIXTURE="$MOERISE_ANDROID_FIXTURE_ROOT/connection.json"
setsid scripts/android/start-fixture.sh > "$MOERISE_ANDROID_FIXTURE_ROOT/fixture.log" 2>&1 &
android_device_fixture_pid=$!
cleanup_android_device_check() {
    kill -TERM -- "-$android_device_fixture_pid" 2>/dev/null || true
    wait "$android_device_fixture_pid" 2>/dev/null || true
    for android_fixture_log in fixture core; do
        if [[ -f "$MOERISE_ANDROID_FIXTURE_ROOT/$android_fixture_log.log" ]]; then
            cp "$MOERISE_ANDROID_FIXTURE_ROOT/$android_fixture_log.log" ".local/android/device-fixture-$android_fixture_log.log"
        fi
    done
}
trap cleanup_android_device_check EXIT
python3 - "$MOERISE_ANDROID_FIXTURE" "$android_device_fixture_pid" <<'PY'
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
