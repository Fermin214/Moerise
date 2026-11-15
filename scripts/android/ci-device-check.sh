#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
mkdir -p .local/android
export MOERISE_ANDROID_FIXTURE_ROOT
MOERISE_ANDROID_FIXTURE_ROOT="$(mktemp -d "$android_repo_root/.local/android/device-XXXXXXXX")"
export MOERISE_ANDROID_FIXTURE="$MOERISE_ANDROID_FIXTURE_ROOT/connection.json"
export ANDROID_SERIAL="${ANDROID_SERIAL:-emulator-5554}"
case "${MOERISE_ANDROID_SUITE:-all}" in
    all)
        android_device_classes=io.github.fermin214.moerise.ImeLifecycleTest,io.github.fermin214.moerise.QuickNoteTest
        android_device_recovery=1 ;;
    ime)
        android_device_classes=io.github.fermin214.moerise.ImeLifecycleTest
        android_device_recovery=0 ;;
    editing|search|actions|insights)
        case "$MOERISE_ANDROID_SUITE" in
            editing) android_device_method=loginWriteRecreateSaveAndReadBack ;;
            search) android_device_method=searchKeywordHybridAndReadingStayOnHome ;;
            actions) android_device_method=archiveRestoreAndDeletePreserveUnfinishedInput ;;
            insights) android_device_method=insightsPickerAndSourceReadingReturnToHome ;;
        esac
        android_device_classes="io.github.fermin214.moerise.QuickNoteTest#$android_device_method"
        android_device_recovery=0 ;;
    recovery)
        android_device_classes=io.github.fermin214.moerise.QuickNoteTest#loginWriteRecreateSaveAndReadBack
        android_device_recovery=1
        export MOERISE_ANDROID_VISUAL_SETUP=true ;;
    *) echo "未知设备检查组：$MOERISE_ANDROID_SUITE" >&2; exit 1 ;;
esac
export MOERISE_ANDROID_TEST_CLASS="${MOERISE_ANDROID_TEST_CLASS:-$android_device_classes}"
export MOERISE_ANDROID_RECOVERY="${MOERISE_ANDROID_RECOVERY:-$android_device_recovery}"
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
