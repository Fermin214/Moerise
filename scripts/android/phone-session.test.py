"""Host-only regression checks for device lifecycle and failure attribution."""
from contextlib import ExitStack, contextmanager, nullcontext, redirect_stdout
import importlib.util
import io
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

import maestro_runner

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
            return self.network.get(args[4], "null")
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
        registry = self.repo / "scripts/android/phone-checks.json"
        registry.parent.mkdir(parents=True)
        shutil.copyfile(Path(__file__).with_name("phone-checks.json"), registry)
        common = mock.patch.object(session, "common_repo", return_value=self.repo)
        common.start()
        self.addCleanup(common.stop)
        trees = mock.patch.object(session, "worktree_roots", return_value=[self.repo])
        trees.start()
        self.addCleanup(trees.stop)
        restore_ime = mock.patch.object(session, "input_method_checkpoint", return_value=self.root / "ime.json")
        restore_ime.start()
        self.addCleanup(restore_ime.stop)
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

    def release_ready(self):
        app = session.APP.removesuffix(".fixture")
        apk = self.root / "release.apk"
        apk.write_bytes(b"release-apk")
        self.adb.installed[app] = apk.read_bytes()
        session.write_json(self.fixture, {"origin": "https://phone-test.example", "environment": "test",
                                        "username": "phone-owner", "password": "test-only"})
        with mock.patch.object(self.driver, "package_hash", return_value=session.digest(apk)):
            self.driver.prepare_release(apk, self.fixture, "https://phone-test.example", confirmed=True)
        return apk

    def test_release_prepare_defaults_to_no_install_clear_or_uninstall(self):
        self.release_ready()
        self.assertEqual(self.driver.app, "io.github.fermin214.moerise")
        self.assertFalse(any(call[0] in ("install", "uninstall") or call[:3] == ("shell", "pm", "clear") for call in self.adb.calls))
        self.assertEqual(self.ui_calls, [])
        self.assertEqual(self.driver.state["mode"], "release-phone")

    def test_release_prepare_requires_human_confirmation_before_device_operations(self):
        with self.assertRaisesRegex(ValueError, "人工"):
            self.driver.prepare_release(self.root / "missing.apk", self.fixture, "https://phone-test.example")
        self.assertEqual(self.adb.calls, [])

    def test_cleanup_default_preserves_app_and_explicit_uninstall_removes_it(self):
        self.prepare()
        self.driver.cleanup()
        self.assertIn(session.APP, self.adb.installed)
        self.assertFalse(any(call[0] == "uninstall" for call in self.adb.calls))

    def test_release_batch_resumes_only_failed_or_missing_modules_and_summarizes_all(self):
        apk = self.release_ready()
        engine = mock.Mock()
        engine.catalog = {"groups": {"editing": ["editing"], "search": ["search"], "home": ["home-smoke"]}}
        attempted = []
        def batch(flows, on_result, select):
            results = {}
            for flow in flows:
                result = select(flow, engine)
                if result is None:
                    status = "harness_error" if flow == "editing" and not attempted else "passed"
                    attempted.append(flow)
                    log = self.root / f"{flow}.log"
                    log.write_text("Maestro result", encoding="utf-8")
                    result = {"status": status, "ui_status": status, "attempted": True,
                              "server_verification": "passed" if flow in session.SERVER_FLOWS else "not_applicable",
                              "flow_sha256": self.driver.execution_digest(flow), "log": str(log)}
                on_result(flow, result)
                results[flow] = result
            return results
        engine.run_batch.side_effect = batch
        with mock.patch.object(self.driver, "package_hash", return_value=session.digest(apk)), \
                mock.patch.object(session, "MaestroRunner", return_value=engine):
            first = self.driver.run(["editing", "search"])
            self.assertEqual(list(first), ["editing", "search"])
            self.driver.run(["editing", "search", "home"])
        self.assertEqual(attempted, ["editing", "search", "editing", "home-smoke"])
        report = session.read_json(self.driver.directory / "report.json")
        self.assertEqual(len(report["items"]), 3)
        self.assertEqual({row["flow"]: row["status"] for row in report["items"]},
                         {"editing": "passed", "search": "reused_pass", "home-smoke": "passed"})
        self.assertTrue((self.driver.directory / "report.md").exists())
        self.assertFalse(any(call[:3] == ("shell", "pm", "clear") for call in self.adb.calls))

    def test_release_restores_network_and_screen_settings_even_when_batch_raises(self):
        apk = self.release_ready()
        settings = {"wifi_on": "1", "mobile_data": "1", "stay_on_while_plugged_in": "0", "screen_off_timeout": "60000"}
        original = self.adb.run
        def adb(*args, **kwargs):
            if args[:3] == ("shell", "settings", "get") and args[4] in settings:
                return settings[args[4]]
            if args[:2] == ("shell", "svc"):
                settings["wifi_on" if args[2] == "wifi" else "mobile_data"] = "1" if args[3] == "enable" else "0"
                return ""
            if args[:3] == ("shell", "settings", "put"):
                settings[args[4]] = args[5]
                return ""
            return original(*args, **kwargs)
        def fail(*args, **kwargs):
            engine.on_phone_access()
            settings.update(wifi_on="0", mobile_data="0", stay_on_while_plugged_in="3", screen_off_timeout="1800000")
            raise ValueError("batch failure")
        engine = mock.Mock(catalog={"groups": {"search": ["search"]}})
        engine.run_batch.side_effect = fail
        with mock.patch.object(self.driver, "package_hash", return_value=session.digest(apk)), \
                mock.patch.object(self.adb, "run", side_effect=adb), \
                mock.patch.object(session, "MaestroRunner", return_value=engine):
            with self.assertRaisesRegex(ValueError, "batch failure"):
                self.driver.run(["search"])
        self.assertEqual(settings, {"wifi_on": "1", "mobile_data": "1", "stay_on_while_plugged_in": "0", "screen_off_timeout": "60000"})
        self.assertNotIn("device_before", self.driver.state)
        self.assertTrue((self.driver.directory / "report.json").exists())

    def test_release_preparation_failure_does_not_uninstall_absent_drivers(self):
        apk = self.release_ready()
        original = self.adb.run
        def adb(*args, **kwargs):
            if args[0] == "uninstall" and args[1] not in self.adb.installed:
                raise session.CommandError("adb_error", "package not installed")
            return original(*args, **kwargs)
        engine = mock.Mock(catalog={"groups": {"search": ["search"]}})
        failure = {"status": "environment_blocked", "cause": "batch_preparation",
                   "preparation_step": "session_login", **session.FixtureError(
                       403, "CLOUDFLARE_1010", method="POST", path="/api/app/v1/session").details()}
        engine.run_batch.return_value = {"search": failure}
        with mock.patch.object(self.driver, "package_hash", return_value=session.digest(apk)), \
                mock.patch.object(self.adb, "run", side_effect=adb), \
                mock.patch.object(session, "MaestroRunner", return_value=engine):
            result = self.driver.run(["search"])
        self.assertEqual(result["search"]["status"], "environment_blocked")
        self.assertEqual(self.driver.state["cleanup_errors"], [])
        self.assertNotIn("resources_pending", self.driver.state)
        self.assertFalse(any(call[0] == "uninstall" for call in self.adb.calls))
        report = session.read_json(self.driver.directory / "report.json")
        self.assertEqual(report["items"][0]["request"], {"method": "POST", "path": "/api/app/v1/session"})
        markdown = (self.driver.directory / "report.md").read_text(encoding="utf-8")
        for text in ["session_login", "POST /api/app/v1/session", "403", "CLOUDFLARE_1010"]:
            self.assertIn(text, markdown)

    def test_release_driver_removal_failure_keeps_resource_checkpoint(self):
        apk = self.release_ready()
        engine = mock.Mock(catalog={"groups": {"search": ["search"]}})
        def batch(*args, **kwargs):
            engine.on_phone_access()
            self.adb.installed[session.DRIVERS[0]] = b"new-driver"
            return {"search": {"status": "passed"}}
        engine.run_batch.side_effect = batch
        original = self.adb.run
        def adb(*args, **kwargs):
            if args[:2] == ("uninstall", session.DRIVERS[0]):
                raise session.CommandError("adb_error", "package manager unavailable")
            return original(*args, **kwargs)
        with mock.patch.object(self.driver, "package_hash", return_value=session.digest(apk)), \
                mock.patch.object(self.adb, "run", side_effect=adb), \
                mock.patch.object(session, "MaestroRunner", return_value=engine):
            self.driver.run(["search"])
        self.assertEqual(self.driver.state["cleanup_errors"], ["driver_remove"])
        self.assertTrue(self.driver.state["resources_pending"])
        self.assertIn(session.DRIVERS[0], self.adb.installed)

    def test_driver_removal_requires_success_and_absent_package_readback(self):
        package = session.DRIVERS[0]
        self.adb.installed[package] = b"new-driver"
        original = self.adb.run
        for text in ["Failure [DELETE_FAILED_INTERNAL_ERROR]", "Success\n"]:
            with self.subTest(output=text):
                def adb(*args, **kwargs):
                    return text if args[:2] == ("uninstall", package) else original(*args, **kwargs)
                with mock.patch.object(self.adb, "run", side_effect=adb):
                    with self.assertRaises(session.CommandError) as caught:
                        self.driver.remove_package(package)
                self.assertEqual(caught.exception.code, "cleanup_failed")

    def test_release_resume_keeps_new_driver_ownership_and_recovers_unrecorded_forward(self):
        apk = self.release_ready()
        self.driver.state.update(resources_pending=True, maestro_preexisting={package: False for package in session.DRIVERS}, forwards_before=[])
        self.adb.installed.update({package: b"new driver" for package in session.DRIVERS})
        original = self.adb.run
        def adb(*args, **kwargs):
            if args == ("forward", "--list"):
                return self.adb.serial + " tcp:7001 tcp:7002\n"
            return original(*args, **kwargs)
        engine = mock.Mock(catalog={"groups": {"search": ["search"]}})
        engine.run_batch.return_value = {"search": {"status": "passed"}}
        with mock.patch.object(self.driver, "package_hash", return_value=session.digest(apk)), \
                mock.patch.object(self.adb, "run", side_effect=adb), \
                mock.patch.object(session, "MaestroRunner", return_value=engine):
            self.driver.run(["search"])
        for package in session.DRIVERS:
            self.assertNotIn(package, self.adb.installed)
        self.assertIn(("forward", "--remove", "tcp:7001"), self.adb.calls)
        self.assertNotIn("resources_pending", self.driver.state)

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

    def test_unrelated_runner_change_does_not_invalidate_flow_pass(self):
        self.prepare()
        script = self.repo / "scripts/android/maestro_runner.py"
        script.write_text("# original\n", encoding="utf-8")
        self.driver.run(["editing"])
        script.write_text("# changed\n", encoding="utf-8")
        result = self.driver.run(["editing"])["editing"]
        self.assertEqual(result["status"], "reused_pass")
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





class FixedWindowsEntry(unittest.TestCase):
    def setUp(self):
        self.repo = Path(__file__).resolve().parents[2]
        parent = self.repo / ".local/android"
        parent.mkdir(parents=True, exist_ok=True)
        storage = tempfile.TemporaryDirectory(prefix="verify-phone-test-", dir=parent)
        self.addCleanup(storage.cleanup)
        self.root = Path(storage.name)
        self.adb = FakeAdb(self.repo)
        self.app = session.APP.removesuffix(".fixture")
        self.apk = self.root / "target.apk"
        self.apk.write_bytes(b"fixture-release-apk")
        self.adb.installed[self.app] = self.apk.read_bytes()
        self.fixture = self.root / "connection.json"
        self.origin = "https://android-tests.example"
        session.write_json(self.fixture, {"origin": self.origin, "username": "android-owner",
                                          "password": "fixture-password", "environment": "test"})
        self.profile = self.root / "profile.json"
        session.write_json(self.profile, {"apk": "target.apk", "fixture": "connection.json",
                                           "sessions_root": "sessions", "confirm_ready": True})
        self.sessions = self.root / "sessions"
        self.engine = mock.Mock(batch_cleanup_errors=[])
        self.engine.catalog = session.catalog(self.repo)
        self.engine.run_batch.side_effect = self.batch
        self.executed = []
        self.events = []
        self.interrupt = None
        self.on_flow = None
        self.flow_results = {}
        for patcher in (
            mock.patch.object(session, "Adb", return_value=self.adb),
            mock.patch.object(session, "MaestroRunner", return_value=self.engine),
            mock.patch.object(session, "discover_serial", return_value=self.adb.serial),
            mock.patch.object(session, "device_lock", return_value=nullcontext()),
            mock.patch.object(session, "common_repo", return_value=self.root),
            mock.patch.object(session, "worktree_roots", return_value=[self.root]),
            mock.patch.object(session, "input_method_checkpoint", return_value=self.root / "ime.json"),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)

    def batch(self, flows, on_result, select, **kwargs):
        results, started = {}, False
        for flow in flows:
            result = select(flow, self.engine)
            if result is None:
                if not started:
                    self.engine.on_phone_access()
                    self.events.append("test-source")
                    self.engine.on_app_launch(session.now())
                    if self.interrupt:
                        self.interrupt()
                    started = True
                self.executed.append(flow)
                log = self.root / f"{flow}.log"
                log.write_text("Maestro result", encoding="utf-8")
                result = {"status": "passed", "ui_status": "passed", "manual_status": "not_applicable",
                          "server_verification": "passed" if flow in session.SERVER_FLOWS else "not_applicable",
                          "flow_sha256": session.flow_digest(self.repo, flow), "attempted": True,
                          "log": str(log),
                          **self.flow_results.get(flow, {})}
            results[flow] = result
            if self.on_flow:
                self.on_flow(flow)
            if on_result(flow, result):
                break
        return results

    def cli(self, *args):
        with mock.patch.object(sys, "argv", ["phone-session", *args]), redirect_stdout(io.StringIO()) as out:
            code = session.main()
        return code, json.loads(out.getvalue())

    def verify(self, *args):
        return self.cli("verify", "--profile", str(self.profile), *args)

    def active(self):
        return session.active_directory(self.sessions)

    def rows(self):
        report = session.read_json(self.active() / "report.json")
        return {item["id"]: item for item in report["items"]}

    def record_baseline(self, records, **override):
        baseline = self.root / "baseline.json"
        session.write_json(baseline, {"apk_sha256": session.digest(self.apk), "serial": self.adb.serial,
                                      "test_origin": self.origin, "checks": records, **override})
        return str(baseline)

    def evidence(self, flow, **override):
        path = self.root / "evidence.json"
        path.write_text("{}", encoding="utf-8")
        return {"ui_status": "passed", "server_verification": "passed" if flow in session.SERVER_FLOWS else "not_applicable",
                "manual_status": "not_applicable", "evidence_paths": [path.name],
                "flow_sha256": session.flow_digest(self.repo, flow), **override}

    def telemetry_csv(self):
        path = self.root / "telemetry.csv"
        path.write_text("week_ending_utc,platform,client_version,sample_count\n", encoding="utf-8")
        captured = time.time() - 2
        os.utime(path, (captured, captured))
        return path

    @contextmanager
    def real_batch(self):
        api = mock.Mock(csrf="", **{"login.return_value": {"instance_id": "fixture", "user": {"name": "users/owner"}}})

        def engine(*args, **kwargs):
            real = maestro_runner.MaestroRunner(*args, **kwargs)
            real.api = api
            return real

        real = maestro_runner.MaestroRunner
        with mock.patch.object(session, "MaestroRunner", side_effect=engine), \
                mock.patch.object(maestro_runner, "common_repo", return_value=self.root), \
                mock.patch.object(maestro_runner, "worktree_roots", return_value=[self.root]), \
                mock.patch.object(real, "probe_phone_offline", return_value={"status": "passed"}) as offline, \
                mock.patch.object(real, "prepare_data", return_value=({}, None)) as prepare, \
                mock.patch.object(real, "verify") as readback, \
                mock.patch.object(real, "run_flow", return_value={"status": "inconclusive", "cause": "timeout",
                                                                  "log": str(self.root / "probe.log")}) as execute:
            yield api, offline, prepare, readback, execute

    def assert_report_result(self, name, status, cause=None, evidence=None):
        row = self.rows()[name]
        self.assertEqual(row["status"], status)
        line = next(line for line in (self.active() / "report.md").read_text(encoding="utf-8").splitlines()
                    if line.startswith(f"| {name} |"))
        self.assertIn(status, line)
        if cause:
            self.assertEqual(row["cause"], cause)
            self.assertIn(cause, line)
        if evidence:
            self.assertEqual(row["evidence_paths"], [str(evidence)])
            self.assertEqual(row["reuse"], status)
            self.assertIn(evidence.as_uri(), line)
        return row

    def test_acceptance_state_table(self):
        cases = (
            ("source_timeout", "verify", None, (), "environment_blocked", "environment_blocked", None, 1),
            ("login_failure", "verify", None, (), "environment_blocked", "environment_blocked", None, 1),
            ("offline_failure", "verify", None, (), "harness_error", "harness_error", None, 1),
            ("ui_failure", "verify", None, ("editing", "search"), "failed", "passed", None, 1),
            ("readback_failure", "run", None, ("editing", "search"), "failed", "passed", None, 1),
            ("retry_failure", "verify", None, ("editing", "search"), "failed", "passed", None, 1),
            ("retry_failure", "run", None, ("editing", "search"), "failed", "passed", None, 1),
            ("passed", "verify", "target", ("editing",), "passed", "not_run", "editing", 2),
            ("source_timeout", "verify", "target", (), "environment_blocked", "not_run", "editing", 1),
            ("reused", "verify", "target", (), "reused_baseline", "not_run", "editing", 2),
            ("passed", "verify", "marker", ("editing",), "passed", "not_run", "editing", 2),
            ("source_timeout", "verify", "marker", (), "environment_blocked", "not_run", "editing", 1),
            ("reused", "verify", "marker", (), "reused_baseline", "not_run", "editing", 2),
            ("result_write_error", "verify", "target", ("editing",), "not_run", "not_run", None, 1),
            ("session_write_error", "verify", "marker", ("editing",), "not_run", "not_run", None, 1),
            ("retry_write_error", "verify", "marker", ("editing",), "not_run", "not_run", None, 1),
            ("blocked_write_error", "verify", "marker", (), "not_run", "not_run", None, 1),
            ("reuse_write_error", "verify", "target", (), "not_run", "not_run", None, 1),
            ("stop_write_error", "verify", "marker", ("editing",), "passed", "not_run", None, 1),
            ("cleanup_failure", "verify", "target", ("editing",), "passed", "not_run", "editing", 1),
        )
        for index, (event, entry, stop, executed, first, following, stopped, exit_code) in enumerate(cases):
            with self.subTest(event=event, entry=entry, stop=stop):
                self.sessions = self.root / f"sessions-{index}"
                base_args = ("--non-interactive", "--already-launched", "--sessions-root", str(self.sessions))
                retrying = event in ("retry_failure", "retry_write_error")
                if entry == "run" or retrying:
                    initial = "editing" if retrying else "language"
                    self.assertEqual(self.verify(*base_args, "--check", initial)[0], 0)
                    if retrying:
                        prior = session.read_json(self.active() / "session.json")["flow_results"]["editing"]
                        self.assertEqual(prior["status"], "passed")
                if stop and event not in ("source_timeout", "blocked_write_error"):
                    self.assertEqual(self.verify(*base_args, "--check", "search")[0], 0)
                retry = ("--retry",) if retrying else ()
                baseline = self.record_baseline({"editing": self.evidence("editing")})
                baseline_args = ("--baseline", baseline) if retrying or event in ("reused", "reuse_write_error") else ()
                stop_args = ("--stop-after", "editing") if stop == "target" else ()
                save_session = session.PhoneSession.save
                write_result = maestro_runner.write_result
                save_events, ui = [], []
                injected = False

                def save(engine):
                    nonlocal injected
                    view = engine.state.get("coverage", {})
                    if engine.state.get("active_run_id") and "editing" in view.get("results", {}) and not injected:
                        if stop == "marker":
                            session.request_stop(engine.directory)
                        injected = True
                        if event in ("session_write_error", "retry_write_error", "blocked_write_error", "reuse_write_error"):
                            if event == "retry_write_error":
                                saved = session.read_json(engine.output / "maestro/editing/result.json")
                                self.assertEqual((saved["status"], saved["attempted"]), ("failed", True))
                            raise OSError("checkpoint cannot be persisted")
                    if event == "stop_write_error" and view.get("stopped") == "editing":
                        raise OSError("stop cannot be persisted")
                    save_session(engine)
                    if view.get("results", {}).get("editing") and not save_events:
                        save_events.append("editing")

                def write(path, result):
                    if event == "result_write_error" and path.parts[-2:] == ("editing", "result.json"):
                        raise OSError("result cannot be persisted")
                    write_result(path, result)

                def execute(flow, **_kwargs):
                    if flow == "test-source" and event in ("source_timeout", "blocked_write_error"):
                        return {"status": "inconclusive", "cause": "timeout"}
                    if flow != "test-source":
                        ui.append(flow)
                        if flow == "search":
                            self.assertEqual(session.read_json(self.active() / "session.json")["flow_results"]["editing"]["status"], first)
                    return {"status": "failed" if flow == "editing" and (retrying or event == "ui_failure") else "passed",
                            "flow_sha256": session.flow_digest(self.repo, flow)}

                def readback(flow, _values):
                    if flow == "editing" and event == "readback_failure":
                        raise AssertionError("server value mismatch")
                    return {}

                real = maestro_runner.MaestroRunner
                with self.real_batch() as (api, offline, prepare, verify_observation, run_flow), ExitStack() as stack:
                    for patcher in (
                        mock.patch.object(real, "recover_editor_state", return_value=[]),
                        mock.patch.object(real, "prepare_conflict"),
                        mock.patch.object(real, "wait_verify", side_effect=readback),
                        mock.patch.object(session.PhoneSession, "save", autospec=True, side_effect=save),
                        mock.patch.object(maestro_runner, "write_result", side_effect=write),
                    ):
                        stack.enter_context(patcher)
                    if event == "login_failure":
                        api.login.side_effect = maestro_runner.FixtureError(403, "FORBIDDEN")
                    if event == "offline_failure":
                        offline.side_effect = OSError("offline isolation failed")
                    if event == "cleanup_failure":
                        stack.enter_context(mock.patch.object(session.PhoneSession, "restore_device_input",
                                                              side_effect=OSError("input restore failed")))
                    prepare.side_effect = lambda flow, _item: ({"NOTE_NAME": "memos/synthetic", "NOTE_TEXT": "fixture"}, None)
                    verify_observation.return_value = {}
                    run_flow.side_effect = execute
                    if entry == "verify":
                        code, output = self.verify(*base_args, "--check", "editing", "--check", "search",
                                                   *retry, *baseline_args, *stop_args)
                    else:
                        code, output = self.cli("run", "--release", "--session", str(self.active()),
                                                "--group", "editing", "--group", "search", *retry)

                self.assertEqual(code, exit_code, output)
                self.assertEqual(tuple(ui), executed)
                self.assertEqual([call.args[0] for call in prepare.call_args_list if call.args[0] != "test-source"], list(executed))
                state = session.read_json(self.active() / "session.json")
                report = session.read_json(self.active() / "report.json")
                rows = {row.get("id", row.get("flow")): row for row in report["items"]}
                self.assertEqual((rows["editing"]["status"], rows["search"]["status"]), (first, following))
                self.assertEqual(report.get("stopped_at"), stopped)
                self.assertEqual(state["coverage"].get("stopped"), stopped)
                self.assertFalse((self.active() / "STOP_AFTER_CURRENT").exists())
                if "write_error" in event:
                    self.assertEqual(output["error"]["cause"], "OSError")
                if event in ("login_failure", "offline_failure"):
                    self.assertEqual(rows["editing"]["preparation_step"],
                                     "session_login" if event == "login_failure" else "phone_offline_identity")
                if event == "cleanup_failure":
                    self.assertEqual(report["cleanup_errors"], ["input_restore"])
                    with mock.patch.object(session.PhoneSession, "restore_device_input", side_effect=OSError("restore")):
                        self.assertEqual(self.cli("cleanup", "--release", "--session", str(self.active()))[0], 1)
                    self.assertEqual(session.read_json(self.active() / "session.json")["cleanup_errors"], ["input_restore"])
                if retrying:
                    self.assertEqual(state["flow_results"]["editing"]["status"], "failed")
                    if event == "retry_write_error":
                        self.assertEqual(rows["editing"]["existing_status"], "failed")
                    self.executed.clear()
                    code, _ = self.verify(*base_args, "--check", "editing", "--baseline", baseline)
                    self.assertEqual(code, 0)
                    self.assertEqual(self.executed, ["editing"])

    def test_old_pass_with_no_valid_evidence_is_executed_again(self):
        args = ("--non-interactive", "--already-launched", "--check", "language")
        self.assertEqual(self.verify(*args)[0], 0)
        path = self.active() / "session.json"
        state = session.read_json(path)
        inputs = self.root / "test-data.json"
        session.write_json(inputs, {"APP_ID": self.app})
        state["flow_results"]["language"].update(log=None, evidence_paths=["missing"], screenshots=[],
                                                  test_data_path=str(inputs))
        session.write_json(path, state)
        self.executed.clear()
        self.assertEqual(self.verify(*args)[0], 0)
        self.assertEqual(self.executed, ["language"])
        self.assertEqual(self.rows()["language"]["status"], "passed")

    def test_missing_manual_baseline_evidence_returns_to_cards_and_can_be_replaced(self):
        args = ("--non-interactive", "--already-launched", "--check", "language")
        check = "editing-keyboard"
        old = self.root / "evidence.json"
        record = self.evidence("editing", ui_status="not_applicable",
                               server_verification="not_applicable", manual_status="passed")
        baseline = self.record_baseline({check: record})
        self.assertEqual(self.verify(*args, "--baseline", baseline)[0], 0)
        self.assert_report_result(check, "passed")
        self.assertEqual(self.rows()[check]["reuse"], "reused_baseline")
        old.unlink()

        self.assertEqual(self.verify(*args)[0], 0)
        row = self.assert_report_result(check, "pending", "人工基线证据已缺失，需重新取证")
        self.assertIsNone(row["reuse"])
        report = session.read_json(self.active() / "report.json")
        self.assertEqual(report["manual_results"][check]["status"], "pending")
        self.assertIn(f"--check {check}", (self.active() / "manual-cards.md").read_text(encoding="utf-8"))

        new = self.root / "replacement.png"
        new.write_bytes(b"replacement evidence")
        replacement = self.record_baseline({check: {**record, "evidence_paths": [new.name]}})
        self.assertEqual(self.verify(*args, "--baseline", replacement)[0], 0)
        self.assertEqual(self.rows()[check]["status"], "passed")
        self.assertEqual(self.rows()[check]["reuse"], "reused_baseline")
        self.assertEqual(self.rows()[check]["evidence_paths"], [str(new)])

    def test_invalid_coverage_keeps_state_and_previous_reports(self):
        args = ("--non-interactive", "--already-launched", "--check", "language")
        self.assertEqual(self.verify(*args)[0], 0)
        output = self.active() / "report.json"
        earlier = output.read_bytes()
        validate = session.validate_coverage

        def invalid(checks, selected, current, stopped, rows):
            self.assertEqual(session.read_json(self.active() / "session.json")["coverage"]["status"], "running")
            rows.append(rows[0])
            validate(checks, selected, current, stopped, rows)

        for stop in ((), ("--stop-after", "language")):
            with self.subTest(stop=stop), mock.patch.object(session, "validate_coverage", side_effect=invalid):
                code, result = self.verify(*args, *stop)
                self.assertEqual(code, 1)
                self.assertRegex(result["message"], "检查项缺失或重复")
                self.assertEqual(output.read_bytes(), earlier)
                saved = session.read_json(self.active() / "session.json")
                self.assertNotEqual(saved["coverage"]["run_id"], json.loads(earlier)["run_id"])
                self.assertEqual(saved["coverage"]["results"]["language"]["status"], "reused_pass")
                self.assertEqual(saved["coverage"]["status"], "failed")
                self.assertEqual(saved["coverage"]["error"]["cause"], "coverage_report_failed")
                self.assertEqual(saved["coverage"]["error"]["message"], result["message"])
                self.assertNotIn("active_run_id", saved)

    def test_failed_terminal_save_keeps_all_previous_reports(self):
        args = ("--non-interactive", "--already-launched", "--check", "language")
        self.assertEqual(self.verify(*args)[0], 0)
        directory = self.active()
        paths = [directory / name for name in ("report.json", "report.md", "manual-cards.md")]
        earlier = [path.read_bytes() for path in paths]
        prior_run = json.loads(earlier[0])["run_id"]
        save = session.PhoneSession.save

        def fail_terminal_save(phone):
            if phone.state["coverage"]["status"] in ("completed", "stopped"):
                raise OSError("terminal session cannot be saved")
            save(phone)

        for stop in ((), ("--stop-after", "language")):
            with self.subTest(stop=stop), mock.patch.object(session.PhoneSession, "save", fail_terminal_save):
                code, result = self.verify(*args, *stop)
                self.assertEqual(code, 1)
                self.assertEqual(result["message"], "terminal session cannot be saved")
                self.assertEqual([path.read_bytes() for path in paths], earlier)
                state = session.read_json(directory / "session.json")
                self.assertNotEqual(state["coverage"]["run_id"], prior_run)
                self.assertEqual(state["coverage"]["status"], "running")
                self.assertEqual(state["active_run_id"], state["coverage"]["run_id"])
                self.assertEqual(state["coverage"]["results"]["language"]["status"], "reused_pass")

    def test_probe_timeout_keeps_baselines_previous_pass_and_capability_skip(self):
        evidence_path = self.root / "evidence.json"
        self.flow_results["home-smoke"] = {"evidence_paths": [str(evidence_path)], "log": None}
        self.assertEqual(self.verify("--non-interactive", "--already-launched", "--check", "home")[0], 0)
        baselines = ("navigation-review", "navigation-walk", "navigation-map", "language")
        baseline = self.record_baseline({name: self.evidence(name) for name in baselines})
        args = [part for name in ("editing", "search", "insights-disconnect", "home", *baselines) for part in ("--check", name)]
        with self.real_batch() as (api, _, prepare, _, execute):
            code, output = self.verify("--non-interactive", "--baseline", baseline, *args)
        self.assertEqual(code, 1, output)
        api.login.assert_called_once()
        prepare.assert_called_once()
        execute.assert_called_once()
        for name in ("editing", "search"):
            row = self.assert_report_result(name, "environment_blocked", "phone_test_source_unverified")
            self.assertEqual((row["preparation_step"], row["probe_cause"]), ("test_source_ui", "timeout"))
            self.assertEqual(row["evidence_paths"], [str(self.root / "probe.log")])
        for name in baselines:
            self.assert_report_result(name, "reused_baseline", evidence=evidence_path)
        self.assert_report_result("home", "reused_pass", evidence=evidence_path)
        self.assert_report_result("insights-disconnect", "skipped", "capability_missing")

    def test_batch_preparation_failure_keeps_local_results_without_pending_readback(self):
        session.write_json(self.root / "test-data.json", {"ATTACHMENT_SHA256": "a" * 64,
                           "ATTACHMENT_NAME": "example.txt", "ATTACHMENT_NOTE_TEXT": "Marker"})
        baseline = self.record_baseline({
            "language": self.evidence("language"),
            "attachments": self.evidence("attachments", server_verification="pending", test_data_path="test-data.json"),
        })
        cases = (("session_login", "environment_blocked", maestro_runner.FixtureError(403, "FORBIDDEN")),
                 ("test_source_data", "environment_blocked", maestro_runner.FixtureError(503, "UNAVAILABLE")),
                 ("phone_offline_identity", "harness_error", RuntimeError("offline isolation failed")))
        for index, (step, status, error) in enumerate(cases):
            with self.subTest(step=step), self.real_batch() as (api, offline, prepare, readback, execute):
                {"session_login": api.login, "test_source_data": prepare, "phone_offline_identity": offline}[step].side_effect = error
                code, output = self.verify("--non-interactive", "--already-launched", "--baseline", baseline,
                                           "--check", "editing", "--check", "language", "--check", "attachments",
                                           "--check", "insights-disconnect")
                self.assertEqual(code, 1, output)
                api.login.assert_called_once()
                readback.assert_not_called()
                execute.assert_not_called()
                for name in ("editing", "attachments"):
                    row = self.assert_report_result(name, status, "batch_preparation")
                    self.assertEqual(row["preparation_step"], step)
                    self.assertEqual(row["error_message"], str(error))
                self.assert_report_result("language", "reused_pass" if index else "reused_baseline",
                                          evidence=self.root / "evidence.json")
                self.assert_report_result("insights-disconnect", "skipped", "capability_missing")

    def test_probe_failure_stops_at_target_or_consumes_matching_run_marker(self):
        self.assertEqual(self.verify("--non-interactive", "--already-launched", "--check", "language")[0], 0)
        metadata = self.active() / "session.json"
        original = session.read_json(metadata)["flow_results"]
        marker = self.active() / "STOP_AFTER_CURRENT"
        args = ("--check", "editing", "--check", "language", "--check", "insights-disconnect")
        for stop in ("target", "probe_marker", "prompt_marker"):
            with self.subTest(stop=stop), self.real_batch() as (_, _, _, _, execute), \
                    mock.patch.object(session.sys.stdin, "isatty", return_value=True), \
                    mock.patch.object(session, "ask", return_value="y") as prompt:
                if stop == "probe_marker":
                    def stop_during_probe(*_args, **_kwargs):
                        session.request_stop(self.active())
                        return execute.return_value

                    execute.side_effect = stop_during_probe
                if stop == "prompt_marker":
                    def stop_during_prompt(*_args):
                        session.request_stop(self.active())
                        return "y"

                    prompt.side_effect = stop_during_prompt
                code, output = self.verify(*args, *(("--stop-after", "editing") if stop == "target" else ()))
                execute.assert_called_once()
                self.assertEqual(prompt.call_count, int(stop == "prompt_marker"))
                self.assertEqual(code, 1, output)
                self.assertEqual(session.read_json(self.active() / "report.json")["stopped_at"], "editing")
                self.assert_report_result("editing", "environment_blocked", "phone_test_source_unverified")
                for name in ("language", "insights-disconnect"):
                    self.assertEqual(self.assert_report_result(name, "not_run")["not_run_reason"], "stopped_at_boundary")
                self.assertEqual(session.read_json(metadata)["flow_results"], original)
                self.assertFalse(marker.exists())

    def test_probe_checkpoint_stops_when_marker_arrives_during_save(self):
        baseline = self.record_baseline({"language": self.evidence("language")})
        save_session = session.PhoneSession.save
        requested = []

        def save(engine):
            results = engine.state.get("coverage", {}).get("results", {})
            if engine.state.get("active_run_id") and "editing" in results and not requested:
                requested.append(session.request_stop(engine.directory)["run_id"])
            save_session(engine)

        with self.real_batch() as (_, _, _, _, execute), \
                mock.patch.object(session.PhoneSession, "save", autospec=True, side_effect=save):
            code, output = self.verify("--non-interactive", "--already-launched", "--baseline", baseline,
                                       "--check", "editing", "--check", "language")
        self.assertEqual(code, 1, output)
        report = session.read_json(self.active() / "report.json")
        self.assertEqual(requested, [report["run_id"]])
        self.assertEqual(report["stopped_at"], "editing")
        self.assert_report_result("editing", "environment_blocked", "phone_test_source_unverified")
        self.assertEqual(self.assert_report_result("language", "not_run")["not_run_reason"], "stopped_at_boundary")
        self.assertFalse((self.active() / "STOP_AFTER_CURRENT").exists())
        execute.assert_called_once()

    def test_stop_item_write_error_ends_batch_in_top_level_report(self):
        baseline = self.record_baseline({"language": self.evidence("language")})
        write_result = maestro_runner.write_result

        def write(path, result):
            if path.parts[-2:] == ("editing", "result.json"):
                raise PermissionError("editing result is not writable")
            write_result(path, result)

        with self.real_batch(), mock.patch.object(maestro_runner, "write_result", side_effect=write) as writes:
            code, output = self.verify("--non-interactive", "--already-launched", "--baseline", baseline,
                                       "--check", "editing", "--check", "language", "--stop-after", "editing")
        self.assertEqual(code, 1, output)
        self.assertEqual(output["error"], {"cause": "PermissionError", "message": "editing result is not writable"})
        self.assert_report_result("editing", "not_run")
        self.assert_report_result("language", "not_run")
        self.assertEqual(writes.call_args.args[1], {})
        self.assertFalse(session.read_json(self.active() / "session.json").get("flow_results"))

    def test_selection_read_error_does_not_hide_later_local_results(self):
        baseline = self.record_baseline({"language": self.evidence("language")})
        execution_digest = session.PhoneSession.execution_digest
        for cause in ("selection", "probe"):
            with self.subTest(cause=cause), self.real_batch() as (api, _, _, _, execute):
                missing_flow = "editing" if cause == "selection" else "search"

                def digest(engine, flow):
                    if flow == missing_flow:
                        raise FileNotFoundError(f"{flow}.yaml is missing")
                    return execution_digest(engine, flow)

                with mock.patch.object(session.PhoneSession, "execution_digest", autospec=True, side_effect=digest) as fingerprints:
                    code, output = self.verify("--non-interactive", "--already-launched", "--baseline", baseline,
                                               "--check", "editing", "--check", "search", "--check", "language",
                                               "--check", "insights-disconnect")
                self.assertEqual(code, 1, output)
                self.assertIsNone(output["error"])
                row = self.assert_report_result(missing_flow, "harness_error", "batch_preparation")
                self.assertEqual((row["preparation_step"], row["error_message"]),
                                 ("result_selection", f"{missing_flow}.yaml is missing"))
                self.assertEqual([call.args[1] for call in fingerprints.call_args_list].count(missing_flow), 1)
                self.assert_report_result("language", "reused_baseline" if cause == "selection" else "reused_pass",
                                          evidence=self.root / "evidence.json")
                self.assert_report_result("insights-disconnect", "skipped", "capability_missing")
                self.assertEqual(api.login.call_count, int(cause == "probe"))
                self.assertEqual(execute.call_count, int(cause == "probe"))

    def test_stop_after_first_reused_baseline_never_starts_the_probe(self):
        baseline = self.record_baseline({"navigation-review": self.evidence("navigation-review")})
        with self.real_batch() as (api, offline, prepare, _, execute):
            code, output = self.verify("--non-interactive", "--baseline", baseline, "--check", "navigation-review",
                                       "--check", "home", "--stop-after", "navigation-review")
        self.assertEqual(code, 2, output)
        self.assert_report_result("navigation-review", "reused_baseline", evidence=self.root / "evidence.json")
        self.assertEqual(self.assert_report_result("home", "not_run")["not_run_reason"], "stopped_at_boundary")
        for operation in (api.login, offline, prepare, execute):
            operation.assert_not_called()

    def test_missing_phone_access_confirmation_stays_in_top_level_report(self):
        with self.real_batch() as (api, offline, _, _, _):
            code, output = self.verify("--non-interactive", "--no-confirm-ready", "--check", "editing")
        self.assertEqual(code, 1)
        self.assertEqual(output["error"]["cause"], "test_account_confirmation_pending")
        self.assert_report_result("editing", "not_run")
        api.login.assert_not_called()
        offline.assert_not_called()

    def test_verify_record_from_card_cleanup_and_new_session(self):
        code, first = self.verify("--non-interactive", "--already-launched", "--check", "home")
        self.assertEqual(code, 0, first)
        first_dir = self.active()
        text = (first_dir / "manual-cards.md").read_text(encoding="utf-8")
        command = next(line for line in text.splitlines() if "--check editing-caret " in line)
        self.assertEqual(self.cli(*shlex.split(command)[3:])[0], 0)
        self.assertEqual(self.rows()["editing-caret"]["manual_status"], "passed")
        self.assertEqual(self.cli("cleanup", "--release", "--session", str(first_dir))[0], 0)
        self.assertEqual(self.verify("--non-interactive", "--check", "home")[0], 0)
        self.assertNotEqual(self.active(), first_dir)
        self.assertEqual(session.read_json(first_dir / "report.json")["status"], "completed")
        self.assertEqual(self.rows()["telemetry-before-launch"]["manual_status"], "inconclusive")

    def test_prelaunch_runs_for_manually_installed_apk_before_probe(self):
        code, result = self.verify("--non-interactive", "--check", "home")
        self.assertEqual(code, 1)
        self.assertEqual(result["error"]["cause"], "pre_launch_evidence_pending")
        self.assertEqual(self.events, [])
        evidence = self.telemetry_csv()
        with mock.patch.object(session, "ask", side_effect=lambda *_: self.events.append("pre_launch") or str(evidence)):
            self.assertEqual(self.verify("--check", "home")[0], 0)
        self.assertEqual(self.events, ["pre_launch", "test-source"])
        self.assertEqual(self.rows()["telemetry-before-launch"]["manual_status"], "passed")

    def test_preinstall_and_prelaunch_are_independent_and_precede_any_probe(self):
        self.adb.installed[self.app] = b"old"
        original = self.adb.run
        def device(*args, **kwargs):
            if args[0] == "install":
                self.events.append("install")
                self.adb.installed[self.app] = self.apk.read_bytes()
                return "Success\n"
            return original(*args, **kwargs)
        def reply(interactive, text):
            self.events.append(text.split(" · ", 1)[0])
            return str(self.telemetry_csv()) if text.startswith("telemetry-before-launch") else "y"
        with mock.patch.object(self.adb, "run", side_effect=device), mock.patch.object(session, "ask", side_effect=reply):
            code, report = self.verify("--install", "--check", "home")
            self.assertEqual(code, 0, report)
        self.assertEqual(self.events, ["upgrade-before", "install", "telemetry-before-launch", "test-source"])

    def test_baseline_status_identity_fingerprint_and_pending_readback(self):
        actions = self.evidence("actions", server_verification="pending")
        attachments = self.evidence("attachments", server_verification="pending", test_data_path="test-data.json")
        values = {"ATTACHMENT_SHA256": "a" * 64, "ATTACHMENT_NAME": "example.txt",
                  "ATTACHMENT_NOTE_TEXT": "Maestro file 0123456789"}
        session.write_json(self.root / "test-data.json", values)
        baseline = self.record_baseline({"search": self.evidence("search", flow_sha256="obsolete"),
                                        "language": self.evidence("language"), "actions": actions, "attachments": attachments})
        self.engine.verify.return_value = {"attachment_bytes_match": True}
        args = ("--non-interactive", "--already-launched", "--check", "language", "--check", "search",
                "--check", "actions", "--check", "attachments", "--baseline", baseline)
        self.assertEqual(self.verify(*args)[0], 0)
        self.assertEqual(self.executed, ["search", "actions"])
        self.assertEqual(self.rows()["language"]["status"], "reused_baseline")
        self.assertEqual(self.rows()["attachments"]["server_verification"], "passed")
        self.engine.verify.assert_called_once_with("attachments", values)
        self.record_baseline({}, apk_sha256="other")
        code, output = self.verify(*args)
        self.assertEqual(code, 1)
        self.assertEqual(output["error"]["cause"], "baseline_identity_mismatch")

    def test_legacy_baseline_accepts_only_pinned_equivalents(self):
        def legacy(flow, **override):
            return {field: value for field, value in self.evidence(flow, **override).items() if field != "flow_sha256"}
        baseline = self.record_baseline({"language": legacy("language"), "navigation-review": legacy("navigation-review"),
                                         "search": legacy("search"), "actions": legacy("actions", server_verification="pending")},
                                         legacy_handoff=True, apk_sha256=session.LEGACY_APK)
        real_digest = session.digest
        def target_digest(path):
            result = real_digest(path)
            return session.LEGACY_APK if result == real_digest(self.apk) else result
        with mock.patch.object(session, "digest", side_effect=target_digest):
            self.assertEqual(self.verify("--non-interactive", "--already-launched", "--baseline", baseline,
                                         "--check", "language", "--check", "search", "--check", "actions",
                                         "--check", "navigation-review")[0], 0)
        self.assertEqual(self.executed, ["search", "actions"])
        self.assertEqual(self.rows()["navigation-review"]["status"], "reused_baseline")

    def test_stop_after_reused_and_pending_preserves_existing_results(self):
        args = ("--non-interactive", "--already-launched", "--check", "editing", "--check", "search")
        self.assertEqual(self.verify(*args, "--stop-after", "editing")[0], 2)
        self.assertEqual(self.executed, ["editing"])
        self.assertEqual(self.rows()["search"]["status"], "not_run")
        self.assertEqual(self.verify(*args)[0], 0)
        self.assertEqual(self.executed, ["editing", "search"])
        self.assertEqual(self.verify(*args, "--stop-after", "editing")[0], 2)
        self.assertEqual(self.rows()["editing"]["status"], "reused_pass")
        self.assertEqual(self.rows()["search"]["existing_status"], "passed")
        self.assertEqual(self.rows()["search"]["not_run_reason"], "stopped_at_boundary")
        self.assertEqual(session.read_json(self.active() / "session.json")["flow_results"]["search"]["status"], "passed")

    def test_run_bound_stop_and_ctrl_c_do_not_stop_next_run(self):
        args = ("--non-interactive", "--already-launched", "--check", "editing", "--check", "search")
        def stop(flow):
            if flow == "editing":
                requested = session.request_stop(self.active())
                self.assertEqual(requested["run_id"], session.read_json(self.active() / "session.json")["active_run_id"])
        self.on_flow = stop
        self.assertEqual(self.verify(*args)[0], 2)
        self.assertEqual(self.executed, ["editing"])
        self.on_flow = None
        def interrupt():
            session.request_stop(self.active())
            raise KeyboardInterrupt
        self.interrupt = interrupt
        self.assertEqual(self.verify(*args)[0], 1)
        self.assertFalse((self.active() / "STOP_AFTER_CURRENT").exists())
        self.interrupt = None
        self.assertEqual(self.verify(*args)[0], 0)
        self.assertEqual(self.executed, ["editing", "search"])

    def test_apk_switch_restores_old_resources_and_failed_restore_leaves_pointer(self):
        args = ("--non-interactive", "--already-launched", "--check", "home")
        self.assertEqual(self.verify(*args)[0], 0)
        previous = self.active()
        metadata = previous / "session.json"
        state = session.read_json(metadata)
        state.update(resources_pending=True, maestro_preexisting={session.DRIVERS[0]: False}, forwards_before=[])
        session.write_json(metadata, state)
        self.adb.installed[session.DRIVERS[0]] = b"driver"
        self.apk.write_bytes(b"second-apk")
        self.adb.installed[self.app] = self.apk.read_bytes()
        with mock.patch.object(session.PhoneSession, "remove_package", side_effect=session.CommandError("adb_error", "lost device")):
            code, output = self.verify(*args)
        self.assertEqual(code, 1)
        self.assertEqual(output["cause"], "previous_session_restore_failed")
        self.assertEqual(self.active(), previous)
        self.assertTrue(session.read_json(metadata)["resources_pending"])
        self.assertEqual(self.verify(*args)[0], 0)
        self.assertNotEqual(self.active(), previous)
        self.assertNotIn(session.DRIVERS[0], self.adb.installed)
        self.assertFalse(session.read_json(metadata).get("resources_pending"))

    def test_missing_fail_marker_skips_disconnect_without_preparing(self):
        self.assertEqual(self.verify("--non-interactive", "--already-launched", "--check", "insights-disconnect")[0], 0)
        self.assertEqual(self.executed, [])
        self.assertEqual(self.rows()["insights-disconnect"]["cause"], "capability_missing")

    def test_device_owner_survives_switches_and_a_second_worktree_session_root(self):
        args = ("--non-interactive", "--already-launched", "--check", "home")
        self.assertEqual(self.verify(*args)[0], 0)
        original = self.active()
        state = session.read_json(original / "session.json")
        state.update(resources_pending=True, maestro_preexisting={session.DRIVERS[0]: False}, forwards_before=[])
        session.write_json(original / "session.json", state)
        owner = session.device_resource_owner(self.repo, self.adb.serial)
        session.write_json(owner, {"serial": self.adb.serial, "session": str(original / "session.json")})
        self.adb.installed[session.DRIVERS[0]] = b"first-device-installed-driver"
        device_b = FakeAdb(self.repo)
        device_b.serial = "another-device"
        device_b.installed[self.app] = self.apk.read_bytes()
        with mock.patch.object(session, "Adb", return_value=device_b):
            self.assertEqual(self.verify(*args)[0], 0)
        self.assertTrue(owner.exists())
        self.assertNotEqual(self.active(), original)
        different_root = self.root / "second-worktree-sessions"
        self.assertEqual(self.verify(*args, "--sessions-root", str(different_root))[0], 0)
        self.assertNotIn(session.DRIVERS[0], self.adb.installed)
        self.assertNotIn("maestro_preexisting", session.read_json(original / "session.json"))
        self.assertFalse(owner.exists())

    def test_verify_recovers_legacy_worktree_session_without_an_owner_index(self):
        other = self.root / "old-worktree"
        directory = other / ".local/phone/session"
        legacy = session.PhoneSession(self.repo, directory, directory, self.adb, "old")
        session.create_release_state(legacy, {"path": str(self.apk), "sha256": session.digest(self.apk)},
                                     self.fixture, self.origin, False)
        legacy.state.update(resources_pending=True, maestro_preexisting={session.DRIVERS[0]: False},
                            forwards_before=[])
        legacy.save()
        self.adb.installed[session.DRIVERS[0]] = b"legacy-driver"
        self.assertFalse(session.device_resource_owner(self.repo, self.adb.serial).exists())
        mock_engine = self.engine
        def batch(*args, **kwargs):
            self.assertNotIn(session.DRIVERS[0], self.adb.installed)
            result = self.batch(*args, **kwargs)
            self.assertFalse(session.read_json(self.active() / "session.json")["maestro_preexisting"][session.DRIVERS[0]])
            return result
        mock_engine.run_batch.side_effect = batch
        with mock.patch.object(session, "worktree_roots", return_value=[self.root, other]):
            self.assertEqual(self.verify("--non-interactive", "--already-launched", "--check", "home")[0], 0)
        self.assertFalse(session.read_json(legacy.path).get("resources_pending"))

    def test_verify_and_run_share_the_latest_flow_after_cross_entry_retry(self):
        args = ("--non-interactive", "--already-launched", "--check", "search")
        self.assertEqual(self.verify(*args)[0], 0)
        active = str(self.active())
        self.assertEqual(self.cli("run", "--release", "--session", active, "--group", "search")[0], 0)
        self.assertEqual(self.executed, ["search"])
        self.flow_results["search"] = {"status": "failed", "ui_status": "passed", "server_verification": "failed"}
        self.assertEqual(self.cli("run", "--release", "--session", active, "--group", "search", "--retry")[0], 1)
        self.flow_results.clear()
        self.assertEqual(self.verify(*args)[0], 0)
        self.assertEqual(self.rows()["search"]["status"], "passed")
        self.assertEqual(self.executed, ["search", "search", "search"])

    def test_real_batch_resumes_logged_in_phone_and_accounts_for_confirmed_first_start(self):
        identity = {"instance_id": "fixture-instance", "user": {"name": "users/owner", "display_name": "Test Owner"}}
        api = mock.Mock(csrf="", **{"login.return_value": identity})
        state = {"screen": "welcome", "airplane_mode_on": "0", "wifi_on": "1", "fail_start": True}
        device = self.adb.run
        flows = []

        def run_adb(*args, **kwargs):
            if args[:4] == ("shell", "settings", "get", "global") and args[4] in ("airplane_mode_on", "wifi_on"):
                return state[args[4]]
            if args[:4] == ("shell", "cmd", "connectivity", "airplane-mode"):
                state["airplane_mode_on"] = str(int(args[4] == "enable"))
                return ""
            if args[:3] == ("shell", "svc", "wifi"):
                state["wifi_on"] = str(int(args[3] == "enable"))
                return ""
            if args == ("shell", "dumpsys", "connectivity"):
                return "Active default network: none\nCurrent Networks:\n\nStatus for known UIDs:\n"
            if args[:3] == ("shell", "am", "start"):
                if state["fail_start"]:
                    return "Status: timeout\n"
                flows.append(("start", state["airplane_mode_on"]))
                return f"Status: ok\nActivity: {self.app}/.MainActivity\n"
            return device(*args, **kwargs)

        def dump(*args, **kwargs):
            self.assertEqual(state["airplane_mode_on"], "1")
            tags = {"welcome": ["welcome_enter"], "blank": ["server_login", "server_origin_empty"],
                    "signed-in": ["open_menu"]}[state["screen"]]
            return "<hierarchy>" + "".join(f'<node package="{self.app}" resource-id="{tag}"/>' for tag in tags) + "</hierarchy>"

        def execute(engine, flow, prepared=None, deferred=False):
            values = prepared[0]
            flows.append((flow, state["airplane_mode_on"]))
            if flow == "test-source-welcome":
                self.assertEqual(state["screen"], "welcome")
                state["screen"] = "blank"
            if flow == "test-source":
                self.assertEqual(state["screen"], "blank")
                self.assertIn("test-source", values["NOTE_TEXT"])
                state.update(screen="signed-in", note_name=values["NOTE_NAME"], note_text=values["NOTE_TEXT"])
            if flow == "test-source-resume":
                self.assertEqual((values["NOTE_NAME"], values["NOTE_TEXT"]), (state["note_name"], state["note_text"]))
            log = self.root / flow / "maestro.log"
            log.parent.mkdir(exist_ok=True)
            log.write_text("Maestro result", encoding="utf-8")
            return {"status": "passed", "flow_sha256": session.flow_digest(self.repo, flow), "log": str(log)}

        def engine(*args, **kwargs):
            real = maestro_runner.MaestroRunner(*args, **kwargs)
            real.api = api
            return real

        api.create.side_effect = lambda text, key: {"name": "memos/" + key}
        args = ("--non-interactive", "--check", "language", "--check", "home")
        with mock.patch.object(session, "MaestroRunner", side_effect=engine), \
                mock.patch.object(self.adb, "run", side_effect=run_adb), \
                mock.patch.object(maestro_runner, "fresh_ui_dump", side_effect=dump), \
                mock.patch.object(maestro_runner.MaestroRunner, "run_flow", autospec=True, side_effect=execute), \
                mock.patch.object(maestro_runner.MaestroRunner, "recover_editor_state", return_value=[]), \
                mock.patch.object(maestro_runner, "common_repo", return_value=self.root), \
                mock.patch.object(maestro_runner, "worktree_roots", return_value=[self.root]):
            first_csv = self.telemetry_csv()
            with mock.patch.object(session, "ask", return_value=str(first_csv)):
                self.assertEqual(self.verify(*args)[0], 1)
            metadata = self.active() / "session.json"
            pending = session.read_json(metadata)
            self.assertIn("last_app_launch_attempt_at", pending)
            self.assertFalse(session.first_launch(pending))
            self.assertEqual(flows, [])

            csv = self.root / "recorded-after-failed-start.csv"
            csv.write_bytes(first_csv.read_bytes())
            self.assertEqual(self.cli("record", "--release", "--session", str(self.active()),
                                      "--check", "telemetry-before-launch", "--outcome", "passed",
                                      "--evidence", str(csv))[0], 0)
            self.assertGreater(session.read_json(metadata)["manual_results"]["telemetry-before-launch"]["evidence_captured_at"],
                               pending["last_app_launch_attempt_at"])
            state["fail_start"] = False
            self.assertEqual(self.verify(*args, "--stop-after", "home")[0], 2)
            stopped = session.read_json(metadata)
            self.assertTrue(session.first_launch(stopped))
            self.assertEqual(stopped["source_receipt"]["instance_id"], identity["instance_id"])
            self.assertEqual(flows, [("start", "1"), ("test-source-welcome", "1"), ("test-source", "0"),
                                     ("home-smoke", "0")])

            profile = session.read_json(self.profile)
            profile.pop("confirm_ready")
            session.write_json(self.profile, profile)
            flows.clear()
            self.assertEqual(self.verify(*args)[0], 0)
            self.assertEqual(flows, [("start", "1"), ("test-source-resume", "1"), ("start", "0"), ("language", "0")])
            self.assertEqual(self.rows()["home"]["status"], "reused_pass")
            self.assertEqual(self.rows()["language"]["status"], "passed")
            flows.clear()
            api.reset_mock()
            self.assertEqual(self.verify(*args)[0], 0)
            self.assertEqual(flows, [])
            api.login.assert_not_called()

    def test_old_release_cleanup_leaves_drivers_owned_by_a_later_task(self):
        self.assertEqual(self.verify("--non-interactive", "--already-launched", "--check", "home")[0], 0)
        metadata = self.active() / "session.json"
        state = session.read_json(metadata)
        self.assertNotIn("maestro_preexisting", state)
        state["maestro_preexisting"] = {session.DRIVERS[0]: False}
        session.write_json(metadata, state)
        self.adb.installed[session.DRIVERS[0]] = b"installed-by-later-task"
        self.assertEqual(self.cli("cleanup", "--release", "--session", str(self.active()))[0], 0)
        self.assertEqual(self.adb.installed[session.DRIVERS[0]], b"installed-by-later-task")

    def test_retry_failure_replaces_pass_and_does_not_fall_back_to_baseline(self):
        baseline = self.record_baseline({"search": self.evidence("search")})
        args = ("--non-interactive", "--already-launched", "--check", "search", "--baseline", baseline)
        self.assertEqual(self.verify(*args)[0], 0)
        self.assertEqual(self.rows()["search"]["status"], "reused_baseline")
        self.flow_results["search"] = {"status": "failed", "ui_status": "failed"}
        self.assertEqual(self.verify(*args, "--retry")[0], 1)
        self.assertEqual(session.read_json(self.active() / "session.json")["flow_results"]["search"]["status"], "failed")
        self.flow_results.clear()
        self.assertEqual(self.verify(*args)[0], 0)
        self.assertEqual(self.executed, ["search", "search"])
        self.assertEqual(self.rows()["search"]["status"], "passed")

    def test_preparation_failure_ends_stop_boundary_for_both_requests(self):
        args = ("--non-interactive", "--already-launched", "--check", "editing", "--check", "search")
        self.flow_results["editing"] = {"status": "environment_blocked", "ui_status": "not_run", "cause": "data_preparation"}
        for marker in (False, True):
            with self.subTest(marker=marker):
                self.executed.clear()
                self.on_flow = (lambda _: session.request_stop(self.active())) if marker else None
                stop = () if marker else ("--stop-after", "editing")
                self.assertEqual(self.verify(*args, *stop)[0], 1)
                self.assertEqual(self.executed, ["editing"])
                self.assertEqual(self.rows()["search"]["not_run_reason"], "stopped_at_boundary")

    def test_missed_launch_card_preserves_reason_and_requires_earlier_csv(self):
        self.assertEqual(self.verify("--non-interactive", "--already-launched", "--check", "home")[0], 0)
        text = (self.active() / "manual-cards.md").read_text(encoding="utf-8")
        command = next(line for line in text.splitlines() if "--check telemetry-before-launch " in line)
        self.assertIn("--outcome inconclusive", command)
        self.assertEqual(self.cli(*shlex.split(command)[3:])[0], 0)
        row = self.rows()["telemetry-before-launch"]
        self.assertEqual(row["manual_status"], "inconclusive")
        self.assertIn("错过", row["cause"])
        args = ("record", "--release", "--session", str(self.active()), "--check", "telemetry-before-launch", "--outcome", "passed")
        self.assertEqual(self.cli(*args)[0], 1)
        evidence = self.telemetry_csv()
        first_launch = session.datetime.fromtimestamp(time.time() - 120, session.timezone.utc)
        launch = first_launch.timestamp()
        self.assertEqual(self.cli(*args, "--evidence", str(evidence))[0], 1)
        args = (*args, "--first-launch-at", first_launch.isoformat())
        os.utime(evidence, (launch + 60, launch + 60))
        self.assertEqual(self.cli(*args, "--evidence", str(evidence))[0], 1)
        os.utime(evidence, (launch - 60, launch - 60))
        self.assertEqual(self.cli(*args, "--evidence", str(evidence))[0], 0)
        self.assertEqual(self.rows()["telemetry-before-launch"]["manual_status"], "passed")

    def test_offline_probe_confirms_no_network_and_force_stops_before_restoring_radios(self):
        self.assertEqual(self.verify("--non-interactive", "--already-launched", "--check", "home")[0], 0)
        phone = session.PhoneSession(self.repo, self.active(), self.active(), self.adb, "test")
        original = self.adb.run
        settings = {"airplane_mode_on": "0", "wifi_on": "1"}
        events = []
        def device(*args, **kwargs):
            if args[:4] == ("shell", "settings", "get", "global") and args[4] in settings:
                return settings[args[4]]
            if args[:4] == ("shell", "cmd", "connectivity", "airplane-mode"):
                settings["airplane_mode_on"] = str(int(args[4] == "enable"))
                events.append("airplane:" + settings["airplane_mode_on"])
                return ""
            if args[:3] == ("shell", "svc", "wifi"):
                settings["wifi_on"] = str(int(args[3] == "enable"))
                events.append("wifi:" + settings["wifi_on"])
                return ""
            if args == ("shell", "dumpsys", "connectivity"):
                events.append("network-checked")
                return "Active default network: none\n\nCurrent Networks:\n\nStatus for known UIDs:\n"
            if args[:3] == ("shell", "am", "force-stop"):
                events.append("force-stop")
            return original(*args, **kwargs)
        with mock.patch.object(self.adb, "run", side_effect=device):
            with phone.isolate_source():
                self.assertEqual(settings, {"airplane_mode_on": "1", "wifi_on": "0"})
                events.append("offline-app-start")
        self.assertEqual(events, ["airplane:1", "wifi:0", "network-checked", "force-stop",
                                  "offline-app-start", "force-stop", "airplane:0", "wifi:1"])
        self.assertNotIn("source_network_before", phone.state)

    def test_offline_probe_does_not_start_app_when_ethernet_remains(self):
        self.assertEqual(self.verify("--non-interactive", "--already-launched", "--check", "home")[0], 0)
        phone = session.PhoneSession(self.repo, self.active(), self.active(), self.adb, "test")
        self.adb.network.update(airplane_mode_on="1", wifi_on="0")
        device = self.adb.run
        def connected(*args, **kwargs):
            if args == ("shell", "dumpsys", "connectivity"):
                return ("Active default network: none\nCurrent Networks:\n"
                        "  NetworkAgentInfo{network{100} Transports: ETHERNET}\n\nStatus for known UIDs:\n")
            return device(*args, **kwargs)
        with mock.patch.object(self.adb, "run", side_effect=connected), \
                mock.patch.object(session.time, "monotonic", side_effect=[0, 13]):
            with self.assertRaises(session.CommandError) as caught, phone.isolate_source():
                self.fail("隔离未确认时不得启动 App")
        self.assertEqual(caught.exception.code, "source_network_not_isolated")

    def test_capture_reloads_recovered_network_checkpoint_before_starting_new_work(self):
        self.assertEqual(self.verify("--non-interactive", "--already-launched", "--check", "home")[0], 0)
        phone = session.PhoneSession(self.repo, self.active(), self.active(), self.adb, "test")
        self.adb.network.update(airplane_mode_on="0", wifi_on="1")
        phone.state["source_network_before"] = {"airplane_mode_on": "0", "wifi_on": "1"}
        phone.state["files"] = ["/sdcard/Download/moerise-maestro-" + "a" * 32 + ".txt"]
        phone.state["resources_pending"] = True
        phone.capture_resources()
        self.assertNotIn("source_network_before", phone.state)
        self.assertEqual(phone.state["files"], [])
        self.assertEqual(phone.restore_resources(), [])

    def test_interrupted_channel_cleanup_error_is_in_top_level_report(self):
        def interrupt():
            self.engine.batch_cleanup_errors = ["channel_settings_restore"]
            raise KeyboardInterrupt
        self.interrupt = interrupt
        code, report = self.verify("--non-interactive", "--already-launched", "--check", "navigation-settings")
        self.assertEqual(code, 1)
        self.assertEqual(report["status"], "interrupted")
        self.assertEqual(report["cleanup_errors"], ["channel_settings_restore"])


class SharedDeviceLock(unittest.TestCase):
    def test_different_worktrees_and_manual_fail_before_any_adb(self):
        root = Path(__file__).resolve().parents[2] / ".local/android"
        root.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="phone-lock-test-", dir=root) as directory:
            repo, other = Path(directory) / "main", Path(directory) / "linked"
            def git(*args):
                subprocess.run(["git", *map(str, args)], check=True, capture_output=True)
            git("init", "--quiet", repo)
            git("-C", repo, "-c", "user.name=Phone Host Test", "-c", "user.email=host-test@example.invalid",
                "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "test")
            git("-C", repo, "worktree", "add", "--detach", other)
            self.assertEqual(session.common_repo(repo), session.common_repo(other))
            commit = session.git(repo, "rev-parse", "HEAD")
            with session.device_lock(repo, "verify", commit), \
                    mock.patch.object(session, "discover_serial") as discover, \
                    mock.patch.object(session, "Adb") as adb:
                for source in (repo, other):
                    with mock.patch.object(sys, "argv", ["phone-session", "manual", "--repo", str(source),
                            "--session", str(source / "session")]), redirect_stdout(io.StringIO()) as out:
                        self.assertEqual(session.main(), 1)
                    self.assertEqual(json.loads(out.getvalue())["cause"], "device_busy")
                discover.assert_not_called()
                adb.assert_not_called()


if __name__ == "__main__":
    unittest.main()
