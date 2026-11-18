#!/usr/bin/env python3
"""Fetch checksum-pinned official Perfetto processors for the benchmark runner."""
import argparse
import hashlib
from pathlib import Path
import urllib.request

VERSION = "v59.1"
BINARIES = {
    "aarch64": ("android-arm64", "6242dcbf8ae56c0a9fc21c54463e7046e92a55f9fc0606180e53539818bfd7f2"),
    "x86_64": ("android-x64", "541cdf41c9916a12040d91e9c367dc2fff3512d82d35357a89f1269acac9f074"),
    "arm": ("android-arm", "18a29c6d16949291fe2bf6a03724afb1005068293e2eb58102e2264506a7630e"),
    "x86": ("android-x86", "7deaab500b2cab96e90c3d9ea6cff372a9fbf67b8a75aac2be0dff30cd0d2433"),
}

parser = argparse.ArgumentParser()
parser.add_argument("directory", type=Path)
args = parser.parse_args()
args.directory.mkdir(parents=True, exist_ok=True)
for abi, (arch, checksum) in BINARIES.items():
    target = args.directory / ("trace_processor_shell_" + abi)
    if target.is_file() and hashlib.sha256(target.read_bytes()).hexdigest() == checksum:
        continue
    url = f"https://commondatastorage.googleapis.com/perfetto-luci-artifacts/{VERSION}/{arch}/trace_processor_shell"
    with urllib.request.urlopen(url, timeout=120) as response:
        body = response.read()
    assert hashlib.sha256(body).hexdigest() == checksum, f"Perfetto 摘要不匹配：{arch}"
    temporary = target.with_suffix(".download")
    temporary.write_bytes(body)
    temporary.replace(target)
print(f"Perfetto {VERSION}: {len(BINARIES)} ABIs")
