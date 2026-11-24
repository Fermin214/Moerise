"""Host checks for Maestro isolation, failure evidence and server assertions."""
import base64
from contextlib import nullcontext
import hashlib
import io
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest import mock

import maestro_runner as runner


class FixtureRequestTest(unittest.TestCase):
    def setUp(self):
        self.api = runner.FixtureAPI({
            "origin": "https://acceptance.example", "environment": "test",
            "username": "test-owner", "password": "private-password"},
            trusted_https=True, test_origin="https://acceptance.example")

    def response(self, data, status=200):
        response = mock.MagicMock()
        response.__enter__.return_value = response
        response.read.return_value = data
        response.status = status
        return response

    def test_login_and_authenticated_requests_identify_the_acceptance_client(self):
        with mock.patch.object(self.api.opener, "open", side_effect=[
                self.response(b'{"csrf_token":"private-csrf"}'), self.response(b'{"notes":[]}')]) as request:
            self.api.login()
            self.api.request("GET", "/api/app/v1/notes")
        for call in request.call_args_list:
            self.assertEqual(call.args[0].get_header("User-agent"), runner.USER_AGENT)
            self.assertEqual(call.args[0].get_header("Accept"), "application/json")
        self.assertEqual(request.call_args.args[0].get_header("X-csrf-token"), "private-csrf")

    def test_cloudflare_rejection_reports_only_safe_request_details(self):
        error = runner.urllib.error.HTTPError(
            "https://acceptance.example/api/app/v1/session", 403, "Forbidden",
            {"Server": "cloudflare"}, io.BytesIO(b"error code: 1010\nprivate-password"))
        with mock.patch.object(self.api.opener, "open", side_effect=error):
            with self.assertRaises(runner.FixtureError) as caught:
                self.api.request("POST", "/api/app/v1/session?password=private-password", {})
        details = caught.exception.details()
        self.assertEqual(details["error_code"], "CLOUDFLARE_1010")
        self.assertEqual(details["http_status"], 403)
        self.assertEqual(details["request"], {"method": "POST", "path": "/api/app/v1/session"})
        self.assertNotIn("private-password", json.dumps(details))
        self.assertTrue(error.fp.closed)

    def test_core_error_keeps_its_code_without_echoing_response_secrets(self):
        error = runner.urllib.error.HTTPError(
            self.api.origin, 401, "Unauthorized", {"Content-Type": "application/json"},
            io.BytesIO(b'{"error":{"code":"AUTH_FAILED","message":"private-password"}}'))
        with mock.patch.object(self.api.opener, "open", side_effect=error):
            with self.assertRaises(runner.FixtureError) as caught:
                self.api.login()
        self.assertEqual(caught.exception.code, "AUTH_FAILED")
        self.assertNotIn("private-password", str(caught.exception))

    def test_network_errors_explain_the_failure_without_echoing_the_url(self):
        cases = [
            (runner.ssl.SSLCertVerificationError("private-password"), "TLS 证书校验失败"),
            (TimeoutError("private-password"), "请求超时"),
            (runner.socket.gaierror("private-password"), "域名解析失败"),
            (runner.http.client.IncompleteRead(b"private-password"), "服务响应不完整或连接已中断"),
        ]
        for reason, expected in cases:
            with self.subTest(reason=type(reason).__name__):
                with mock.patch.object(self.api.opener, "open", side_effect=runner.urllib.error.URLError(reason)):
                    with self.assertRaises(runner.FixtureError) as caught:
                        self.api.login()
                self.assertIn(expected, str(caught.exception))
                self.assertNotIn("private-password", str(caught.exception))
                self.assertEqual(caught.exception.details()["request"]["path"], "/api/app/v1/session")

    def test_non_json_success_is_a_reportable_fixture_error(self):
        with mock.patch.object(self.api.opener, "open", return_value=self.response(b"<html>private-password</html>")):
            with self.assertRaises(runner.FixtureError) as caught:
                self.api.login()
        self.assertEqual(caught.exception.code, "invalid_json")
        self.assertNotIn("private-password", str(caught.exception))


class FakeAdb:
    serial = "emulator-5584"

    def __init__(self):
        self.calls = []

    def run(self, *args, **kwargs):
        self.calls.append(args)
        return ""


class MaestroExecutionTest(unittest.TestCase):
    def setUp(self):
        root = runner.ROOT / ".local/android"
        root.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(prefix="maestro-host-test-", dir=root)
        self.addCleanup(self.temp.cleanup)
        self.output = Path(self.temp.name)
        self.connection = self.output / "connection.json"
        self.connection.write_text(json.dumps({
            "origin": "http://127.0.0.1:1501", "username": "android-owner",
            "password": "isolated-password-value", "sourceToken": "isolated-token-value"}))
        self.adb = FakeAdb()
        self.engine = runner.MaestroRunner(runner.ROOT, self.adb, self.connection,
                                           self.output / "execution", "maestro-host-test")
        self.engine.api = mock.Mock()
        self.engine.api.csrf = ""
        for patcher in (
            mock.patch.object(runner, "common_repo", return_value=self.output),
            mock.patch.object(runner, "worktree_roots", return_value=[self.output]),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)
        self.validate = mock.patch.object(runner.subprocess, "run",
                                         return_value=mock.Mock(returncode=0, stdout=b"", stderr=b""))
        self.validate.start()
        self.addCleanup(self.validate.stop)

    def values(self):
        return {"APP_ID": runner.APP, "SCREENSHOT_PREFIX": "login"}

    def successful_execution(self, command, log, timeout, environment):
        report = Path(command[command.index("--output") + 1])
        report.write_text('<testsuite><testcase name="host-test"/></testsuite>')
        encoded = base64.urlsafe_b64encode(("isolated-password-value" + runner.UNICODE_INPUT_SUFFIX).encode())
        Path(log).write_text(
            "isolated-password-value isolated-token-value " + encoded.decode().rstrip("="), encoding="utf-8")
        return 0

    def test_credentials_use_process_environment_and_artifacts_are_redacted(self):
        with mock.patch.object(self.engine, "prepare_data", return_value=(self.values(), None)), \
                mock.patch.object(runner, "execute", side_effect=self.successful_execution) as execute:
            result = self.engine.run_flow("login")
        self.assertEqual(result["status"], "passed")
        command, log, _, environment = execute.call_args.args
        self.assertIn("--no-reinstall-driver", command)
        self.assertEqual(command[command.index("--udid") + 1], self.adb.serial)
        self.assertNotIn("isolated-password-value", " ".join(command))
        self.assertNotIn("isolated-token-value", " ".join(command))
        self.assertEqual(environment["MAESTRO_FIXTURE_PASSWORD"], "isolated-password-value")
        self.assertNotIn("isolated-password-value", Path(log).read_text())
        self.assertNotIn("isolated-token-value", Path(log).read_text())
        self.assertEqual(Path(log).read_text(), "[redacted] [redacted] [redacted]")
        self.assertFalse(any(call[0] in ("install", "uninstall") for call in self.adb.calls))
        self.engine.api.close.assert_called_once()

    def test_timeout_preserves_result_and_removes_only_the_created_test_file(self):
        remote = "/sdcard/Download/moerise-maestro-" + "a" * 32 + ".txt"
        with mock.patch.object(self.engine, "prepare_data", return_value=(self.values(), remote)), \
                mock.patch.object(runner, "execute", side_effect=runner.subprocess.TimeoutExpired("test", 1)):
            result = self.engine.run_flow("login")
        self.assertEqual(result["status"], "inconclusive")
        self.assertIn(("shell", "rm", "-f", remote), self.adb.calls)
        self.assertTrue((self.engine.output / "login/result.json").exists())
        self.engine.api.close.assert_called_once()

    def test_phone_input_method_restores_default_and_enabled_state_on_timeout(self):
        original = "org.example.ime/.Service"
        active, enabled = original, False
        self.adb.serial = "real-phone"
        checkpoint = runner.input_method_checkpoint(self.output, self.adb.serial)

        def phone(*args):
            nonlocal active, enabled
            if args == ("shell", "settings", "get", "secure", "default_input_method"):
                return active + "\r\n"
            if args == ("shell", "ime", "list", "-a", "-s"):
                return original + "\r\n" + runner.MAESTRO_IME + "\r\n"
            if args == ("shell", "ime", "list", "-s"):
                return original + ("\r\n" + runner.MAESTRO_IME if enabled else "")
            if args[:3] == ("shell", "ime", "set"):
                active = args[-1]
            elif args[:3] in (("shell", "ime", "enable"), ("shell", "ime", "disable")):
                enabled = args[2] == "enable"
            else:
                self.fail("Unexpected ADB operation")
            return ""

        self.adb.run = phone
        with self.assertRaises(TimeoutError):
            with runner.preserve_input_method(self.adb, checkpoint):
                active, enabled = runner.MAESTRO_IME, True  # CLI switched before it timed out.
                self.assertEqual((active, enabled), (runner.MAESTRO_IME, True))
                self.assertTrue(checkpoint.exists())
                raise TimeoutError
        self.assertEqual((active, enabled), (original, False))
        self.assertFalse(checkpoint.exists())

    def test_fixture_failure_does_not_start_device_interactions(self):
        self.engine.api.login.side_effect = runner.FixtureError(503, "unavailable")
        with mock.patch.object(runner, "execute") as execute:
            result = self.engine.run_flow("login")
        self.assertEqual(result["status"], "environment_blocked")
        self.assertEqual(self.adb.calls, [])
        execute.assert_not_called()

    def test_batch_login_failure_reports_request_and_stage_for_all_modules(self):
        self.engine.phone = True
        self.engine.api.login.side_effect = runner.FixtureError(
            403, "CLOUDFLARE_1010", method="POST", path="/api/app/v1/session")
        flows = [flow for group in self.engine.catalog["groups"].values() for flow in group]
        with mock.patch.object(self.engine, "prepare_data") as prepare, \
                mock.patch.object(self.engine, "run_flow") as execute:
            results = self.engine.run_batch(flows)
        for flow, result in results.items():
            self.assertEqual(result["preparation_step"], "session_login")
            self.assertEqual(result["request"], {"method": "POST", "path": "/api/app/v1/session"})
            self.assertEqual((result["http_status"], result["error_code"]), (403, "CLOUDFLARE_1010"))
            self.assertEqual(json.loads((self.engine.output / flow / "result.json").read_text(encoding="utf-8")), result)
        prepare.assert_not_called()
        execute.assert_not_called()
        self.assertEqual(self.adb.calls, [])

    def test_batch_source_preparation_failure_is_distinguished_from_login(self):
        self.engine.phone = True
        with mock.patch.object(self.engine, "probe_phone_offline", return_value={"status": "passed"}), \
                mock.patch.object(self.engine, "prepare_data", side_effect=runner.FixtureError(
                503, "UPSTREAM_UNAVAILABLE", method="POST", path="/api/app/v1/notes")), \
                mock.patch.object(self.engine, "run_flow") as execute:
            result = self.engine.run_batch(["search"])["search"]
        self.assertEqual(result["preparation_step"], "test_source_data")
        self.assertEqual(result["request"]["path"], "/api/app/v1/notes")
        execute.assert_not_called()

    def test_batch_failure_checks_only_local_results_until_checkpoint_stops(self):
        self.engine.phone = True
        self.engine.api.login.side_effect = runner.FixtureError(503, "UNAVAILABLE")
        evidence = {"status": "reused_pass", "log": str(self.output / "previous" / "maestro.log")}
        select = mock.Mock(side_effect=[None, None, evidence])
        checkpoint = mock.Mock(side_effect=[False, True])
        results = self.engine.run_batch(["editing", "language", "navigation-map"], on_result=checkpoint, select=select)
        self.assertEqual(list(results), ["editing", "language"])
        self.assertEqual(results["editing"]["preparation_step"], "session_login")
        self.assertEqual(results["language"]["log"], evidence["log"])
        self.assertEqual(results["language"]["status"], "reused_pass")
        self.assertFalse(Path(evidence["log"]).parent.exists())
        self.assertEqual(select.call_args_list, [
            mock.call("editing", self.engine),
            mock.call("editing", self.engine, local_only=True),
            mock.call("language", self.engine, local_only=True),
        ])
        self.assertEqual([call.args[0] for call in checkpoint.call_args_list], ["editing", "language"])
        self.engine.api.login.assert_called_once()

    def test_preparation_error_messages_redact_credentials_before_writing_reports(self):
        with mock.patch.object(self.engine, "prepare_data", side_effect=ValueError(
                "invalid isolated-password-value isolated-token-value android-owner")):
            result = self.engine.run_batch(["search"])["search"]
        self.assertEqual(result["preparation_step"], "search.data_preparation")
        self.assertEqual(result["error_message"], "invalid [redacted] [redacted] [redacted]")
        serialized = (self.engine.output / "search/result.json").read_text(encoding="utf-8")
        self.assertNotIn("isolated-password-value", serialized)
        self.assertNotIn("isolated-token-value", serialized)

    def test_release_mode_requires_emulator_and_trusted_loopback_https(self):
        connection = json.loads(self.connection.read_text())
        connection["origin"] = "https://localhost:28443"
        self.connection.write_text(json.dumps(connection))
        with self.assertRaises(ValueError):
            runner.FixtureAPI(connection)
        self.adb.serial = "real-phone"
        with self.assertRaises(ValueError):
            runner.MaestroRunner(runner.ROOT, self.adb, self.connection, self.output / "release", release=True)
        self.adb.serial = "emulator-5554"
        release = runner.MaestroRunner(runner.ROOT, self.adb, self.connection, self.output / "release", release=True)
        self.assertEqual(release.app, runner.APP.removesuffix(".fixture"))
        connection["origin"] = "https://notes.example:443"
        with self.assertRaises(ValueError):
            runner.FixtureAPI(connection, trusted_https=True)

    def test_release_phone_requires_explicit_test_environment_and_matching_origin(self):
        connection = json.loads(self.connection.read_text())
        connection.update(origin="https://phone-test.example", environment="test")
        self.connection.write_text(json.dumps(connection))
        self.adb.serial = "real-phone"
        engine = runner.MaestroRunner(runner.ROOT, self.adb, self.connection, self.output / "phone",
                                      release=True, phone=True, test_origin=connection["origin"])
        self.assertEqual(engine.app, "io.github.fermin214.moerise")
        with self.assertRaises(ValueError):
            runner.MaestroRunner(runner.ROOT, self.adb, self.connection, self.output / "phone",
                                release=True, phone=True, test_origin="https://other.example")
        connection.pop("environment")
        with self.assertRaises(ValueError):
            runner.FixtureAPI(connection, trusted_https=True, test_origin=connection["origin"])
        runner.FixtureAPI(connection, trusted_https=True, test_origin=connection["origin"], allow_production=True)

    def test_dump_killed_does_not_read_stale_remote_or_local_xml(self):
        output = self.output / "window.xml"
        output.write_text('<hierarchy><node resource-id="open_search"/></hierarchy>')
        def killed(*args, **kwargs):
            if args[:3] == ("shell", "uiautomator", "dump"):
                raise RuntimeError("137 Killed")
            return ""
        with mock.patch.object(self.adb, "run", side_effect=killed) as adb:
            with self.assertRaisesRegex(RuntimeError, "137"):
                runner.fresh_ui_dump(self.adb, output)
        self.assertFalse(output.exists())
        calls = [call.args for call in adb.call_args_list]
        self.assertFalse(any(call[:2] == ("shell", "cat") for call in calls))
        dump = next(index for index, call in enumerate(calls) if call[:3] == ("shell", "uiautomator", "dump"))
        for package in runner.DRIVERS:
            self.assertIn(("shell", "am", "force-stop", package), calls[:dump])
        self.assertTrue(calls[-1][3].endswith(".xml"))

    def test_dump_uses_unique_paths_and_rejects_invalid_xml(self):
        output = self.output / "window.xml"
        def dump(*args, **kwargs):
            self.adb.calls.append(args)
            return '<hierarchy><node resource-id="open_search"/></hierarchy>' if args[:2] == ("shell", "cat") else ""
        with mock.patch.object(self.adb, "run", side_effect=dump):
            runner.fresh_ui_dump(self.adb, output)
            runner.fresh_ui_dump(self.adb, output)
        paths = [call[3] for call in self.adb.calls if call[:3] == ("shell", "uiautomator", "dump")]
        self.assertEqual(len(set(paths)), 2)
        with mock.patch.object(self.adb, "run", return_value="invalid"):
            with self.assertRaises(runner.ET.ParseError):
                runner.fresh_ui_dump(self.adb, output)
        self.assertFalse(output.exists())

    def test_batch_checks_and_persists_each_flow_before_preparing_the_next(self):
        events = []
        flows = ["editing", "actions"]
        def prepare(flow, entry):
            events.append("prepare:" + flow)
            return {"flow": flow}, None
        def execute(flow, **kwargs):
            self.assertTrue(kwargs["deferred"])
            events.append("ui:" + flow)
            return {"status": "harness_error" if flow == "editing" else "passed"}
        def save(flow, result):
            self.assertTrue((self.engine.output / flow / "result.json").exists())
            events.append("save:" + flow)
        with mock.patch.object(self.engine, "prepare_conflict"), \
                mock.patch.object(self.engine, "prepare_data", side_effect=prepare), \
                mock.patch.object(self.engine, "run_flow", side_effect=execute), \
                mock.patch.object(self.engine, "verify", side_effect=lambda flow, values: events.append("observe:" + flow)), \
                mock.patch.object(self.engine, "wait_verify", side_effect=lambda flow, values: events.append("verify:" + flow)):
            result = self.engine.run_batch(flows, on_result=save)
        self.assertEqual(events, ["prepare:editing", "ui:editing", "observe:editing", "save:editing",
                                  "prepare:actions", "ui:actions", "verify:actions", "save:actions"])
        self.assertEqual(result["editing"]["status"], "harness_error")
        self.assertEqual(result["actions"]["status"], "passed")
        self.engine.api.close.assert_called_once()

    def test_navigation_precondition_failure_does_not_block_other_modules(self):
        with mock.patch.object(self.engine, "prepare_channel", side_effect=ValueError("configured")), \
                mock.patch.object(self.engine, "prepare_data", return_value=({}, None)), \
                mock.patch.object(self.engine, "run_flow", return_value={"status": "passed"}) as execute, \
                mock.patch.object(self.engine, "wait_verify", return_value={}):
            result = self.engine.run_batch(["navigation-settings", "search"])
        self.assertEqual(result["navigation-settings"]["status"], "harness_error")
        self.assertEqual(result["search"]["status"], "passed")
        self.assertEqual(execute.call_args.args[0], "search")

    def test_conflict_postcheck_requires_stale_source_rejection_and_remote_preservation(self):
        self.engine.conflict_fixture = {"original": {"name": "memos/conflict", "source_hash": "old", "content": "old"},
                                        "changed": {"name": "memos/conflict", "source_hash": "new", "content": "new"}}
        self.engine.api.request.side_effect = runner.FixtureError(409, "SOURCE_CHANGED")
        self.engine.api.note.return_value = self.engine.conflict_fixture["changed"]
        self.assertTrue(self.engine.verify_conflict()["stale_source_rejected"])
        self.engine.api.request.side_effect = None
        with self.assertRaisesRegex(AssertionError, "stale_source_accepted"):
            self.engine.verify_conflict()

    def test_phone_source_preflight_failure_stops_business_writes(self):
        self.engine.phone = True
        with mock.patch.object(self.engine, "probe_phone_offline", return_value={"status": "passed"}), \
                mock.patch.object(self.engine, "prepare_data", return_value=({}, None)) as prepare, \
                mock.patch.object(self.engine, "run_flow", return_value={"status": "inconclusive"}) as execute, \
                mock.patch.object(self.engine, "prepare_channel") as channel:
            result = self.engine.run_batch(["editing", "navigation"])
        self.assertEqual(prepare.call_args.args[0], "test-source")
        self.assertEqual(execute.call_count, 1)
        channel.assert_not_called()
        self.assertTrue(all(item["cause"] == "phone_test_source_unverified" for item in result.values()))
        self.assertTrue(all(item["status"] == "environment_blocked" for item in result.values()))

    def test_recording_timeout_still_pulls_and_removes_remote_file(self):
        self.engine.record = True
        self.adb.executable = "fake-adb"
        process = mock.Mock()
        process.poll.return_value = None
        process.wait.side_effect = [runner.subprocess.TimeoutExpired("screenrecord", 10), 0]
        with mock.patch.object(runner.subprocess, "Popen", return_value=process):
            with self.assertRaises(runner.subprocess.TimeoutExpired):
                with self.engine.recording(self.output):
                    pass
        self.assertTrue(any(call[0] == "pull" and call[1].endswith(".mp4") for call in self.adb.calls))
        self.assertTrue(any(call[:3] == ("shell", "rm", "-f") and call[3].endswith(".mp4") for call in self.adb.calls))
        process.kill.assert_called_once()

    def test_channel_lost_patch_response_retains_checkpoint_and_restores(self):
        checkpoint = self.output / "channel.json"
        initial = {"version": 1, "settings": {"channel": {"kind": "none", "qq_app_id": ""},
                                               "notifications": {"enabled": True}},
                   "secrets": {"qq_app_secret": {"configured": False}}}
        mutated = {"version": 2, "settings": {"channel": {"kind": "none", "qq_app_id": "100000019"}}}
        self.engine.api.request.side_effect = [initial, runner.FixtureError(0, "unreachable"), mutated, {}]
        with mock.patch.object(self.engine, "channel_checkpoint", return_value=checkpoint):
            with self.assertRaises(runner.FixtureError):
                self.engine.prepare_channel()
            self.assertTrue(checkpoint.exists())
            self.assertIsNotNone(self.engine.channel_fixture)
            self.engine.restore_channel(self.engine.channel_fixture)
        self.assertFalse(checkpoint.exists())
        self.assertEqual(self.engine.api.request.call_args.args[2]["settings"]["notifications"], {"enabled": True})

    def test_known_draft_recovery_clears_only_exact_recorded_content(self):
        checkpoint = self.output / "drafts.json"
        marker = "Maestro draft " + "a" * 32
        checkpoint.write_text(json.dumps([marker]))
        xml = '<hierarchy><node resource-id="note_editor" text="' + marker + '"/></hierarchy>'
        with mock.patch.object(self.engine, "draft_checkpoint", return_value=checkpoint), \
                mock.patch.object(self.engine, "run_flow", return_value={"status": "passed"}) as run, \
                mock.patch.object(runner, "fresh_ui_dump", side_effect=[xml, xml.replace(marker, "")]):
            self.engine.recover_editor_state()
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.kwargs["prepared"][0]["DRAFT_TEXT"], marker)
        self.assertFalse(checkpoint.exists())
        checkpoint.write_text(json.dumps([marker]))
        with mock.patch.object(self.engine, "draft_checkpoint", return_value=checkpoint), \
                mock.patch.object(self.engine, "run_flow", return_value={"status": "passed"}) as run, \
                mock.patch.object(runner, "fresh_ui_dump", return_value=xml.replace(marker, "personal draft")):
            result = self.engine.recover_editor_state()
        run.assert_not_called()
        self.assertEqual(result[0]["cause"], "unknown_draft_preserved")
        self.assertTrue(checkpoint.exists())

    def test_draft_dump_failure_keeps_checkpoint_and_does_not_escape_batch(self):
        checkpoint = self.output / "drafts.json"
        checkpoint.write_text(json.dumps(["Maestro draft " + "a" * 32]))
        for error in (RuntimeError("137"), runner.ET.ParseError("bad xml")):
            with self.subTest(error=type(error).__name__), \
                    mock.patch.object(self.engine, "draft_checkpoint", return_value=checkpoint), \
                    mock.patch.object(self.engine, "run_flow", return_value={"status": "passed"}), \
                    mock.patch.object(runner, "fresh_ui_dump", side_effect=error):
                result = self.engine.recover_editor_state()
            self.assertEqual(result[0]["cause"], "draft_ui_collection")
            self.assertTrue(checkpoint.exists())

    def test_unreported_or_failed_maestro_flow_cannot_pass(self):
        report = self.output / "report.xml"
        self.assertEqual(runner.junit_result(report, 0)["status"], "harness_error")
        report.write_text('<testsuite><testcase><failure message="missing"/></testcase></testsuite>')
        self.assertEqual(runner.junit_result(report, 0)["status"], "inconclusive")
        report.write_text('<testsuite><testcase/></testsuite>')
        self.assertEqual(runner.junit_result(report, 1)["status"], "harness_error")

    def test_driver_death_is_an_environment_failure(self):
        report = self.output / "driver-report.xml"
        report.write_text('<testsuite><testcase><failure message="maestro.android.DeviceServerDiedException: '
                          'UNAVAILABLE"/></testcase></testsuite>')
        result = runner.junit_result(report, 1)
        self.assertEqual(result["status"], "environment_blocked")
        self.assertEqual(result["cause"], "maestro_driver_died")

    def test_server_observation_cannot_turn_a_failed_ui_flow_into_a_pass(self):
        def failed_execution(command, log, timeout, environment):
            report = Path(command[command.index("--output") + 1])
            report.write_text('<testsuite><testcase><failure message="missing UI result"/></testcase></testsuite>')
            Path(log).write_text("UI did not reach the expected result")
            return 1
        with mock.patch.object(self.engine, "prepare_data", return_value=(self.values(), None)), \
                mock.patch.object(runner, "execute", side_effect=failed_execution), \
                mock.patch.object(self.engine, "verify", return_value={"edited_note": True}):
            result = self.engine.run_flow("editing")
        self.assertEqual(result["status"], "inconclusive")
        self.assertEqual(result["server_observation"]["status"], "passed")
        self.assertTrue(result["server_observation"]["assertions"]["edited_note"])

    def test_ui_pass_without_server_postcondition_fails(self):
        values = {"NOTE_NAME": "memos/first", "EDITED_TEXT": "edited", "NEW_NOTE_TEXT": "created",
                  "PEER_NOTE_NAME": "memos/peer", "PEER_NOTE_TEXT": "peer"}
        self.engine.api.note.return_value = {"content": "unchanged"}
        with self.assertRaisesRegex(AssertionError, "edited_note"):
            self.engine.verify("editing", values)
        self.engine.api.note.side_effect = lambda name: {"content": "edited" if name == "memos/first" else "peer"}
        self.engine.api.matching_notes.return_value = [{"content": "created"}, {"content": "created"}]
        with self.assertRaisesRegex(AssertionError, "created_note_count"):
            self.engine.verify("editing", values)

    def test_archived_note_does_not_count_as_deleted(self):
        values = {"NOTE_NAME": "memos/first", "PEER_NOTE_NAME": "memos/peer"}
        self.engine.api.note.return_value = {"state": "ARCHIVED"}
        with self.assertRaisesRegex(AssertionError, "deleted_note"):
            self.engine.verify("actions", values)
        self.engine.api.note.side_effect = [runner.FixtureError(404, "NOT_FOUND"), runner.FixtureError(404, "NOT_FOUND")]
        self.assertEqual(self.engine.verify("actions", values)["deleted_note_count"], 2)

    def test_cancelled_peer_revision_must_not_reach_the_server(self):
        values = {"NOTE_NAME": "memos/first", "EDITED_TEXT": "edited", "NEW_NOTE_TEXT": "created",
                  "PEER_NOTE_NAME": "memos/peer", "PEER_NOTE_TEXT": "peer"}
        self.engine.api.note.side_effect = [{"content": "edited"}, {"content": "cancelled revision"}]
        with self.assertRaisesRegex(AssertionError, "cancelled_edit_uploaded"):
            self.engine.verify("editing", values)

    def test_pending_upload_is_polled_without_repeating_ui_writes(self):
        with mock.patch.object(self.engine, "verify", side_effect=[
                AssertionError("created_note_count"), {"created_note_count": 1}]) as verify, \
                mock.patch.object(runner.time, "sleep"):
            self.assertEqual(self.engine.wait_verify("editing", {}, timeout=1), {"created_note_count": 1})
        self.assertEqual(verify.call_count, 2)
        self.engine.api.create.assert_not_called()

    def test_cancelled_revision_or_wrong_attachment_bytes_is_not_hidden_by_polling(self):
        for assertion in ("cancelled_edit_uploaded", "attachment_bytes"):
            with self.subTest(assertion=assertion), \
                    mock.patch.object(self.engine, "verify", side_effect=AssertionError(assertion)) as verify, \
                    mock.patch.object(runner.time, "sleep") as sleep:
                with self.assertRaisesRegex(AssertionError, assertion):
                    self.engine.wait_verify("editing", {})
                self.assertEqual(verify.call_count, 1)
                sleep.assert_not_called()

    def test_attachment_requires_the_actual_bytes_not_just_a_filename(self):
        values = {"ATTACHMENT_NOTE_TEXT": "file note", "ATTACHMENT_NAME": "test.txt",
                  "ATTACHMENT_SHA256": hashlib.sha256(b"expected").hexdigest()}
        self.engine.api.matching_notes.return_value = [{
            "content": "file note", "attachments": [
                {"filename": "test.txt", "content_url": "/api/app/v1/notes/one/attachments/file/content"}]}]
        self.engine.api.request.return_value = b"wrong"
        with self.assertRaisesRegex(AssertionError, "attachment_bytes"):
            self.engine.verify("attachments", values)
        self.engine.api.request.return_value = b"expected"
        self.assertTrue(self.engine.verify("attachments", values)["attachment_bytes_match"])

    def test_common_environment_failure_stops_group_cascade(self):
        self.engine.catalog = {"groups": {"test": ["first", "second"]}}
        with mock.patch.object(self.engine, "run_flow",
                               return_value={"status": "environment_blocked"}) as run:
            result = self.engine.run_group("test")
        self.assertEqual(list(result), ["first"])
        self.assertEqual(run.call_count, 1)

    def test_pass_identity_changes_when_a_shared_subflow_changes(self):
        root = self.output / "repo"
        target = root / "scripts/android/maestro"
        shutil.copytree(runner.ROOT / "scripts/android/maestro", target)
        before = runner.flow_digest(root, "editing")
        search = runner.flow_digest(root, "search")
        language = runner.flow_digest(root, "language")
        path = target / "search.yaml"
        path.write_bytes(path.read_bytes().replace(b"name:", b"name: Changed", 1))
        self.assertNotEqual(search, runner.flow_digest(root, "search"))
        self.assertEqual(before, runner.flow_digest(root, "editing"))
        self.assertEqual(language, runner.flow_digest(root, "language"))
        with mock.patch.dict(runner.FLOW_CONTRACT_VERSIONS, editing=2):
            self.assertNotEqual(before, runner.flow_digest(root, "editing"))
            self.assertEqual(language, runner.flow_digest(root, "language"))
        path = target / "subflows/home.yaml"
        path.write_bytes(path.read_bytes().replace(b"name:", b"name: Changed", 1))
        self.assertNotEqual(before, runner.flow_digest(root, "editing"))

    def test_existing_inline_editor_is_cancelled_once_only_if_registered(self):
        checkpoint = self.output / "drafts.json"
        checkpoint.write_text(json.dumps({"drafts": [], "notes": ["memos/known"]}))
        inline = ('<hierarchy><node resource-id="note_editor_memos/known"/>'
                  '<node resource-id="cancel_note_memos/known" enabled="true" bounds="[0,0][10,20]"/></hierarchy>')
        home = '<hierarchy><node resource-id="note_editor" text=""/></hierarchy>'
        tap = ("shell", "input", "tap", "5", "10")
        with mock.patch.object(self.engine, "draft_checkpoint", return_value=checkpoint), \
                mock.patch.object(runner, "fresh_ui_dump", side_effect=[inline, home]):
            self.assertEqual(self.engine.recover_editor_state(), [{"status": "passed"}])
        self.assertEqual(self.adb.calls.count(tap), 1)
        self.assertFalse(checkpoint.exists())
        with mock.patch.object(self.engine, "draft_checkpoint", return_value=checkpoint), \
                mock.patch.object(runner, "fresh_ui_dump", return_value=inline):
            self.assertEqual(self.engine.recover_editor_state()[0]["cause"], "unknown_editor_preserved")
        self.assertEqual(self.adb.calls.count(tap), 1)

    def test_source_probe_checks_account_before_sync_without_composer_dependency(self):
        self.engine.phone = True
        self.engine.api.login.return_value = {"user": {"name": "users/owner", "display_name": "Tablet Test"}}
        maestro = runner.ROOT / "scripts/android/maestro"
        source = (maestro / "test-source.yaml").read_text(encoding="utf-8")
        steps = list(runner.yaml.safe_load_all(source))[1]
        marker = {"text": r"\Q${NOTE_TEXT}\E", "childOf": {"id": "note_card_${NOTE_NAME}"}}
        home, conditional, final = steps[-3:]
        self.assertEqual(home["runFlow"]["file"], "subflows/test-source-home.yaml")
        branch = conditional["runFlow"]
        self.assertEqual(branch["when"], {"notVisible": marker})
        self.assertEqual({key: value for key, value in final["assertVisible"].items() if key != "label"}, marker)
        self.assertFalse(any("tapOn" in step or "scrollUntilVisible" in step for step in steps))
        precondition, reopen, sync, wait, scroll_up, find_card = branch["commands"]
        self.assertEqual({key: value for key, value in precondition["assertNotVisible"].items() if key != "label"}, marker)
        self.assertEqual(reopen["runFlow"]["file"], "subflows/open-menu.yaml")
        self.assertEqual(sync["tapOn"]["id"], "sidebar_sync")
        self.assertEqual(wait["extendedWaitUntil"]["visible"], {"id": "home"})
        self.assertEqual(scroll_up["scrollUntilVisible"]["direction"], "UP")
        down = find_card["runFlow"]
        self.assertEqual(down["when"], {"notVisible": {"id": "note_card_${NOTE_NAME}"}})
        self.assertEqual(down["commands"][-1]["scrollUntilVisible"]["direction"], "DOWN")
        self.assertEqual(down["commands"][-1]["scrollUntilVisible"]["element"], {"id": "note_card_${NOTE_NAME}"})
        home_steps = list(runner.yaml.safe_load_all(
            (maestro / "subflows/test-source-home.yaml").read_text(encoding="utf-8")))[1]
        guard = next(index for index, step in enumerate(home_steps)
                     if step.get("runFlow", {}).get("when", {}).get("notVisible", {}).get("id") == "account_menu")
        reset = next(index for index, step in enumerate(home_steps)
                     if step.get("tapOn", {}).get("id") == "nav_home")
        self.assertLess(guard, reset)
        self.assertFalse(any(step.get("assertVisible", {}).get("id") == "note_editor"
                             for step in [*steps, *branch["commands"], *down["commands"], *home_steps]))
        self.assertEqual(self.engine.catalog["flows"]["test-source"]["timeout_seconds"], 300)
        self.assertEqual(self.engine.catalog["flows"]["test-source-resume"]["timeout_seconds"], 120)
        for name in ("ensure-login.yaml", "open-menu.yaml"):
            self.assertNotIn("sidebar_sync", (maestro / "subflows" / name).read_text(encoding="utf-8"))

        def preflight(flow, prepared, deferred):
            self.assertEqual(flow, "test-source")
            self.assertEqual(prepared[0]["EXPECTED_ACCOUNT"], "Tablet Test")
            return {"status": "inconclusive"}
        with mock.patch.object(self.engine, "probe_phone_offline", return_value={"status": "passed"}), \
                mock.patch.object(self.engine, "prepare_data", return_value=({}, None)) as prepare, \
                mock.patch.object(self.engine, "run_flow", side_effect=preflight), \
                mock.patch.object(self.engine, "recover_editor_state") as recovery:
            result = self.engine.run_batch(["editing"])
        self.assertEqual(result["editing"]["cause"], "phone_test_source_unverified")
        recovery.assert_not_called()
        self.assertEqual(prepare.call_count, 1)

    def test_source_probe_navigates_home_even_when_archive_or_query_uses_home_tag(self):
        source = runner.ROOT / "scripts/android/maestro/subflows/test-source-home.yaml"
        steps = list(runner.yaml.safe_load_all(source.read_text(encoding="utf-8")))[1]
        taps = [step["tapOn"]["id"] for step in steps if "tapOn" in step] + ["sidebar_sync"]
        for start in ("archive", "filtered-home"):
            with self.subTest(start=start):
                page = start
                for target in taps:
                    if target == "nav_home":
                        page = "unfiltered-home"
                    if target == "sidebar_sync":
                        self.assertEqual(page, "unfiltered-home")
                self.assertEqual(taps, ["nav_home", "sidebar_sync"])

    def test_failed_single_recovery_skips_other_editor_dependent_flows(self):
        self.engine.phone = True
        error = {"status": "environment_blocked", "cause": "test_editor_cancel_unconfirmed"}
        with mock.patch.object(self.engine, "probe_phone_offline", return_value={"status": "passed"}), \
                mock.patch.object(self.engine, "prepare_data", return_value=({}, None)) as prepare, \
                mock.patch.object(self.engine, "run_flow", return_value={"status": "passed"}) as execute, \
                mock.patch.object(self.engine, "recover_editor_state", side_effect=[[], [error]]) as recover, \
                mock.patch.object(self.engine, "draft_checkpoint", return_value=self.output / "drafts.json"):
            result = self.engine.run_batch(["search", "language", "home-smoke"])
        self.assertEqual([call.args[0] for call in execute.call_args_list], ["test-source", "search"])
        self.assertEqual(prepare.call_count, 2)
        self.assertEqual(recover.call_count, 2)
        self.assertEqual(result["language"]["status"], "skipped")
        self.assertEqual(result["home-smoke"]["cause"], "test_editor_cancel_unconfirmed")

    def test_phone_rejects_wrong_account_and_same_name_on_wrong_origin_before_online_launch(self):
        self.engine.phone = True
        self.engine.phone_isolation = nullcontext
        self.engine.api.login.return_value = {"user": {"name": "users/owner", "display_name": "Same Name"}}
        for name in ("Wrong Name", "Same Name"):
            xml = (f'<hierarchy><node package="{self.engine.app}" resource-id="open_menu"/>'
                   f'<node package="{self.engine.app}" resource-id="account_menu" text="{name}"/></hierarchy>')
            with self.subTest(account=name), mock.patch.object(self.engine, "prepare_data") as prepare, \
                    mock.patch.object(runner, "fresh_ui_dump", return_value=xml), \
                    mock.patch.object(self.engine, "start_app") as start, \
                    mock.patch.object(self.engine, "run_flow") as execute:
                result = self.engine.run_batch(["search"])["search"]
                self.assertEqual(result["preparation_step"], "phone_offline_identity")
                self.assertEqual(result["probe_cause"], "selected_account_present")
                execute.assert_not_called()
                prepare.assert_not_called()
                start.assert_called_once()

    def test_matching_host_receipt_cannot_reconnect_a_same_named_phone_without_cached_note(self):
        self.engine.phone = True
        self.engine.phone_isolation = nullcontext
        identity = {"instance_id": "test-instance", "user": {"name": "users/owner", "display_name": "Same Name"}}
        self.engine.api.login.return_value = identity
        self.engine.source_receipt = {**self.engine.source_identity(identity), "note_name": "memos/probe",
                                      "note_text": "Maestro test-source " + "a" * 32}
        xml = f'<hierarchy><node package="{self.engine.app}" resource-id="open_menu"/></hierarchy>'
        with mock.patch.object(runner, "fresh_ui_dump", return_value=xml), \
                mock.patch.object(self.engine, "start_app") as start, \
                mock.patch.object(self.engine, "run_flow", return_value={"status": "inconclusive"}) as execute, \
                mock.patch.object(self.engine, "prepare_data") as prepare:
            result = self.engine.run_batch(["search"])["search"]
        self.assertEqual(result["probe_cause"], "cached_test_source_unverified")
        start.assert_called_once()
        prepare.assert_not_called()
        self.assertEqual(execute.call_args.args, ("test-source-resume",))

    def test_offline_probe_requires_a_blank_form_from_the_target_app(self):
        self.engine.phone_isolation = nullcontext
        for package, field, status in ((self.engine.app, "server_origin_empty", "passed"),
                                       (self.engine.app, "server_origin", "environment_blocked"),
                                       ("another.app", "server_origin_empty", "environment_blocked")):
            xml = (f'<hierarchy><node package="{package}" resource-id="server_login"/>'
                   f'<node package="{package}" resource-id="{field}"/></hierarchy>')
            with self.subTest(package=package, field=field), \
                    mock.patch.object(runner, "fresh_ui_dump", return_value=xml):
                self.assertEqual(self.engine.probe_phone_offline()["status"], status)

    def test_shared_checkpoints_find_previous_worktree_and_normalize_origin(self):
        other = self.output / "another-worktree"
        local = other / ".local/android"
        local.mkdir(parents=True)
        source = "https://acceptance.example:443/"
        channel_key = hashlib.sha256(source.encode()).hexdigest()[:16]
        ime_key = hashlib.sha256(self.adb.serial.encode()).hexdigest()[:16]
        old_channel = local / f"maestro-channel-{channel_key}.json"
        old_ime = local / f"maestro-ime-{ime_key}.json"
        old_channel.write_text("{}", encoding="utf-8")
        old_ime.write_text("{}", encoding="utf-8")
        with mock.patch.object(runner, "worktree_roots", return_value=[self.output, other]):
            self.engine.connection["origin"] = source
            channel = self.engine.channel_checkpoint()
            self.engine.connection["origin"] = "https://ACCEPTANCE.EXAMPLE/"
            self.assertEqual(self.engine.channel_checkpoint(), channel)
            ime = runner.input_method_checkpoint(other, self.adb.serial)
        self.assertTrue(ime.is_file() and channel.is_file())
        self.assertEqual(ime.parent, self.output / ".local/android")
        self.assertFalse(old_ime.exists() or old_channel.exists())

    def test_interrupt_after_channel_patch_keeps_checkpoint_and_reports_failed_restore(self):
        checkpoint = self.output / "channels.json"
        saved = {"channel": {"kind": "none", "qq_app_id": ""}, "notifications": {"enabled": False}}
        def prepare():
            runner.write_result(checkpoint, saved)
            self.engine.channel_fixture = saved
        with mock.patch.object(self.engine, "channel_checkpoint", return_value=checkpoint), \
                mock.patch.object(self.engine, "prepare_channel", side_effect=prepare), \
                mock.patch.object(self.engine, "prepare_data", return_value=({}, None)), \
                mock.patch.object(self.engine, "run_flow", side_effect=KeyboardInterrupt), \
                mock.patch.object(self.engine, "restore_channel", side_effect=runner.FixtureError(503, "SETTINGS_BUSY")) as restore:
            with self.assertRaises(KeyboardInterrupt):
                self.engine.run_batch(["navigation-settings"])
        self.assertEqual(restore.call_count, 2)
        self.assertTrue(checkpoint.is_file())
        self.assertEqual(self.engine.batch_cleanup_errors, ["channel_settings_restore"])

    def test_navigation_channel_verification_in_group_and_batch(self):
        config = {"settings": {"channel": {"kind": "qq", "qq_app_id": "100000019"}},
                  "secrets": {"qq_app_secret": {"configured": True}}}
        self.engine.api.request.return_value = config
        for batch in (False, True):
            with self.subTest(batch=batch), \
                    mock.patch.object(self.engine, "prepare_data", return_value=(self.values(), None)), \
                    mock.patch.object(runner, "execute", side_effect=self.successful_execution), \
                    mock.patch.object(self.engine, "restore_pending_channel"), \
                    mock.patch.object(self.engine, "prepare_channel") as prepare, \
                    mock.patch.object(self.engine, "restore_channel") as restore:
                saved = {"channel": {"kind": "none"}}
                def staged():
                    self.engine.channel_fixture = saved
                    return saved
                def restore_saved(value):
                    self.assertEqual(value, saved)
                    self.engine.channel_fixture = None
                prepare.side_effect = staged
                restore.side_effect = restore_saved
                results = (self.engine.run_batch(["navigation-settings"]) if batch
                           else self.engine.run_group("navigation"))
                self.assertEqual(list(results), ["navigation-settings"] if batch else
                                 ["navigation-review", "navigation-walk", "navigation-map", "navigation-settings"])
                self.assertTrue(results["navigation-settings"]["server_assertions"]["qq_credentials_retained"])
                prepare.assert_called_once()
                restore.assert_called_once_with(saved)

    def test_search_only_restores_an_interrupted_channel_before_selection(self):
        saved = {"channel": {"kind": "none"}, "notifications": {"enabled": True}}
        checkpoint = self.output / "channel.json"
        checkpoint.write_text(json.dumps(saved))
        self.engine.api.request.return_value = {"version": 4, "settings": {"channel": {"qq_app_id": "100000019"}}}
        def select(flow, engine, *, local_only=False):
            self.assertFalse(checkpoint.exists())
            self.assertEqual(engine.api.request.call_args.args[:2], ("PATCH", "/api/app/v1/settings"))
        with mock.patch.object(self.engine, "channel_checkpoint", return_value=checkpoint), \
                mock.patch.object(self.engine, "prepare_data", return_value=(self.values(), None)), \
                mock.patch.object(runner, "execute", side_effect=self.successful_execution):
            self.assertEqual(self.engine.run_batch(["search"], select=select)["search"]["status"], "passed")

    def test_timeout_attribution_falls_back_from_command_to_console_and_host(self):
        commands = self.output / "commands.json"
        commands.write_text(json.dumps([{"metadata": {"status": "RUNNING", "sequenceNumber": 7},
                                        "command": {"tapOnElementCommand": {"label": "Tap settings"}}}]))
        observed = runner.last_failed_step(self.output, "maestro_execution", 300)
        self.assertEqual((observed["source"], observed["kind"], observed["status"]),
                         ("commands_json", "last_observed", "RUNNING"))
        commands.write_text("[truncated")
        log = self.output / "console.log"
        log.write_text("Open settings COMPLETED\nTap save RUNNING\n")
        observed = runner.last_failed_step(self.output, "maestro_execution", 300)
        self.assertEqual((observed["source"], observed["kind"], observed["label"]),
                         ("console_log", "last_observed", "Tap save"))
        log.unlink()
        self.assertEqual(runner.last_failed_step(self.output, "activity_launch", 60), {
            "source": "runner_stage", "kind": "host_stage", "stage": "activity_launch", "timeout_seconds": 60})

    def test_disconnect_checks_both_exact_questions_and_partial_protocol_reply(self):
        values = {"NOTE_NAME": "memos/target", "DISCONNECT_MARKER": "ANDROID_FAIL", "FOLLOWUP_TEXT": "Normal followup"}
        first = {"user": {"content": values["DISCONNECT_MARKER"]},
                 "assistant": {"status": "failed", "error_code": "LLM_PROTOCOL", "content": "Partial text"}}
        second = {"user": {"content": values["FOLLOWUP_TEXT"]},
                  "assistant": {"status": "completed", "content": "Complete text"}}
        summary = {"chats": [{"id": "a" * 36, "material_count": 1}]}
        chat = {"materials": [{"name": values["NOTE_NAME"]}], "turns": [first, second]}
        self.engine.api.request.side_effect = [summary, chat, summary, chat]
        self.assertEqual(self.engine.verify("insights-disconnect", values)["turn_count"], 2)
        second["user"]["content"] = "ANDROID_FAIL Normal followup"
        with self.assertRaisesRegex(AssertionError, "followup_question"):
            self.engine.verify("insights-disconnect", values)
        source = runner.ROOT / "scripts/android/maestro/insights-disconnect.yaml"
        steps = list(runner.yaml.safe_load_all(source.read_text(encoding="utf-8")))[1]
        sends = [index for index, step in enumerate(steps) if step.get("tapOn", {}).get("id") == "chat_send"]
        self.assertEqual([steps[index - 1]["assertVisible"]["text"] for index in sends],
                         [r"\Q${DISCONNECT_MARKER}\E", r"\Q${FOLLOWUP_TEXT}\E"])
        self.assertEqual(sum(step.get("runFlow", {}).get("file") == "subflows/clear-question.yaml" for step in steps), 2)

    def test_release_smoke_persists_created_note_count(self):
        values = {**self.values(), "NEW_NOTE_TEXT": "Created"}
        self.engine.api.matching_notes.return_value = [{"name": "memos/created", "content": "Created"}]
        self.engine.api.note.return_value = {"content": "Created"}
        with mock.patch.object(self.engine, "prepare_data", return_value=(values, None)), \
                mock.patch.object(runner, "execute", side_effect=self.successful_execution):
            result = self.engine.run_flow("release-smoke")
        self.assertEqual(result["status"], "passed")
        self.assertEqual(result["server_assertions"]["created_note_count"], 1)
        saved = json.loads((self.engine.output / "release-smoke/result.json").read_text(encoding="utf-8"))
        self.assertEqual(saved["server_assertions"]["created_note_count"], 1)

    def test_real_accounts_and_remote_services_are_rejected(self):
        for origin, username in (("https://example.com", "android-owner"),
                                 ("http://127.0.0.1:1501", "real-user"),
                                 ("http://user@127.0.0.1:1501", "android-owner")):
            with self.subTest(origin=origin, username=username):
                with self.assertRaises(ValueError):
                    runner.FixtureAPI({"origin": origin, "username": username, "password": "test"})


if __name__ == "__main__":
    unittest.main()
