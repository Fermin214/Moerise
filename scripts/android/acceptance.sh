#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
android_fixture_connection="${MOERISE_ANDROID_FIXTURE:?设置 MOERISE_ANDROID_FIXTURE 为隔离环境连接档案}"
test -f "$android_fixture_connection"
android_device="${ANDROID_SERIAL:?设置 ANDROID_SERIAL 为用于验收的设备}"
adb -s "$android_device" get-state >/dev/null
if [[ "${MOERISE_ANDROID_SKIP_BUILD:-0}" == 1 ]]; then
    python3 scripts/android/fixture-artifacts.py verify
else
    android_acceptance_cpu="${MOERISE_ANDROID_CPU:-$(python3 -c 'import os; print(min(os.sched_getaffinity(0)))')}"
    taskset -c "$android_acceptance_cpu" nice -n 10 mobile/gradlew -p mobile --no-daemon --max-workers=1 \
        :androidApp:assembleFixture :androidApp:assembleFixtureAndroidTest
    python3 scripts/android/fixture-artifacts.py write
fi
android_fixture_port="$(python3 -c 'import json,sys,urllib.parse; print(urllib.parse.urlparse(json.load(open(sys.argv[1]))["origin"]).port)' "$android_fixture_connection")"
adb -s "$android_device" reverse "tcp:$android_fixture_port" "tcp:$android_fixture_port"
adb -s "$android_device" install -r -t mobile/androidApp/build/outputs/apk/fixture/androidApp-fixture.apk
adb -s "$android_device" install -r -t mobile/androidApp/build/outputs/apk/androidTest/fixture/androidApp-fixture-androidTest.apk
adb -s "$android_device" shell pm clear io.github.fermin214.moerise.fixture >/dev/null
cleanup_android_device_fixture() {
    if timeout 30s adb -s "$android_device" exec-out run-as io.github.fermin214.moerise.fixture \
        tar -C cache -cf - acceptance-diagnostics > .local/android/device-diagnostics.tar 2>/dev/null; then
        timeout 30s adb -s "$android_device" shell run-as io.github.fermin214.moerise.fixture \
            rm -r cache/acceptance-diagnostics >/dev/null 2>&1 || true
    else
        rm .local/android/device-diagnostics.tar
    fi
    timeout 30s adb -s "$android_device" shell am force-stop io.github.fermin214.moerise.fixture >/dev/null 2>&1 || true
    timeout 30s adb -s "$android_device" shell rm -f /data/local/tmp/moerise-android-fixture.json >/dev/null 2>&1 || true
    if [[ -f .local/android/device-acceptance-phase.log ]]; then rm .local/android/device-acceptance-phase.log; fi
}
trap cleanup_android_device_fixture EXIT
adb -s "$android_device" push "$android_fixture_connection" /data/local/tmp/moerise-android-fixture.json >/dev/null
mkdir -p .local/android
IFS=',' read -r -a android_acceptance_classes <<< "${MOERISE_ANDROID_TEST_CLASS:-all}"
: > .local/android/device-acceptance.log
printf 'group\tstatus\tseconds\n' > .local/android/device-results.tsv
android_acceptance_failed=0
for android_acceptance_class in "${android_acceptance_classes[@]}"; do
    if [[ "$android_acceptance_class" == none ]]; then continue; fi
    android_acceptance_started=$SECONDS
    android_acceptance_phase_failed=0
    android_acceptance_filter=()
    if [[ "$android_acceptance_class" != all ]]; then
        android_acceptance_filter=(-e class "$android_acceptance_class")
    fi
    # 每组使用新进程，避免 Activity 和测试输入会话跨 instrumentation 复用。
    if ! timeout 30s adb -s "$android_device" shell am force-stop io.github.fermin214.moerise.fixture; then
        android_acceptance_phase_failed=1
    fi
    : > .local/android/device-acceptance-phase.log
    if ! timeout --signal=TERM --kill-after=15s 600s adb -s "$android_device" shell am instrument -w \
        -e timeout_msec 300000 -e visualSetup "${MOERISE_ANDROID_VISUAL_SETUP:-false}" "${android_acceptance_filter[@]}" \
        io.github.fermin214.moerise.fixture.test/androidx.test.runner.AndroidJUnitRunner |
        tee -a .local/android/device-acceptance.log .local/android/device-acceptance-phase.log; then
        android_acceptance_phase_failed=1
    fi
    if ! grep -Eq '^OK \([1-9][0-9]* tests?\)' .local/android/device-acceptance-phase.log ||
        grep -Eq 'FAILURES!!!|INSTRUMENTATION_FAILED' .local/android/device-acceptance-phase.log; then
        android_acceptance_phase_failed=1
    fi
    android_acceptance_status=passed
    if [[ "$android_acceptance_phase_failed" == 1 ]]; then
        android_acceptance_failed=1
        android_acceptance_status=failed
    fi
    printf '%s\t%s\t%s\n' "$android_acceptance_class" "$android_acceptance_status" \
        "$((SECONDS - android_acceptance_started))" >> .local/android/device-results.tsv
done
if [[ "${MOERISE_ANDROID_RECOVERY:-0}" == 1 ]]; then
    android_acceptance_started=$SECONDS
    android_acceptance_status=passed
    if ! python3 scripts/android/device-recovery.py --serial "$android_device" \
        --fixture "$android_fixture_connection" --source-root "$android_repo_root" \
        --output "$android_repo_root/.local/android/device-recovery"; then
        android_acceptance_failed=1
        android_acceptance_status=failed
    fi
    printf 'recovery\t%s\t%s\n' "$android_acceptance_status" \
        "$((SECONDS - android_acceptance_started))" >> .local/android/device-results.tsv
fi
if [[ "$(wc -l < .local/android/device-results.tsv)" -lt 2 ]]; then
    echo '设备流程没有执行测试' >&2
    exit 1
fi
cat .local/android/device-results.tsv
exit "$android_acceptance_failed"
