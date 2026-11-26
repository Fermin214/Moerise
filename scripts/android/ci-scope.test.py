from itertools import product
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import yaml

ANDROID = Path(__file__).resolve().parent
ROOT = ANDROID.parents[1]
WORKFLOW = yaml.load((ROOT / ".github/workflows/checks.yml").read_text(encoding="utf-8"), Loader=yaml.BaseLoader)
JOBS = WORKFLOW["jobs"]
SCOPE_FLAGS = ("go", "web", "android", "linux_release", "release_guards")


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], stderr=subprocess.PIPE)


def commit(root, message):
    git(root, "add", "-A")
    git(root, "commit", "-qm", message)


def scope_repository(root):
    git(root, "init", "-q")
    git(root, "config", "user.name", "Scope Test")
    git(root, "config", "user.email", "scope@example.invalid")
    script = root / "scripts/android/ci-scope.py"
    script.parent.mkdir(parents=True)
    script.write_bytes((ANDROID / script.name).read_bytes())
    commit(root, "base")


def workflow_scope(root, event):
    step, = (step for step in JOBS["scope"]["steps"] if step.get("id") == "scope")
    with tempfile.TemporaryDirectory() as temporary:
        output = Path(temporary) / "github-output"
        subprocess.run(
            ["bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", step["run"]],
            cwd=root, env=dict(os.environ, GITHUB_EVENT_NAME=event, GITHUB_OUTPUT=str(output)),
            check=True, capture_output=True,
        )
        return dict(line.split("=", 1) for line in output.read_text().splitlines())


class ScopeTest(unittest.TestCase):
    def test_path_table(self):
        cases = {
            "README.md": [],
            "docs/Android guide\n说明.md": [],
            "docs/OPERATIONS.md": [],
            "docs/STARGAZING.md": [],
            "docs/android/INSTALL.md": [],
            "docs/android/RELEASE-CHECKLIST.md": [],
            "docs/android/THIRD_PARTY_NOTICES.md": ["android"],
            "docs/android/licenses/dependency.txt": ["android"],
            "mobile/androidApp/src/main/Product.kt": ["android"],
            "scripts/android/requirements.txt": ["android"],
            "internal/chat.go": ["go", "android", "linux_release"],
            "main.go": ["go", "android", "linux_release"],
            "go.mod": ["go", "android", "linux_release"],
            "go.sum": ["go", "android", "linux_release"],
            "scripts/testweb/main.go": ["go", "android", "linux_release"],
            "cmd/moerise-telemetry/main.go": ["go", "linux_release"],
            "tests/acceptance_test.go": ["go", "linux_release"],
            "config.example.json": ["android", "linux_release"],
            "LICENSE": ["android", "linux_release"],
            "web/assets.go": ["go", "web", "android", "linux_release"],
            "web/subdir/assets.go": ["web", "linux_release"],
            "web/src/app.js": ["web", "linux_release"],
            "services/layout/server.py": ["go", "linux_release"],
            "Dockerfile": ["linux_release"],
            ".dockerignore": ["linux_release"],
            ".env.example": ["linux_release"],
            "compose.yaml": ["linux_release"],
            "scripts/lib/process.mjs": ["web", "android", "linux_release"],
            "scripts/acceptance/check-compose.mjs": ["web", "linux_release"],
            "scripts/acceptance/check-telemetry.mjs": ["web", "linux_release"],
            "scripts/release/build-images.sh": ["linux_release", "release_guards"],
            "scripts/release/smoke-core.sh": ["linux_release", "release_guards"],
            "scripts/release/publish.py": ["release_guards"],
            ".github/workflows/checks.yml": SCOPE_FLAGS,
            ".github/workflows/android-checks.yml": ["android"],
            ".github/workflows/release.yml": ["linux_release"],
            ".github/workflows/main-smoke.yml": ["linux_release", "release_guards"],
            ".github/workflows/publish.yml": ["linux_release", "release_guards"],
            ".github/workflows/android-release.yml": ["release_guards"],
        }
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            scope_repository(root)
            for name, required in cases.items():
                with self.subTest(path=name):
                    path = root / name
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_text("updated\n", encoding="utf-8")
                    commit(root, "modify " + name)
                    self.assertEqual(workflow_scope(root, "pull_request"), {
                        flag: "true" if flag in required else "false" for flag in SCOPE_FLAGS
                    })

    def test_workflow_scope_detects_moves_and_deletions(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            scope_repository(root)
            original = root / "scripts/lib/process.mjs"
            original.parent.mkdir(parents=True)
            original.write_text("shared\n", encoding="utf-8")
            commit(root, "add shared dependency")
            moved = original.rename(root / "MOVED.md")
            commit(root, "move out of scope")
            expected = dict(go="false", web="true", android="true", linux_release="true", release_guards="false")
            self.assertEqual(workflow_scope(root, "pull_request"), expected)
            moved.rename(original)
            commit(root, "move into scope")
            self.assertEqual(workflow_scope(root, "pull_request"), expected)
            original.unlink()
            commit(root, "delete shared dependency")
            self.assertEqual(workflow_scope(root, "pull_request"), expected)

    def test_manual_scope_selects_every_check_without_diff(self):
        with tempfile.TemporaryDirectory() as temporary:
            self.assertEqual(workflow_scope(Path(temporary), "workflow_dispatch"), dict.fromkeys(SCOPE_FLAGS, "true"))

    def test_workflow_wiring(self):
        self.assertEqual(set(WORKFLOW["on"]), {"pull_request", "workflow_dispatch"})
        self.assertFalse(WORKFLOW["on"]["workflow_dispatch"])
        self.assertNotIn("if", JOBS["scope"])
        self.assertEqual(JOBS["scope"]["outputs"], {
            flag: "${{ steps.scope.outputs." + flag + " }}" for flag in SCOPE_FLAGS
        })
        guards = JOBS["scope"]["steps"][-1]
        self.assertEqual(guards["if"], " || ".join(
            f"steps.scope.outputs.{flag} == 'true'" for flag in ("release_guards", "linux_release", "android")
        ))
        self.assertEqual(guards["run"], "python3 -B -m unittest discover -s scripts/release -p 'test_*.py'")
        self.assertEqual(JOBS["scope"]["steps"][-2]["run"],
                         "test -f docs/OPERATIONS.md && test -f docs/STARGAZING.md")
        for name, flag in (("go-suites", "go"), ("web", "web"), ("android-pr", "android"),
                           ("linux-preview", "linux_release")):
            with self.subTest(job=name):
                self.assertEqual(JOBS[name]["needs"], "scope")
                self.assertEqual(JOBS[name]["if"], f"needs.scope.outputs.{flag} == 'true'")
        self.assertEqual(JOBS["android-pr"]["uses"], "./.github/workflows/android-checks.yml")
        self.assertNotIn("with", JOBS["android-pr"])
        self.assertEqual(JOBS["linux-preview"]["uses"], "./.github/workflows/release.yml")
        self.assertEqual(JOBS["go"]["needs"], ["scope", "go-suites"])
        self.assertEqual(JOBS["go"]["steps"][-1]["env"], {
            "REQUIRED": "${{ needs.scope.outputs.go }}", "RESULT": "${{ needs.go-suites.result }}",
        })
        gate = JOBS["pr-gate"]
        self.assertEqual(gate["name"], "PR gate")
        self.assertEqual(gate["needs"], ["scope", "go", "web", "android-pr", "linux-preview"])
        self.assertEqual(gate["env"], {
            "SCOPE_RESULT": "${{ needs.scope.result }}", "GO_RESULT": "${{ needs.go.result }}",
            "WEB_REQUIRED": "${{ needs.scope.outputs.web }}", "WEB_RESULT": "${{ needs.web.result }}",
            "ANDROID_REQUIRED": "${{ needs.scope.outputs.android }}", "ANDROID_RESULT": "${{ needs.android-pr.result }}",
            "LINUX_REQUIRED": "${{ needs.scope.outputs.linux_release }}", "LINUX_RESULT": "${{ needs.linux-preview.result }}",
        })
        for job in (JOBS["go"], gate):
            self.assertEqual(job["if"], "${{ always() && github.event_name == 'pull_request' }}")

    def test_gate_truth_table(self):
        script = JOBS["pr-gate"]["steps"][-1]["run"].split()[-1]
        results = ("success", "failure", "cancelled", "skipped")
        base = dict(os.environ, SCOPE_RESULT="success", GO_RESULT="success")
        for name in ("WEB", "ANDROID", "LINUX"):
            base.update({name + "_REQUIRED": "false", name + "_RESULT": "skipped"})
        for check, required, result in product(("WEB", "ANDROID", "LINUX"), ("true", "false", ""), results):
            with self.subTest(check=check, required=required, result=result):
                env = base | {check + "_REQUIRED": required, check + "_RESULT": result}
                expected = 0 if (required, result) in {("true", "success"), ("false", "skipped")} else 1
                process = subprocess.run([sys.executable, str(ROOT / script)], env=env, capture_output=True)
                self.assertEqual(process.returncode, expected, process.stdout)
        for check, result in product(("SCOPE_RESULT", "GO_RESULT"), results[1:]):
            with self.subTest(check=check, result=result):
                process = subprocess.run([sys.executable, str(ROOT / script)],
                                         env=base | {check: result}, capture_output=True)
                self.assertEqual(process.returncode, 1, process.stdout)


if __name__ == "__main__":
    unittest.main()
