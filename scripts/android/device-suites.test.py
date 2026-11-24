from collections import Counter
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest

SCRIPT = Path(__file__).with_name("device-suites.py")


def select(*args):
    return subprocess.check_output([sys.executable, str(SCRIPT), *args], text=True).strip()


class DeviceSuitesTest(unittest.TestCase):
    def test_full_matrix_covers_each_member_once(self):
        matrix = json.loads(select("all", "--matrix"))
        self.assertEqual(matrix, ["records-edits", "records-browse", "insights", "explore"])
        expected = ("editing", "search", "actions", "attachments", "home", "insights", "navigation",
                    "language", "lifecycle", "ime", "recovery", "stars")
        self.assertEqual(Counter(member for suite in matrix for member in select(suite).splitlines()),
                         Counter(expected))

    def test_records_alias_and_ui(self):
        shards = json.loads(select("records", "--matrix"))
        self.assertEqual(shards, ["records-edits", "records-browse"])
        self.assertEqual(select("records-edits").splitlines(), ["editing", "actions", "ime", "recovery"])
        self.assertEqual(select("records-browse").splitlines(), ["search", "attachments", "home", "lifecycle"])
        self.assertEqual(select("records").splitlines(), [member for shard in shards for member in select(shard).splitlines()])
        catalog = json.loads((SCRIPT.parent / "maestro/catalog.json").read_text(encoding="utf-8"))
        self.assertEqual(select("ui").splitlines(), list(catalog["groups"]))

    def test_invalid_suite(self):
        result = subprocess.run([sys.executable, str(SCRIPT), "invalid", "--matrix"], capture_output=True,
                                env={**os.environ, "PYTHONUTF8": "1"}, text=True, encoding="utf-8")
        self.assertEqual(result.returncode, 2)
        self.assertIn("未知设备检查组：invalid", result.stderr)


if __name__ == "__main__":
    unittest.main()
