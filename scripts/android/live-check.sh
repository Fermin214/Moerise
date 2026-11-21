#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
mkdir -p .local/android
android_live_root="$(mktemp -d "$android_repo_root/.local/android/live-XXXXXXXX")"
export MOERISE_ANDROID_FIXTURE_ROOT="$android_live_root"
export MOERISE_ANDROID_FIXTURE="$android_live_root/connection.json"
setsid scripts/android/start-fixture.sh > "$android_live_root/fixture.log" 2>&1 &
android_live_pid=$!
cleanup_android_live() {
    kill -TERM -- "-$android_live_pid" 2>/dev/null || true
    wait "$android_live_pid" 2>/dev/null || true
}
trap cleanup_android_live EXIT
python3 - "$MOERISE_ANDROID_FIXTURE" "$android_live_pid" <<'PY'
from pathlib import Path
import os, sys, time
path, pid = Path(sys.argv[1]), int(sys.argv[2])
end = time.monotonic() + 180
while not path.exists():
    try: os.kill(pid, 0)
    except ProcessLookupError: raise SystemExit('fixture 启动失败，查看私有目录中的 fixture.log')
    if time.monotonic() >= end: raise SystemExit('fixture 启动超时')
    time.sleep(1)
PY
if [[ "${MOERISE_ANDROID_SEED_BENCHMARK:-0}" == 1 ]]; then node scripts/android/seed-benchmark.mjs; fi
android_live_task=coreIntegrationTest
if [[ "${MOERISE_ANDROID_STARS:-0}" == 1 ]]; then android_live_task=starsIntegrationTest; fi
android_gradle ":androidApp:$android_live_task" "$@"
