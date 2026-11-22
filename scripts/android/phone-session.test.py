"""Host-only regression checks for device lifecycle and failure attribution."""
import importlib.util
import json
from pathlib import Path
import tempfile
import time
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("phone_session", Path(__file__).with_name("phone-session.py"))
session = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(session)


class FakeAdb:
    serial = "test-device"
    executable = "not-a-real-adb"

    def __init__(self, repo):
        self.repo = repo
        self.installed = {}
        self.calls = []
        self.mappings = set()
        self.fail_runner_install = False
        self.network = {key: "null" for key in session.NETWORK_KEYS}
        self.next_instrumentation = "OK (1 test)\n"

    def run(self, *args, timeout=30, log=None, binary=False, check=True):
        self.calls.append(args)
        text = ""
        if args == ("get-state",):
            return "device"
        if args[:4] == ("shell", "settings", "get", "global"):
            return self.network[args[4]]
        if args[:3] == ("shell", "pm", "path"):
            return "package:/test/" + args[3] + ".apk\r\n" if args[3] in self.installed else ""
        if args[0] == "pull":
            package = Path(args[1]).name.removesuffix(".apk")
            Path(args[2]).write_bytes(self.installed[package])
        elif args[0] == "install":
            apk = Path(args[-1])
            package = next(key for key, path in session.APK_PATHS.items() if apk == self.repo / path)
            if self.fail_runner_install and package == session.RUNNER_APP:
                raise session.CommandError("adb_error", "cancelled", "INSTALL_FAILED_USER_RESTRICTED")
            self.installed[package] = apk.read_bytes()
            text = "Success\n"
        elif args[0] == "uninstall":
            self.installed.pop(args[1], None)
            text = "Success\n"
        elif args == ("reverse", "--list"):
            text = "".join("device " + item + " " + item + "\n" for item in sorted(self.mappings))
        elif args[:2] == ("reverse", "--remove"):
            self.mappings.discard(args[2])
        elif args[0] == "reverse":
            self.mappings.add(args[1])
        elif args[:3] == ("shell", "am", "instrument"):
            text = self.next_instrumentation
        elif args[0] == "exec-out":
            raise session.CommandError("adb_error", "no diagnostics")
        if log:
            Path(log).write_text(text, encoding="utf-8")
        return text


class SessionLifecycle(unittest.TestCase):
    def setUp(self):
        output = Path(__file__).resolve().parents[2] / ".local/android"
        output.mkdir(parents=True, exist_ok=True)
        self.tmp = tempfile.TemporaryDirectory(prefix="phone-harness-test-", dir=output)
        self.root = Path(self.tmp.name)
        self.repo = self.root / "repo"
        artifacts = []
        for index, path in enumerate(session.APK_PATHS.values()):
            target = self.repo / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(("apk-" + str(index)).encode())
            artifacts.append({"path": path, "sha256": session.digest(target)})
        self.manifest = self.root / "artifacts.json"
        session.write_json(self.manifest, {"commit": "test-commit", "artifacts": artifacts})
        self.fixture = self.root / "connection.json"
        session.write_json(self.fixture, {"origin": "http://127.0.0.1:1501",
                                          "username": "android-owner", "password": "test-only"})
        self.adb = FakeAdb(self.repo)
        self.driver = session.PhoneSession(self.repo, self.root / "session", self.root / "output",
                                           self.adb, "test-commit")
        self.probe = mock.patch.object(session, "probe_fixture")
        self.probe.start()
        self.ui_calls = []
        self.next_ui_status = "passed"
        self.maestro = mock.patch.object(session, "MaestroRunner", side_effect=self.fake_maestro)
        self.maestro.start()

    def tearDown(self):
        self.probe.stop()
        self.maestro.stop()
        self.assertTrue(self.root.resolve().is_relative_to(Path(__file__).resolve().parents[2] / ".local/android"))
        for attempt in range(10):
            try:
                self.tmp.cleanup()
                break
            except OSError as error:
                if getattr(error, "winerror", None) not in (5, 32, 33, 145) or attempt == 9:
                    raise
                time.sleep(.05)

    def fake_maestro(self, repo, adb, fixture, output, executable):
        runner = mock.Mock(output=Path(output))

        def run_flow(flow, note_name=None):
            self.ui_calls.append((flow, note_name))
            return {"kind": "maestro", "status": self.next_ui_status,
                    "log": str(Path(output) / "console.log")}

        runner.run_flow.side_effect = run_flow
        runner.run_group.side_effect = lambda group: {group: run_flow(group)}
        return runner

    def prepare(self, specialists=False):
        self.driver.prepare(self.manifest, self.fixture, specialists=specialists)

    def installs(self):
        return [call for call in self.adb.calls if call[0] == "install"]

    def assert_no_data_or_network_mutation(self):
        self.assertFalse(any(call[:3] == ("shell", "pm", "clear") for call in self.adb.calls))
        self.assertFalse(any(call[:3] in (("shell", "settings", "put"),
                                         ("shell", "settings", "delete")) for call in self.adb.calls))

    def test_retry_after_install_confirmation_does_not_reinstall_completed_app(self):
        self.adb.fail_runner_install = True
        with self.assertRaisesRegex(session.CommandError, "手机确认"):
            self.prepare(specialists=True)
        self.assertIn(session.APP, self.adb.installed)
        self.adb.fail_runner_install = False
        self.prepare(specialists=True)
        self.assertEqual(sum(call[-1].endswith("androidApp-fixture.apk") for call in self.installs()), 1)
        self.assertEqual(len(self.installs()), 3)  # target once, runner rejected then accepted
        self.assert_no_data_or_network_mutation()

    def test_prepare_manual_and_rerun_keep_installed_app_and_data(self):
        self.prepare()
        self.prepare()
        self.driver.manual()
        self.assertEqual(len(self.installs()), 1)
        self.assertFalse(any(call[:3] == ("shell", "am", "instrument") for call in self.adb.calls))
        self.assertEqual(self.ui_calls, [("login", None)])
        self.assertFalse(any(call[0] == "uninstall" for call in self.adb.calls))
        self.assert_no_data_or_network_mutation()

    def test_prepare_resumes_after_login_checkpoint_before_ready_checkpoint(self):
        self.prepare()
        self.driver.state["status"] = "installed"
        self.driver.save()
        self.driver = session.PhoneSession(self.repo, self.root / "session", self.root / "resumed-output",
                                           self.adb, "test-commit")
        self.prepare()
        self.assertEqual(self.driver.state["status"], "ready")
        self.assertEqual(self.ui_calls, [("login", None)])
        self.assertEqual(len(self.installs()), 1)

    def test_incomplete_install_cannot_start_business_tests(self):
        self.adb.fail_runner_install = True
        with self.assertRaises(session.CommandError):
            self.prepare(specialists=True)
        with self.assertRaisesRegex(ValueError, "准备尚未完成"):
            self.driver.run(["editing"])
        self.assertFalse(any(call[:3] == ("shell", "am", "instrument") for call in self.adb.calls))

    def test_conflicting_usb_mapping_is_preserved(self):
        self.prepare()
        original = self.adb.run

        def conflicting(*args, **kwargs):
            if args == ("reverse", "--list"):
                return "device tcp:1501 tcp:9876\n"
            return original(*args, **kwargs)

        before = len(self.adb.calls)
        with mock.patch.object(self.adb, "run", side_effect=conflicting):
            with self.assertRaisesRegex(session.CommandError, "已有其他"):
                self.driver.manual()
        self.assertFalse(any(call[:1] == ("reverse",) for call in self.adb.calls[before:]))

    def test_run_reuses_success_and_retries_only_requested_failure(self):
        self.prepare()
        self.driver.run(["editing"])
        before = len(self.ui_calls)
        self.assertEqual(self.driver.run(["editing"])["editing"]["status"], "reused_pass")
        after = len(self.ui_calls)
        self.assertEqual(before, after)
        self.driver.run(["editing"], retry=True)
        self.assertEqual(len(self.ui_calls), after + 1)

    def test_changed_device_apk_blocks_run_without_automatic_install(self):
        self.prepare()
        self.adb.installed[session.APP] = b"another-version"
        with self.assertRaisesRegex(session.CommandError, "设备 APK"):
            self.driver.run(["editing"])
        self.assertEqual(len(self.installs()), 1)

    def test_reinstalling_missing_target_requires_login_and_invalidates_passes(self):
        self.prepare()
        self.driver.run(["editing"])
        self.adb.installed.pop(session.APP)
        self.prepare()
        self.assertEqual(self.ui_calls[-1], ("login", None))
        self.assertNotIn("editing", self.driver.state["results"])
        self.assertEqual(len(self.installs()), 2)

    def test_host_script_change_invalidates_pass_without_reinstall(self):
        self.prepare()
        script = self.repo / "scripts/android/maestro_runner.py"
        script.parent.mkdir(parents=True)
        script.write_text("# original\n", encoding="utf-8")
        self.driver.run(["editing"])
        script.write_text("# changed\n", encoding="utf-8")
        result = self.driver.run(["editing"])["editing"]
        self.assertEqual(result["status"], "passed")
        self.assertEqual(len(self.installs()), 1)

    def test_flow_only_commit_change_reuses_the_same_installed_apk(self):
        self.prepare()
        self.driver.commit = "a-new-flow-only-commit"
        self.driver.run(["search"])
        self.assertEqual(len(self.installs()), 1)
        self.assertEqual(self.driver.state["commit"], "test-commit")
        self.assertEqual(self.ui_calls[-1][0], "search")

    def test_global_proxy_blocks_prepare_before_install_without_rewriting_settings(self):
        self.adb.network["global_http_proxy_host"] = "127.0.0.1"
        self.adb.network["global_http_proxy_port"] = "28080"
        with self.assertRaisesRegex(session.CommandError, "全局代理"):
            self.prepare()
        self.assertFalse(self.installs())
        self.assert_no_data_or_network_mutation()

    def test_common_login_failure_does_not_fan_out_to_business_failures(self):
        self.prepare()
        self.next_ui_status = "environment_blocked"
        result = self.driver.run(["editing", "actions", "search"])
        self.assertEqual(list(result), ["editing"])
        self.assertEqual(result["editing"]["status"], "environment_blocked")

    def test_cleanup_is_explicit_and_keeps_preexisting_mapping(self):
        self.adb.mappings.add("tcp:1501")
        self.prepare()
        self.adb.installed["unrelated.app"] = b"keep"
        result = self.driver.cleanup()
        self.assertTrue(all(result["packages_removed"].values()))
        self.assertIn("tcp:1501", self.adb.mappings)
        self.assertIn("unrelated.app", self.adb.installed)
        self.assert_no_data_or_network_mutation()

    def test_cleanup_removes_only_session_mapping(self):
        self.adb.mappings.add("tcp:9999")
        self.prepare()
        self.driver.cleanup()
        self.assertEqual(self.adb.mappings, {"tcp:9999"})

    def test_target_only_cleanup_keeps_unused_test_apk_and_is_idempotent(self):
        self.adb.installed[session.RUNNER_APP] = b"preexisting-test-package"
        self.prepare()
        self.driver.cleanup()
        self.assertIn(session.RUNNER_APP, self.adb.installed)
        before = len(self.adb.calls)
        self.driver.cleanup()
        self.assertEqual(len(self.adb.calls), before)

    def test_disconnected_cleanup_does_not_report_success(self):
        self.prepare()
        original = self.adb.run

        def disconnected(*args, **kwargs):
            if args == ("get-state",):
                raise session.CommandError("adb_error", "offline")
            return original(*args, **kwargs)

        with mock.patch.object(self.adb, "run", side_effect=disconnected):
            with self.assertRaises(session.CommandError):
                self.driver.cleanup()
        self.assertEqual(session.read_json(self.driver.path)["status"], "ready")
        self.assertIn(session.APP, self.adb.installed)

    def test_package_query_error_is_not_an_absent_apk(self):
        with mock.patch.object(self.adb, "run", return_value="adb: device offline"):
            with self.assertRaisesRegex(session.CommandError, "安装包路径"):
                self.driver.package_hash(session.APP)

    def test_ci_target_only_manifest_can_prepare_without_test_apk(self):
        artifact = session.APK_PATHS[session.APP]
        session.write_json(self.manifest, {
            "commit": "ci-build-commit",
            "apks": {artifact: session.digest(self.repo / artifact)},
        })
        (self.repo / session.APK_PATHS[session.RUNNER_APP]).unlink()
        self.prepare()
        self.assertEqual(self.driver.state["commit"], "ci-build-commit")
        self.assertEqual(set(self.adb.installed), {session.APP})

    def test_fixture_without_explicit_port_is_rejected_before_install(self):
        data = session.read_json(self.fixture)
        data["origin"] = "http://127.0.0.1"
        session.write_json(self.fixture, data)
        with self.assertRaisesRegex(ValueError, "回环隔离服务"):
            self.prepare()
        self.assertFalse(self.installs())

    def test_instrument_timeout_and_cleanup_disconnect_preserve_result(self):
        self.prepare(specialists=True)
        original = self.adb.run

        def disconnected(*args, **kwargs):
            if args[:3] == ("shell", "am", "instrument"):
                raise session.CommandError("timeout", "deadline",
                    "INSTRUMENTATION_STATUS: acceptance_step=save\n")
            if args[:3] == ("shell", "rm", "-f"):
                raise session.CommandError("adb_error", "offline")
            return original(*args, **kwargs)

        with mock.patch.object(self.adb, "run", side_effect=disconnected):
            result = self.driver.run(["ime"])["ime"]
        self.assertEqual((result["status"], result["cause"]), ("inconclusive", "host_timeout"))
        self.assertEqual(result["last_step"], "save")
        self.assertEqual(result["cleanup_errors"], ["credential_remove"])
        self.assertEqual(session.read_json(self.driver.path)["results"]["ime"], result)

    def test_successful_instrument_with_cleanup_failure_cannot_pass(self):
        self.prepare(specialists=True)
        original = self.adb.run

        def cleanup_failure(*args, **kwargs):
            if args[:3] == ("shell", "rm", "-f"):
                raise session.CommandError("adb_error", "cannot remove")
            return original(*args, **kwargs)

        with mock.patch.object(self.adb, "run", side_effect=cleanup_failure):
            result = self.driver.run(["ime"])["ime"]
        self.assertEqual((result["status"], result["cause"]), ("harness_error", "cleanup_error"))

    def test_recovery_does_not_reuse_report_from_previous_attempt(self):
        self.prepare(specialists=True)
        report = self.driver.output / "recovery.json"
        session.write_json(report, {"status": "succeeded"})
        with mock.patch.object(session, "run_logged", return_value=0):
            result = self.driver.run(["recovery"])["recovery"]
        self.assertEqual((result["status"], result["cause"]),
                         ("harness_error", "recovery_report_missing"))

    def test_recovery_classifies_assertions_tools_and_cleanup_separately(self):
        self.prepare(specialists=True)
        for report, expected in (
            ({"status": "failed", "failure": {"category": "assertion"}}, "failed"),
            ({"status": "failed", "failure": {"category": "harness-error"}}, "harness_error"),
            ({"status": "failed", "cleanup_errors": ["credential remove"]}, "harness_error"),
            ({"status": "inconclusive"}, "inconclusive"),
        ):
            with self.subTest(report=report):
                def execute(*args, **kwargs):
                    session.write_json(self.driver.output / "recovery.json", report)
                    return 1
                with mock.patch.object(session, "run_logged", side_effect=execute):
                    result = self.driver.run(["recovery"], retry=True)["recovery"]
                self.assertEqual(result["status"], expected)

    def test_recovery_login_failure_is_saved_under_requested_group(self):
        self.prepare(specialists=True)
        self.adb.next_instrumentation = "INSTRUMENTATION_FAILED: missing runner\n"
        result = self.driver.run(["recovery"])["recovery"]
        self.assertEqual(result["status"], "harness_error")
        self.assertEqual(session.read_json(self.driver.path)["results"]["recovery"], result)


    def test_ui_wait_timeout_is_inconclusive_and_preserves_last_step(self):
        result = session.instrument_result(
            "INSTRUMENTATION_STATUS: acceptance_step=archive/restore\n"
            "ComposeTimeoutException: Condition still not satisfied\nFAILURES!!!")
        self.assertEqual(result["status"], "inconclusive")
        self.assertEqual(result["last_step"], "archive/restore")


    def test_instrumentation_distinguishes_timeout_driver_and_business_failure(self):
        cases = (
            ("TestTimedOutException\nFAILURES!!!", "inconclusive", "instrumentation_timeout"),
            ("UiAutomationService already registered\nINSTRUMENTATION_FAILED", "harness_error",
             "uiautomation_conflict"),
            ("Insight picker: CONNECTION_FAILED\nFAILURES!!!", "failed", "automation_assertion"),
        )
        for log, status, cause in cases:
            with self.subTest(cause=cause):
                result = session.instrument_result(log)
                self.assertEqual((result["status"], result["cause"]), (status, cause))

    def test_maestro_cached_pass_does_not_accept_a_different_test_note(self):
        self.prepare()
        self.driver.state["results"]["maestro:archive"] = {
            "status": "passed", "log": "previous.log", "note_name": "memos/first",
            "flow_sha256": self.driver.execution_digest("archive")}
        self.assertEqual(self.driver.ui("archive", "maestro", "memos/first")["status"], "reused_pass")
        self.driver.ui("archive", "maestro", "memos/second")
        self.assertEqual(self.ui_calls[-1], ("archive", "memos/second"))

    def test_ui_does_not_install_instrumentation_but_specialists_require_explicit_prepare(self):
        self.prepare()
        self.driver.run(["search", "actions"])
        self.assertEqual([call[0] for call in self.ui_calls], ["login", "search", "actions"])
        with self.assertRaisesRegex(session.CommandError, "Specialists"):
            self.driver.run(["ime"])
        self.prepare(specialists=True)
        self.driver.run(["ime", "lifecycle"])
        self.assertEqual(len(self.installs()), 2)
        instrumentation = [call for call in self.adb.calls if call[:3] == ("shell", "am", "instrument")]
        self.assertEqual(len(instrumentation), 2)
        self.assertTrue(any("ImeLifecycleTest" in " ".join(call) for call in instrumentation))
        self.assertTrue(any("QuickNoteTest#loginWriteRecreateSaveAndReadBack" in " ".join(call)
                            for call in instrumentation))

    def test_switch_to_specialist_releases_maestro_without_uninstalling(self):
        self.prepare(specialists=True)
        self.adb.installed.update({package: b"driver" for package in session.DRIVERS})
        start = len(self.adb.calls)
        self.assertEqual(self.driver.run(["ime"])["ime"]["status"], "passed")
        calls = self.adb.calls[start:]
        instrument = next(i for i, call in enumerate(calls) if call[:3] == ("shell", "am", "instrument"))
        for package in session.DRIVERS:
            self.assertIn(("shell", "am", "force-stop", package), calls[:instrument])
            self.assertIn(package, self.adb.installed)

    def test_ui_timeout_does_not_skip_an_independent_group(self):
        self.prepare()
        default_run = self.fake_maestro

        def with_timeout(repo, adb, fixture, output, executable):
            runner = default_run(repo, adb, fixture, output, executable)
            runner.run_group.side_effect = lambda group: {
                group: {"status": "inconclusive" if group == "editing" else "passed"}}
            return runner

        with mock.patch.object(session, "MaestroRunner", side_effect=with_timeout):
            results = self.driver.run(["editing", "search"])
        self.assertEqual({name: result["status"] for name, result in results.items()},
                         {"editing": "inconclusive", "search": "passed"})


    def test_cleanup_keeps_existing_maestro_driver_but_removes_new_driver(self):
        self.prepare()
        self.driver.state["maestro_preexisting"] = {
            session.DRIVERS[0]: True, session.DRIVERS[1]: False}
        for package in session.DRIVERS:
            self.adb.installed[package] = b"driver"
        self.driver.cleanup()
        self.assertIn(session.DRIVERS[0], self.adb.installed)
        self.assertNotIn(session.DRIVERS[1], self.adb.installed)

    def test_unavailable_service_blocks_before_install(self):
        with mock.patch.object(session, "probe_fixture", side_effect=session.CommandError("fixture_unavailable", "offline")):
            with self.assertRaises(session.CommandError):
                self.prepare()
        self.assertFalse(self.installs())





if __name__ == "__main__":
    unittest.main()
