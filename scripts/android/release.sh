#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
android_release_mode="${1:---unsigned}"
[[ "$android_release_mode" == --unsigned || "$android_release_mode" == --sign ]] || {
    echo "用法：scripts/android/release.sh [--unsigned|--sign]" >&2
    exit 1
}
test -z "$(git status --porcelain)"
if [[ "$android_release_mode" == --sign ]]; then
    test -f "${MOERISE_ANDROID_KEYSTORE:?设置发行 keystore 路径}"
    : "${MOERISE_ANDROID_KEY_ALIAS:?设置密钥别名}"
    : "${MOERISE_ANDROID_STORE_PASSWORD:?设置 keystore 密码}"
    : "${MOERISE_ANDROID_KEY_PASSWORD:?设置签名密钥密码}"
    export MOERISE_ANDROID_STORE_PASSWORD MOERISE_ANDROID_KEY_PASSWORD
fi
android_release_cpu="${MOERISE_ANDROID_CPU:-$(python3 -c 'import os; print(min(os.sched_getaffinity(0)))')}"
taskset -c "$android_release_cpu" nice -n 10 mobile/gradlew -p mobile --no-daemon --max-workers=1 \
    :androidApp:assembleRelease :androidApp:lintRelease :androidApp:exportReleaseDependencies
python3 scripts/android/licenses.py --check
android_release_commit="$(git rev-parse HEAD)"
android_release_version="$(python3 -c 'import json; print(json.load(open("mobile/androidApp/build/outputs/apk/release/output-metadata.json"))["elements"][0]["versionName"])')"
android_release_output=".local/android/releases/v$android_release_version-${android_release_commit:0:7}"
mkdir -p "$android_release_output"
android_release_file="moerise-android-v$android_release_version"
if [[ "$android_release_mode" == --unsigned ]]; then
    android_release_file="$android_release_file-unsigned"
fi
"$ANDROID_HOME/build-tools/36.0.0/zipalign" -P 16 -f 4 \
    mobile/androidApp/build/outputs/apk/release/androidApp-release-unsigned.apk \
    "$android_release_output/$android_release_file.apk"
android_release_verify=(--unsigned)
if [[ "$android_release_mode" == --sign ]]; then
    "$ANDROID_HOME/build-tools/36.0.0/apksigner" sign --ks "$MOERISE_ANDROID_KEYSTORE" \
        --ks-key-alias "$MOERISE_ANDROID_KEY_ALIAS" \
        --ks-pass env:MOERISE_ANDROID_STORE_PASSWORD --key-pass env:MOERISE_ANDROID_KEY_PASSWORD \
        "$android_release_output/$android_release_file.apk"
    android_release_verify=()
fi
python3 scripts/android/verify-apk.py "$android_release_output/$android_release_file.apk" \
    --release --package io.github.fermin214.moerise --version "$android_release_version" "${android_release_verify[@]}" \
    > "$android_release_output/apk-verification.txt"
cp docs/android/THIRD_PARTY_NOTICES.md "$android_release_output/"
cp -r docs/android/licenses "$android_release_output/"
cp docs/android/INSTALL.md "$android_release_output/"
cp LICENSE "$android_release_output/"
cp mobile/androidApp/build/outputs/mapping/release/mapping.txt "$android_release_output/"
python3 - "$android_release_output" "$android_release_file.apk" "$android_release_commit" <<'PY'
import hashlib, json, os, pathlib, subprocess, sys, zipfile
output, filename, commit = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
checksum = hashlib.sha256((output / filename).read_bytes()).hexdigest()
(output / (filename + ".sha256")).write_text(f"{checksum}  {filename}\n")
metadata = dict(commit=commit, apk=filename, sha256=checksum,
                signed=not filename.endswith("-unsigned.apk"),
                source_date_epoch=int(subprocess.check_output(["git", "show", "-s", "--format=%ct", commit])),
                java=subprocess.check_output([os.environ["JAVA_HOME"] + "/bin/java", "-version"], stderr=subprocess.STDOUT, text=True).splitlines()[0])
(output / (filename + ".json")).write_text(json.dumps(metadata, indent=2) + "\n")
with zipfile.ZipFile(output / (filename.removesuffix("-unsigned.apk").removesuffix(".apk") + "-notices.zip"), "w", zipfile.ZIP_DEFLATED) as archive:
    for path in sorted(output.rglob("*")):
        if path.is_file() and (path.suffix in (".md", ".txt") and path.name != "mapping.txt" or path.name == "LICENSE"):
            archive.write(path, str(path.relative_to(output)))
print(f"{output / filename}\nSHA-256: {checksum}")
PY
