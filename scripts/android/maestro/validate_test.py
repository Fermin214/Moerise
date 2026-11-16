"""Host-only regression checks for destructive or ambiguous flow definitions."""

from copy import deepcopy
import unittest

import validate


class FlowPolicyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.documents = {
            name: validate.parse((validate.ROOT / name).read_text(encoding="utf-8"))
            for name in validate.FILES
        }

    def reject_step(self, step):
        documents = deepcopy(self.documents)
        documents["home-smoke.yaml"][1].append(step)
        with self.assertRaises(ValueError):
            validate.validate_documents(documents)

    def test_flow_definitions_and_source_ids(self):
        validate.validate_documents(self.documents)
        validate.validate_source_ids()

    def test_destructive_commands_and_failure_suppression_are_rejected(self):
        for command in ("clearState", "installApp", "uninstallApp", "setAirplaneMode",
                        "setPermissions", "openLink", "retry", "repeat", "runScript", "evalScript"):
            with self.subTest(command=command):
                self.reject_step({command: {"label": "Unsafe operation"}})
        self.reject_step({"launchApp": {
            "label": "Clear state", "clearState": True, "stopApp": False,
        }})
        self.reject_step({"launchApp": {"label": "Default launch stops the app"}})
        self.reject_step({"tapOn": {
            "id": "open_menu", "enabled": True, "optional": True, "label": "Ignore failure",
        }})

    def test_coordinates_unknown_ids_text_input_and_save_are_rejected(self):
        for value in (
            {"point": "50%,50%", "label": "Coordinate tap"},
            {"id": "nav_stargazing", "enabled": True, "label": "Invented ID"},
            {"id": "save_note", "enabled": True, "label": "Save an existing draft"},
        ):
            with self.subTest(value=value):
                self.reject_step({"tapOn": value})
        for text in ("中文输入", "${DRAFT_TEXT}", "ASCII draft"):
            with self.subTest(text=text):
                self.reject_step({"inputText": {"text": text, "label": "Change the draft"}})

    def test_missing_failure_screenshot_and_cyclic_subflow_are_rejected(self):
        documents = deepcopy(self.documents)
        documents["archive.yaml"][0].pop("onFlowComplete")
        with self.assertRaises(ValueError):
            validate.validate_documents(documents)
        self.reject_step({"runFlow": {"file": "home-smoke.yaml", "label": "Cycle"}})
        self.reject_step({"runFlow": {"file": "../outside.yaml", "label": "Escape"}})

    def test_duplicate_keys_and_aliases_are_rejected(self):
        for text in ("appId: first\nappId: second\n---\n- back\n",
                     "appId: &pkg package\nname: *pkg\n---\n- back\n"):
            with self.subTest(text=text):
                with self.assertRaises(ValueError):
                    validate.parse(text)

    def test_runtime_parameters_reject_paths_credentials_and_regex(self):
        validate.validate_runtime("archive.yaml", "io.github.example.moerise",
                                  "run_20261116/archive", "memos/unique-123")
        for prefix in ("../escape", "C:/result", "/absolute", "https://user:token@host", "run/token=value"):
            with self.subTest(prefix=prefix):
                with self.assertRaises(ValueError):
                    validate.validate_runtime("archive.yaml", "io.github.example.moerise",
                                              prefix, "memos/unique-123")
        for note in (None, "memos/.*", "memos/(first|second)", "memos/one/other", "memos/中文"):
            with self.subTest(note=note):
                with self.assertRaises(ValueError):
                    validate.validate_runtime("archive.yaml", "io.github.example.moerise",
                                              "run/archive", note)


if __name__ == "__main__":
    unittest.main()
