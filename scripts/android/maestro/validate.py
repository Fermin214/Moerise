"""Parse Maestro definitions and enforce the local, host-only flow policy."""

import argparse
from pathlib import Path
import re
import sys

try:
    import yaml
except ImportError:
    raise SystemExit("PyYAML is required in the selected Python environment.")


ROOT = Path(__file__).resolve().parent
REPO = ROOT.parents[2]
PRESENTATION = REPO / "mobile/shared/src/commonMain/kotlin/io/github/fermin214/moerise/presentation"
FILES = {
    "home-smoke.yaml", "navigation.yaml", "archive.yaml",
    "insights-material.yaml", "attachment-cancel.yaml",
    "subflows/home.yaml", "subflows/settings-roundtrip.yaml",
    "subflows/insights-roundtrip.yaml",
}
# Values are literal source fragments, including the actual Kotlin interpolation.
SOURCE_IDS = {
    "open_menu": ("MoeriseUi.kt", 'testTag("open_menu")'),
    "nav_home": ("WebSidebar.kt", 'testTag("nav_$target")'),
    "nav_settings": ("WebSidebar.kt", 'testTag("nav_settings")'),
    "nav_insights": ("WebSidebar.kt", 'testTag("nav_$target")'),
    "account_menu": ("WebSidebar.kt", 'testTag("account_menu")'),
    "nav_archive": ("WebSidebar.kt", 'testTag("nav_archive")'),
    "settings_page": ("WebSettings.kt", 'testTag("settings_page")'),
    "insights_page": ("InsightScreen.kt", 'testTag("insights_page")'),
    "archive_page": ("MoeriseApp.kt", 'testTag("archive_page")'),
    "note_editor": ("MoeriseApp.kt", 'editorTag: String = "note_editor"'),
    "save_note": ("MoeriseApp.kt", 'saveTag: String = "save_note"'),
    "add_attachment": ("PlatformAttachments.kt", 'testTag("add_attachment")'),
    "note_card_${NOTE_NAME}": ("WebNoteCard.kt", 'testTag("note_card_${note.name}")'),
    "note_more_${NOTE_NAME}": ("WebNoteCard.kt", 'tag = "note_more_${note.name}"'),
    "archive_note_${NOTE_NAME}": ("WebNoteCard.kt", 'tag = "archive_note_${note.name}"'),
    "chat_add_materials": ("InsightScreen.kt", 'testTag("chat_add_materials")'),
    "chat_material_dialog": ("InsightScreen.kt", 'tag = "chat_material_dialog"'),
    "chat_picker_tab_1": ("InsightScreen.kt", 'tagPrefix = "chat_picker_tab"'),
    "chat_picker_note_${NOTE_NAME}": ("InsightScreen.kt", 'testTag("chat_picker_note_${note.name}")'),
    "chat_apply_materials": ("InsightScreen.kt", 'testTag("chat_apply_materials")'),
    "chat_remove_${NOTE_NAME}": ("InsightScreen.kt", 'testTag("chat_remove_$name")'),
}
TAP_IDS = set(SOURCE_IDS) - {
    "save_note", "note_card_${NOTE_NAME}", "settings_page", "insights_page",
    "archive_page", "chat_material_dialog",
}
SELECTOR_KEYS = {"id", "enabled", "focused", "label"}


def require(condition, message):
    if not condition:
        raise ValueError(message)


class UniqueLoader(yaml.SafeLoader):
    def construct_mapping(self, node, deep=False):
        result = {}
        for key_node, value_node in node.value:
            key = self.construct_object(key_node, deep=deep)
            require(isinstance(key, str), "YAML keys must be strings")
            require(key not in result, "Duplicate YAML key")
            result[key] = self.construct_object(value_node, deep=deep)
        return result


def parse(text):
    require(not any(isinstance(event, yaml.AliasEvent) for event in yaml.parse(text)),
            "YAML aliases are not allowed")
    docs = list(yaml.load_all(text, Loader=UniqueLoader))
    require(len(docs) == 2, "Expected one configuration and one command document")
    return docs


def selector(value, labelled=False):
    require(isinstance(value, dict), "Selectors must be ID mappings")
    allowed = SELECTOR_KEYS if labelled else SELECTOR_KEYS - {"label"}
    require(set(value) <= allowed, "Unsupported selector property")
    require(isinstance(value.get("id"), str) and value["id"] in SOURCE_IDS,
            "Selector ID is outside the source-backed allowlist")
    for key in ("enabled", "focused"):
        if key in value:
            require(type(value[key]) is bool, "Selector flags must be booleans")


def screenshot(value):
    require(set(value) == {"path", "label"}, "Screenshot requires only path and label")
    require(isinstance(value["path"], str) and
            re.fullmatch(r"\$\{SCREENSHOT_PREFIX\}-[a-z0-9-]+", value["path"]) is not None,
            "Screenshot path must use the supplied prefix and a literal suffix")


def commands(items, flow, references, inline=False, hook=False):
    require(isinstance(items, list) and items, "Command lists must not be empty")
    for item in items:
        if isinstance(item, str):
            require(inline and item in {"back", "hideKeyboard"},
                    "Only a labelled inline flow may contain Back or hideKeyboard")
            continue
        require(isinstance(item, dict) and len(item) == 1, "Expected one command per step")
        kind, value = next(iter(item.items()))
        require(isinstance(value, dict), "Commands require explicit labelled mappings")
        require(isinstance(value.get("label"), str) and value["label"].strip(),
                "Every mapped command requires a label")
        require("${" not in value["label"], "Command labels must be literal")
        if hook:
            require(kind == "takeScreenshot", "Completion hooks may only capture screenshots")
        if kind == "launchApp":
            require(set(value) == {"clearState", "stopApp", "label"} and
                    value["clearState"] is False and value["stopApp"] is False,
                    "launchApp must preserve state and the running process")
        elif kind in {"tapOn", "assertVisible", "assertNotVisible"}:
            selector(value, labelled=True)
            if kind == "tapOn":
                require(value["id"] in TAP_IDS and value.get("enabled") is True,
                        "Tap must target an enabled allowed control; saving is prohibited")
        elif kind == "extendedWaitUntil":
            target = set(value) - {"timeout", "label"}
            require(target in ({"visible"}, {"notVisible"}) and
                    type(value.get("timeout")) is int and value["timeout"] == 15000,
                    "Wait requires one visibility condition and a 15000 ms timeout")
            selector(value[next(iter(target))])
        elif kind == "takeScreenshot":
            screenshot(value)
        elif kind == "runFlow":
            require(set(value) in ({"file", "label"}, {"commands", "label"}),
                    "Subflows require either a literal file or inline commands")
            if "file" in value:
                require(isinstance(value["file"], str) and "\\" not in value["file"] and
                        ".." not in value["file"] and "${" not in value["file"],
                        "Subflow paths must be literal relative paths")
                target = (Path(flow).parent / value["file"]).as_posix()
                require(target in FILES, "Subflow reference must remain in the flow allowlist")
                references.add(target)
            else:
                commands(value["commands"], flow, references, inline=True)
        else:
            raise ValueError("Command is outside the safe allowlist")


def validate_documents(documents):
    require(set(documents) == FILES, "Flow file inventory differs from the allowlist")
    graph = {}
    for flow, docs in documents.items():
        require(isinstance(docs, (list, tuple)) and len(docs) == 2, "Expected two YAML documents")
        header, body = docs
        require(isinstance(header, dict) and set(header) == {"appId", "name", "onFlowComplete"},
                "Flow configuration requires only appId, name and completion screenshot")
        require(header["appId"] == "${APP_ID}", "appId must come from APP_ID")
        require(isinstance(header["name"], str) and header["name"].strip() and
                "${" not in header["name"], "Flow name must be literal")
        require(isinstance(header["onFlowComplete"], list) and len(header["onFlowComplete"]) == 1,
                "Every flow requires exactly one terminal screenshot")
        refs = set()
        commands(header["onFlowComplete"], flow, refs, hook=True)
        commands(body, flow, refs)
        graph[flow] = refs

    def visit(flow, stack):
        require(flow not in stack, "Cyclic runFlow dependency")
        for dependency in graph[flow]:
            visit(dependency, stack | {flow})

    for flow in graph:
        visit(flow, set())


def validate_source_ids():
    for identifier, (filename, fragment) in SOURCE_IDS.items():
        if identifier == "add_attachment":
            path = REPO / "mobile/shared/src/androidMain/kotlin/io/github/fermin214/moerise/presentation/PlatformAttachments.kt"
            require(fragment in path.read_text(encoding="utf-8"),
                    "Attachment ID is absent from Android source")
        else:
            require(fragment in (PRESENTATION / filename).read_text(encoding="utf-8"),
                    "Allowlisted ID fragment is absent from source")
    sidebar = (PRESENTATION / "WebSidebar.kt").read_text(encoding="utf-8")
    for target in ("home", "insights"):
        require(f'Triple("{target}",' in sidebar, "Navigation target is absent from source")
    tabs = (PRESENTATION / "MoeriseUi.kt").read_text(encoding="utf-8")
    require('testTag("${tagPrefix}_$index")' in tabs, "Indexed tab ID is absent from source")


def validate_runtime(flow, app_id, prefix, note_name):
    require(flow in FILES, "Select a known relative flow path")
    require(isinstance(app_id, str) and
            re.fullmatch(r"[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+", app_id),
            "APP_ID must be an Android package name")
    require(isinstance(prefix, str) and
            re.fullmatch(r"[A-Za-z0-9_-]+(?:/[A-Za-z0-9_-]+)*", prefix),
            "SCREENSHOT_PREFIX must contain only relative safe path segments")
    if flow in {"archive.yaml", "insights-material.yaml"} or note_name is not None:
        require(isinstance(note_name, str) and
                re.fullmatch(r"memos/[A-Za-z0-9_-]+", note_name),
                "NOTE_NAME must be one exact memos resource name without regex metacharacters")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--flow", choices=sorted(FILES))
    parser.add_argument("--app-id")
    parser.add_argument("--screenshot-prefix")
    parser.add_argument("--note-name")
    args = parser.parse_args()
    try:
        paths = sorted(ROOT.rglob("*.yaml")) + sorted(ROOT.rglob("*.yml"))
        documents = {path.relative_to(ROOT).as_posix(): parse(path.read_text(encoding="utf-8"))
                     for path in paths}
        validate_documents(documents)
        validate_source_ids()
        if any(value is not None for value in vars(args).values()):
            validate_runtime(args.flow, args.app_id, args.screenshot_prefix, args.note_name)
    except (ValueError, OSError, yaml.YAMLError) as error:
        # Do not echo supplied values or parser snippets that may contain secrets.
        detail = str(error) if isinstance(error, ValueError) else type(error).__name__
        print(f"Host validation failed: {detail}", file=sys.stderr)
        return 1
    print(f"Host validation passed: {len(documents)} YAML flows, source IDs and safety policy.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
