#!/usr/bin/env python3
"""Record and verify fixture APKs against their Android sources."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = ROOT / ".local/android/fixture-artifacts.json"
APKS = (
    "mobile/androidApp/build/outputs/apk/fixture/androidApp-fixture.apk",
    "mobile/androidApp/build/outputs/apk/androidTest/fixture/androidApp-fixture-androidTest.apk",
)
UPGRADE_VERSION_CODE = 2


def source_digest():
    paths = subprocess.check_output(
        ["git", "ls-files", "-c", "-o", "--exclude-standard", "-z", "--", "mobile", ":(exclude)mobile/androidApp/schemas"], cwd=ROOT
    ).split(b"\0")
    digest = hashlib.sha256()
    for relative in sorted(set(paths) - {b""}):
        path = ROOT / relative.decode()
        digest.update(relative + b"\0")
        digest.update(hashlib.sha256(path.read_bytes()).digest() if path.is_file() else b"deleted")
    return digest.hexdigest()


def apk_digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def apk_version_code(path):
    aapt = "aapt2.exe" if os.name == "nt" else "aapt2"
    executable = Path(os.environ["ANDROID_HOME"]) / "build-tools/36.0.0" / aapt
    output = subprocess.check_output([executable, "dump", "badging", path], encoding="utf-8")
    version = re.search(r"^package: name='[^']+' versionCode='(\d+)'", output, re.MULTILINE)
    if not version:
        raise ValueError("无法读取升级 fixture APK 的 versionCode。")
    return int(version[1])


def write_upgrade(path):
    path = path.resolve()
    relative = path.relative_to(ROOT)
    version = apk_version_code(path)
    if version != UPGRADE_VERSION_CODE:
        raise ValueError(f"升级 fixture APK 的 versionCode 应为 {UPGRADE_VERSION_CODE}，实际为 {version}。")
    return {"path": relative.as_posix(), "version_code": version, "sha256": apk_digest(path)}


def verify_upgrade(record, expected_path=None):
    rebuild = "需要重新构建并记录升级 fixture APK。"
    if (not isinstance(record, dict) or not isinstance(record.get("path"), str)
            or not isinstance(record.get("sha256"), str) or "version_code" not in record):
        raise ValueError("清单缺少升级 APK 摘要，" + rebuild)
    path = (ROOT / record["path"]).resolve()
    if (not path.is_relative_to(ROOT) or (expected_path and path != expected_path.resolve())
            or record["version_code"] != UPGRADE_VERSION_CODE
            or not path.is_file() or apk_digest(path) != record["sha256"]):
        raise ValueError("升级 APK 路径、版本或摘要不匹配，" + rebuild)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("write", "verify", "source-digest"))
    parser.add_argument("--target-only", action="store_true",
                        help="Verify Android sources and the target APK without requiring the test APK")
    parser.add_argument("--upgrade-apk", type=Path)
    parser.add_argument("--require-upgrade", action="store_true")
    args = parser.parse_args()
    if args.target_only and args.operation != "verify":
        parser.error("--target-only 仅用于 verify。")
    if args.upgrade_apk and args.operation != "write" and not args.require_upgrade:
        parser.error("--upgrade-apk 用于 write 或 verify --require-upgrade。")
    if args.require_upgrade and args.operation != "verify":
        parser.error("--require-upgrade 仅用于 verify。")
    if args.operation == "source-digest":
        print(source_digest())
        return
    apks = APKS[:1] if args.target_only else APKS
    actual = {
        "android_sources_sha256": source_digest(),
        "apks": {relative: apk_digest(ROOT / relative) for relative in apks},
    }
    if args.operation == "write":
        actual["commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        if args.upgrade_apk:
            actual["upgrade"] = write_upgrade(args.upgrade_apk)
        MANIFEST.parent.mkdir(parents=True, exist_ok=True)
        MANIFEST.write_text(json.dumps(actual, indent=2) + "\n")
    else:
        recorded = json.loads(MANIFEST.read_text())
        if args.target_only:
            recorded["apks"] = {relative: recorded.get("apks", {}).get(relative) for relative in apks}
        if any(recorded.get(key) != value for key, value in actual.items()):
            raise SystemExit("APK 或 Android 源码已变化，需要重新构建 fixture APK。")
        if args.require_upgrade:
            verify_upgrade(recorded.get("upgrade"), args.upgrade_apk)
    print("Fixture 目标 APK 与 Android 源码摘要一致。" if args.target_only else "Fixture APK 与 Android 源码摘要一致。")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, TypeError, subprocess.CalledProcessError) as error:
        raise SystemExit(str(error)) from None
