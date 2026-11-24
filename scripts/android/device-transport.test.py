import json
from pathlib import Path
import tempfile
import unittest

from device_transport import attempt_summary


def failed(command, sequence, message):
    return {"command": {command: {}}, "metadata": {"status": "FAILED", "sequenceNumber": sequence,
            "error": {"message": message}}}


class DeviceTransportTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        group = self.root / "maestro/editing"
        group.mkdir(parents=True)
        self.results = group / "results.json"
        self.result = {"status": "environment_blocked", "cause": "maestro_driver_died"}
        self.write_result()
        self.commands = group / "editing/artifacts/timestamp/commands.json"
        self.commands.parent.mkdir(parents=True)
        self.offline = "ADB error (host:transport:emulator-5554): device offline"

    def write_result(self):
        self.results.write_text(json.dumps({"editing": self.result}), encoding="utf-8")

    def summary(self, *, attempt=1, passed=False, probe=True, restored=True):
        return attempt_summary(self.root, "records-edits", "editing", attempt, passed, probe, restored)

    def report(self, *commands):
        self.commands.write_text(json.dumps(commands), encoding="utf-8")

    def test_business_assertion_precedes_failed_completion_screenshot(self):
        self.report(failed("takeScreenshotCommand", 11, self.offline),
                    failed("assertConditionCommand", 7, "Assertion is false"))
        result = self.summary()
        self.assertEqual(result["first_failure_command"],
                         {"flow": "editing", "command": "assertConditionCommand", "sequence": 7})
        self.assertTrue(result["screenshot_secondary_failure"])
        self.assertEqual(result["classification"], "assertion_failure")
        self.assertFalse(result["retry_allowed"])

    def test_missing_invalid_and_screenshot_only_evidence_cannot_retry(self):
        for sample in (None, "[truncated", "{}", "[]", json.dumps([failed("takeScreenshotCommand", 7, self.offline)])):
            with self.subTest(sample=sample):
                if sample is not None:
                    self.commands.write_text(sample, encoding="utf-8")
                result = self.summary()
                self.assertEqual(result["classification"], "evidence_incomplete")
                self.assertFalse(result["retry_allowed"])

    def test_cleanup_failure_keeps_original_failed_command_and_cannot_retry(self):
        self.report(failed("tapOnElementCommand", 4, self.offline))
        for fixture_restored, errors in ((False, []), (True, ["forward_remove"])):
            self.result["cleanup_errors"] = errors
            self.write_result()
            result = self.summary(restored=fixture_restored)
            self.assertEqual(result["classification"], "cleanup_failure")
            self.assertFalse(result["fixture_restored"])
            self.assertFalse(result["retry_allowed"])
            self.assertEqual(result["first_failure_command"]["command"], "tapOnElementCommand")

    def test_first_transport_failure_can_retry_once(self):
        self.report(failed("tapOnElementCommand", 4, "Device server died during tap"),
                    failed("takeScreenshotCommand", 5, self.offline))
        result = self.summary()
        self.assertEqual(result["classification"], "transport_retryable")
        self.assertTrue(result["retry_allowed"])
        self.assertFalse(self.summary(attempt=2)["retry_allowed"])
        self.assertFalse(self.summary(probe=False)["retry_allowed"])
        self.assertEqual(self.summary(attempt=2, passed=True), {
            "suite": "records-edits", "member": "editing", "attempt": 2, "outcome": "passed",
            "adb_probe_ok": True, "fixture_restored": True,
        })


if __name__ == "__main__":
    unittest.main()
