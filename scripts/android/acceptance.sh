#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
android_fixture_connection="${MOERISE_ANDROID_FIXTURE:?设置 MOERISE_ANDROID_FIXTURE 为隔离环境连接档案}"
test -f "$android_fixture_connection"
android_device="${ANDROID_SERIAL:?设置 ANDROID_SERIAL 为用于验收的设备}"
adb -s "$android_device" get-state >/dev/null
android_acceptance_cpu="${MOERISE_ANDROID_CPU:-$(python3 -c 'import os; print(min(os.sched_getaffinity(0)))')}"
taskset -c "$android_acceptance_cpu" nice -n 10 mobile/gradlew -p mobile --no-daemon --max-workers=1 \
    :androidApp:assembleFixture :androidApp:assembleFixtureAndroidTest
android_fixture_port="$(python3 -c 'import json,sys,urllib.parse; print(urllib.parse.urlparse(json.load(open(sys.argv[1]))["origin"]).port)' "$android_fixture_connection")"
adb -s "$android_device" reverse "tcp:$android_fixture_port" "tcp:$android_fixture_port"
adb -s "$android_device" install -r mobile/androidApp/build/outputs/apk/fixture/androidApp-fixture.apk
adb -s "$android_device" install -r mobile/androidApp/build/outputs/apk/androidTest/fixture/androidApp-fixture-androidTest.apk
adb -s "$android_device" shell pm clear io.github.fermin214.moerise.fixture >/dev/null
cleanup_android_device_fixture() {
    adb -s "$android_device" shell rm -f /data/local/tmp/moerise-android-fixture.json >/dev/null 2>&1 || true
}
trap cleanup_android_device_fixture EXIT
adb -s "$android_device" push "$android_fixture_connection" /data/local/tmp/moerise-android-fixture.json >/dev/null
mkdir -p .local/android
adb -s "$android_device" shell am instrument -w \
    io.github.fermin214.moerise.fixture.test/androidx.test.runner.AndroidJUnitRunner |
    tee .local/android/device-acceptance.log
rg -q '^OK \([1-9][0-9]* tests?\)' .local/android/device-acceptance.log
