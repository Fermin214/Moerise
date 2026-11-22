#!/usr/bin/env bash
set -euo pipefail
android_repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
android_tools_root="${MOERISE_ANDROID_TOOLS:-$HOME/.local/share/moerise-android}"
export JAVA_HOME="${JAVA_HOME:-$android_tools_root/jdk-17.0.21+9}"
export ANDROID_HOME="${ANDROID_HOME:-$android_tools_root/android-sdk}"
export PATH="$JAVA_HOME/bin:$ANDROID_HOME/platform-tools:$ANDROID_HOME/cmdline-tools/latest/bin:$PATH"
if ! command -v java >/dev/null || ! test -d "$ANDROID_HOME/platforms/android-36"; then
    echo "需要 JDK 17 和 Android API 36；设置 JAVA_HOME、ANDROID_HOME。" >&2
    exit 1
fi

android_gradle() {
    local -a launcher=() options=()
    if [[ "${MOERISE_ANDROID_CI_BUILD:-0}" == 1 ]]; then
        options=(--parallel --max-workers=2 "-Dorg.gradle.jvmargs=-Xmx3g -XX:MaxMetaspaceSize=1g -XX:ActiveProcessorCount=2 -Dfile.encoding=UTF-8")
    else
        local cpu="${MOERISE_ANDROID_CPU:-$(python3 -c 'import os; print(min(os.sched_getaffinity(0)))')}"
        launcher=(taskset -c "$cpu" nice -n 10)
    fi
    "${launcher[@]}" "$android_repo_root/mobile/gradlew" -p "$android_repo_root/mobile" --no-daemon "${options[@]}" "$@"
}
