#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
android_milestone="${1:?填写里程碑，例如 M2}"
[[ "$android_milestone" =~ ^M[0-9]+$ ]]
test -z "$(git status --porcelain --untracked-files=no)"
android_commit="$(git rev-parse --short HEAD)"
android_apk="mobile/androidApp/build/outputs/apk/debug/androidApp-debug.apk"
python3 scripts/android/verify-apk.py "$android_apk" --package io.github.fermin214.moerise.dev
mkdir -p .local/android/artifacts
android_destination=".local/android/artifacts/moerise-$android_milestone-$android_commit.apk"
cp "$android_apk" "$android_destination"
sha256sum "$android_destination"
