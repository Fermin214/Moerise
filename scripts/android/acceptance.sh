#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
android_fixture_connection="${MOERISE_ANDROID_FIXTURE:?设置 MOERISE_ANDROID_FIXTURE 为隔离环境连接档案}"
test -f "$android_fixture_connection"
android_device="${ANDROID_SERIAL:?设置 ANDROID_SERIAL 为用于验收的设备}"
if [[ ! "$android_device" =~ ^emulator-[0-9]+$ ]]; then
    echo "安装和清空数据只用于隔离模拟器；真机使用 scripts/android/phone-session.py。" >&2
    exit 1
fi
android_acceptance_ui_text="$(python3 -c 'import json; print("\n".join(json.load(open("scripts/android/maestro/catalog.json"))["groups"]))')"
mapfile -t android_acceptance_ui <<< "$android_acceptance_ui_text"
android_acceptance_specialists=(lifecycle ime recovery stars)
android_acceptance_needs_tests=0
android_acceptance_suite="${MOERISE_ANDROID_SUITE:-all}"
if [[ -n "${MOERISE_ANDROID_TEST_CLASS:-}" ]]; then
    if [[ -n "${MOERISE_ANDROID_SUITE:-}" && "$android_acceptance_suite" != diagnostic ]]; then
        echo "手动类筛选请单独使用 MOERISE_ANDROID_SUITE=diagnostic，不能与业务组叠加。" >&2
        exit 1
    fi
    android_acceptance_suite=diagnostic
fi
case "$android_acceptance_suite" in
    diagnostic)
        IFS=',' read -r -a android_acceptance_groups <<< "${MOERISE_ANDROID_TEST_CLASS:?手动诊断需要显式类或方法}"
        for android_acceptance_class in "${android_acceptance_groups[@]}"; do
            if [[ ! "$android_acceptance_class" =~ ^io\.github\.fermin214\.moerise\.[A-Za-z][A-Za-z0-9_]*(#[A-Za-z][A-Za-z0-9_]*)?$ ]]; then
                echo "诊断类需要完整包名和可选方法名：$android_acceptance_class" >&2
                exit 1
            fi
        done
        android_acceptance_needs_tests=1 ;;
    *)
        android_acceptance_selected="$(python3 scripts/android/device-suites.py "$android_acceptance_suite")"
        mapfile -t android_acceptance_groups <<< "$android_acceptance_selected"
        for android_acceptance_group in "${android_acceptance_groups[@]}"; do
            if [[ " ${android_acceptance_specialists[*]} " == *" $android_acceptance_group "* ]]; then
                android_acceptance_needs_tests=1
            fi
        done ;;
esac
for android_acceptance_group in "${android_acceptance_groups[@]}"; do
if [[ " ${android_acceptance_ui[*]} " == *" $android_acceptance_group "* ]]; then
    test -f scripts/android/maestro-run.py
    command -v maestro >/dev/null
fi
done
adb -s "$android_device" get-state >/dev/null
if [[ "${MOERISE_ANDROID_SKIP_BUILD:-0}" != 1 ]]; then
    android_acceptance_cpu="${MOERISE_ANDROID_CPU:-$(python3 -c 'import os; print(min(os.sched_getaffinity(0)))')}"
    taskset -c "$android_acceptance_cpu" nice -n 10 mobile/gradlew -p mobile --no-daemon --max-workers=1 \
        :androidApp:assembleFixture :androidApp:assembleFixtureAndroidTest
    python3 scripts/android/fixture-artifacts.py write
fi
if [[ "$android_acceptance_needs_tests" == 1 ]]; then
    python3 scripts/android/fixture-artifacts.py verify
else
    python3 scripts/android/fixture-artifacts.py verify --target-only
fi
mkdir -p .local/android/device-instrumentation .local/android/maestro
: > .local/android/device-acceptance.log
printf 'group\tstatus\tseconds\n' > .local/android/device-results.tsv
android_fixture_port="$(python3 -c 'import json,sys,urllib.parse; print(urllib.parse.urlparse(json.load(open(sys.argv[1]))["origin"]).port)' "$android_fixture_connection")"
cleanup_android_device_fixture() {
    if timeout 30s adb -s "$android_device" exec-out run-as io.github.fermin214.moerise.fixture \
        tar -C cache -cf - acceptance-diagnostics > .local/android/device-diagnostics.tar 2>/dev/null; then
        timeout 30s adb -s "$android_device" shell run-as io.github.fermin214.moerise.fixture \
            rm -r cache/acceptance-diagnostics >/dev/null 2>&1 || true
    else
        rm -f .local/android/device-diagnostics.tar
    fi
    timeout 30s adb -s "$android_device" shell am force-stop io.github.fermin214.moerise.fixture >/dev/null 2>&1 || true
    timeout 30s adb -s "$android_device" shell rm -f /data/local/tmp/moerise-android-fixture.json >/dev/null 2>&1 || true
    timeout 30s adb -s "$android_device" reverse --remove "tcp:$android_fixture_port" >/dev/null 2>&1 || true
}
trap cleanup_android_device_fixture EXIT
adb -s "$android_device" reverse "tcp:$android_fixture_port" "tcp:$android_fixture_port"
adb -s "$android_device" install -r -d -t mobile/androidApp/build/outputs/apk/fixture/androidApp-fixture.apk
if [[ "$android_acceptance_needs_tests" == 1 ]]; then
    adb -s "$android_device" install -r -t mobile/androidApp/build/outputs/apk/androidTest/fixture/androidApp-fixture-androidTest.apk
fi
adb -s "$android_device" shell pm clear io.github.fermin214.moerise.fixture >/dev/null
run_android_instrumentation() {
    local android_class="$1" android_log="$2" android_visual="${3:-false}" android_privacy_upgrade=false
    if [[ -n "${MOERISE_ANDROID_UPGRADE_APK:-}" ]]; then android_privacy_upgrade=true; fi
    for android_driver in dev.mobile.maestro dev.mobile.maestro.test; do
        timeout 30s adb -s "$android_device" shell am force-stop "$android_driver" || return 1
    done
    # 只把登录字段传入设备，Memos 来源令牌留在主机。
    if ! python3 - "$android_fixture_connection" <<'PY' | adb -s "$android_device" shell 'cat > /data/local/tmp/moerise-android-fixture.json'
import json, sys
with open(sys.argv[1], encoding="utf-8-sig") as source:
    connection = json.load(source)
print(json.dumps({key: connection[key] for key in ("origin", "username", "password")}))
PY
    then
        return 1
    fi
    # 每组使用新进程，避免 Activity 和测试输入会话跨 instrumentation 复用。
    timeout 30s adb -s "$android_device" shell am force-stop io.github.fermin214.moerise.fixture || return 1
    : > "$android_log"
    if ! timeout --signal=TERM --kill-after=15s 600s adb -s "$android_device" shell am instrument -w \
        -e timeout_msec 300000 -e visualSetup "$android_visual" -e privacyUpgrade "$android_privacy_upgrade" -e class "$android_class" \
        io.github.fermin214.moerise.fixture.test/androidx.test.runner.AndroidJUnitRunner |
        tee -a .local/android/device-acceptance.log "$android_log"; then
        return 1
    fi
    grep -Eq '^OK \([1-9][0-9]* tests?\)' "$android_log" &&
        ! grep -Eq 'FAILURES!!!|INSTRUMENTATION_FAILED' "$android_log"
}
android_acceptance_failed=0
for android_acceptance_group in "${android_acceptance_groups[@]}"; do
    android_acceptance_started=$SECONDS
    android_acceptance_status=passed
    android_acceptance_log=".local/android/device-instrumentation/$android_acceptance_group.log"
    if [[ " ${android_acceptance_ui[*]} " == *" $android_acceptance_group "* ]]; then
        android_maestro_output="$android_repo_root/.local/android/maestro/$android_acceptance_group"
        mkdir -p "$android_maestro_output"
        rm -f "$android_maestro_output/results.json"
        android_maestro_command_failed=0
        if ! python3 scripts/android/maestro-run.py --serial "$android_device" --adb adb \
            --fixture "$android_fixture_connection" --output "$android_maestro_output" \
            --group "$android_acceptance_group" --source-root "$android_repo_root" 2>&1 |
            tee -a .local/android/device-acceptance.log "$android_maestro_output/host.log"; then
            android_maestro_command_failed=1
        fi
        if ! android_acceptance_status="$(python3 - "$android_maestro_output/results.json" "$android_maestro_command_failed" <<'PY'
import json, sys
from pathlib import Path

priority = ("environment_blocked", "harness_error", "inconclusive", "failed", "passed")
try:
    results = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8-sig"))
    if not isinstance(results, dict) or not results:
        raise ValueError("Empty or invalid Maestro results")
    statuses = {result["status"] for result in results.values()}
    if not statuses <= set(priority):
        raise ValueError("Unknown Maestro result status")
    status = next(value for value in priority if value in statuses)
except (OSError, ValueError, KeyError, TypeError):
    status = "harness_error"
if sys.argv[2] == "1" and status == "passed":
    status = "harness_error"
print(status)
PY
        )"; then
            android_acceptance_status=harness_error
        fi
    else
        case "$android_acceptance_group" in
        lifecycle)
            if ! run_android_instrumentation 'io.github.fermin214.moerise.QuickNoteTest#loginWriteRecreateSaveAndReadBack' "$android_acceptance_log" ||
                ! node scripts/android/seed-timeline.mjs "$android_fixture_connection" ||
                ! run_android_instrumentation 'io.github.fermin214.moerise.QuickNoteTest#editThirtiethNoteInPlace' "${android_acceptance_log%.log}-position.log"; then
                android_acceptance_status=failed
            fi ;;
        ime|stars)
            android_acceptance_class=io.github.fermin214.moerise.ImeLifecycleTest
            if [[ "$android_acceptance_group" == stars ]]; then android_acceptance_class=io.github.fermin214.moerise.StarRenderingTest; fi
            if ! run_android_instrumentation "$android_acceptance_class" "$android_acceptance_log"; then
                android_acceptance_status=failed
            fi ;;
        recovery)
            if ! run_android_instrumentation 'io.github.fermin214.moerise.QuickNoteTest#loginWriteRecreateSaveAndReadBack' "$android_acceptance_log" true; then
                android_acceptance_status=failed
            elif ! python3 scripts/android/device-recovery.py --serial "$android_device" --adb adb \
                --fixture "$android_fixture_connection" --source-root "$android_repo_root" \
                --output "$android_repo_root/.local/android/device-recovery" 2>&1 |
                tee -a .local/android/device-acceptance.log "$android_acceptance_log"; then
                android_acceptance_status=failed
            fi ;;
        *)
            if ! run_android_instrumentation "$android_acceptance_group" "$android_acceptance_log" "${MOERISE_ANDROID_VISUAL_SETUP:-false}"; then
                android_acceptance_status=failed
            fi ;;
        esac
    fi
    if [[ "$android_acceptance_status" != passed ]]; then android_acceptance_failed=1; fi
    printf '%s\t%s\t%s\n' "$android_acceptance_group" "$android_acceptance_status" \
        "$((SECONDS - android_acceptance_started))" >> .local/android/device-results.tsv
    if [[ "$android_acceptance_status" == environment_blocked ]]; then break; fi
done
if [[ "$(wc -l < .local/android/device-results.tsv)" -lt 2 ]]; then
    echo '设备流程没有执行测试' >&2
    exit 1
fi
cat .local/android/device-results.tsv
exit "$android_acceptance_failed"
