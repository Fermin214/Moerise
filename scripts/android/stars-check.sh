#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
android_stars_cpu="${MOERISE_ANDROID_CPU:-$(python3 -c 'import os; print(min(os.sched_getaffinity(0)))')}"
export MOERISE_ANDROID_LAYOUT_IMAGE="moerise-android-layout:$(git rev-parse --short HEAD)"
docker build --cpuset-cpus "$android_stars_cpu" --memory 1g \
    -t "$MOERISE_ANDROID_LAYOUT_IMAGE" services/layout
export MOERISE_ANDROID_STARS=1
exec scripts/android/live-check.sh "$@"
