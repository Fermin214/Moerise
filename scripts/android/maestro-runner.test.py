"""Host checks for Maestro isolation, failure evidence and server assertions."""
import base64
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import maestro_runner as runner


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
        directory = self.output / "repo/scripts/android/maestro/subflows"
        directory.mkdir(parents=True)
        path = directory / "home.yaml"
        path.write_text("first")
        root = self.output / "repo"
        before = runner.flow_digest(root, "editing")
        path.write_text("second")
        self.assertNotEqual(before, runner.flow_digest(root, "editing"))

    def test_real_accounts_and_remote_services_are_rejected(self):
        for origin, username in (("https://example.com", "android-owner"),
                                 ("http://127.0.0.1:1501", "real-user"),
                                 ("http://user@127.0.0.1:1501", "android-owner")):
            with self.subTest(origin=origin, username=username):
                with self.assertRaises(ValueError):
                    runner.FixtureAPI({"origin": origin, "username": username, "password": "test"})


if __name__ == "__main__":
    unittest.main()
