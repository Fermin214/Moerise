#!/usr/bin/env python3
"""Exercise host-driven process death for an already configured fixture APK."""
import argparse
from pathlib import Path
import re
import subprocess
import time
import uuid
import xml.etree.ElementTree as E

parser = argparse.ArgumentParser()
parser.add_argument("--serial", required=True)
args = parser.parse_args()
package = "io.github.fermin214.moerise.fixture"
adb = ["adb", "-s", args.serial]

def shell(*command):
    return subprocess.check_output([*adb, "shell", *command], text=True)

def tree():
    shell("uiautomator", "dump", "/data/local/tmp/moerise-recovery.xml")
    return E.fromstring(shell("cat", "/data/local/tmp/moerise-recovery.xml"))

def editor():
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        for node in tree().iter("node"):
            if node.get("resource-id") == "note_editor":
                return node
        time.sleep(.5)
    raise AssertionError("找不到主页编辑器；先完成隔离包登录并返回主页")

try:
    shell("am", "start", "-W", "-n", package + "/io.github.fermin214.moerise.MainActivity")
    node = editor()
    bounds = list(map(int, re.findall(r"\d+", node.get("bounds", ""))))
    assert len(bounds) == 4
    shell("input", "tap", str((bounds[0] + bounds[2]) // 2), str((bounds[1] + bounds[3]) // 2))
    shell("input", "keyevent", "KEYCODE_MOVE_END")
    marker = "Process_recovery_" + uuid.uuid4().hex
    shell("input", "text", marker)
    time.sleep(1.3)
    shell("am", "force-stop", package)
    shell("am", "start", "-W", "-n", package + "/io.github.fermin214.moerise.MainActivity")
    assert marker in editor().get("text", ""), "进程终止后没有恢复已落盘草稿"
    shell("input", "keyevent", "KEYCODE_HOME")
    shell("am", "start", "-W", "-n", package + "/io.github.fermin214.moerise.MainActivity")
    assert marker in editor().get("text", ""), "后台返回后没有恢复草稿"
    print("进程终止重开与后台返回的草稿恢复通过。")
finally:
    shell("rm", "-f", "/data/local/tmp/moerise-recovery.xml")
