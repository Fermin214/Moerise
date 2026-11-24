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

probe_device() {
    timeout 10s adb -s "$ANDROID_SERIAL" shell pm path android | grep -q '^package:'
}

wait_for_device() {
    local ready_since=-1 deadline=$((SECONDS + 30))
    while (( SECONDS < deadline )); do
        if [[ $(timeout 2s adb -s "$ANDROID_SERIAL" get-state 2>/dev/null) == device ]]; then
            if (( ready_since < 0 )); then ready_since=$SECONDS; fi
            if (( SECONDS - ready_since >= 5 )); then probe_device; return $?; fi
        else
            ready_since=-1
        fi
        sleep 1
    done
    return 1
}

restore_device_fixture() {
    local port="$1" mapping
    if ! probe_device && ! wait_for_device; then return 1; fi
    timeout 30s adb -s "$ANDROID_SERIAL" shell am force-stop io.github.fermin214.moerise.fixture || return 1
    timeout 30s adb -s "$ANDROID_SERIAL" shell rm -f /data/local/tmp/moerise-android-fixture.json || return 1
    mapping=$(timeout 30s adb -s "$ANDROID_SERIAL" reverse --list) || return 1
    if awk -v port="tcp:$port" 'NF >= 2 && $(NF-1) == port && $NF == port { found = 1 } END { exit !found }' <<< "$mapping"; then
        timeout 30s adb -s "$ANDROID_SERIAL" reverse --remove "tcp:$port" || return 1
    fi
}

run_device_check() (
    local member="$1" cleanup_receipt="$2" fixture_port=""
    export MOERISE_ANDROID_SUITE="$member"
    if [[ "$member" != recovery ]]; then unset MOERISE_ANDROID_UPGRADE_APK; fi
    export MOERISE_ANDROID_FIXTURE_ROOT
    export MOERISE_ANDROID_FIXTURE_DISPOSE_ON_EXIT=1
    MOERISE_ANDROID_FIXTURE_ROOT="$(mktemp -d "$android_repo_root/.local/android/device-XXXXXXXX")" || exit 1
    export MOERISE_ANDROID_FIXTURE="$MOERISE_ANDROID_FIXTURE_ROOT/connection.json"
    setsid scripts/android/start-fixture.sh > "$MOERISE_ANDROID_FIXTURE_ROOT/fixture.log" 2>&1 &
    fixture_pid=$!
    cleanup_fixture() {
        local result=$? restored=true
        trap - EXIT
        kill -TERM "$fixture_pid" 2>/dev/null || true
        if ! wait "$fixture_pid"; then restored=false; fi
        if [[ -n "$fixture_port" ]] && ! restore_device_fixture "$fixture_port"; then restored=false; fi
        for log in fixture core; do
            if [[ -f "$MOERISE_ANDROID_FIXTURE_ROOT/$log.log" ]]; then
                cp "$MOERISE_ANDROID_FIXTURE_ROOT/$log.log" ".local/android/device-fixture-$log.log" || result=1
                cp "$MOERISE_ANDROID_FIXTURE_ROOT/$log.log" ".local/android/device-fixture-$member-$log.log" || result=1
            fi
        done
        printf '%s\n' "$restored" > "$cleanup_receipt" || exit 1
        if [[ "$restored" != true ]]; then result=1; fi
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
    fixture_port="$(python3 -c 'import json,sys,urllib.parse; print(urllib.parse.urlparse(json.load(open(sys.argv[1]))["origin"]).port)' \
        "$MOERISE_ANDROID_FIXTURE")" || exit 1
    scripts/android/acceptance.sh
)

check_device_member() {
    local suite="$1" member="$2" attempt decision attempt_output cleanup_receipt path
    local -a flags
    for attempt in 1 2; do
        attempt_output=".local/android/device-flows/$member/attempt-$attempt"
        mkdir -p "$attempt_output" || return 1
        cleanup_receipt="$attempt_output/fixture-restored"
        rm -f "$cleanup_receipt" || return 1
        flags=()
        if [[ "${MOERISE_ANDROID_RETRY_TRANSPORT:-0}" == 1 ]]; then flags+=(--retry-transport); fi
        : > .local/android/device-results.tsv
        : > .local/android/device-acceptance.log
        rm -f .local/android/device-fixture-{core,fixture}.log .local/android/device-diagnostics.tar || return 1
        if [[ "$attempt" == 1 ]]; then rm -rf ".local/android/maestro/$member" || return 1; fi
        if wait_for_device; then
            flags+=(--adb-probe-ok)
            if run_device_check "$member" "$cleanup_receipt"; then flags+=(--passed); fi
            if [[ -f "$cleanup_receipt" && $(< "$cleanup_receipt") == true ]]; then flags+=(--fixture-restored); fi
        else
            echo "ADB 设备探针失败：$member（尝试 $attempt）" >&2
            flags+=(--fixture-restored)
        fi
        decision=$(python3 -B scripts/android/device_transport.py .local/android "$suite" "$member" "$attempt" "${flags[@]}") || return 1
        for path in .local/android/device-results.tsv .local/android/device-acceptance.log \
            .local/android/device-diagnostics.tar .local/android/device-fixture-{core,fixture}.log; do
            if [[ -f "$path" ]]; then cp "$path" "$attempt_output/" || return 1; fi
        done
        rm -f "$cleanup_receipt" || return 1
        case "$decision" in
            passed) return 0 ;;
            retry)
                mv ".local/android/maestro/$member" "$attempt_output/maestro" || return 1
                echo "Maestro 设备连接中断，等待 ADB 恢复后重跑 $member。"
                ;;
            *) return 1 ;;
        esac
    done
    return 1
}
android_ci_summary=.local/android/device-results-combined.tsv
printf 'group\tstatus\tseconds\n' > "$android_ci_summary"
android_ci_failed=0
for android_ci_suite in "${android_ci_suites[@]}"; do
    android_ci_started=$SECONDS
    android_ci_result=0
    if ! check_device_member "$MOERISE_ANDROID_SUITE" "$android_ci_suite"; then
        android_ci_result=1
        android_ci_failed=1
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
