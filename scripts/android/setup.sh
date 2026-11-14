#!/usr/bin/env bash
set -euo pipefail
android_setup_root="${MOERISE_ANDROID_TOOLS:-$HOME/.local/share/moerise-android}"
mkdir -p "$android_setup_root/downloads"
android_setup_cache="$android_setup_root/downloads"
if ! test -x "$android_setup_root/jdk-17.0.21+9/bin/java"; then
    curl -fL --retry 2 'https://github.com/adoptium/temurin17-binaries/releases/download/jdk-17.0.21%2B9/OpenJDK17U-jdk_x64_linux_hotspot_17.0.21_9.tar.gz' -o "$android_setup_cache/jdk.tar.gz"
fi
if ! test -x "$android_setup_root/android-sdk/cmdline-tools/latest/bin/sdkmanager"; then
    curl -fL --retry 2 https://dl.google.com/android/repository/commandlinetools-linux-16111833_latest.zip -o "$android_setup_cache/sdk.zip"
fi
python3 - "$android_setup_root" <<'PY'
from pathlib import Path
import hashlib, sys, tarfile, zipfile
root = Path(sys.argv[1])
cache = root / 'downloads'
def verify(path, expected):
    with path.open('rb') as handle:
        assert hashlib.file_digest(handle, 'sha256').hexdigest() == expected, f'摘要不匹配：{path.name}'
if not (root / 'jdk-17.0.21+9/bin/java').exists():
    verify(cache / 'jdk.tar.gz', '8b9b611d917e3dfd8419d3566b5949226f69b052fed7e109c1368add1dfe8711')
    with tarfile.open(cache / 'jdk.tar.gz') as archive:
        archive.extractall(root, filter='data')
if not (root / 'android-sdk/cmdline-tools/latest/bin/sdkmanager').exists():
    verify(cache / 'sdk.zip', '0877a1d048fe4a24efe2eff536ca4223f7adeb58648bb81909d33c446918cfa8')
    target = root / 'android-sdk/cmdline-tools'
    with zipfile.ZipFile(cache / 'sdk.zip') as archive:
        archive.extractall(target)
    (target / 'cmdline-tools').rename(target / 'latest')
    for path in (target / 'latest/bin').iterdir():
        path.chmod(0o755)
PY
export JAVA_HOME="$android_setup_root/jdk-17.0.21+9"
export ANDROID_HOME="$android_setup_root/android-sdk"
"$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager" 'platform-tools' 'platforms;android-36' 'build-tools;36.0.0'
echo "Android 工具目录：$android_setup_root"
