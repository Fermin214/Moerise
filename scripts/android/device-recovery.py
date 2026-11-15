#!/usr/bin/env python3
"""Host-driven offline queue, process restart and background reconnect checks."""
import argparse
import io
import json
import os
from pathlib import Path
import re
import sqlite3
import subprocess
import tarfile
import tempfile
import time
import urllib.parse
import urllib.request
import uuid

PACKAGE = "io.github.fermin214.moerise.fixture"
RUNNER = PACKAGE + ".test/androidx.test.runner.AndroidJUnitRunner"
SOURCE_ROOT = Path(r"C:\Projects\Moerise")


def assert_queue(source_root, output):
    if os.name != "nt":
        return
    if Path(source_root).resolve() != SOURCE_ROOT.resolve():
        raise ValueError("SourceRoot 必须为 C:\\Projects\\Moerise")
    environment = dict(os.environ)
    environment["MOERISE_RECOVERY_LOCK_CHECK"] = str(Path(__file__).with_name("device-lock.ps1").resolve())
    check = subprocess.run([
        "pwsh", "-NoProfile", "-Command",
        ". $env:MOERISE_RECOVERY_LOCK_CHECK; "
        "$record = Assert-MoeriseDeviceLock 'C:\\Projects\\Moerise'; $record.output_root",
    ], env=environment, capture_output=True, text=True)
    if check.returncode:
        raise RuntimeError("需要主仓库 with-device-lock.ps1 的持锁进程执行")
    if Path(output).resolve() != Path(check.stdout.strip()).resolve():
        raise ValueError("结果目录必须与设备锁记录一致")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--serial", required=True)
    parser.add_argument("--adb", default="adb")
    parser.add_argument("--fixture", required=True)
    parser.add_argument("--source-root", default=str(SOURCE_ROOT))
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    assert_queue(args.source_root, args.output)
    if not re.fullmatch(r"[A-Za-z0-9_.:-]+", args.serial):
        raise ValueError("设备序列号无效")
    fixture = json.loads(Path(args.fixture).read_text(encoding="utf-8-sig"))
    origin = urllib.parse.urlsplit(fixture["origin"])
    source = urllib.parse.urlsplit(fixture["sourceOrigin"])
    for endpoint in (origin, source):
        if (endpoint.scheme != "http" or endpoint.hostname not in ("127.0.0.1", "localhost", "::1")
                or endpoint.path not in ("", "/") or endpoint.query or endpoint.fragment or endpoint.username):
            raise ValueError("恢复流程只使用回环 HTTP 隔离服务")
    if fixture["username"] != "android-owner":
        raise ValueError("恢复流程只使用隔离账号")
    adb = [args.adb, "-s", args.serial]
    marker = uuid.uuid4().hex
    saved = [f"Saved offline {marker}_one", f"Saved offline {marker}_two"]
    unfinished = f"Unsent input {marker}"
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    report = {"marker": marker, "serial": args.serial, "started_at": time.time(), "stages": []}

    def command(*arguments, binary=False):
        process = subprocess.run([*adb, *arguments], capture_output=True, timeout=180)
        if process.returncode:
            raise RuntimeError(f"ADB {arguments[0]} 失败，退出码 {process.returncode}")
        return process.stdout if binary else process.stdout.decode("utf-8", errors="replace")

    def instrument(phase):
        text = command("shell", "am", "instrument", "-w", "-e", "class",
                       "io.github.fermin214.moerise.OfflineQueueTest", "-e", "recoveryPhase", phase,
                       "-e", "markerId", marker, RUNNER)
        (output / f"recovery-{phase}.log").write_text(text, encoding="utf-8")
        if not re.search(r"^OK \([1-9][0-9]* tests?\)", text, re.M) or re.search(
                r"FAILURES!!!|INSTRUMENTATION_FAILED", text):
            raise AssertionError(f"设备恢复阶段 {phase} 失败")
        report["stages"].append(phase)

    def persisted():
        archive = command("exec-out", "run-as", PACKAGE, "tar", "-C", "no_backup/data", "-cf", "-", ".", binary=True)
        # 只复制固定文件名，不按 tar 中的路径解包；WAL 与数据库一起读取。
        with tempfile.TemporaryDirectory(prefix="moerise-recovery-") as directory:
            with tarfile.open(fileobj=io.BytesIO(archive)) as files:
                for filename in ("moerise.sqlite3", "moerise.sqlite3-wal"):
                    member = next((entry for entry in files.getmembers()
                                   if entry.name in (filename, "./" + filename) and entry.isfile()), None)
                    if member:
                        (Path(directory) / filename).write_bytes(files.extractfile(member).read())
            database_path = Path(directory) / "moerise.sqlite3"
            if not database_path.is_file():
                raise AssertionError("隔离包数据库不可读")
            with sqlite3.connect(database_path) as database:
                contents = [row[0] for row in database.execute(
                    "SELECT content FROM drafts WHERE content IN (?, ?, ?)", (*saved, unfinished))]
                home = database.execute("SELECT content FROM drafts WHERE slot = 'home' AND content = ?",
                                        (unfinished,)).fetchone()
            return all(text in contents for text in saved) and home is not None

    def wait_persisted():
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            try:
                if persisted():
                    report["stages"].append("sqlite_wal_persisted")
                    return
            except (sqlite3.Error, tarfile.TarError, OSError):
                pass
            time.sleep(.3)
        raise AssertionError("离线队列和未完成输入没有同时落盘")

    def remote_counts():
        counts = {text: 0 for text in (*saved, unfinished)}
        page_token = ""
        seen_tokens = set()
        for _ in range(100):
            query = urllib.parse.urlencode({"pageSize": 100, **({"pageToken": page_token} if page_token else {})})
            request = urllib.request.Request(fixture["sourceOrigin"] + "/api/v1/memos?" + query,
                                             headers={"Authorization": "Bearer " + fixture["sourceToken"]})
            # 本机回环请求不经过系统代理，异常不带请求头或凭据。
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            try:
                with opener.open(request, timeout=15) as response:
                    page = json.load(response)
            except Exception:
                raise RuntimeError("Memos 原文读取失败") from None
            for note in page.get("memos", []):
                if note.get("content") in counts:
                    counts[note["content"]] += 1
            page_token = page.get("nextPageToken", "")
            if not page_token:
                return counts
            if page_token in seen_tokens:
                raise AssertionError("Memos 分页游标重复")
            seen_tokens.add(page_token)
            time.sleep(.5)
        raise AssertionError("Memos 分页超过上限")

    def check_counts(counts):
        if counts[unfinished] != 0 or any(counts[text] > 1 for text in saved):
            raise AssertionError("未保存输入被上传或保存笔记重复创建")
        return all(counts[text] == 1 for text in saved)

    mapping = f"tcp:{origin.port or 80}"
    existing = command("reverse", "--list")
    if not any(line.split()[-2:] == [mapping, mapping] for line in existing.splitlines()):
        raise ValueError("先建立该隔离服务的 ADB reverse 映射")
    disconnected = False
    failure = None
    try:
        command("reverse", "--remove", mapping)
        disconnected = True
        instrument("prepare")
        command("shell", "am", "start", "-W", "-n", PACKAGE + "/io.github.fermin214.moerise.MainActivity")
        if not command("shell", "pidof", PACKAGE).strip():
            raise AssertionError("落盘确认前应用进程没有运行")
        wait_persisted()
        command("shell", "am", "force-stop", PACKAGE)
        instrument("offlineRestart")
        wait_persisted()
        command("shell", "am", "start", "-W", "-n", PACKAGE + "/io.github.fermin214.moerise.MainActivity")
        command("shell", "input", "keyevent", "KEYCODE_HOME")
        # 后台恢复阶段不使用 force-stop：Android 会禁止被强停包的系统任务自动启动。
        command("reverse", mapping, mapping)
        disconnected = False
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            counts = remote_counts()
            if check_counts(counts):
                break
            time.sleep(3)
        else:
            raise AssertionError("恢复连接后后台队列没有自动排空")
        report["stages"].append("background_auto_upload")
        command("shell", "am", "force-stop", PACKAGE)
        instrument("onlineRestart")
        time.sleep(3)
        if not check_counts(remote_counts()):
            raise AssertionError("重开后服务器笔记数量不一致")
        report["stages"].append("remote_exactly_once_and_unfinished_absent")
        report["saved_remote_count"] = 2
        report["unfinished_remote_count"] = 0
        print("离线入队、SQLite/WAL落盘、杀进程重开、后台自动上传和远端无重复通过。")
    except Exception as error:
        failure = error
        raise
    finally:
        cleanup_errors = []
        if disconnected:
            try:
                command("reverse", mapping, mapping)
            except Exception:
                cleanup_errors.append("ADB reverse 恢复失败")
        try:
            command("shell", "rm", "-f", "/data/local/tmp/moerise-android-fixture.json")
        except Exception:
            cleanup_errors.append("设备连接档案清理失败")
        report["finished_at"] = time.time()
        report["status"] = "failed" if failure or cleanup_errors else "succeeded"
        report["cleanup_errors"] = cleanup_errors
        (output / "recovery.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        if cleanup_errors and failure is None:
            raise RuntimeError("；".join(cleanup_errors))


if __name__ == "__main__":
    main()
