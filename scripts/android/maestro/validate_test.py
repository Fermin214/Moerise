"""Host-only checks for catalog integrity, fixture isolation and unsafe flow mutations."""

from copy import deepcopy
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import validate


class FlowPolicyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.catalog = validate.load_catalog()
        cls.documents = validate.load_documents()
        cls.values = {
            "APP_ID": "io.github.example.moerise", "SCREENSHOT_PREFIX": "run_20261117/editing",
            "INPUT_SUFFIX": ".",
            "NOTE_NAME": "memos/unique-note", "PEER_NOTE_NAME": "memos/unique-peer",
            "NOTE_TEXT": "Maestro target unique123", "QUERY_TEXT": "Maestro target unique123",
            "PEER_NOTE_TEXT": "Maestro peer unique123", "EDITED_TEXT": "Maestro edited unique123",
            "PEER_EDITED_TEXT": "Maestro peer edited unique123",
            "DRAFT_TEXT": "Maestro draft unique123", "NEW_NOTE_TEXT": "Maestro created unique123",
            "ATTACHMENT_NOTE_TEXT": "Maestro attached unique123", "CHAT_TITLE": "Maestro chat unique123",
            "ATTACHMENT_NAME": "maestro-unique123.txt",
        }
        cls.environment = {
            "MAESTRO_FIXTURE_ORIGIN": "https://fixture.example",
            "MAESTRO_FIXTURE_USERNAME": "fixture-user",
            "MAESTRO_FIXTURE_PASSWORD": "fixture-password",
        }

    def reject_step(self, step, preceding=(), following=()):
        documents = deepcopy(self.documents)
        documents["home-smoke.yaml"][1] = [*preceding, step, *following]
        with self.assertRaises(ValueError):
            validate.validate_documents(documents, self.catalog)

    def reject_input(self, variable, preceding=()):
        self.reject_step(
            {"inputText": {"text": "${" + variable + "}${INPUT_SUFFIX}", "label": "Input in the wrong field"}},
            preceding,
            [{"eraseText": {"charactersToErase": 1, "label": "Remove the temporary input suffix"}}],
        )

    def runtime(self, flow, values=None, environment=None):
        return validate.validate_runtime(
            flow, self.values if values is None else values,
            self.environment if environment is None else environment,
            self.catalog, self.documents,
        )

    def test_all_definitions_have_source_ids_and_reachable_subflows(self):
        graph = validate.validate_documents(self.documents, self.catalog)
        validate.validate_source_ids()
        reachable = set()

        def visit(flow):
            if flow not in reachable:
                reachable.add(flow)
                for child in graph[flow]:
                    visit(child)

        for entry in self.catalog["flows"].values():
            visit(entry["file"])
        self.assertEqual(reachable, set(self.documents))

    def test_eight_groups_and_independent_compatibility_entries(self):
        self.assertEqual(set(self.catalog["groups"]), {
            "editing", "search", "actions", "insights", "navigation", "attachments", "language", "home",
        })
        for name in ("login", "archive", "insights-material", "attachment-cancel"):
            self.assertIn(name, self.catalog["flows"])
        for name in ("archive", "insights-material"):
            self.assertTrue(self.catalog["flows"][name]["seed_notes"])

    def test_catalog_rejects_missing_files_extra_groups_and_bad_metadata(self):
        mutations = (
            lambda c: c["flows"]["editing"].update(file="../outside.yaml"),
            lambda c: c["flows"]["editing"].update(seed_notes="true"),
            lambda c: c["flows"]["editing"].update(timeout_seconds=True),
            lambda c: c["groups"].update(login=["login"]),
            lambda c: c["groups"].update(editing=["unknown"]),
        )
        for mutation in mutations:
            catalog = deepcopy(self.catalog)
            mutation(catalog)
            with self.subTest(catalog=catalog), self.assertRaises(ValueError):
                validate.validate_documents(self.documents, catalog)
        documents = deepcopy(self.documents)
        documents.pop("login.yaml")
        with self.assertRaises(ValueError):
            validate.validate_documents(documents, self.catalog)

    def test_duplicate_keys_yaml_aliases_and_unsafe_tags_are_rejected(self):
        for text in ("appId: first\nappId: second\n---\n- back\n",
                     "appId: &pkg package\nname: *pkg\n---\n- back\n",
                     "appId: !!python/object:example {}\n---\n- back\n"):
            with self.subTest(text=text), self.assertRaises((ValueError, validate.yaml.YAMLError)):
                validate.parse(text)

    def test_network_installation_scripts_retries_and_stop_are_rejected(self):
        for command in ("clearState", "clearKeychain", "installApp", "uninstallApp", "stopApp",
                        "setAirplaneMode", "setPermissions", "openLink", "retry", "repeat",
                        "runScript", "evalScript", "setLocation", "hideKeyboard"):
            with self.subTest(command=command):
                self.reject_step({command: {"label": "Disallowed operation"}})
        for flags in ({"clearState": True, "stopApp": False},
                      {"clearState": False, "stopApp": True}, {}):
            self.reject_step({"launchApp": {**flags, "label": "Unsafe launch"}})

    def test_coordinates_optional_unknown_ids_and_unbounded_text_are_rejected(self):
        for selector in (
            {"point": "50%,50%"}, {"id": "nav_stargazing"}, {"id": "open_menu", "optional": True},
            {"id": "open_menu", "repeat": 4}, {"text": "Arbitrary destructive action"},
            {"id": ["open_menu"]}, {"id": "note_editor", "text": ""},
        ):
            with self.subTest(selector=selector):
                self.reject_step({"tapOn": {**selector, "enabled": True, "label": "Ambiguous selector"}})
        for text in ("中文输入", "literal fixture text", "${UNKNOWN}", "${NOTE_TEXT}"):
            self.reject_step({"inputText": {"text": text, "label": "Unbounded text"}})

    def test_inputs_require_the_correct_focused_field(self):
        self.reject_input("EDITED_TEXT")
        self.reject_input(
            "EDITED_TEXT",
            [{"tapOn": {"id": "note_editor", "enabled": True, "label": "Focus composer"}}],
        )
        self.reject_input(
            "MAESTRO_FIXTURE_PASSWORD",
            [{"tapOn": {"id": "server_password", "enabled": True, "label": "Focus password"}}],
        )
        for erase in ({"characters": 100}, {"charactersToErase": 1000}, {"charactersToErase": 1}):
            self.reject_step(
                {"eraseText": {**erase, "label": "Invalid erase"}},
                [{"tapOn": {"id": "note_editor", "enabled": True, "label": "Focus test field"}}],
            )
        self.reject_step({"longPressOn": {
            "id": "save_note", "enabled": True, "label": "Select a non-input control",
        }})
        self.reject_step({"tapOn": {
            "text": "Select all|Select All|全选", "enabled": True, "label": "Select without input",
        }})
        self.reject_step({"tapOn": {
            "id": "android:id/overflow", "enabled": True, "label": "Overflow without a fixture input",
        }})
        self.reject_step({"scrollUntilVisible": {
            "element": {"id": "note_editor"}, "direction": "RIGHT",
            "timeout": 15000, "centerElement": False, "label": "Wrong horizontal scroll target",
        }})

    def test_conditional_flows_must_check_their_preconditions(self):
        self.reject_step({"runFlow": {
            "when": {"visible": {"id": "server_login"}},
            "commands": [{"assertVisible": {"id": "open_menu", "label": "Wrong branch check"}}],
            "label": "Unchecked branch",
        }})
        self.reject_step({"runFlow": {
            "when": {"true": "${true}"}, "commands": ["back"], "label": "Script condition",
        }})
        for guard in (None, {"visible": {"id": "server_login"}}):
            documents = deepcopy(self.documents)
            branch = documents["subflows/hide-keyboard.yaml"][1][0]["runFlow"]
            if guard is None:
                branch.pop("when")
            else:
                branch["when"] = guard
                branch["commands"][0]["assertVisible"]["id"] = "server_login"
            with self.subTest(guard=guard), self.assertRaises(ValueError):
                validate.validate_documents(documents, self.catalog)

    def test_failure_screenshot_is_required_and_hook_cannot_change_state(self):
        documents = deepcopy(self.documents)
        documents["archive.yaml"][0].pop("onFlowComplete")
        with self.assertRaises(ValueError):
            validate.validate_documents(documents, self.catalog)
        documents = deepcopy(self.documents)
        documents["archive.yaml"][0]["onFlowComplete"] = [
            {"tapOn": {"id": "save_note", "enabled": True, "label": "Cleanup after failure"}},
        ]
        with self.assertRaises(ValueError):
            validate.validate_documents(documents, self.catalog)
        self.reject_step({"takeScreenshot": {"path": "../escape", "label": "Unsafe output"}})

    def test_subflow_cycles_missing_paths_and_traversal_are_rejected(self):
        for file in ("home-smoke.yaml", "../outside.yaml", "subflows/missing.yaml"):
            with self.subTest(file=file):
                self.reject_step({"runFlow": {"file": file, "label": "Invalid dependency"}})

    def test_runner_contract_accepts_every_catalog_entry(self):
        for name in self.catalog["flows"]:
            with self.subTest(name=name):
                self.assertEqual(self.runtime(name), name)

    def test_runner_contract_rejects_missing_data_credentials_in_parameters_and_bad_environment(self):
        values = dict(self.values)
        values.pop("NEW_NOTE_TEXT")
        with self.assertRaises(ValueError):
            self.runtime("editing", values)
        with self.assertRaises(ValueError):
            self.runtime("login", {**self.values, "MAESTRO_FIXTURE_PASSWORD": "not-cli-data"})
        for environment in ({}, {**self.environment, "MAESTRO_FIXTURE_PASSWORD": "x" * 101}):
            with self.subTest(environment=bool(environment)), self.assertRaises(ValueError):
                self.runtime("login", environment=environment)

    def test_runner_contract_rejects_ambiguous_notes_queries_and_unsafe_paths(self):
        for changes in (
            {"NOTE_NAME": "memos/.*"}, {"PEER_NOTE_NAME": self.values["NOTE_NAME"]},
            {"QUERY_TEXT": "different"}, {"EDITED_TEXT": "中文"},
            {"NEW_NOTE_TEXT": self.values["NOTE_TEXT"]}, {"ATTACHMENT_NAME": "../file.txt"},
            {"SCREENSHOT_PREFIX": "../escape"}, {"SCREENSHOT_PREFIX": "https://user:password@host"},
            {"NOTE_TEXT": "x" * 101, "QUERY_TEXT": "x" * 101},
        ):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.runtime("editing", {**self.values, **changes})

    def test_offline_parser_diagnostics_cannot_be_mistaken_for_success(self):
        for code, output in ((0, ""), (0, "Unknown Property: characters\nOK"), (1, "OK")):
            result = SimpleNamespace(returncode=code, stdout=output, stderr="")
            with patch.object(validate.subprocess, "run", return_value=result), self.assertRaises(ValueError):
                validate.check_syntax({"editing.yaml": None}, "maestro")
        with patch.object(validate.subprocess, "run",
                          return_value=SimpleNamespace(returncode=0, stdout="OK\n", stderr="")) as run:
            self.assertEqual(validate.check_syntax({"editing.yaml": None}, "maestro"), 1)
            self.assertEqual(run.call_args.args[0][1], "check-syntax")

    def test_each_provider_requires_its_own_complete_return_checks(self):
        for flow in ("subflows/pick-documentsui.yaml", "subflows/pick-xiaomi.yaml"):
            for index in (-3, -2, -1):
                documents = deepcopy(self.documents)
                documents[flow][1].pop(index)
                with self.subTest(flow=flow, removed=index), self.assertRaises(ValueError):
                    validate.validate_documents(documents, self.catalog)

    def test_xiaomi_selection_requires_exact_filename_and_observed_list(self):
        base = {
            "id": validate.XIAOMI + "file_name", "text": r"\Q${ATTACHMENT_NAME}\E",
            "childOf": {"id": "android:id/list"},
        }
        validate.selector(base)
        for changes in (
            {"text": "Downloads|下载"}, {"childOf": {"id": "note_editor"}},
        ):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                validate.selector({**base, **changes})
        self.reject_step({"tapOn": {
            "id": validate.XIAOMI + "file_name", "enabled": True, "label": "Unscoped file selection",
        }})

    def test_ime_submission_requires_enter_from_a_focused_search_field(self):
        documents = deepcopy(self.documents)
        body = documents["search.yaml"][1]
        first = next(item["pressKey"] for item in body if "pressKey" in item)
        for key in ("Search", "Home", "Back", "${KEY}"):
            first["key"] = key
            with self.subTest(key=key), self.assertRaises(ValueError):
                validate.validate_documents(documents, self.catalog)
        self.reject_step({"pressKey": {"key": "Enter", "label": "Unfocused IME action"}})

    def test_timestamp_click_is_scoped_and_detail_routes_are_absence_only(self):
        validate.selector({
            "text": validate.TIMESTAMP_TEXT, "childOf": {"id": "note_card_${NOTE_NAME}"},
        })
        with self.assertRaises(ValueError):
            validate.selector({"text": validate.TIMESTAMP_TEXT})
        for identifier in validate.ABSENCE_IDS:
            validate.selector({"id": identifier}, absent=True)
            with self.subTest(identifier=identifier), self.assertRaises(ValueError):
                validate.selector({"id": identifier})

    def test_peer_edit_requires_its_own_value_and_exact_peer_field(self):
        values = dict(self.values)
        values.pop("PEER_EDITED_TEXT")
        with self.assertRaises(ValueError):
            self.runtime("editing", values)
        for variable, field in (
            ("PEER_EDITED_TEXT", "note_editor_${NOTE_NAME}"),
            ("EDITED_TEXT", "note_editor_${PEER_NOTE_NAME}"),
        ):
            with self.subTest(variable=variable):
                self.reject_input(
                    variable,
                    [{"tapOn": {"id": field, "enabled": True, "label": "Focus the wrong note field"}}],
                )


if __name__ == "__main__":
    unittest.main()
