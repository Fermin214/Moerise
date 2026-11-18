#!/usr/bin/env bash
set -euo pipefail
android_fixture_repo="$(cd "$(dirname "$0")/../.." && pwd)"
mkdir -p "$android_fixture_repo/.local/android"
android_fixture_root="${MOERISE_ANDROID_FIXTURE_ROOT:-$(mktemp -d "$android_fixture_repo/.local/android/fixture-XXXXXXXX")}"
mkdir -p "$android_fixture_root"
cd "$android_fixture_repo"
if [[ -n "${MOERISE_ANDROID_FIXTURE_BINARY:-}" ]]; then
    install -m 700 "$MOERISE_ANDROID_FIXTURE_BINARY" "$android_fixture_root/moerise"
else
    android_fixture_cpu="${MOERISE_ANDROID_CPU:-$(python3 -c 'import os; print(min(os.sched_getaffinity(0)))')}"
    GOMAXPROCS=1 taskset -c "$android_fixture_cpu" nice -n 10 go build -p 1 -o "$android_fixture_root/moerise" .
fi
exec node scripts/android/fixture.mjs "$android_fixture_root" "$@"
