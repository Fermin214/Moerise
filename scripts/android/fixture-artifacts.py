#!/usr/bin/env python3
"""Record and verify fixture APKs against their Android sources."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = ROOT / ".local/android/fixture-artifacts.json"
APKS = (
    "mobile/androidApp/build/outputs/apk/fixture/androidApp-fixture.apk",
    "mobile/androidApp/build/outputs/apk/androidTest/fixture/androidApp-fixture-androidTest.apk",
)


def source_digest():
    paths = subprocess.check_output(
        ["git", "ls-files", "-c", "-o", "--exclude-standard", "-z", "--", "mobile"], cwd=ROOT
    ).split(b"\0")
    digest = hashlib.sha256()
    for relative in sorted(set(paths) - {b""}):
        path = ROOT / relative.decode()
        digest.update(relative + b"\0")
        digest.update(hashlib.sha256(path.read_bytes()).digest() if path.is_file() else b"deleted")
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("write", "verify"))
    args = parser.parse_args()
    actual = {
        "android_sources_sha256": source_digest(),
        "apks": {relative: hashlib.sha256((ROOT / relative).read_bytes()).hexdigest() for relative in APKS},
    }
    if args.operation == "write":
        actual["commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        MANIFEST.parent.mkdir(parents=True, exist_ok=True)
        MANIFEST.write_text(json.dumps(actual, indent=2) + "\n")
    else:
        recorded = json.loads(MANIFEST.read_text())
        if any(recorded.get(key) != value for key, value in actual.items()):
            raise SystemExit("APK 或 Android 源码已变化，需要重新构建 fixture APK。")
    print("Fixture APK 与 Android 源码摘要一致。")


if __name__ == "__main__":
    main()
