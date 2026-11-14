#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
android_milestone="${1:?填写里程碑，例如 M2}"
[[ "$android_milestone" =~ ^M[0-9]+$ ]]
test -z "$(git status --porcelain --untracked-files=no)"
android_commit="$(git rev-parse --short HEAD)"
android_apk="mobile/androidApp/build/outputs/apk/debug/androidApp-debug.apk"
"$ANDROID_HOME/build-tools/36.0.0/apksigner" verify "$android_apk"
"$ANDROID_HOME/build-tools/36.0.0/zipalign" -c -P 16 4 "$android_apk"
python3 - "$android_apk" <<'PY'
import struct, sys, zipfile
count = 0
with zipfile.ZipFile(sys.argv[1]) as archive:
    for name in archive.namelist():
        if not name.startswith("lib/") or not name.endswith(".so"):
            continue
        binary = archive.read(name)
        assert binary[:4] == b'\x7fELF' and binary[5] == 1, name
        required = 16384 if name.split('/')[1] in ('arm64-v8a', 'x86_64') else 4096
        if binary[4] == 2:
            offset = struct.unpack_from('<Q', binary, 32)[0]
            size, number = struct.unpack_from('<HH', binary, 54)
            layout = '<IIQQQQQQ'
        else:
            offset = struct.unpack_from('<I', binary, 28)[0]
            size, number = struct.unpack_from('<HH', binary, 42)
            layout = '<IIIIIIII'
        for index in range(number):
            header = struct.unpack_from(layout, binary, offset + index * size)
            if header[0] == 1:
                assert header[-1] >= required, f'{name}: PT_LOAD alignment {header[-1]}'
        if required == 16384:
            count += 1
assert count, 'APK 缺少预期的 SQLite native 库'
print(f'{count} 个 64 位 native 库的 ELF LOAD 段支持 16 KiB 对齐。')
PY
mkdir -p .local/android/artifacts
android_destination=".local/android/artifacts/moerise-$android_milestone-$android_commit.apk"
cp "$android_apk" "$android_destination"
sha256sum "$android_destination"
