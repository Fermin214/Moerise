import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import yaml

ANDROID = Path(__file__).resolve().parent
ROOT = ANDROID.parents[1]


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], stderr=subprocess.PIPE)


def classify(*paths):
    return classify_diff(b"\0".join(path.encode() for path in paths) + b"\0")


def classify_diff(diff):
    return subprocess.check_output([sys.executable, str(ANDROID / "ci-scope.py")], input=diff).decode().strip()


class ScopeTest(unittest.TestCase):
    def test_path_table(self):
        for paths, expected in (
            (("docs/Android guide\n说明.md", "README.md"), "none"),
            (("scripts/android/phone-session.py", "scripts/android/device-recovery.test.py", "README.md"), "host"),
            (("scripts/android/phone-checks.json", "scripts/android/adb_common.py"), "full"),
            (("scripts/android/device-recovery.py",), "full"),
            (("scripts/android/maestro/subflows/swipe.yaml",), "full"),
            (("scripts/android/maestro_runner.py",), "full"),
            (("scripts/android/release-smoke.py",), "full"),
            (("scripts/android/new-script.py",), "full"),
            (("mobile/androidApp/src/main/Product.kt",), "full"),
            (("internal/chat.go",), "full"),
            (("web/assets.go",), "full"),
            ((".github/workflows/android-release.yml",), "full"),
            (("scripts/release/publish.py",), "full"),
            (("docs/android/licenses/fixture.txt",), "full"),
        ):
            with self.subTest(paths=paths):
                self.assertEqual(classify(*paths), expected)

    def test_git_diff_detects_deleted_and_moved_shared_paths(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            git(root, "init", "-q")
            git(root, "config", "user.name", "Scope Test")
            git(root, "config", "user.email", "scope@example.invalid")
            original = root / "scripts/android/adb_common.py"
            original.parent.mkdir(parents=True)
            original.write_text("shared\n", encoding="utf-8")
            git(root, "add", ".")
            git(root, "commit", "-qm", "base")
            original.rename(root / "moved.py")
            git(root, "add", "-A")
            git(root, "commit", "-qm", "move")
            diff = git(root, "diff", "--no-renames", "--name-only", "-z", "HEAD^1", "HEAD")
            self.assertIn(b"scripts/android/adb_common.py\0", diff)
            self.assertIn(b"moved.py\0", diff)
            self.assertEqual(classify_diff(diff), "full")
            git(root, "rm", "-q", "moved.py")
            git(root, "commit", "-qm", "delete")
            diff = git(root, "diff", "--no-renames", "--name-only", "-z", "HEAD~2", "HEAD")
            self.assertEqual(diff, b"scripts/android/adb_common.py\0")
            self.assertEqual(classify_diff(diff), "full")

    def test_workflow_wiring_and_gate_truth_table(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/checks.yml").read_text(encoding="utf-8"))
        jobs = workflow["jobs"]
        self.assertEqual(jobs["scope"]["outputs"]["android_mode"], "${{ steps.scope.outputs.android_mode }}")
        self.assertEqual(jobs["android-pr"]["if"], "needs.scope.outputs.android_mode != 'none'")
        self.assertEqual(jobs["android-pr"]["with"]["mode"], "${{ needs.scope.outputs.android_mode }}")
        gate = jobs["pr-gate"]
        self.assertEqual(gate["name"], "PR gate")
        self.assertEqual(gate["env"]["ANDROID_REQUIRED"], "${{ needs.scope.outputs.android }}")
        script = gate["steps"][-1]["run"].split()[-1]
        for required, result, expected in (
            ("true", "success", 0), ("false", "skipped", 0),
            ("true", "failure", 1), ("true", "cancelled", 1), ("true", "skipped", 1),
            ("false", "success", 1),
        ):
            for check in ("WEB", "ANDROID", "LINUX"):
                with self.subTest(check=check, required=required, result=result):
                    env = dict(os.environ, SCOPE_RESULT="success", GO_RESULT="success")
                    for name in ("WEB", "ANDROID", "LINUX"):
                        env[name + "_REQUIRED"] = required if name == check else "false"
                        env[name + "_RESULT"] = result if name == check else "skipped"
                    process = subprocess.run([sys.executable, str(ROOT / script)], env=env, capture_output=True)
                    self.assertEqual(process.returncode, expected, process.stderr)


if __name__ == "__main__":
    unittest.main()
