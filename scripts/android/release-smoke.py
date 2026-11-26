#!/usr/bin/env python3
"""Check the signed release on an isolated emulator through system-trusted HTTPS."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import time
import urllib.request
import xml.etree.ElementTree as ET

from adb_common import Adb, CommandError
from maestro_runner import DRIVERS, MaestroRunner, ROOT, write_result, fresh_ui_dump


def apk_version_name(apk):
    executable = "aapt2.exe" if os.name == "nt" else "aapt2"
    tool = Path(os.environ["ANDROID_HOME"]) / "build-tools" / "36.0.0" / executable
    badging = subprocess.check_output([tool, "dump", "badging", apk], encoding="utf-8")
    version = re.search(r"(?m)^package: name='[^']+' versionCode='\d+' versionName='([^']+)'", badging)
    if version is None:
        raise ValueError("无法读取基线 APK 的 versionName")
    return version[1]


def command_failure(error):
    output = error.output.decode("utf-8", "replace") if isinstance(error.output, bytes) else error.output
    return {"cause": error.code, "output": output}


def require_restored_session(adb, app, output, command_error):
    for package in reversed(DRIVERS):
        if adb.run("shell", "pm", "path", package, check=False).strip():
            adb.run("shell", "am", "force-stop", package)
    adb.run("shell", "am", "start", "-W", "-n", app + "/io.github.fermin214.moerise.MainActivity")
    deadline = time.monotonic() + 70
    errors = []
    while time.monotonic() < deadline:
        try:
            xml = fresh_ui_dump(adb, output)
            ids = {node.get("resource-id", "").rsplit(":id/", 1)[-1]
                   for node in ET.fromstring(xml).iter("node")}
        except (command_error, ET.ParseError) as error:
            errors.append(type(error).__name__)
            time.sleep(.5)
            continue
        assert ids.isdisjoint({"server_login", "welcome_enter"}), "升级后未恢复账号或 Keystore 会话"
        if "open_menu" in ids:
            return xml
        time.sleep(.5)
    raise RuntimeError("升级后未进入已登录主页：" + ",".join(errors))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--serial", default="emulator-5554")
    parser.add_argument("--fixture", required=True)
    parser.add_argument("--apk", required=True, type=Path)
    parser.add_argument("--upgrade-from", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    if not args.serial.startswith("emulator-"):
        parser.error("发行自动检查只使用隔离模拟器")
    adb = Adb("adb", args.serial)
    runner = MaestroRunner(ROOT, adb, args.fixture, args.output / "maestro", release=True)
    page_size = adb.run("shell", "getconf", "PAGESIZE", check=False).strip()
    if not page_size.isdigit():
        smaps = adb.run("shell", "cat", "/proc/self/smaps")
        match = re.search(r"(?m)^KernelPageSize:\s+(\d+)\s+kB", smaps)
        if match is None:
            raise RuntimeError("无法读取模拟器内核页大小")
        page_size = str(int(match[1]) * 1024)
    report = {
        "commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
        "run_id": os.environ.get("GITHUB_RUN_ID"),
        "apk_sha256": hashlib.sha256(args.apk.read_bytes()).hexdigest(),
        "api": adb.run("shell", "getprop", "ro.build.version.sdk").strip(),
        "page_size": page_size,
        "https": runner.connection["origin"], "flows": {},
    }
    try:
        if adb.run("shell", "pm", "path", runner.app, check=False).strip():
            raise RuntimeError("发行检查要求尚未安装此包的模拟器")
        upgrade = None
        if args.upgrade_from:
            baseline_version = apk_version_name(args.upgrade_from)
            adb.run("install", str(args.upgrade_from.resolve()), timeout=120)
            prior = runner.run_flow("release-smoke")
            if prior["status"] != "passed":
                raise RuntimeError(f"{baseline_version} preparation failed: {prior.get('cause')}")
            previous_note = json.loads((args.output / "maestro/release-smoke/test-data.json").read_text())["NEW_NOTE_TEXT"]
            spec = importlib.util.spec_from_file_location("upgrade_state", ROOT / "scripts/android/upgrade-state.py")
            upgrade_state = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(upgrade_state)
            upgrade = upgrade_state.seed_upgrade(adb, runner.app, args.output / "upgrade")
            adb.run("install", "-r", str(args.apk.resolve()), timeout=120)
            runner = MaestroRunner(ROOT, adb, args.fixture, args.output / "upgraded" / "maestro", release=True)
            require_restored_session(adb, runner.app, args.output / "upgrade/installed-window.xml", CommandError)
        else:
            adb.run("install", str(args.apk.resolve()), timeout=120)
        runner.api.login()
        jar = next(handler.cookiejar for handler in runner.api.opener.handlers
                   if isinstance(handler, urllib.request.HTTPCookieProcessor))
        assert any(cookie.name == "__Host-moerise_session" and cookie.secure for cookie in jar), "Secure Cookie 缺失"
        report["secure_cookie"] = True
        runner.api.close()
        result = runner.run_flow("upgrade-smoke" if upgrade else "release-smoke")
        report["flows"]["upgrade-smoke" if upgrade else "release-smoke"] = result
        if result["status"] != "passed":
            raise RuntimeError("发行场景失败：" + str(result["cause"]))
        if upgrade:
            runner.api.login()
            deadline = time.monotonic() + 60
            while len(runner.api.matching_notes(upgrade["queued"])) != 1:
                if time.monotonic() >= deadline:
                    raise RuntimeError("upgraded pending write did not drain")
                time.sleep(.5)
            xml = require_restored_session(adb, runner.app, args.output / "upgrade/completed-window.xml", CommandError)
            assert upgrade["draft"] in xml, "upgraded draft missing"
            assert "welcome_enter" not in xml and "server_login" not in xml, "upgraded account or Keystore missing"
            assert len(runner.api.matching_notes(previous_note)) == 1, f"{baseline_version} 笔记丢失"
            report["upgrade"] = {"from": baseline_version, "account": True, "draft": True, "pending_write_count": 1,
                                 "keystore_session": True, "previous_apk_sha256": hashlib.sha256(args.upgrade_from.read_bytes()).hexdigest()}

        for package in reversed(DRIVERS):
            if adb.run("shell", "pm", "path", package, check=False).strip():
                adb.run("shell", "am", "force-stop", package)
        adb.run("shell", "am", "force-stop", runner.app)
        adb.run("shell", "am", "start", "-W", "-n",
                runner.app + "/io.github.fermin214.moerise.MainActivity")
        deadline = time.monotonic() + 70
        report["session_restore_errors"] = []
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError("应用重开后未恢复登录")
            try:
                xml = fresh_ui_dump(adb, args.output / "reopened-window.xml", timeout=min(10, remaining))
                ids = {node.get("resource-id", "").rsplit(":id/", 1)[-1]
                       for node in ET.fromstring(xml).iter("node")}
                if "open_menu" in ids and ids.isdisjoint({"server_login", "welcome_enter"}):
                    report["session_restored_without_login"] = True
                    break
            except CommandError as error:
                report["session_restore_errors"].append(command_failure(error))
            except ET.ParseError:
                report["session_restore_errors"].append({"cause": "invalid_ui_xml"})
            time.sleep(1)
        adb.run("shell", "pidof", runner.app)
        if upgrade:
            assert upgrade["draft"] in xml, "重开后草稿丢失"
            assert len(runner.api.matching_notes(upgrade["queued"])) == 1, "重开后待发笔记重复"
            upgrade_state.verify_upgrade(adb, runner.app, upgrade)
            report["upgrade"]["local_database_restored"] = True
        report["status"] = "passed"
    except Exception as error:
        report["failure"] = {"type": type(error).__name__, "message": str(error)}
        if isinstance(error, CommandError):
            report["failure"].update(command_failure(error))
        raise
    finally:
        report.setdefault("status", "failed")
        write_result(args.output / "result.json", report)
        try:
            (args.output / "logcat.txt").write_text(adb.run("logcat", "-d", "-t", "1000", check=False), encoding="utf-8")
        except CommandError:
            pass
    print(json.dumps({"status": report["status"], "flows": list(report["flows"])}, ensure_ascii=False))


if __name__ == "__main__":
    main()
