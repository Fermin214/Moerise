#!/usr/bin/env python3
"""Run shared Maestro UI suites against an already installed fixture APK."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import urllib.parse

from adb_common import Adb, network_settings, proxy_enabled, reverse_conflict
from maestro_runner import MaestroRunner, ROOT, catalog


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--serial", required=True)
    parser.add_argument("--adb", default="adb")
    parser.add_argument("--fixture", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--source-root", default=str(ROOT))
    parser.add_argument("--group", required=True, choices=tuple(catalog()["groups"]))
    parser.add_argument("--maestro", default=shutil.which("maestro") or shutil.which("maestro.bat") or "maestro")
    args = parser.parse_args()
    if os.name == "nt" and not args.serial.startswith("emulator-"):
        recovery_spec = importlib.util.spec_from_file_location(
            "device_recovery", Path(__file__).with_name("device-recovery.py"))
        recovery = importlib.util.module_from_spec(recovery_spec)
        recovery_spec.loader.exec_module(recovery)
        recovery.assert_queue(args.source_root, args.output)
    adb = Adb(args.adb, args.serial)
    if proxy_enabled(network_settings(adb)):
        parser.error("设备存在全局代理；Maestro 不改写设备网络")
    runner = MaestroRunner(args.source_root, adb, args.fixture, args.output, args.maestro)
    mapping = f"tcp:{urllib.parse.urlsplit(runner.connection['origin']).port}"
    if reverse_conflict(adb, mapping):
        parser.error("设备端口存在其他 USB 映射")
    adb.run("reverse", mapping, mapping)
    result = runner.run_group(args.group)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0 if all(value["status"] == "passed" for value in result.values()) else 1


if __name__ == "__main__":
    raise SystemExit(main())
