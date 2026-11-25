import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("fixture_artifacts", Path(__file__).with_name("fixture-artifacts.py"))
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class FixtureArtifactsTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.manifest = self.root / "fixture-artifacts.json"
        for relative in fixture.APKS:
            path = self.root / relative
            path.parent.mkdir(parents=True)
            path.write_bytes(relative.encode())
        self.upgrade = self.root / "fixture-upgrade.apk"
        self.upgrade.write_bytes(b"upgrade version 2")
        for patcher in (
            patch.object(fixture, "ROOT", self.root), patch.object(fixture, "MANIFEST", self.manifest),
            patch.object(fixture, "source_digest", return_value="source"),
            patch.object(fixture.subprocess, "check_output", return_value="commit\n"),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)

    def run_command(self, *args):
        with patch.object(sys, "argv", ["fixture-artifacts.py", *args]):
            fixture.main()

    def test_default_two_apks_and_target_only(self):
        self.run_command("write")
        self.assertNotIn("upgrade", json.loads(self.manifest.read_text()))
        self.run_command("verify")
        with self.assertRaisesRegex(ValueError, "缺少升级 APK 摘要.*重新构建"):
            self.run_command("verify", "--require-upgrade")
        (self.root / fixture.APKS[1]).unlink()
        self.run_command("verify", "--target-only")

    def test_upgrade_is_locked_and_checked_when_required(self):
        with patch.object(fixture, "apk_version_code", return_value=2):
            self.run_command("write", "--upgrade-apk", str(self.upgrade))
        record = json.loads(self.manifest.read_text())["upgrade"]
        self.assertEqual(record, {"path": self.upgrade.name, "version_code": 2,
                                  "sha256": fixture.apk_digest(self.upgrade)})
        self.run_command("verify", "--require-upgrade", "--upgrade-apk", str(self.upgrade))
        with self.assertRaisesRegex(ValueError, "路径、版本或摘要不匹配"):
            self.run_command("verify", "--require-upgrade", "--upgrade-apk", str(self.root / "other.apk"))
        self.upgrade.write_bytes(b"different")
        with self.assertRaisesRegex(ValueError, "摘要不匹配"):
            self.run_command("verify", "--require-upgrade")
        self.upgrade.unlink()
        self.run_command("verify", "--target-only")
        with self.assertRaisesRegex(ValueError, "重新构建"):
            self.run_command("verify", "--require-upgrade")

    def test_upgrade_wrong_version_cannot_be_recorded(self):
        with patch.object(fixture, "apk_version_code", return_value=1):
            with self.assertRaisesRegex(ValueError, "versionCode 应为 2"):
                self.run_command("write", "--upgrade-apk", str(self.upgrade))
        self.assertFalse(self.manifest.exists())


if __name__ == "__main__":
    unittest.main()
