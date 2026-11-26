"""Host-only validation of the Maestro catalog, definitions and fixture contract."""

import argparse
from pathlib import Path
import json
import os
import re
import shutil
import subprocess
import sys

try:
    import yaml
except ImportError:
    raise SystemExit("Use a Python environment containing the pinned PyYAML dependency.")

ROOT = Path(__file__).resolve().parent
REPO = ROOT.parents[2]
PRESENTATION = REPO / "mobile/androidApp/src/main/kotlin/io/github/fermin214/moerise/presentation"
CREDENTIALS = {
    "MAESTRO_FIXTURE_ORIGIN": "server_origin",
    "MAESTRO_FIXTURE_USERNAME": "server_username",
    "MAESTRO_FIXTURE_PASSWORD": "server_password",
}
DATA_VARIABLES = {
    "APP_ID", "SCREENSHOT_PREFIX", "INPUT_SUFFIX", "NOTE_NAME", "PEER_NOTE_NAME", "NOTE_TEXT",
    "PEER_NOTE_TEXT", "EDITED_TEXT", "PEER_EDITED_TEXT", "DRAFT_TEXT", "QUERY_TEXT", "ATTACHMENT_NAME",
    "NEW_NOTE_TEXT", "ATTACHMENT_NOTE_TEXT", "EXPECTED_ACCOUNT",
    "ATTACHMENT_SHA256",
}
TEXT_VARIABLES = {
    "NOTE_TEXT", "PEER_NOTE_TEXT", "EDITED_TEXT", "PEER_EDITED_TEXT", "DRAFT_TEXT", "QUERY_TEXT",
    "NEW_NOTE_TEXT", "ATTACHMENT_NOTE_TEXT", "ATTACHMENT_NAME",
}
INPUT_FIELDS = {
    **CREDENTIALS, "EDITED_TEXT": "note_editor_${NOTE_NAME}",
    "PEER_EDITED_TEXT": "note_editor_${PEER_NOTE_NAME}",
    "DRAFT_TEXT": "note_editor", "QUERY_TEXT": "search_query",
    "NEW_NOTE_TEXT": "note_editor", "ATTACHMENT_NOTE_TEXT": "note_editor",
}
VARIABLE = re.compile(r"\$\{([A-Z][A-Z0-9_]*)\}")
TIMESTAMP_TEXT = r"\d{4}-\d{2}-\d{2} \d{2}:\d{2}"
ABSENCE_IDS = {
    "detail_back": ("DeviceUiFlow.kt", 'onNodeWithTag("detail_back").assertDoesNotExist()'),
    "note_detail": ("DeviceUiFlow.kt", 'onNodeWithTag("note_detail").assertDoesNotExist()'),
    "open_note_${NOTE_NAME}": ("QuickNoteTest.kt", 'onNodeWithTag("open_note_$name").assertDoesNotExist()'),
}
PROVIDER = r"(com\.android\.documentsui|com\.google\.android\.documentsui):id/"
SYSTEM_IDS = {PROVIDER + suffix for suffix in ("toolbar", "roots_list", "dir_list", "action_menu_select")}
XIAOMI = r"com\.android\.fileexplorer:id/"
SYSTEM_IDS |= {XIAOMI + suffix for suffix in ("pick_container", "file_name", "btnConfirm", "path_item")}
SYSTEM_IDS.add("android:id/list")
SYSTEM_IDS.add("android:id/overflow")
SYSTEM_IDS.add("com.android.systemui:id/ok")
SYSTEM_TAP_IDS = {PROVIDER + "action_menu_select", XIAOMI + "file_name", XIAOMI + "btnConfirm"}
SYSTEM_TAP_IDS.add("android:id/overflow")
SYSTEM_TAP_IDS.add("com.android.systemui:id/ok")
UNION_IDS = {
    "open_menu|server_login", "welcome_enter|open_menu|server_login", "walk_settings|walk_start|walk_end",
    "note_editor|" + PROVIDER + "action_menu_select",
    PROVIDER + "toolbar|" + XIAOMI + "pick_container",
    "note_editor|note_editor_memos/[A-Za-z0-9_-]+",
    "note_card_${NOTE_NAME}|note_editor|note_editor_memos/[A-Za-z0-9_-]+",
}
LITERAL_TEXT = {
    "^$", "在主页查看|View on home", "简体中文", "English", "跟随系统|Follow system",
    "简体中文|English|跟随系统|Follow system", "语言|Language",
    "Show roots|显示根目录", "Downloads|下载",
    "浏览|Browse", "Download|下载",
    "关键词|Keyword", "混合|Hybrid", TIMESTAMP_TEXT,
    "Select all|Select All|全选",
    "回顾|Review", "漫步|Walk", "观星|Stargazing",
    "QQ 私聊|QQ private chat", "发送记录|Delivery history", "设置已保存。|Settings saved\\.",
    r"设置服务繁忙，请稍后重新保存。|The settings service is busy\. Please save again later\.",
    r"设置已保存。|Settings saved\.|设置服务繁忙，请稍后重新保存。|The settings service is busy\. Please save again later\.",
    r"今天没有可回顾的旧笔记。|No old notes to review today\.",
    r"今天没有可回顾的旧笔记。|No old notes to review today\.|下一条|Next note|再看一遍|Start again",
    "100000019", "围绕所选笔记提问或追问…|Ask about the selected notes…",
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def unique_pairs(pairs):
    result = {}
    for key, value in pairs:
        require(isinstance(key, str) and key not in result, "Duplicate or non-string mapping key")
        result[key] = value
    return result


class UniqueLoader(yaml.SafeLoader):
    def construct_mapping(self, node, deep=False):
        return unique_pairs((self.construct_object(key, deep=deep),
                             self.construct_object(value, deep=deep))
                            for key, value in node.value)


def parse(text):
    require(not any(isinstance(event, yaml.AliasEvent) for event in yaml.parse(text)),
            "YAML aliases are not allowed")
    docs = list(yaml.load_all(text, Loader=UniqueLoader))
    require(len(docs) == 2, "Each flow requires configuration and commands documents")
    return docs


def load_catalog(root=ROOT):
    value = json.loads((root / "catalog.json").read_text(encoding="utf-8"),
                       object_pairs_hook=unique_pairs)
    validate_catalog(value)
    return value


def validate_catalog(value):
    require(isinstance(value, dict) and set(value) == {"flows", "groups"}, "Invalid catalog structure")
    flows, groups = value["flows"], value["groups"]
    require(isinstance(flows, dict) and flows and isinstance(groups, dict) and groups,
            "Catalog flows and groups must be nonempty mappings")
    paths = []
    for name, entry in flows.items():
        require(re.fullmatch(r"[a-z][a-z0-9-]*", name), "Invalid catalog flow name")
        require(isinstance(entry, dict) and set(entry) == {"file", "timeout_seconds", "seed_notes"},
                "Invalid catalog flow metadata")
        require(isinstance(entry["file"], str) and
                re.fullmatch(r"[a-z][a-z0-9-]*\.yaml", entry["file"]), "Entry must name a root YAML file")
        require(type(entry["timeout_seconds"]) is int and 30 <= entry["timeout_seconds"] <= 900,
                "Catalog timeout must be between 30 and 900 seconds")
        require(type(entry["seed_notes"]) is bool, "seed_notes must be a boolean")
        paths.append(entry["file"])
    require(len(set(paths)) == len(paths), "Catalog entry files must be unique")
    for group, names in groups.items():
        require(re.fullmatch(r"[a-z][a-z0-9-]*", group), "Invalid group name")
        require(isinstance(names, list) and names and all(isinstance(name, str) for name in names),
                "Groups must contain flow names")
        require(len(set(names)) == len(names) and set(names) <= flows.keys(), "Invalid group membership")
    require(set(groups) == {"editing", "search", "actions", "navigation",
                            "attachments", "language", "home"},
            "Catalog must expose exactly the eight UI groups")
    for group, names in groups.items():
        primary = "home-smoke" if group == "home" else group
        require(all(name == primary or name.startswith(primary + "-") for name in names),
                "UI groups must select entries belonging to their module")


def validate_phone_checks(value, catalog):
    require(isinstance(value, dict) and set(value) == {"checks"} and isinstance(value["checks"], list),
            "Phone checklist requires a checks list")
    ids, flows, coverage = set(), set(), set()
    for item in value["checks"]:
        require(isinstance(item, dict), "Phone check must be a mapping")
        fields = {"id", "module", "handoff_items", "kind", "phase", "requires_clean_editor"}
        require(fields <= item.keys(), "Phone check metadata is incomplete")
        name, kind, phase = item["id"], item["kind"], item["phase"]
        require(isinstance(name, str) and re.fullmatch(r"[a-z][a-z0-9-]*", name) and name not in ids,
                "Phone check ID must be unique and suitable for the command line")
        ids.add(name)
        require(isinstance(item["module"], str) and re.fullmatch(r"[a-z][a-z0-9-]*", item["module"]),
                "Phone check module name is invalid")
        require(kind in {"auto", "visual", "manual"} and phase in {"pre_install", "pre_launch", "post_run"}
                and type(item["requires_clean_editor"]) is bool, "Phone check kind, phase or editor dependency is invalid")
        require(isinstance(item["handoff_items"], list) and item["handoff_items"] and
                all(type(number) is int or number in ("upgrade", "offline", "telemetry")
                    for number in item["handoff_items"]), "Phone check requires handoff references")
        coverage.update(item["handoff_items"])
        if kind == "auto":
            require(set(item) == fields | {"flow"} and phase == "post_run"
                    and item.get("flow") in catalog["flows"] and item["flow"] not in flows,
                    "Automatic phone check must select one distinct catalog flow")
            flows.add(item["flow"])
        else:
            card = item.get("manual_card")
            require(set(item) == fields | {"manual_card"} and isinstance(card, dict) and
                    set(card) == {"start_state", "steps", "pass_criteria", "evidence_hint"},
                    "Manual or visual phone check needs a complete operation card")
            require(isinstance(card["steps"], list) and card["steps"] and
                    all(isinstance(text, str) and text.strip() for text in
                        (card["start_state"], card["pass_criteria"], card["evidence_hint"], *card["steps"])),
                    "Operation card must describe steps, evidence and pass criteria")
    require(coverage == {3, *range(6, 19), "upgrade", "offline", "telemetry"},
            "Phone checklist must cover the supported handoff checks")


def source_ids():
    """Explicit IDs with literal Kotlin evidence; no generated or guessed app IDs."""
    result = {}

    def add(filename, identifier, fragment=None):
        result[identifier] = (filename, fragment or f'testTag("{identifier}")')

    for identifier in ("open_menu", "open_search", "search_query", "search_close", "sync_notes"):
        add("MoeriseUi.kt", identifier)
    add("MoeriseUi.kt", "search_query_empty", '"search_query_empty"')
    for identifier in ("account_menu", "nav_settings", "nav_archive", "sidebar_account", "sidebar_sync"):
        add("WebSidebar.kt", identifier)
    for target in ("home", "review", "walk", "map"):
        add("WebSidebar.kt", "nav_" + target, 'testTag("nav_$target")')
    add("SettingsScreen.kt", "settings_page")
    add("SettingsScreen.kt", "settings_language", 'tag = "settings_language"')
    add("SettingsScreen.kt", "settings_review_count", '"review.count", strings.text("每次数量（条）"')
    add("SettingsScreen.kt", "settings_stargazing_enabled", 'testTag("settings_${path.replace(\'.\', \'_\')}")')
    add("SettingsScreen.kt", "settings_channel_kind", '"channel.kind", strings.text("定时回顾发往"')
    add("SettingsScreen.kt", "settings_channel_qq_app_id", '"channel.qq_app_id", "QQ AppID"')
    add("SettingsScreen.kt", "settings_secret_qq_app_secret_action", 'tag = "settings_secret_${name}_action"')
    add("SettingsScreen.kt", "settings_save")
    for index in range(4):
        add("SettingsScreen.kt", f"settings_tab_{index}", 'tagPrefix = "settings_tab"')
    for identifier in ("server_origin", "server_username", "server_password"):
        add("ConnectionScreen.kt", identifier, f'"{identifier}", !state.busy')
        add("ConnectionScreen.kt", identifier + "_empty", '"${tag}_empty"')
    add("ConnectionScreen.kt", "server_login")
    add("PrivacyScreen.kt", "privacy_share")
    add("PrivacyScreen.kt", "welcome_enter")
    add("ConnectionScreen.kt", "server_change")
    add("MoeriseApp.kt", "note_editor", 'editorTag: String = "note_editor"')
    for identifier in ("ime_visible", "ime_hidden"):
        add("MoeriseApp.kt", identifier, f'"{identifier}"')
    add("MoeriseApp.kt", "note_editor_empty", '"${editorTag}_empty"')
    add("MoeriseApp.kt", "save_note", 'saveTag: String = "save_note"')
    add("MoeriseApp.kt", "archive_page")
    add("MoeriseApp.kt", "home")
    for variable in ("NOTE_NAME", "PEER_NOTE_NAME"):
        for prefix in ("note_card", "note_more", "edit_note", "archive_note", "delete_note", "delete_confirm"):
            fragment = (f'testTag("{prefix}_${{note.name}}")' if prefix in {"note_card", "delete_confirm"}
                        else f'tag = "{prefix}_${{note.name}}"')
            add("WebNoteCard.kt", prefix + "_${" + variable + "}", fragment)
        for prefix, source in (("note_editor", 'editorTag = "note_editor_$id"'),
                               ("save_note", 'saveTag = "save_note_$id"'),
                               ("cancel_note", 'cancelTag = "cancel_note_$id"')):
            add("MoeriseApp.kt", prefix + "_${" + variable + "}", source)
        add("MoeriseApp.kt", "note_editor_${" + variable + "}_empty", '"${editorTag}_empty"')
    for page in ("review", "walk"):
        add("DiscoveryScreen.kt", page + "_page", 'testTag("${page}_page")')
    for identifier in ("review_next", "walk_start", "walk_end", "walk_settings",
                       "walk_configuration_error", "discovery_error"):
        add("DiscoveryScreen.kt", identifier)
    add("StarMapScreen.kt", "map_page")
    add("StarMapScreen.kt", "map_fullscreen", 'tag = "map_fullscreen"')
    add("PlatformAttachments.kt", "add_attachment")
    return result


SOURCE_IDS = source_ids()
NON_TAP_IDS = {
    "settings_page", "archive_page", "review_page", "walk_page",
    "home", "map_page", "sidebar_account",
    "ime_visible", "ime_hidden",
    "walk_configuration_error", "discovery_error", "settings_stargazing_enabled",
}


def selector(value, labelled=False, relation=False, absent=False):
    allowed = {"id", "text", "enabled", "focused", "checked", "childOf"}
    if labelled:
        allowed.add("label")
    require(isinstance(value, dict) and set(value) <= allowed, "Unsupported selector property")
    require("id" in value or "text" in value, "Selector requires a known ID or exact text")
    if "id" in value:
        identifier = value["id"]
        require(isinstance(identifier, str) and
                identifier in set(SOURCE_IDS) | SYSTEM_IDS | UNION_IDS | ABSENCE_IDS.keys(),
                "Selector ID is outside the source-backed allowlist")
        require(identifier not in ABSENCE_IDS or absent,
                "Historical detail-route IDs may only be used to assert absence")
    if "text" in value:
        text = value["text"]
        dynamic = {r"\Q${" + variable + r"}\E" for variable in TEXT_VARIABLES | {"EXPECTED_ACCOUNT"}}
        require(isinstance(text, str) and text in LITERAL_TEXT | dynamic,
                "Selector text must be a known label or a quoted fixture variable")
        if text == TIMESTAMP_TEXT:
            require(value.get("childOf") == {"id": "note_card_${NOTE_NAME}"},
                    "Timestamp matching must be scoped to the dedicated test note")
        if text == r"\Q${EXPECTED_ACCOUNT}\E":
            require(value.get("id") == "account_menu" or value.get("childOf") == {"id": "account_menu"},
                    "Account identity must be checked inside the account control")
    if value.get("id") == XIAOMI + "file_name":
        require(value.get("text") in {r"\Q${ATTACHMENT_NAME}\E", "Download|下载"} and
                value.get("childOf") == {"id": "android:id/list"},
                "Xiaomi selection requires Download or the exact fixture filename in the observed file list")
    for key in ("enabled", "focused", "checked"):
        if key in value:
            require(type(value[key]) is bool, "Selector flags must be booleans")
    if "childOf" in value:
        require(not relation, "Nested selector relations are not needed")
        selector(value["childOf"], relation=True)
        require(set(value["childOf"]) == {"id"}, "Parent selection requires one exact known ID")


def commands(items, flow, refs, inline=False, hook=False, focused=None, ime_guarded=False):
    require(isinstance(items, list) and items, "Command lists must not be empty")
    selected_all = False
    for index, item in enumerate(items):
        if isinstance(item, str):
            require(inline and item == "back" and flow == "attachment-cancel.yaml",
                    "Back is only allowed to cancel the verified system file picker")
            focused = None
            continue
        require(isinstance(item, dict) and len(item) == 1, "Expected one command per step")
        kind, value = next(iter(item.items()))
        require(isinstance(value, dict), "Commands require labelled mappings")
        label = value.get("label")
        require(isinstance(label, str) and label.strip() and "${" not in label,
                "Every mapped command requires a literal label")
        keys = set(value) - {"label"}
        if hook:
            require(kind == "takeScreenshot", "Completion hooks may only capture screenshots")
        if kind == "launchApp":
            require(keys == {"clearState", "stopApp"} and
                    value["clearState"] is False and value["stopApp"] is False,
                    "launchApp must preserve state and the running process")
            focused = None
        elif kind in {"tapOn", "longPressOn", "assertVisible", "assertNotVisible"}:
            selector(value, labelled=True, absent=kind == "assertNotVisible")
            if kind == "longPressOn":
                require(keys == {"id", "enabled"} and value["enabled"] is True and
                        value["id"] in INPUT_FIELDS.values(),
                        "Long press is limited to known fixture input fields")
                focused, selected_all = value["id"], False
            elif kind == "tapOn":
                identifier = value.get("id")
                if identifier == "android:id/overflow":
                    require(keys == {"id", "enabled"} and value["enabled"] is True and
                            focused in INPUT_FIELDS.values(),
                            "Text selection overflow requires a known focused fixture input")
                    selected_all = False
                    continue
                if value.get("text") == "Select all|Select All|全选":
                    require(keys == {"text", "enabled"} and value["enabled"] is True and
                            focused in INPUT_FIELDS.values(),
                            "Select all requires a known focused fixture input")
                    selected_all = True
                    continue
                require(value.get("enabled") is True and identifier not in NON_TAP_IDS and
                        identifier not in UNION_IDS and identifier not in SYSTEM_IDS - SYSTEM_TAP_IDS,
                        "Tap requires an enabled allowed control")
                require(not identifier or not identifier.startswith("note_card_"),
                        "Use a specific note action rather than tapping a card")
                focused = identifier if identifier in set(INPUT_FIELDS.values()) else None
                selected_all = False
        elif kind == "extendedWaitUntil":
            target = keys - {"timeout"}
            require(target in ({"visible"}, {"notVisible"}) and
                    type(value.get("timeout")) is int and value["timeout"] in {15000, 65000},
                    "Wait requires one visibility condition and a bounded UI or request timeout")
            selector(value[next(iter(target))], absent=target == {"notVisible"})
        elif kind == "scrollUntilVisible":
            require(keys == {"element", "direction", "timeout", "centerElement"} and
                    value["direction"] in {"UP", "DOWN", "LEFT", "RIGHT"} and type(value["timeout"]) is int and
                    value["timeout"] in {15000, 65000} and type(value["centerElement"]) is bool,
                    "Scrolling requires a bounded element search without coordinates")
            selector(value["element"])
            if value["direction"] in {"LEFT", "RIGHT"}:
                require(value["element"].get("id") in {f"settings_tab_{index}" for index in range(4)},
                        "Horizontal scrolling is limited to the settings tab strip")
            focused = None
        elif kind == "swipe":
            require(keys == {"from", "direction", "duration"} and
                    value["direction"] in {"LEFT", "RIGHT"} and value["duration"] == 400 and
                    isinstance(value["from"], dict) and set(value["from"]) == {"id"} and
                    value["from"]["id"] in {f"settings_tab_{index}" for index in range(4)},
                    "Swipe requires a settings tab origin and a bounded horizontal gesture")
            focused, selected_all = None, False
        elif kind == "takeScreenshot":
            require(keys == {"path"} and isinstance(value["path"], str) and
                    re.fullmatch(r"\$\{SCREENSHOT_PREFIX\}-[a-z0-9-]+", value["path"]),
                    "Screenshots must use the supplied prefix with a literal suffix")
        elif kind == "inputText":
            require(keys == {"text"} and isinstance(value["text"], str), "Invalid text input mapping")
            suffix = "${INPUT_SUFFIX}"
            require(value["text"].endswith(suffix), "Fixture text requires the temporary input suffix")
            variable = VARIABLE.fullmatch(value["text"].removesuffix(suffix))
            require(variable is not None and variable[1] in INPUT_FIELDS,
                    "Text input must use an allowed ASCII fixture variable")
            require(focused == INPUT_FIELDS[variable[1]], "Text input must follow focus of its specific test field")
            following = items[index + 1] if index + 1 < len(items) else {}
            require(isinstance(following, dict) and isinstance(following.get("eraseText"), dict) and
                    following["eraseText"].get("charactersToErase") == 1,
                    "Remove the fixture input suffix before performing other actions")
            if variable[1] in CREDENTIALS:
                require(flow == "subflows/ensure-login.yaml", "Credentials belong only in ensure-login")
        elif kind == "eraseText":
            follows_input = index > 0 and isinstance(items[index - 1], dict) and "inputText" in items[index - 1]
            require(keys == {"charactersToErase"} and type(value["charactersToErase"]) is int and
                    (value["charactersToErase"] == 100 or
                     value["charactersToErase"] == 1 and (selected_all or follows_input)) and focused in INPUT_FIELDS.values(),
                    "Erase requires a known focused field and a bounded character count")
            selected_all = False
        elif kind == "pressKey":
            login_next = {
                "server_origin": "server_username", "server_username": "server_password",
                "server_password": None,
            }
            require(keys == {"key"} and value["key"] == "Enter" and
                    (flow in ("search.yaml", "test-source-resume.yaml") and focused == "search_query" or
                     flow == "subflows/ensure-login.yaml" and focused in login_next),
                    "Enter requires the focused search field or a login form field")
            focused = login_next[focused] if flow == "subflows/ensure-login.yaml" else None
        elif kind == "hideKeyboard":
            require(not keys and inline and ime_guarded and flow == "subflows/hide-keyboard.yaml",
                    "Hide keyboard requires the observed IME visibility guard in its dedicated subflow")
            focused, selected_all = None, False
        elif kind == "runFlow":
            body_keys = keys - {"when"}
            require(body_keys in ({"file"}, {"commands"}), "Subflow requires one file or command list")
            if "when" in value:
                condition = value["when"]
                require(isinstance(condition, dict) and set(condition) in ({"visible"}, {"notVisible"}),
                        "Conditions must check a visible or absent known element")
                guard, target = next(iter(condition.items()))
                selector(target, absent=guard == "notVisible")
                require("commands" in value and isinstance(value["commands"], list) and value["commands"],
                        "Conditional branches require inline checks")
                first = value["commands"][0]
                expected = "assertVisible" if guard == "visible" else "assertNotVisible"
                require(isinstance(first, dict) and set(first) == {expected} and
                        {k: v for k, v in first[expected].items() if k != "label"} == target,
                        "Every conditional branch must begin with its explicit precondition")
            if "file" in value:
                path = value["file"]
                require(isinstance(path, str) and re.fullmatch(r"(subflows/)?[a-z][a-z0-9-]*\.yaml", path),
                        "Subflow paths must be literal relative YAML paths")
                refs.add((Path(flow).parent / path).as_posix())
            else:
                nested_focus, _ = commands(value["commands"], flow, refs, inline=True, focused=focused,
                    ime_guarded=value.get("when") == {"visible": {"id": "ime_visible"}})
                focused = focused if nested_focus == focused else None
            if "file" in value:
                focused = None
            selected_all = False
        else:
            raise ValueError("Command is outside the safe allowlist")
    return focused, selected_all


def validate_documents(documents, catalog=None):
    catalog = catalog or load_catalog()
    validate_catalog(catalog)
    require({name for name in documents if "/" not in name} ==
            {entry["file"] for entry in catalog["flows"].values()},
            "Root YAML entries must match catalog.json exactly")
    graph = {}
    for flow, docs in documents.items():
        require(re.fullmatch(r"(subflows/)?[a-z][a-z0-9-]*\.yaml", flow), "Invalid YAML path")
        require(isinstance(docs, (list, tuple)) and len(docs) == 2, "Expected two YAML documents")
        header, body = docs
        require(isinstance(header, dict) and set(header) == {"appId", "name", "onFlowComplete"},
                "Flow configuration requires appId, name and a terminal screenshot")
        require(header["appId"] == "${APP_ID}", "appId must come from APP_ID")
        require(isinstance(header["name"], str) and header["name"].strip() and
                "${" not in header["name"], "Flow name must be literal")
        require(isinstance(header["onFlowComplete"], list) and len(header["onFlowComplete"]) == 1,
                "Every flow requires exactly one terminal screenshot")
        refs = set()
        try:
            commands(header["onFlowComplete"], flow, refs, hook=True)
            commands(body, flow, refs)
        except ValueError as error:
            raise ValueError(flow + ": " + str(error)) from error
        return_markers = {
            "subflows/pick-documentsui.yaml": PROVIDER + "toolbar",
            "subflows/pick-xiaomi.yaml": XIAOMI + "pick_container",
        }
        if flow in return_markers:
            require(len(body) >= 3 and
                    body[-3].get("extendedWaitUntil", {}).get("visible") == {"id": "note_editor"} and
                    body[-2].get("extendedWaitUntil", {}).get("visible") == {"text": r"\Q${ATTACHMENT_NAME}\E"} and
                    body[-1].get("assertNotVisible", {}).get("id") == return_markers[flow],
                    "Each file provider must verify return to the app, the exact attachment and picker dismissal")
        graph[flow] = refs
    require(all(ref in graph for refs in graph.values() for ref in refs), "Subflow reference is missing")

    def visit(flow, stack):
        require(flow not in stack, "Cyclic runFlow dependency")
        for dependency in graph[flow]:
            visit(dependency, stack | {flow})

    for flow in graph:
        visit(flow, set())
    return graph


def validate_source_ids():
    for filename, fragment in SOURCE_IDS.values():
        path = PRESENTATION / filename
        if filename == "PlatformAttachments.kt":
            path = REPO / "mobile/androidApp/src/main/kotlin/io/github/fermin214/moerise/presentation" / filename
        require(fragment in path.read_text(encoding="utf-8"), "Allowlisted ID fragment is absent from source")
    sidebar = (PRESENTATION / "WebSidebar.kt").read_text(encoding="utf-8")
    for target in ("home", "review", "walk", "map"):
        require(f'Triple("{target}",' in sidebar, "Navigation target is absent from source")
    require('testTag("${tagPrefix}_$index")' in (PRESENTATION / "MoeriseUi.kt").read_text(encoding="utf-8"),
            "Indexed tab ID is absent from source")
    ui = (PRESENTATION / "MoeriseUi.kt").read_text(encoding="utf-8")
    for fragment in ('keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search)',
                     'keyboardActions = KeyboardActions(onSearch = { onSubmit() })',
                     'strings.text("关键词", "Keyword")', 'strings.text("混合", "Hybrid")'):
        require(fragment in ui, "Search action or mode label is absent from source")
    tests = REPO / "mobile/androidApp/src/androidTest/kotlin/io/github/fermin214/moerise"
    for filename, fragment in ABSENCE_IDS.values():
        require(fragment in (tests / filename).read_text(encoding="utf-8"),
                "Original home-reading absence assertion is missing")


def load_documents(root=ROOT):
    return {path.relative_to(root).as_posix(): parse(path.read_text(encoding="utf-8"))
            for path in sorted(root.rglob("*.yaml")) + sorted(root.rglob("*.yml"))}


def required_variables(flow, documents, graph):
    found = set()
    visited = set()

    def scan(value):
        if isinstance(value, str):
            found.update(VARIABLE.findall(value))
        elif isinstance(value, dict):
            for item in value.values():
                scan(item)
        elif isinstance(value, list):
            for item in value:
                scan(item)

    def visit(path):
        if path in visited:
            return
        visited.add(path)
        scan(documents[path])
        for dependency in graph[path]:
            visit(dependency)

    visit(flow)
    return found


def validate_runtime(flow, values, environment=None, catalog=None, documents=None):
    """Validate runner data separately from inherited credential environment values."""
    catalog = catalog or load_catalog()
    documents = documents or load_documents()
    graph = validate_documents(documents, catalog)
    by_file = {entry["file"]: name for name, entry in catalog["flows"].items()}
    name = flow if flow in catalog["flows"] else by_file.get(flow)
    require(name is not None, "Select a catalog flow name or entry file")
    require(isinstance(values, dict) and set(values) <= DATA_VARIABLES,
            "Runner parameters may contain only the fixed non-credential data variables")
    needed = required_variables(catalog["flows"][name]["file"], documents, graph)
    require(needed - CREDENTIALS.keys() <= values.keys(), "Required fixture data is missing")
    require(re.fullmatch(r"[A-Za-z]\w*(?:\.[A-Za-z]\w*)+", str(values.get("APP_ID", "")), re.ASCII),
            "APP_ID must be an Android package name")
    require(re.fullmatch(r"[A-Za-z0-9_-]+(?:/[A-Za-z0-9_-]+)*", str(values.get("SCREENSHOT_PREFIX", ""))),
            "SCREENSHOT_PREFIX must contain only safe relative path segments")
    for key, value in values.items():
        require(isinstance(value, str), "Fixture values must be strings")
        if key in {"NOTE_NAME", "PEER_NOTE_NAME"}:
            require(re.fullmatch(r"memos/[A-Za-z0-9_-]+", value), "Invalid exact note resource name")
        elif key == "INPUT_SUFFIX":
            require(value in {".", "。"}, "Use the fixed ASCII or Unicode input suffix")
        elif key == "ATTACHMENT_NAME":
            require(re.fullmatch(r"[A-Za-z0-9_-]+\.txt", value), "Attachment must have one safe ASCII txt filename")
        elif key == "ATTACHMENT_SHA256":
            require(re.fullmatch(r"[a-f0-9]{64}", value), "Attachment digest must be a SHA-256")
        elif key == "EXPECTED_ACCOUNT":
            require(0 < len(value) <= 100 and value.isprintable() and r"\E" not in value and "${" not in value,
                    "Account identity must be a bounded literal label")
        elif key in TEXT_VARIABLES:
            require(re.fullmatch(r"[A-Za-z0-9_-]+(?: [A-Za-z0-9_-]+)*", value) and len(value) <= 90,
                    "Fixture text must be a short unique ASCII marker without markup")
    if "NOTE_NAME" in values and "PEER_NOTE_NAME" in values:
        require(values["NOTE_NAME"] != values["PEER_NOTE_NAME"], "Dedicated notes must be distinct")
    if "NOTE_TEXT" in values and "QUERY_TEXT" in values:
        require(values["NOTE_TEXT"] == values["QUERY_TEXT"], "QUERY_TEXT must equal NOTE_TEXT")
    texts = [value for key, value in values.items() if key in TEXT_VARIABLES - {"QUERY_TEXT", "ATTACHMENT_NAME"}]
    require(len(texts) == len(set(texts)), "Independent fixture texts must be distinct")
    environment = os.environ if environment is None else environment
    for key in needed & CREDENTIALS.keys():
        value = environment.get(key)
        require(isinstance(value, str) and 0 < len(value) <= 100 and
                all(32 <= ord(char) <= 126 for char in value) and "${" not in value,
                "Fixture credentials must be supplied as bounded literal ASCII process environment values")
    return name


def check_syntax(documents, executable):
    """Invoke only the device-free CLI parser; never test, ADB or package operations."""
    for flow in sorted(documents):
        result = subprocess.run([executable, "check-syntax", str(ROOT / flow)],
                                capture_output=True, text=True, timeout=30)
        output = (result.stdout + result.stderr).strip()
        require(result.returncode == 0 and "Unknown Property:" not in output and
                re.search(r"(?m)^OK\s*$", output) is not None,
                "Maestro syntax check failed for " + flow)
    return len(documents)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--flow", help="Catalog flow name or root YAML file")
    parser.add_argument("--parameters-json", type=Path, help="Non-credential runner values")
    parser.add_argument("--check-syntax", action="store_true", help="Run only Maestro's offline parser")
    parser.add_argument("--maestro", default=shutil.which("maestro") or r"C:\maestro\bin\maestro.bat")
    args = parser.parse_args()
    try:
        catalog, documents = load_catalog(), load_documents()
        validate_documents(documents, catalog)
        checks = json.loads((ROOT.parent / "phone-checks.json").read_text(encoding="utf-8-sig"),
                            object_pairs_hook=unique_pairs)
        validate_phone_checks(checks, catalog)
        validate_source_ids()
        require(bool(args.flow) == bool(args.parameters_json), "--flow and --parameters-json must be used together")
        if args.flow:
            values = json.loads(args.parameters_json.read_text(encoding="utf-8"), object_pairs_hook=unique_pairs)
            validate_runtime(args.flow, values, catalog=catalog, documents=documents)
        if args.check_syntax:
            count = check_syntax(documents, args.maestro)
            print(f"Maestro offline check-syntax passed: {count} files.")
    except (ValueError, OSError, yaml.YAMLError, subprocess.SubprocessError) as error:
        # Parser output and runtime values can contain fixture credentials.
        detail = str(error) if type(error) is ValueError else type(error).__name__
        print("Host validation failed: " + detail, file=sys.stderr)
        return 1
    print(f"Host validation passed: {len(catalog['flows'])} entries, {len(documents)} YAML files, source IDs and safety policy.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
