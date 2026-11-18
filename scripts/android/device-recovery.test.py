#!/usr/bin/env python3
"""宿主合成数据回归；所有子进程和网络请求均被替换。"""
from contextlib import redirect_stdout
import importlib.util
import io
import json
from pathlib import Path
import sqlite3
import subprocess
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch
import uuid

SCRIPT = Path(__file__).with_name("device-recovery.py")
spec = importlib.util.spec_from_file_location("device_recovery", SCRIPT)
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)
SCHEMA = SCRIPT.parents[2] / "mobile/shared/schemas/io.github.fermin214.moerise.data.LocalDatabase/1.json"
MARKER = "a" * 32
SAVED = [f"Saved offline {MARKER}_one", f"Saved offline {MARKER}_two"]
UNFINISHED = f"Unsent input {MARKER}"
SECRET = "private-fixture-credential"
DB_NAME = "moerise.sqlite3"


def archive_files(files):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w") as archive:
        for name, data in files.items():
            member = tarfile.TarInfo("./" + name)
            member.size = len(data)
            archive.addfile(member, io.BytesIO(data))
    return output.getvalue()


def snapshot_files(*, contents=None, home_slot="home", wal=False):
    if contents is None:
        contents = [*SAVED, UNFINISHED]
    with tempfile.TemporaryDirectory(prefix="moerise-synthetic-") as directory:
        path = Path(directory) / DB_NAME
        database = sqlite3.connect(path)
        try:
            if wal:
                assert database.execute("PRAGMA journal_mode=WAL").fetchone()[0] == "wal"
                database.execute("PRAGMA wal_autocheckpoint=0")
            for entity in json.loads(SCHEMA.read_text(encoding="utf-8"))["database"]["entities"]:
                database.execute(entity["createSql"].replace("${TABLE_NAME}", entity["tableName"]))
            database.commit()
            if wal:
                database.execute("PRAGMA wal_checkpoint(TRUNCATE)")
            for index, content in enumerate(contents):
                slot = home_slot if content == UNFINISHED else f"queued:{index}"
                database.execute(
                    "INSERT INTO drafts (id, accountKey, content, revision, attachmentsJson, slot) "
                    "VALUES (?, ?, ?, 1, '[]', ?)", (str(index), "synthetic-owner", content, slot))
            database.commit()
            files = {DB_NAME: path.read_bytes()}
            if wal:
                files[DB_NAME + "-wal"] = path.with_name(DB_NAME + "-wal").read_bytes()
            return files
        finally:
            database.close()


class RecoveryEvidenceTest(unittest.TestCase):
    def setUp(self):
        # 任一遗漏的替换都会立即失败，禁止测试意外调用 ADB、设备锁或真实 HTTP。
        self.process_guard = patch.object(recovery.subprocess, "run",
                                          side_effect=AssertionError("禁止执行外部进程"))
        self.network_guard = patch.object(recovery.urllib.request.OpenerDirector, "open",
                                          side_effect=AssertionError("禁止真实网络请求"))
        self.process_guard.start()
        self.network_guard.start()
        self.addCleanup(self.process_guard.stop)
        self.addCleanup(self.network_guard.stop)

    def inspect(self, files):
        return recovery.persisted(Mock(return_value=archive_files(files)), SAVED, UNFINISHED)

    def assert_redacted(self, report):
        text = json.dumps(report, ensure_ascii=False)
        for secret in (*SAVED, UNFINISHED, SECRET, "SQLite format 3"):
            self.assertNotIn(secret, text)

    def test_committed_wal_is_read_and_database_alone_lacks_rows(self):
        files = snapshot_files(wal=True)
        self.assertGreater(len(files[DB_NAME + "-wal"]), 32)
        result = self.inspect(files)
        self.assertEqual(result["status"], "passed")
        self.assertEqual(result["observed"], {
            "saved_drafts_present": [True, True], "unfinished_home_present": True,
        })
        self.assertEqual(self.inspect({DB_NAME: files[DB_NAME]})["observed"], {
            "saved_drafts_present": [False, False], "unfinished_home_present": False,
        })
        self.assert_redacted(result)

    def test_database_without_wal_can_pass(self):
        result = self.inspect(snapshot_files())
        self.assertEqual(result["status"], "passed")
        self.assertEqual(result["files"][DB_NAME + "-wal"], {"present": False})

    def test_read_archive_database_and_host_errors_are_not_assertions(self):
        cases = [
            ("read", Mock(side_effect=OSError(13, SECRET))),
            ("read", Mock(side_effect=recovery.CommandError({"reason": "command-exit", "exit_code": 1}))),
            ("read", Mock(side_effect=subprocess.TimeoutExpired(SECRET, 180, output=SECRET.encode()))),
            ("archive", Mock(return_value=b"not a tar archive")),
            ("archive", Mock(return_value=archive_files({}))),
            ("database", Mock(return_value=archive_files({DB_NAME: b"not a database"}))),
        ]
        # 有效 SQLite 但缺 drafts 表属于 schema/SQL 故障。
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / DB_NAME
            database = sqlite3.connect(path)
            database.execute("CREATE TABLE unrelated (value TEXT)")
            database.commit()
            database.close()
            cases.append(("database", Mock(return_value=archive_files({DB_NAME: path.read_bytes()}))))
        for step, command in cases:
            with self.subTest(step=step, command=command):
                report = {"stages": []}
                with redirect_stdout(io.StringIO()):
                    recovery.wait_persisted(command, report, "after_prepare", SAVED, UNFINISHED, timeout=0)
                check = report["persistence_checks"][0]
                self.assertEqual(check["status"], "inconclusive")
                self.assertEqual(check["reason"], "harness-error")
                self.assertEqual(check["attempts"][0]["step"], step)
                self.assertEqual(check["attempts"][0]["status"], "harness-error")
                self.assertEqual(report["stages"], [])
                self.assert_redacted(report)
        with patch.object(recovery.tempfile, "TemporaryDirectory", side_effect=OSError(13, SECRET)):
            result = self.inspect({})
        self.assertEqual((result["status"], result["step"]), ("harness-error", "host-files"))
        files = snapshot_files()
        with patch.object(Path, "write_bytes", side_effect=OSError(13, SECRET)):
            result = self.inspect(files)
        self.assertEqual((result["status"], result["step"]), ("harness-error", "host-files"))
        self.assert_redacted(result)

    def test_successful_read_reports_missing_saved_draft_and_home_slot(self):
        files = snapshot_files(contents=[SAVED[0], UNFINISHED], home_slot="saved:other")
        report = {"stages": []}
        with self.assertRaisesRegex(AssertionError, "成功读取的快照缺少已保存草稿编号 2；home 槽未完成输入"):
            recovery.wait_persisted(Mock(return_value=archive_files(files)), report,
                                    "after_prepare", SAVED, UNFINISHED, timeout=0)
        check = report["persistence_checks"][0]
        self.assertEqual(check["status"], "assertion-failed")
        self.assertEqual(check["attempts"][0]["observed"], {
            "saved_drafts_present": [True, False], "unfinished_home_present": False,
        })
        self.assertEqual(report["stages"], [])
        self.assert_redacted(report)

    def test_retry_retains_errors_then_valid_evidence_can_pass(self):
        command = Mock(side_effect=[OSError(13, SECRET), archive_files(snapshot_files())])
        report = {"stages": []}
        with patch.object(recovery.time, "sleep"):
            recovery.wait_persisted(command, report, "after_prepare", SAVED, UNFINISHED)
        check = report["persistence_checks"][0]
        self.assertEqual(check["status"], "passed")
        self.assertEqual([item["status"] for item in check["attempts"]], ["harness-error", "passed"])
        self.assertEqual(report["stages"], ["sqlite_wal_persisted"])

    def test_old_missing_sample_followed_by_read_failure_is_inconclusive(self):
        command = Mock(side_effect=[archive_files(snapshot_files(contents=[])), OSError(13, SECRET)])
        report = {"stages": []}
        with patch.object(recovery.time, "monotonic", side_effect=[0, 0, 21]), \
                patch.object(recovery.time, "sleep"), redirect_stdout(io.StringIO()):
            recovery.wait_persisted(command, report, "after_prepare", SAVED, UNFINISHED)
        check = report["persistence_checks"][0]
        self.assertEqual(check["status"], "inconclusive")
        self.assertEqual([item["status"] for item in check["attempts"]],
                         ["assertion-failed", "harness-error"])

    def test_timeout_keeps_partial_stdout_and_stderr_but_not_in_diagnostic(self):
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "recovery-prepare.log"
            error = subprocess.TimeoutExpired(["fake-adb", SECRET], 180,
                                              output=b"partial instrumentation\n", stderr=SECRET.encode())
            with patch.object(recovery.subprocess, "run", side_effect=error), \
                    self.assertRaises(recovery.CommandError) as caught:
                recovery.run_command(["fake-adb"], ["shell", "am", "instrument"], log=log)
            self.assertIn("partial instrumentation", log.read_text(encoding="utf-8"))
            self.assertIn(SECRET, log.read_text(encoding="utf-8"))
            self.assertEqual(caught.exception.details["reason"], "command-timeout")
            self.assert_redacted(caught.exception.details)
            binary_log = Path(directory) / "archive.log"
            with patch.object(recovery.subprocess, "run", side_effect=error):
                result = recovery.persisted(
                    lambda *args, **kwargs: recovery.run_command(["fake-adb"], args, log=binary_log, **kwargs),
                    SAVED, UNFINISHED)
            self.assertFalse(binary_log.exists())
            self.assertEqual((result["status"], result["step"]), ("harness-error", "read"))

    def run_host_flow(self, *, archive_error=False, instrumentation_timeout=False,
                      remote_timeout=False, missing_content=False, instrumentation_output=None):
        """运行主流程的宿主替身，保留真实 JSON 落盘、检查和状态汇总逻辑。"""
        calls = []
        files = snapshot_files(contents=[SAVED[0]] if missing_content else None)
        clock = iter(range(0, 10000, 21))

        def fake_process(arguments, **kwargs):
            calls.append(arguments)
            if "instrument" in arguments:
                phase = arguments[arguments.index("recoveryPhase") + 1]
                if instrumentation_timeout and phase == "prepare":
                    raise subprocess.TimeoutExpired(arguments, 180, output=b"prepare partial output\n")
                stdout = instrumentation_output if instrumentation_output is not None else b"OK (1 test)\n"
            elif arguments[-2:] == ["reverse", "--list"]:
                stdout = b"synthetic tcp:18080 tcp:18080\n"
            elif "exec-out" in arguments:
                if archive_error:
                    raise OSError(13, SECRET)
                stdout = archive_files(files)
            elif "pidof" in arguments:
                stdout = b"1234\n"
            else:
                stdout = b""
            return subprocess.CompletedProcess(arguments, 0, stdout, b"")

        def fake_open(*args, **kwargs):
            memos = [] if remote_timeout else [{"content": text} for text in SAVED]
            return io.BytesIO(json.dumps({"memos": memos}).encode())

        with tempfile.TemporaryDirectory(prefix="moerise-host-flow-") as directory:
            output = Path(directory) / "output"
            fixture = Path(directory) / "fixture.json"
            fixture.write_text(json.dumps({
                "origin": "http://127.0.0.1:18080", "sourceOrigin": "http://127.0.0.1:18081",
                "username": "android-owner", "sourceToken": SECRET, "password": SECRET,
            }), encoding="utf-8")
            arguments = [str(SCRIPT), "--serial", "synthetic", "--fixture", str(fixture),
                         "--output", str(output)]
            opener = Mock()
            opener.open.side_effect = fake_open
            with patch("sys.argv", arguments), patch.object(recovery, "assert_queue"), \
                    patch.object(recovery.uuid, "uuid4", return_value=uuid.UUID(MARKER)), \
                    patch.object(recovery.subprocess, "run", side_effect=fake_process), \
                    patch.object(recovery.urllib.request, "build_opener", return_value=opener), \
                    patch.object(recovery.time, "monotonic", side_effect=lambda: next(clock)), \
                    patch.object(recovery.time, "sleep"), redirect_stdout(io.StringIO()) as console:
                if instrumentation_timeout or instrumentation_output is not None:
                    with self.assertRaises(recovery.CommandError):
                        recovery.main()
                    if instrumentation_timeout:
                        self.assertIn("prepare partial output",
                                      (output / "recovery-prepare.log").read_text(encoding="utf-8"))
                    exit_code = None
                elif remote_timeout:
                    with self.assertRaisesRegex(AssertionError, "后台队列没有自动排空"):
                        recovery.main()
                    exit_code = None
                elif missing_content:
                    with self.assertRaisesRegex(AssertionError, "成功读取的快照缺少"):
                        recovery.main()
                    exit_code = None
                else:
                    exit_code = recovery.main()
            report = json.loads((output / "recovery.json").read_text(encoding="utf-8"))
            self.assert_redacted(report)
            self.assertFalse(list(output.glob("*.sqlite3*")))
            return report, exit_code, calls, console.getvalue()

    def test_read_fault_allows_restart_and_background_flow_without_false_success(self):
        report, exit_code, calls, console = self.run_host_flow(archive_error=True)
        self.assertEqual(exit_code, 2)
        self.assertEqual(report["status"], "inconclusive")
        self.assertEqual(report["end_to_end_status"], "succeeded")
        self.assertEqual([check["status"] for check in report["persistence_checks"]],
                         ["inconclusive", "inconclusive"])
        self.assertEqual(report["stages"], [
            "prepare", "offlineRestart", "background_auto_upload", "onlineRestart",
            "removeAccount", "afterRemoval", "remote_exactly_once_and_unfinished_absent",
        ])
        self.assertEqual(sum("force-stop" in call for call in calls), 3)
        self.assertIn("KEYCODE_HOME", [argument for call in calls for argument in call])
        self.assertNotIn("SQLite/WAL落盘", console)

    def test_complete_readable_evidence_can_succeed(self):
        report, exit_code, _, _ = self.run_host_flow()
        self.assertEqual((exit_code, report["status"]), (0, "succeeded"))
        self.assertEqual(report["stages"].count("sqlite_wal_persisted"), 2)

    def test_instrumentation_timeout_records_harness_error_and_partial_log(self):
        report, _, _, _ = self.run_host_flow(instrumentation_timeout=True)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["failure"]["category"], "harness-error")
        self.assertEqual(report["failure"]["reason"], "command-timeout")
        self.assertNotIn("prepare", report["stages"])

    def test_missing_or_incomplete_runner_is_a_harness_failure(self):
        for output, reason in (
            (b"INSTRUMENTATION_FAILED: Unable to find instrumentation\n", "runner-unavailable"),
            (b"INSTRUMENTATION_STATUS: acceptance_step=prepare\n", "runner-result-incomplete"),
        ):
            with self.subTest(reason=reason):
                report, _, _, _ = self.run_host_flow(instrumentation_output=output)
                self.assertEqual(report["failure"]["category"], "harness-error")
                self.assertEqual(report["failure"]["reason"], reason)
                self.assertNotIn("prepare", report["stages"])

    def test_windows_queue_accepts_child_output_and_rejects_other_directory(self):
        root = recovery.SOURCE_ROOT / ".local/android/synthetic-lock"
        process = subprocess.CompletedProcess([], 0, str(root) + "\n", "")
        with patch.object(recovery, "os", SimpleNamespace(name="nt", environ=recovery.os.environ)), \
                patch.object(recovery.subprocess, "run", return_value=process):
            recovery.assert_queue(recovery.SOURCE_ROOT, root / "device-recovery")
            with self.assertRaisesRegex(ValueError, "目录内"):
                recovery.assert_queue(recovery.SOURCE_ROOT, root.with_name("unrelated"))

    def test_background_timeout_counts_do_not_include_note_text(self):
        report, _, _, _ = self.run_host_flow(archive_error=True, remote_timeout=True)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["failure"]["category"], "assertion")
        self.assertEqual(report["remote_counts"], {"saved": [0, 0], "unfinished": 0})

    def test_missing_content_fails_report_with_explicit_assertion(self):
        report, _, _, _ = self.run_host_flow(missing_content=True)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["failure"]["category"], "assertion")
        check = report["persistence_checks"][0]
        self.assertEqual(check["status"], "assertion-failed")
        self.assertEqual(check["attempts"][0]["reason"], "expected-drafts-missing")
        self.assertNotIn("offlineRestart", report["stages"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
