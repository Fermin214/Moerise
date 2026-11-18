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
parser.add_argument("--version-code", type=int)
parser.add_argument("--require-profile", action="store_true")
parser.add_argument("--same-signer-as", type=Path)
args = parser.parse_args()
tools = Path(os.environ["ANDROID_HOME"]) / "build-tools/36.0.0"

def tool(name):
    suffixes = (".exe", ".bat", "") if os.name == "nt" else ("",)
    for suffix in suffixes:
        candidate = tools / (name + suffix)
        if candidate.is_file():
            return candidate
    raise FileNotFoundError(f"Android Build Tools 缺少 {name}：{tools}")

badging = subprocess.check_output([tool("aapt2"), "dump", "badging", args.apk],
                                 text=True, encoding="utf-8")
identity = re.search(r"package: name='([^']+)' versionCode='([^']*)' versionName='([^']*)'", badging)
assert identity, "无法读取 APK 身份"
package, code, version = identity.groups()
properties = (Path(__file__).resolve().parents[2] / "mobile/gradle.properties").read_text(encoding="utf-8")
expected_min_sdk = int(re.search(r"(?m)^moerise\.minSdk=(\d+)$", properties)[1])
minimum = re.search(r"(?m)^minSdkVersion:'(\d+)'$", badging)
assert minimum, "无法读取 APK 最低 API"
min_sdk = int(minimum[1])
assert min_sdk == expected_min_sdk, f"APK 最低 API 为 {min_sdk}，期望 {expected_min_sdk}"
assert not args.package or package == args.package, f"包名不匹配：{package}"
assert not args.version or version == args.version, f"版本不匹配：{version}"
assert args.version_code is None or code == str(args.version_code), f"versionCode 不匹配：{code}"
assert not args.release or "application-debuggable" not in badging, "发行包不能开启调试"
assert not args.release or code.isdecimal() and int(code) > 0, "发行包需要有效 versionCode"
subprocess.run([tool("zipalign"), "-c", "-P", "16", "4", args.apk], check=True)
def signers(apk):
    result = subprocess.check_output(
        [tool("apksigner"), "verify", "--verbose", "--print-certs", apk], text=True, encoding="utf-8")
    print(result, end="")
    digests = set(re.findall(r"Signer #\d+ certificate SHA-256 digest: ([0-9a-fA-F]+)", result))
    assert digests, "未读取到 APK 签名证书摘要"
    return {digest.lower() for digest in digests}

if not args.unsigned:
    certificates = signers(args.apk)
    if args.same_signer_as:
        assert certificates == signers(args.same_signer_as), "目标 APK 与 runner 的签名不一致"
else:
    assert not args.same_signer_as, "未签名 APK 不能比较签名"
abis = set()
native_count = 0
with zipfile.ZipFile(args.apk) as archive:
    if args.require_profile:
        for name, magic in (("assets/dexopt/baseline.prof", b"pro\x00"),
                            ("assets/dexopt/baseline.profm", b"prm\x00")):
            profile = archive.read(name)
            assert len(profile) > 8 and profile.startswith(magic), f"缺少有效 Profile：{name}"
    for name in archive.namelist():
        if not name.startswith("lib/") or not name.endswith(".so"):
            continue
        binary = archive.read(name)
        assert len(binary) >= 64 and binary[:4] == b"\x7fELF" and binary[5] == 1 and binary[4] in (1, 2), name
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
        assert count > 0 and size >= struct.calcsize(layout) and offset + size * count <= len(binary), name
        loads = 0
        for index in range(count):
            header = struct.unpack_from(layout, binary, offset + index * size)
            if header[0] == 1:
                loads += 1
                alignment = header[-1]
                file_offset, virtual_address = (header[2], header[3]) if binary[4] == 2 else (header[1], header[2])
                assert alignment >= required and alignment & (alignment - 1) == 0, f"{name}: LOAD 对齐为 {alignment}"
                assert file_offset % required == virtual_address % required, f"{name}: LOAD 地址不满足 {required} 对齐"
        assert loads, f"{name}: 缺少 LOAD 段"
        native_count += 1
assert not args.release or {"arm64-v8a", "x86_64"} <= abis, f"缺少发行 ABI：{abis}"
print(json.dumps(dict(package=package, version_name=version, version_code=int(code) if code else None, min_sdk=min_sdk,
                     abis=sorted(abis), native_libraries=native_count, page_alignment=16384),
                 ensure_ascii=False))
