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
cd mobile
android_live_cpu="${MOERISE_ANDROID_CPU:-$(python3 -c 'import os; print(min(os.sched_getaffinity(0)))')}"
taskset -c "$android_live_cpu" nice -n 10 ./gradlew --no-daemon --max-workers=1 :shared:coreIntegrationTest "$@"
