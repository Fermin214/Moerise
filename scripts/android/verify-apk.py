#!/usr/bin/env python3
"""Check an APK's identity, signing and native page alignment."""
import argparse
import json
import os
from pathlib import Path
import re
import struct
import subprocess
import zipfile

parser = argparse.ArgumentParser()
parser.add_argument("apk", type=Path)
parser.add_argument("--unsigned", action="store_true")
parser.add_argument("--release", action="store_true")
parser.add_argument("--package")
parser.add_argument("--version")
args = parser.parse_args()
tools = Path(os.environ["ANDROID_HOME"]) / "build-tools/36.0.0"
badging = subprocess.check_output([tools / "aapt2", "dump", "badging", args.apk], text=True)
identity = re.search(r"package: name='([^']+)' versionCode='([^']*)' versionName='([^']*)'", badging)
assert identity, "无法读取 APK 身份"
package, code, version = identity.groups()
assert not args.package or package == args.package, f"包名不匹配：{package}"
assert not args.version or version == args.version, f"版本不匹配：{version}"
assert not args.release or "application-debuggable" not in badging, "发行包不能开启调试"
assert not args.release or code.isdecimal() and int(code) > 0, "发行包需要有效 versionCode"
subprocess.run([tools / "zipalign", "-c", "-P", "16", "4", args.apk], check=True)
if not args.unsigned:
    subprocess.run([tools / "apksigner", "verify", "--verbose", "--print-certs", args.apk], check=True)
abis = set()
native_count = 0
with zipfile.ZipFile(args.apk) as archive:
    for name in archive.namelist():
        if not name.startswith("lib/") or not name.endswith(".so"):
            continue
        binary = archive.read(name)
        assert binary[:4] == b"\x7fELF" and binary[5] == 1, name
        abi = name.split("/")[1]
        abis.add(abi)
        required = 16384 if abi in ("arm64-v8a", "x86_64") else 4096
        if binary[4] == 2:
            offset = struct.unpack_from("<Q", binary, 32)[0]
            size, count = struct.unpack_from("<HH", binary, 54)
            layout = "<IIQQQQQQ"
        else:
            offset = struct.unpack_from("<I", binary, 28)[0]
            size, count = struct.unpack_from("<HH", binary, 42)
            layout = "<IIIIIIII"
        for index in range(count):
            header = struct.unpack_from(layout, binary, offset + index * size)
            if header[0] == 1:
                assert header[-1] >= required, f"{name}: LOAD 对齐为 {header[-1]}"
        native_count += 1
assert {"arm64-v8a", "x86_64"} <= abis, f"缺少发行 ABI：{abis}"
print(json.dumps(dict(package=package, version_name=version, version_code=int(code) if code else None,
                     abis=sorted(abis), native_libraries=native_count, page_alignment=16384),
                 ensure_ascii=False))
