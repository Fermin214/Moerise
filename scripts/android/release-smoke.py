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

from maestro_runner import DRIVERS, MaestroRunner, ROOT, write_result


def command_failure(error):
    output = error.output.decode("utf-8", "replace") if isinstance(error.output, bytes) else error.output
    return {"cause": error.code, "output": output}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--serial", default="emulator-5554")
    parser.add_argument("--fixture", required=True)
    parser.add_argument("--apk", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    if not args.serial.startswith("emulator-"):
        parser.error("发行自动检查只使用隔离模拟器")
    spec = importlib.util.spec_from_file_location("phone_session", ROOT / "scripts/android/phone-session.py")
    phone = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(phone)
    adb = phone.Adb("adb", args.serial)
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
        adb.run("install", str(args.apk.resolve()), timeout=120)
        runner.api.login()
        jar = next(handler.cookiejar for handler in runner.api.opener.handlers
                   if isinstance(handler, urllib.request.HTTPCookieProcessor))
        assert any(cookie.name == "__Host-moerise_session" and cookie.secure for cookie in jar), "Secure Cookie 缺失"
        report["secure_cookie"] = True
        runner.api.close()
        result = runner.run_flow("release-smoke")
        report["flows"]["release-smoke"] = result
        if result["status"] != "passed":
            raise RuntimeError("发行场景失败：" + str(result["cause"]))
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
                adb.run("shell", "rm", "-f", "/sdcard/moerise-release-reopen.xml")
                adb.run("shell", "uiautomator", "dump", "/sdcard/moerise-release-reopen.xml",
                        timeout=min(10, remaining))
                xml = adb.run("shell", "cat", "/sdcard/moerise-release-reopen.xml")
                (args.output / "reopened-window.xml").write_text(xml, encoding="utf-8")
                ids = {node.get("resource-id", "").rsplit(":id/", 1)[-1]
                       for node in ET.fromstring(xml).iter("node")}
                if "open_menu" in ids and "server_login" not in ids:
                    report["session_restored_without_login"] = True
                    break
            except phone.CommandError as error:
                report["session_restore_errors"].append(command_failure(error))
            except ET.ParseError:
                report["session_restore_errors"].append({"cause": "invalid_ui_xml"})
            time.sleep(1)
        adb.run("shell", "pidof", runner.app)
        report["status"] = "passed"
    except Exception as error:
        report["failure"] = {"type": type(error).__name__, "message": str(error)}
        if isinstance(error, phone.CommandError):
            report["failure"].update(command_failure(error))
        raise
    finally:
        report.setdefault("status", "failed")
        write_result(args.output / "result.json", report)
    print(json.dumps({"status": report["status"], "flows": list(report["flows"])}, ensure_ascii=False))


if __name__ == "__main__":
    main()
