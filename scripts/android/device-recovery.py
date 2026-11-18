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


def error_details(error):
    # 异常消息、命令参数和 stderr 可能包含正文或凭据，只记录类型和数值错误码。
    details = {"error_type": type(error).__name__}
    for attribute in ("errno", "sqlite_errorcode"):
        value = getattr(error, attribute, None)
        if isinstance(value, int):
            details[attribute] = value
    return details


class CommandError(RuntimeError):
    def __init__(self, details):
        super().__init__("宿主命令失败，见结构化诊断和阶段日志")
        self.details = details


def run_command(adb, arguments, *, binary=False, log=None):
    def write_log(stdout, stderr):
        if log is None or binary:
            return

        def decode(value):
            return value.decode("utf-8", errors="replace") if isinstance(value, bytes) else (value or "")

        text = decode(stdout)
        if stderr:
            text += "\n--- stderr ---\n" + decode(stderr)
        Path(log).write_text(text, encoding="utf-8")

    try:
        process = subprocess.run([*adb, *arguments], capture_output=True, timeout=180)
    except subprocess.TimeoutExpired as error:
        write_log(error.stdout, error.stderr)
        raise CommandError({"reason": "command-timeout", "timeout_seconds": 180,
                            **error_details(error)}) from None
    except OSError as error:
        raise CommandError({"reason": "command-start-failed", **error_details(error)}) from None
    write_log(process.stdout, process.stderr)
    if process.returncode:
        raise CommandError({"reason": "command-exit", "exit_code": process.returncode})
    return process.stdout if binary else process.stdout.decode("utf-8", errors="replace")


def persisted(command, saved, unfinished):
    """读取临时 SQLite/WAL 副本，只返回文件元数据和匹配结果。"""
    diagnostic = {"status": "harness-error", "step": "read"}
    try:
        archive = command("exec-out", "run-as", PACKAGE, "tar", "-C", "no_backup/data",
                          "-cf", "-", ".", binary=True)
    except CommandError as error:
        return {**diagnostic, **error.details}
    except (OSError, subprocess.TimeoutExpired) as error:
        return {**diagnostic, "reason": "archive-read-failed", **error_details(error)}

    diagnostic.update(step="host-files", archive_bytes=len(archive), files={})
    try:
        with tempfile.TemporaryDirectory(prefix="moerise-recovery-") as directory:
            # 只复制固定文件名，不按 tar 中的路径解包；不保留数据库或 WAL。
            diagnostic["step"] = "archive"
            with tarfile.open(fileobj=io.BytesIO(archive)) as files:
                for filename in ("moerise.sqlite3", "moerise.sqlite3-wal"):
                    members = [entry for entry in files.getmembers()
                               if entry.name in (filename, "./" + filename)]
                    if not members:
                        diagnostic["files"][filename] = {"present": False}
                        continue
                    if len(members) != 1 or not members[0].isfile():
                        return {**diagnostic, "reason": "invalid-database-member"}
                    member = members[0]
                    diagnostic["files"][filename] = {"present": True, "bytes": member.size}
                    # extractfile 读取可能抛出 tar 异常；本机写入失败属于 host-files。
                    data = files.extractfile(member).read()
                    if len(data) != member.size:
                        return {**diagnostic, "reason": "truncated-database-member"}
                    diagnostic["step"] = "host-files"
                    (Path(directory) / filename).write_bytes(data)
                    diagnostic["step"] = "archive"
            database_path = Path(directory) / "moerise.sqlite3"
            if not database_path.is_file():
                return {**diagnostic, "reason": "database-member-missing"}
            diagnostic["step"] = "database"
            # mode=ro 防止缺失数据库被自动创建；不使用 immutable，以便 SQLite 读取 WAL。
            database = sqlite3.connect(database_path.as_uri() + "?mode=ro", uri=True)
            try:
                database.execute("BEGIN")
                saved_present = [database.execute(
                    "SELECT EXISTS(SELECT 1 FROM drafts WHERE content = ?)", (text,)
                ).fetchone()[0] == 1 for text in saved]
                home_present = database.execute(
                    "SELECT EXISTS(SELECT 1 FROM drafts WHERE slot = 'home' AND content = ?)",
                    (unfinished,)
                ).fetchone()[0] == 1
            finally:
                # Connection 的上下文管理器不关闭句柄；Windows 删除临时副本前必须关闭。
                database.close()
            diagnostic["observed"] = {
                "saved_drafts_present": saved_present,
                "unfinished_home_present": home_present,
            }
            diagnostic["status"] = "passed" if all(saved_present) and home_present else "assertion-failed"
            if diagnostic["status"] == "assertion-failed":
                diagnostic["reason"] = "expected-drafts-missing"
            diagnostic["step"] = "host-files"
        diagnostic["step"] = "database"
        return diagnostic
    except (sqlite3.Error, tarfile.TarError, OSError) as error:
        return {**diagnostic, "status": "harness-error", "reason": "snapshot-read-failed",
                **error_details(error)}


def wait_persisted(command, report, checkpoint, saved, unfinished, *, timeout=20):
    check = {"checkpoint": checkpoint, "attempts": []}
    report.setdefault("persistence_checks", []).append(check)
    deadline = time.monotonic() + timeout
    while True:
        result = persisted(command, saved, unfinished)
        check["attempts"].append(result)
        if result["status"] == "passed":
            check["status"] = "passed"
            report["stages"].append("sqlite_wal_persisted")
            return
        if time.monotonic() >= deadline:
            break
        time.sleep(.3)
    # 以最后一次采样定性：早先缺失而后读取失败，无法判断是否已完成落盘。
    if result["status"] == "harness-error":
        check.update(status="inconclusive", reason="harness-error")
        print(f"SQLite/WAL 辅助检查 {checkpoint} 无结论：宿主读取故障；继续端到端验证。", flush=True)
        return
    check["status"] = "assertion-failed"
    missing = [str(index + 1) for index, present in enumerate(result["observed"]["saved_drafts_present"])
               if not present]
    absent = []
    if missing:
        absent.append("已保存草稿编号 " + "、".join(missing))
    if not result["observed"]["unfinished_home_present"]:
        absent.append("home 槽未完成输入")
    raise AssertionError(f"SQLite/WAL 辅助检查 {checkpoint}：等待后成功读取的快照缺少" + "；".join(absent))


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
    if not Path(output).resolve().is_relative_to(Path(check.stdout.strip()).resolve()):
        raise ValueError("结果目录必须位于设备锁记录的目录内")


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
    report = {"marker": marker, "serial": args.serial, "started_at": time.time(), "stages": [],
              "end_to_end_status": "incomplete"}

    def command(*arguments, binary=False, log=None):
        return run_command(adb, arguments, binary=binary, log=log)

    def instrument(phase):
        print(f"设备恢复阶段 {phase} 开始。", flush=True)
        text = command("shell", "am", "instrument", "-w", "-e", "class",
                       "io.github.fermin214.moerise.OfflineQueueTest", "-e", "recoveryPhase", phase,
                       "-e", "markerId", marker, "-e", "timeout_msec", "120000", RUNNER,
                       log=output / f"recovery-{phase}.log")
        if re.search(r"INSTRUMENTATION_FAILED|Unable to find instrumentation|ClassNotFoundException", text):
            raise CommandError({"reason": "runner-unavailable"})
        if not re.search(r"^OK \([1-9][0-9]* tests?\)|FAILURES!!!|"
                         r"INSTRUMENTATION_STATUS: (?:stack=|failure=)", text, re.M):
            raise CommandError({"reason": "runner-result-incomplete"})
        if not re.search(r"^OK \([1-9][0-9]* tests?\)", text, re.M) or re.search(
                r"FAILURES!!!|INSTRUMENTATION_FAILED", text):
            raise AssertionError(f"设备恢复阶段 {phase} 失败")
        report["stages"].append(phase)
        print(f"设备恢复阶段 {phase} 通过。", flush=True)

    def background_diagnostics(label):
        for name, arguments in {
            "jobs": ("shell", "dumpsys", "jobscheduler", PACKAGE),
            "connectivity": ("shell", "dumpsys", "connectivity"),
            "services": ("shell", "dumpsys", "activity", "services", PACKAGE),
        }.items():
            log = output / f"background-{label}-{name}.log"
            try:
                command(*arguments, log=log)
            except Exception as error:
                if not log.exists():
                    log.write_text(type(error).__name__, encoding="utf-8")
        try:
            pid = command("shell", "pidof", PACKAGE).strip()
            if re.fullmatch(r"[0-9]+", pid):
                command("shell", "logcat", "-d", "-v", "threadtime", "-t", "1000", "--pid=" + pid,
                        log=output / f"background-{label}-app.log")
        except Exception:
            pass

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
    disconnected = False
    failure = None
    try:
        existing = command("reverse", "--list")
        if not any(line.split()[-2:] == [mapping, mapping] for line in existing.splitlines()):
            raise ValueError("先建立该隔离服务的 ADB reverse 映射")
        command("reverse", "--remove", mapping)
        disconnected = True
        instrument("prepare")
        command("shell", "am", "start", "-W", "-n", PACKAGE + "/io.github.fermin214.moerise.MainActivity")
        if not command("shell", "pidof", PACKAGE).strip():
            raise AssertionError("落盘确认前应用进程没有运行")
        wait_persisted(command, report, "after_prepare", saved, unfinished)
        command("shell", "am", "force-stop", PACKAGE)
        instrument("offlineRestart")
        wait_persisted(command, report, "after_offline_restart", saved, unfinished)
        command("shell", "am", "start", "-W", "-n", PACKAGE + "/io.github.fermin214.moerise.MainActivity")
        command("shell", "input", "keyevent", "KEYCODE_HOME")
        # 后台恢复阶段不使用 force-stop：Android 会禁止被强停包的系统任务自动启动。
        command("reverse", mapping, mapping)
        disconnected = False
        background_diagnostics("start")
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            counts = remote_counts()
            if check_counts(counts):
                break
            time.sleep(3)
        else:
            report["remote_counts"] = {
                "saved": [counts[text] for text in saved], "unfinished": counts[unfinished],
            }
            background_diagnostics("timeout")
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
        report["end_to_end_status"] = "succeeded"
    except Exception as error:
        failure = error
        report["failure"] = {
            "category": "assertion" if isinstance(error, AssertionError) else "harness-error",
            **(error.details if isinstance(error, CommandError) else error_details(error)),
        }
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
        inconclusive = any(check.get("status") == "inconclusive"
                           for check in report.get("persistence_checks", []))
        report["status"] = "failed" if failure or cleanup_errors else (
            "inconclusive" if inconclusive else "succeeded")
        report["cleanup_errors"] = cleanup_errors
        (output / "recovery.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        if cleanup_errors and failure is None:
            raise RuntimeError("；".join(cleanup_errors))
    if inconclusive:
        print("用户可见端到端验证通过；SQLite/WAL 辅助检查无结论（harness-error），见 recovery.json。")
        return 2
    print("离线入队、SQLite/WAL落盘、杀进程重开、后台自动上传和远端无重复通过。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
