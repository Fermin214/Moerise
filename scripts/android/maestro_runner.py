"""Shared Maestro execution for persistent phones and isolated CI devices."""
from contextlib import contextmanager
from datetime import datetime, timezone
import base64
import hashlib
import http.client
import http.cookiejar
import json
import os
from pathlib import Path
import re
import signal
import socket
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
import xml.etree.ElementTree as ET
import yaml

ROOT = Path(__file__).resolve().parents[2]
APP = "io.github.fermin214.moerise.fixture"
ACTIVITY = APP + "/io.github.fermin214.moerise.MainActivity"
DRIVERS = ("dev.mobile.maestro", "dev.mobile.maestro.test")
MAESTRO_IME = DRIVERS[0] + "/.input.MaestroInputMethodService"
UNICODE_INPUT_SUFFIX = "。"
USER_AGENT = "Moerise-Android-Acceptance/1.0"
CHANNEL_FLOWS = {"navigation", "navigation-settings"}
FLOW_CONTRACT_VERSIONS = {
    "actions": 1, "archive": 1, "attachments": 1, "attachment-cancel": 1,
    "editing": 1, "home-smoke": 1, "insights": 1, "insights-material": 1,
    "insights-disconnect": 1, "language": 1, "login": 1, "navigation": 1,
    "navigation-review": 1, "navigation-walk": 1, "navigation-map": 1, "navigation-settings": 1,
    "recover-draft": 1, "editor-home": 1, "release-smoke": 1, "search": 1, "test-source": 2,
    "test-source-resume": 1, "test-source-welcome": 1,
    "upgrade-smoke": 1,
}
SERVER_FLOWS = CHANNEL_FLOWS | {
    "editing", "release-smoke", "actions", "archive", "attachments", "insights", "insights-disconnect",
}


def catalog(root=ROOT):
    return json.loads((Path(root) / "scripts/android/maestro/catalog.json").read_text(encoding="utf-8"))


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], encoding="utf-8").strip()


def common_repo(root):
    return Path(git(root, "rev-parse", "--path-format=absolute", "--git-common-dir")).parent


def worktree_roots(root):
    return [Path(field.removeprefix("worktree ")) for field in git(root, "worktree", "list", "--porcelain", "-z").split("\0")
            if field.startswith("worktree ")]


def shared_checkpoint(root, name, legacy_names=()):
    primary = common_repo(root)
    common = primary / ".local/android" / name
    candidates = {tree / ".local/android" / basename for tree in {primary, *worktree_roots(root)}
                  for basename in (name, *legacy_names)} - {common}
    existing = [path for path in sorted(candidates) if path.is_file()]
    if existing:
        if common.exists() or len(existing) > 1:
            raise ValueError(f"恢复检查点重复：{name}；保留所有记录")
        common.parent.mkdir(parents=True, exist_ok=True)
        os.replace(existing[0], common)
    return common


def normalized_origin(value):
    origin = urllib.parse.urlsplit(value)
    host = origin.hostname
    if not host:
        raise ValueError("测试服务地址缺少主机名")
    if ":" in host:
        host = f"[{host}]"
    port = origin.port
    suffix = f":{port}" if port and port != {"http": 80, "https": 443}.get(origin.scheme) else ""
    return f"{origin.scheme}://{host}{suffix}"


def write_result(path, value):
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    Path(path).write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def flow_digest(root, flow):
    directory = Path(root) / "scripts/android/maestro"
    entry = catalog(root)["flows"][flow]
    digest = hashlib.sha256(json.dumps([flow, entry, FLOW_CONTRACT_VERSIONS[flow]], sort_keys=True).encode())
    visited = set()

    def scan(value, parent):
        if isinstance(value, list):
            for item in value:
                scan(item, parent)
        elif isinstance(value, dict):
            subflow = value.get("runFlow")
            if isinstance(subflow, dict) and "file" in subflow:
                visit(parent / subflow["file"])
            for item in value.values():
                scan(item, parent)

    def visit(path):
        if path in visited:
            return
        visited.add(path)
        data = (directory / path).read_text(encoding="utf-8-sig").encode("utf-8")
        digest.update(path.as_posix().encode() + b"\0" + data + b"\0")
        for document in yaml.safe_load_all(data):
            scan(document, path.parent)

    visit(Path(entry["file"]))
    return digest.hexdigest()


class FixtureError(RuntimeError):
    def __init__(self, status, code, *, method=None, path=None, reason=None):
        self.request = {"method": method, "path": path.partition("?")[0]} if method and path else None
        target = f"{method} {self.request['path']}；" if self.request else ""
        super().__init__(f"隔离服务请求失败：{target}HTTP {status} / {code}" + (f"；{reason}" if reason else ""))
        self.status, self.code = status, code

    def details(self):
        result = {"http_status": self.status, "error_code": self.code, "error_message": str(self)}
        if self.request:
            result["request"] = self.request
        return result


def response_error(data, headers):
    try:
        error = json.loads(data).get("error", {})
        code = error.get("code", "")
        if isinstance(code, str) and re.fullmatch(r"[A-Za-z][A-Za-z0-9_]{0,63}", code):
            return code, None
    except (ValueError, AttributeError):
        pass
    if "cloudflare" in headers.get("Server", "").lower():
        match = re.search(rb"\berror\s+code\s*:?\s*(1[0-9]{3,4})\b", data, re.I)
        if match:
            return "CLOUDFLARE_" + match[1].decode("ascii"), "Cloudflare 拒绝了该客户端请求"
        if headers.get("cf-mitigated") == "challenge":
            return "CLOUDFLARE_CHALLENGE", "Cloudflare 要求交互验证"
    return "request_failed", "服务未返回可识别的 JSON 错误；响应正文已省略"


def connection_error(error):
    reason = error.reason if isinstance(error, urllib.error.URLError) else error
    if isinstance(reason, ssl.SSLCertVerificationError):
        return "TLS 证书校验失败"
    if isinstance(reason, TimeoutError):
        return "请求超时"
    if isinstance(reason, socket.gaierror):
        return "域名解析失败"
    if isinstance(reason, ConnectionRefusedError):
        return "连接被拒绝"
    if isinstance(reason, http.client.HTTPException):
        return "服务响应不完整或连接已中断"
    return "无法建立或读取服务连接"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, file, code, message, headers, url):
        return None


class FixtureAPI:
    def __init__(self, connection, trusted_https=False, test_origin=None, allow_production=False):
        self.connection = connection
        origin = urllib.parse.urlsplit(connection["origin"])
        explicit = test_origin is not None
        if explicit and (connection["origin"].rstrip("/") != test_origin.rstrip("/") or
                         connection.get("environment") != "test" and not allow_production):
            raise ValueError("显式地址必须匹配连接档案，且档案必须声明 environment=test")
        if (origin.scheme not in (("http", "https") if trusted_https else ("http",))
                or not origin.hostname or (not explicit and origin.hostname not in ("127.0.0.1", "localhost", "::1"))
                or origin.path not in ("", "/") or origin.query or origin.fragment
                or origin.username or origin.password or (not explicit and not origin.port)
                or (not explicit and connection.get("username") != "android-owner") or not connection.get("password")):
            raise ValueError("Maestro 仅连接回环隔离服务与 android-owner 测试账号")
        self.origin = connection["origin"].rstrip("/")
        self.csrf = ""
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), NoRedirect(),
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(self, method, path, body=None, key=None, binary=False):
        if not path.startswith("/api/app/v1/") or "://" in path or ".." in path:
            raise ValueError("隔离请求必须使用客户端 API 路径")
        headers = {"Origin": self.origin, "User-Agent": USER_AGENT,
                   "Accept": "*/*" if binary else "application/json"}
        if self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        if key:
            headers["Idempotency-Key"] = key
        if body is not None:
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self.origin + path, method=method, headers=headers,
            data=None if body is None else json.dumps(body).encode("utf-8"))
        for attempt in range(3):
            try:
                with self.opener.open(request, timeout=65) as response:
                    data = response.read()
                    if binary or not data:
                        return data if binary else None
                    try:
                        return json.loads(data)
                    except ValueError:
                        raise FixtureError(response.status, "invalid_json", method=method, path=path,
                                           reason="服务未返回有效 JSON；响应正文已省略") from None
            except urllib.error.HTTPError as error:
                with error:
                    code, reason = response_error(error.read(8192), error.headers)
                settings_busy = (method == "PATCH" and path == "/api/app/v1/settings"
                                 and error.code == 503 and code == "SETTINGS_BUSY")
                if error.code in (429, 503) and attempt < 2 and (method == "GET" or key or settings_busy):
                    retry_after = error.headers.get("Retry-After", "1")
                    time.sleep(min(5, max(1, int(retry_after) if retry_after.isdigit() else 1)))
                    continue
                raise FixtureError(error.code, code, method=method, path=path, reason=reason) from None
            except (OSError, urllib.error.URLError, http.client.HTTPException) as error:
                raise FixtureError(0, "unreachable", method=method, path=path,
                                   reason=connection_error(error)) from None

    def login(self):
        result = self.request("POST", "/api/app/v1/session", {
            key: self.connection[key] for key in ("username", "password")})
        self.csrf = result["csrf_token"]
        return result

    def close(self):
        if self.csrf:
            self.request("DELETE", "/api/app/v1/session")
            self.csrf = ""

    def create(self, text, key):
        return self.request("POST", "/api/app/v1/notes", {"content": text}, key)

    def note(self, name):
        if not re.fullmatch(r"memos/[A-Za-z0-9_-]+", name):
            raise ValueError("测试笔记资源名无效")
        return self.request("GET", "/api/app/v1/notes/" + name.split("/")[1])

    def matching_notes(self, content):
        token, seen, notes = "", set(), []
        for _ in range(100):
            query = urllib.parse.urlencode({"state": "NORMAL", "page_size": 100, "query": content,
                                            **({"page_token": token} if token else {})})
            page = self.request("GET", "/api/app/v1/notes?" + query)
            notes.extend(note for note in page["notes"] if note["content"] == content)
            token = page.get("next_page_token", "")
            if not token:
                return notes
            if token in seen:
                raise ValueError("隔离笔记分页游标重复")
            seen.add(token)
        raise ValueError("隔离笔记分页超过上限")


def fresh_ui_dump(adb, output, timeout=15, stop_drivers=True):
    output = Path(output)
    output.unlink(missing_ok=True)
    remote = "/sdcard/moerise-ui-" + uuid.uuid4().hex + ".xml"
    try:
        if stop_drivers:
            for package in reversed(DRIVERS):
                adb.run("shell", "am", "force-stop", package)
        adb.run("shell", "rm", "-f", remote)
        adb.run("shell", "uiautomator", "dump", remote, timeout=timeout)
        xml = adb.run("shell", "cat", remote)
        if ET.fromstring(xml).tag != "hierarchy":
            raise ValueError("UI 层级根节点无效")
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(xml, encoding="utf-8")
        return xml
    finally:
        adb.run("shell", "rm", "-f", remote, check=False)


def junit_result(path, returncode):
    try:
        cases = list(ET.parse(path).getroot().iter("testcase"))
        if not cases:
            return {"status": "harness_error", "cause": "maestro_no_cases"}
        failures = [item for case in cases for item in (*case.iter("failure"), *case.iter("error"))]
        if failures:
            message = failures[0].text or failures[0].get("message", "")
            if "DeviceServerDiedException" in message:
                return {"status": "environment_blocked", "cause": "maestro_driver_died",
                        "failure_message": message[:500]}
            return {"status": "inconclusive", "cause": "maestro_ui_failure",
                    "failure_message": message[:500]}
        if any(list(case.iter("skipped")) for case in cases):
            return {"status": "inconclusive", "cause": "maestro_skipped"}
        return {"status": "passed", "cause": None} if not returncode else {
            "status": "harness_error", "cause": "maestro_exit_code"}
    except (OSError, ET.ParseError):
        return {"status": "harness_error", "cause": "maestro_report_missing_or_unreadable"}


def execute(command, log, timeout, environment):
    options = {"start_new_session": True} if os.name != "nt" else {}
    with Path(log).open("wb") as stream:
        process = subprocess.Popen(command, stdout=stream, stderr=subprocess.STDOUT,
                                   env=environment, **options)
        try:
            return process.wait(timeout=timeout)
        except (subprocess.TimeoutExpired, KeyboardInterrupt):
            try:
                if os.name == "nt":
                    subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
                else:
                    os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            if process.poll() is None:
                process.kill()
            process.wait(timeout=5)
            raise


def active_input_method(adb):
    return adb.run("shell", "settings", "get", "secure", "default_input_method").strip()


def input_method_checkpoint(root, serial):
    key = hashlib.sha256(serial.encode()).hexdigest()[:16]
    return shared_checkpoint(root, "maestro-ime-" + key + ".json")


def restore_input_method(adb, checkpoint):
    if not checkpoint.exists():
        return
    saved = json.loads(checkpoint.read_text(encoding="utf-8"))
    original = saved["original"]
    if not re.fullmatch(r"[A-Za-z0-9_.]+/[A-Za-z0-9_.]+", original) or type(saved.get("enabled")) is not bool:
        raise ValueError("无效的 Maestro 输入法恢复记录")
    # Do not undo a manual switch to a third IME.
    if original != MAESTRO_IME and active_input_method(adb) == MAESTRO_IME:
        adb.run("shell", "ime", "set", original)
        if active_input_method(adb) != original:
            raise RuntimeError("手机原输入法恢复失败")
    if not saved["enabled"] and original != MAESTRO_IME and MAESTRO_IME in adb.run(
            "shell", "ime", "list", "-s").split():
        adb.run("shell", "ime", "disable", MAESTRO_IME)
        if MAESTRO_IME in adb.run("shell", "ime", "list", "-s").split():
            raise RuntimeError("Maestro 测试输入法未恢复到停用状态")
    checkpoint.unlink()


@contextmanager
def preserve_input_method(adb, checkpoint):
    """Restore the original state even if the CLI stops during a Unicode input."""
    if adb.serial.startswith("emulator-"):
        yield
        return
    restore_input_method(adb, checkpoint)
    original = active_input_method(adb)
    if not re.fullmatch(r"[A-Za-z0-9_.]+/[A-Za-z0-9_.]+", original):
        raise RuntimeError("无法读取手机原输入法，未切换输入法")
    was_enabled = MAESTRO_IME in adb.run("shell", "ime", "list", "-s").split()
    temporary = checkpoint.with_suffix(".tmp")
    write_result(temporary, {"original": original, "enabled": was_enabled})
    os.replace(temporary, checkpoint)
    try:
        yield
    finally:
        restore_input_method(adb, checkpoint)


def redact(directory, secrets):
    for path in Path(directory).rglob("*"):
        if not path.is_file() or path.suffix.lower() not in (
                ".log", ".json", ".xml", ".yaml", ".yml", ".txt", ".html"):
            continue
        text = path.read_text(encoding="utf-8", errors="replace")
        cleaned = text
        for secret in secrets:
            if secret:
                cleaned = cleaned.replace(secret, "[redacted]")
        if cleaned != text:
            path.write_text(cleaned, encoding="utf-8")


def last_failed_step(directory, stage=None, timeout=None):
    observations = []
    for path in Path(directory).rglob("commands.json"):
        try:
            commands = json.loads(path.read_text(encoding="utf-8"))
            for command in commands:
                metadata = command.get("metadata", {})
                status = metadata.get("status")
                if not status or status in ("PENDING", "NOT_STARTED"):
                    continue
                evaluated = metadata.get("evaluatedCommand") or command.get("command", {})
                kind, body = next(iter(evaluated.items()))
                target = body.get("selector", {})
                if kind == "assertConditionCommand":
                    condition = body.get("condition", {})
                    target = condition.get("visible") or condition.get("notVisible") or {}
                failed = status in ("FAILED", "ERROR")
                rank = (2, metadata.get("depth", 0), metadata.get("sequenceNumber", 0)) if failed else (
                    int(status == "RUNNING"), 0, metadata.get("sequenceNumber", 0))
                observations.append((rank, {"source": "commands_json", "kind": "failed" if failed else "last_observed",
                                "status": status, "command": kind, "label": body.get("label", kind),
                                "target_id": target.get("idRegex") or target.get("childOf", {}).get("idRegex"),
                                "sequence": metadata.get("sequenceNumber"),
                                "artifact": str(path)}))
        except (OSError, ValueError, AttributeError, StopIteration):
            continue
    if observations:
        return max(observations, key=lambda item: item[0])[1]
    log = Path(directory) / "console.log"
    if log.exists():
        text = re.sub(r"\x1b\[[0-?]*[ -/]*[@-~]", "", log.read_text(encoding="utf-8", errors="replace"))
        steps = re.findall(r"^\s*(.+?)\s+(RUNNING|COMPLETED|FAILED|ERROR|SKIPPED)\s*$", text, re.M | re.I)
        if steps:
            label, status = steps[-1]
            return {"source": "console_log", "kind": "last_observed", "label": label.strip(),
                    "status": status.upper(), "artifact": str(log)}
    if stage:
        return {"source": "runner_stage", "kind": "host_stage", "stage": stage, "timeout_seconds": timeout}
    return None


class MaestroRunner:
    def __init__(self, root, adb, fixture, output, executable="maestro", release=False,
                 phone=False, test_origin=None, allow_production=False, record=False):
        self.root, self.adb, self.output = Path(root), adb, Path(output)
        self.connection = json.loads(Path(fixture).read_text(encoding="utf-8-sig"))
        if phone and (not release or adb.serial.startswith("emulator-") or test_origin is None):
            raise ValueError("正式包真机需要 --release、真机序列号与显式测试地址")
        if release and (not phone and not adb.serial.startswith("emulator-") or
                        urllib.parse.urlsplit(self.connection["origin"]).scheme != "https"):
            raise ValueError("发行包自动检查需要隔离模拟器与可信 HTTPS")
        self.app = APP.removesuffix(".fixture") if release else APP
        self.api = FixtureAPI(self.connection, trusted_https=release, test_origin=test_origin,
                              allow_production=allow_production)
        self.phone = phone
        self.record = record
        self.fail_marker = None
        self.on_app_launch = None
        self.on_app_launch_attempt = None
        self.on_test_file = None
        self.phone_isolation = None
        self.on_phone_access = None
        self.source_receipt = None
        self.on_source_verified = None
        self.batch_identity = None
        self.executable = executable
        self.catalog = catalog(self.root)
        self.remote_file = None
        self.channel_fixture = None
        self.conflict_fixture = None
        self.output.mkdir(parents=True, exist_ok=True)

    def failure_details(self, error, step=None):
        details = {"error_type": type(error).__name__, "error_message": str(error)}
        if step:
            details["preparation_step"] = step
        if isinstance(error, FixtureError):
            details.update(error.details())
        elif getattr(error, "code", None):
            details["error_code"] = error.code
        secrets = [self.connection.get(key) for key in ("username", "password", "sourceToken")]
        if isinstance(self.api.csrf, str):
            secrets.append(self.api.csrf)
        for key, value in details.items():
            if isinstance(value, str):
                for secret in secrets:
                    if secret:
                        value = value.replace(secret, "[redacted]")
                details[key] = value
        return details

    def prepare_data(self, flow, entry, note_name=None):
        marker = uuid.uuid4().hex
        values = {
            "APP_ID": self.app, "SCREENSHOT_PREFIX": flow,
            # A non-ASCII suffix makes the pinned CLI commit text through its own IME.
            # The flow removes the suffix immediately; emulators retain ordinary ASCII input.
            "INPUT_SUFFIX": "." if self.adb.serial.startswith("emulator-") else UNICODE_INPUT_SUFFIX,
            "NOTE_TEXT": f"Maestro {flow} {marker}", "PEER_NOTE_TEXT": f"Maestro peer {marker}",
            "PEER_EDITED_TEXT": f"Maestro cancelled {marker}",
            "EDITED_TEXT": f"Maestro edited {marker}", "DRAFT_TEXT": f"Maestro draft {marker}",
            "NEW_NOTE_TEXT": f"Maestro created {marker}", "CHAT_TITLE": f"Maestro chat {marker}",
            "ATTACHMENT_NOTE_TEXT": f"Maestro file {marker}",
            "ATTACHMENT_NAME": f"moerise-maestro-{marker}.txt",
        }
        values["QUERY_TEXT"] = values["NOTE_TEXT"]
        if entry.get("seed_notes"):
            if note_name:
                # Compatibility short flows can explicitly target a test note.
                note = self.api.note(note_name)
                values["NOTE_TEXT"] = values["QUERY_TEXT"] = note["content"]
            else:
                note = self.api.create(values["NOTE_TEXT"], "maestro-seed-" + marker)
            peer = self.api.create(values["PEER_NOTE_TEXT"], "maestro-peer-" + marker)
            values.update(NOTE_NAME=note["name"], PEER_NOTE_NAME=peer["name"])
        if flow == "insights-disconnect":
            values.update(DISCONNECT_MARKER=self.fail_marker, FOLLOWUP_TEXT=f"Maestro followup {marker}")
        if flow == "attachments":
            data = f"Moerise attachment {marker}\n".encode()
            local = self.output / values["ATTACHMENT_NAME"]
            local.write_bytes(data)
            remote = "/sdcard/Download/" + values["ATTACHMENT_NAME"]
            self.remote_file = remote
            if self.on_test_file:
                self.on_test_file(remote)
            self.adb.run("push", str(local), remote)
            self.adb.run("shell", "am", "broadcast", "-a", "android.intent.action.MEDIA_SCANNER_SCAN_FILE",
                         "-d", "file://" + remote)
            values["ATTACHMENT_SHA256"] = hashlib.sha256(data).hexdigest()
            return values, remote
        return values, None

    def verify(self, flow, values):
        """Read the actual server result independently of UI disappearance."""
        observed = {}
        if flow == "editing":
            assert self.api.note(values["NOTE_NAME"])["content"] == values["EDITED_TEXT"], "edited_note_mismatch"
            assert self.api.note(values["PEER_NOTE_NAME"])["content"] == values["PEER_NOTE_TEXT"], "cancelled_edit_uploaded"
            matches = self.api.matching_notes(values["NEW_NOTE_TEXT"])
            assert len(matches) == 1, "created_note_count"
            observed.update(edited_note=True, cancelled_edit_not_uploaded=True, created_note_count=len(matches))
        elif flow == "release-smoke":
            matches = self.api.matching_notes(values["NEW_NOTE_TEXT"])
            assert len(matches) == 1, "created_note_count"
            assert self.api.note(matches[0]["name"])["content"] == values["NEW_NOTE_TEXT"], "created_note_content"
            observed["created_note_count"] = len(matches)
        elif flow in CHANNEL_FLOWS:
            response = self.api.request("GET", "/api/app/v1/settings")
            channel = response["settings"]["channel"]
            assert channel["kind"] == "qq", "android_review_destination"
            assert channel["qq_app_id"] == "100000019", "android_qq_app_id"
            assert response["secrets"]["qq_app_secret"]["configured"], "android_qq_secret_cleared"
            observed.update(qq_destination_saved=True, qq_credentials_retained=True)
        elif flow == "actions":
            for key in ("NOTE_NAME", "PEER_NOTE_NAME"):
                try:
                    note = self.api.note(values[key])
                except FixtureError as error:
                    if error.status != 404:
                        raise
                else:
                    assert note.get("state") == "DELETED", "deleted_note_still_present"
            observed["deleted_note_count"] = 2
        elif flow in ("archive",):
            assert self.api.note(values["NOTE_NAME"])["state"] == "NORMAL", "restored_note_not_normal"
            observed["restored_note"] = True
        elif flow == "attachments":
            matches = self.api.matching_notes(values["ATTACHMENT_NOTE_TEXT"])
            assert len(matches) == 1, "attachment_note_count"
            files = [file for file in matches[0].get("attachments", [])
                     if file["filename"] == values["ATTACHMENT_NAME"]]
            assert len(files) == 1, "attachment_count"
            content = self.api.request("GET", files[0]["content_url"], binary=True)
            assert hashlib.sha256(content).hexdigest() == values["ATTACHMENT_SHA256"], "attachment_bytes"
            observed.update(attachment_note_count=1, attachment_bytes_match=True)
        elif flow in ("insights", "insights-disconnect"):
            token, seen = "", set()
            matches = []
            while True:
                query = urllib.parse.urlencode({"page_size": 100, **({"page_token": token} if token else {})})
                page = self.api.request("GET", "/api/app/v1/chats?" + query)
                for summary in page["chats"]:
                    if summary["material_count"] != 1:
                        continue
                    identifier = summary["id"]
                    if not re.fullmatch(r"[a-f0-9-]{36}", identifier):
                        raise ValueError("隔离洞察会话 ID 无效")
                    chat = self.api.request("GET", "/api/app/v1/chats/" + identifier)
                    if [item["name"] for item in chat["materials"]] == [values["NOTE_NAME"]]:
                        matches.append(chat)
                token = page.get("next_page_token")
                if not token:
                    break
                if token in seen or len(seen) >= 100:
                    raise ValueError("隔离洞察分页无效")
                seen.add(token)
            assert len(matches) == 1, "insight_material_count"
            turns = matches[0]["turns"]
            if flow == "insights-disconnect":
                assert len(turns) == 2, "disconnect_turn_count"
                first, second = turns
                failed, completed = first["assistant"], second["assistant"]
                assert first["user"]["content"] == values["DISCONNECT_MARKER"], "disconnect_question"
                assert (failed["status"] == "failed" and failed["error_code"] == "LLM_PROTOCOL"
                        and failed["content"].strip()), "disconnect_partial_output"
                assert second["user"]["content"] == values["FOLLOWUP_TEXT"], "followup_question"
                assert completed["status"] == "completed" and completed["content"].strip(), "followup_incomplete"
                return {"failed_partial_reply": True, "completed_followup": True, "turn_count": 2}
            assert len(turns) == 1 and turns[0]["kind"] == "insight", "insight_generation_count"
            answer = turns[0]["assistant"]
            assert answer["status"] == "completed" and answer["content"], "insight_incomplete"
            assert self.api.note(answer["saved_name"])["content"] == answer["content"], "saved_insight_text"
            assert self.api.note(values["NOTE_NAME"])["content"] == values["NOTE_TEXT"], "insight_source_mismatch"
            observed.update(independent_chat_with_exact_material=True, source_text_matches=True, model_request_count=1,
                            saved_reply_matches=True)
        return observed

    def wait_verify(self, flow, values, timeout=30):
        deadline = time.monotonic() + timeout
        pending_assertions = {
            "edited_note_mismatch", "created_note_count", "deleted_note_still_present",
            "attachment_note_count", "attachment_count", "insight_material_count", "disconnect_turn_count",
        }
        while True:
            try:
                return self.verify(flow, values)
            except AssertionError as error:
                if str(error) not in pending_assertions or time.monotonic() >= deadline:
                    raise
                # Only read back pending results; never repeat a create, save or delete action.
                time.sleep(.5)

    @contextmanager
    def recording(self, directory):
        if not self.record:
            yield
            return
        if self.adb.run("shell", "pidof", "screenrecord", check=False).strip():
            raise RuntimeError("设备已有录像进程；先结束该录像或省略 --record")
        remote = "/sdcard/moerise-recording-" + uuid.uuid4().hex + ".mp4"
        command = [self.adb.executable, "-s", self.adb.serial, "shell", "screenrecord", "--time-limit", "180", remote]
        with (directory / "recording.log").open("wb") as log:
            process = subprocess.Popen(command, stdout=log, stderr=subprocess.STDOUT)
            try:
                yield
            finally:
                try:
                    if process.poll() is None:
                        try:
                            pid = self.adb.run("shell", "pidof", "screenrecord", check=False).strip()
                            if re.fullmatch(r"[0-9]+", pid):
                                self.adb.run("shell", "kill", "-2", pid)
                            process.wait(timeout=10)
                        finally:
                            if process.poll() is None:
                                process.kill()
                                process.wait(timeout=5)
                finally:
                    try:
                        self.adb.run("pull", remote, str(directory / "recording.mp4"), timeout=60)
                    finally:
                        self.adb.run("shell", "rm", "-f", remote)

    def run_flow(self, flow, note_name=None, prepared=None, deferred=False):
        if flow not in self.catalog["flows"]:
            raise ValueError("未知 Maestro 场景")
        entry = self.catalog["flows"][flow]
        directory = self.output / flow
        if directory.exists():
            directory = self.output / (flow + "-" + uuid.uuid4().hex[:8])
        directory.mkdir(parents=True, exist_ok=False)
        result = {"kind": "maestro", "flow": flow, "status": "harness_error",
                  "log": str(directory / "console.log"), "report": str(directory / "report.xml"),
                  "flow_sha256": flow_digest(self.root, flow),
                  "started_at": datetime.now(timezone.utc).isoformat()}
        before, remote, channel_fixture = None, None, None
        stage, deadline = "static_validation", 20
        self.remote_file = None
        try:
            validation = subprocess.run(
                [sys.executable, "-B", str(self.root / "scripts/android/maestro/validate.py")],
                capture_output=True, timeout=20)
            (directory / "validation.log").write_bytes(validation.stdout + validation.stderr)
            if validation.returncode:
                raise ValueError("Maestro 流程静态校验失败")
            if not deferred:
                self.api.login()
                self.restore_pending_channel()
            if flow in CHANNEL_FLOWS and not deferred:
                channel_fixture = self.prepare_channel()
            values, remote = prepared if prepared is not None else self.prepare_data(flow, entry, note_name)
            parameters = {key: value for key, value in values.items() if key != "ATTACHMENT_SHA256"}
            write_result(directory / "test-data.json", values)
            result["test_data_path"] = str(directory / "test-data.json")
            before = set(self.adb.run("forward", "--list").splitlines())
            result["drivers_preexisting"] = {package: bool(self.adb.run(
                "shell", "pm", "path", package, check=False).strip()) for package in DRIVERS}
            # Complete the first Activity launch before attaching the instrumentation driver.
            stage, deadline = "activity_launch", 60
            self.start_app()
            environment = dict(os.environ)
            environment.update({
                "MAESTRO_FIXTURE_ORIGIN": self.connection["origin"],
                "MAESTRO_FIXTURE_USERNAME": self.connection["username"],
                "MAESTRO_FIXTURE_PASSWORD": self.connection["password"],
                "MAESTRO_CLI_NO_ANALYTICS": "true", "MAESTRO_DISABLE_UPDATE_CHECK": "true"})
            stage, deadline = "runtime_validation", 20
            runtime = subprocess.run(
                [sys.executable, "-B", str(self.root / "scripts/android/maestro/validate.py"),
                 "--flow", flow, "--parameters-json", str(directory / "test-data.json")],
                capture_output=True, timeout=20, env=environment)
            (directory / "runtime-validation.log").write_bytes(runtime.stdout + runtime.stderr)
            if runtime.returncode:
                raise ValueError("Maestro 场景参数校验失败")
            args = [self.executable, "--udid", self.adb.serial, "test", "--no-reinstall-driver",
                    "--format", "JUNIT", "--output", result["report"],
                    "--test-output-dir", str(directory / "artifacts"),
                    "--debug-output", str(directory / "artifacts")]
            for key, value in parameters.items():
                args.extend(["-e", key + "=" + value])
            args.append(str(self.root / "scripts/android/maestro" / entry["file"]))
            stage, deadline = "maestro_execution", entry["timeout_seconds"]
            ime = input_method_checkpoint(self.root, self.adb.serial) if not self.adb.serial.startswith("emulator-") else None
            with preserve_input_method(self.adb, ime), self.recording(directory):
                code = execute(args, result["log"], entry["timeout_seconds"], environment)
            result.update(junit_result(result["report"], code))
            result["ui_status"] = result["status"]
            if not deferred:
                self.verify_result(flow, values, result)
        except subprocess.TimeoutExpired:
            result.update(status="inconclusive", cause="maestro_host_timeout")
        except KeyboardInterrupt:
            result.update(status="inconclusive", cause="interrupted")
            raise
        except FixtureError as error:
            result.update(status="environment_blocked", cause="fixture_request",
                          **self.failure_details(error))
        except AssertionError as error:
            result.update(status="failed", cause="server_assertion", assertion=str(error))
        except (OSError, ValueError, KeyError, RuntimeError) as error:
            details = self.failure_details(error)
            result.update(status="harness_error", cause=type(error).__name__,
                          harness_message=details["error_message"], **details)
        finally:
            result["cleanup_errors"] = []
            remote = remote or self.remote_file
            if remote:
                result["remote_test_file"] = remote
                try:
                    self.adb.run("shell", "rm", "-f", remote)
                except Exception:
                    result["cleanup_errors"].append("test_file_remove")
            if before is not None:
                try:
                    result["added_forwards"] = sorted(set(
                        self.adb.run("forward", "--list").splitlines()) - before)
                except Exception:
                    result["cleanup_errors"].append("forward_readback")
            if not deferred and (channel_fixture is not None or self.channel_fixture is not None):
                try:
                    self.restore_channel(channel_fixture or self.channel_fixture)
                except FixtureError as error:
                    result["channel_restore_error"] = {"http_status": error.status, "error_code": error.code}
                    result["cleanup_errors"].append("channel_settings_restore")
                except Exception:
                    result["cleanup_errors"].append("channel_settings_restore")
            if not deferred:
                try:
                    self.api.close()
                except Exception:
                    result["cleanup_errors"].append("host_session_logout")
            if before is not None:
                for package in reversed(DRIVERS):
                    try:
                        self.adb.run("shell", "am", "force-stop", package)
                    except Exception:
                        result["cleanup_errors"].append("driver_stop")
                for line in result.get("added_forwards", []):
                    parts = line.split()
                    if len(parts) == 3 and parts[0] == self.adb.serial:
                        try:
                            self.adb.run("forward", "--remove", parts[1])
                        except Exception:
                            result["cleanup_errors"].append("forward_remove")
                try:
                    fresh_ui_dump(self.adb, directory / "window.xml")
                except Exception as error:
                    result["collection_error"] = type(error).__name__
            result["screenshots"] = [str(path) for path in directory.rglob("*.png")]
            result["recordings"] = [str(path) for path in directory.rglob("*.mp4")]
            if result["cleanup_errors"] and result["status"] == "passed":
                result.update(status="harness_error", cause="cleanup_error")
            password = self.connection["password"]
            encoded_input = base64.urlsafe_b64encode((password + UNICODE_INPUT_SUFFIX).encode()).decode().rstrip("=")
            secrets = [password, self.connection.get("sourceToken"), encoded_input]
            redact(directory, secrets)
            result.setdefault("ui_status", result["status"])
            result["last_step"] = last_failed_step(directory, stage, deadline)
            serialized = json.dumps(result, ensure_ascii=False)
            for secret in secrets:
                if secret:
                    serialized = serialized.replace(secret, "[redacted]")
            result = json.loads(serialized)
            result["finished_at"] = datetime.now(timezone.utc).isoformat()
            write_result(directory / "result.json", result)
            if result.get("cause") == "interrupted":
                self.interrupted_result = result
        return result

    def draft_checkpoint(self):
        return self.source_checkpoint("drafts", self.adb.serial + "\0")

    def editor_checkpoint(self):
        path = self.draft_checkpoint()
        saved = json.loads(path.read_text(encoding="utf-8")) if path.exists() else {}
        return {"drafts": saved, "notes": []} if isinstance(saved, list) else saved

    def remember_drafts(self, values):
        saved = self.editor_checkpoint()
        for field, keys in (("drafts", ("DRAFT_TEXT", "NEW_NOTE_TEXT", "ATTACHMENT_NOTE_TEXT")),
                            ("notes", ("NOTE_NAME", "PEER_NOTE_NAME"))):
            saved[field] = sorted(set(saved.get(field, [])) | {values[key] for key in keys if key in values})
        write_result(self.draft_checkpoint(), saved)

    def recover_editor_state(self):
        try:
            return self.read_and_recover_editor()
        except (RuntimeError, OSError, ValueError, ET.ParseError) as error:
            return [{"status": "harness_error", "cause": "draft_ui_collection", "error_type": type(error).__name__}]

    def editor_nodes(self):
        xml = fresh_ui_dump(self.adb, self.output / "draft-window.xml")
        return {tag: node for node in ET.fromstring(xml).iter("node")
                if (tag := node.get("resource-id", "").split(":id/", 1)[-1])}

    def read_and_recover_editor(self):
        saved = self.editor_checkpoint()
        drafts, notes = saved.get("drafts", []), saved.get("notes", [])
        if (any(not re.fullmatch(r"Maestro (?:draft|created|file) [a-f0-9]{32}", text) for text in drafts)
                or any(not re.fullmatch(r"memos/[A-Za-z0-9_-]+", name) for name in notes)):
            raise ValueError("测试编辑态检查点无效")
        values = {"APP_ID": self.app, "SCREENSHOT_PREFIX": "editor-recovery",
                  "INPUT_SUFFIX": "." if self.adb.serial.startswith("emulator-") else UNICODE_INPUT_SUFFIX}
        nodes = self.editor_nodes()
        if not any(tag == "note_editor" or tag.startswith("note_editor_memos/") for tag in nodes):
            result = self.run_flow("editor-home", prepared=(values, None), deferred=True)
            if result["status"] != "passed":
                return [{**result, "cause": "editor_not_observed"}]
            nodes = self.editor_nodes()
        inline = [tag.removeprefix("note_editor_") for tag in nodes
                  if re.fullmatch(r"note_editor_memos/[A-Za-z0-9_-]+", tag)]
        if any(name not in notes for name in inline):
            return [{"status": "environment_blocked", "cause": "unknown_editor_preserved"}]
        if inline:
            name = inline[0]
            cancel = nodes.get("cancel_note_" + name)
            bounds = re.fullmatch(r"\[(\d+),(\d+)\]\[(\d+),(\d+)\]",
                                  cancel.get("bounds", "") if cancel is not None else "")
            if len(inline) != 1 or bounds is None or cancel.get("enabled") != "true":
                return [{"status": "environment_blocked", "cause": "test_editor_cancel_unavailable"}]
            left, top, right, bottom = map(int, bounds.groups())
            if left >= right or top >= bottom:
                return [{"status": "environment_blocked", "cause": "test_editor_cancel_unavailable"}]
            self.adb.run("shell", "input", "tap", str((left + right) // 2), str((top + bottom) // 2))
            nodes = self.editor_nodes()
            if "note_editor_" + name in nodes or "note_editor" not in nodes:
                return [{"status": "environment_blocked", "cause": "test_editor_cancel_unconfirmed"}]
        editor = nodes.get("note_editor")
        if editor is None:
            return [{"status": "environment_blocked", "cause": "editor_not_observed"}]
        text = editor.get("text", "") if "note_editor_empty" not in nodes else ""
        if text and text not in drafts:
            return [{"status": "environment_blocked", "cause": "unknown_draft_preserved"}]
        if text:
            if inline:
                return [{"status": "environment_blocked", "cause": "test_draft_after_editor_cancel"}]
            result = self.run_flow("recover-draft", prepared=({**values, "DRAFT_TEXT": text}, None), deferred=True)
            if result["status"] != "passed":
                return [result]
            nodes = self.editor_nodes()
            if "note_editor" not in nodes or ("note_editor_empty" not in nodes and nodes["note_editor"].get("text")):
                return [{"status": "environment_blocked", "cause": "test_draft_clear_unconfirmed"}]
        self.draft_checkpoint().unlink(missing_ok=True)
        return [{"status": "passed"}] if text or inline else []

    def prepare_conflict(self):
        marker = uuid.uuid4().hex
        original = self.api.create("Maestro conflict " + marker, "maestro-conflict-" + marker)
        path = "/api/app/v1/notes/" + original["name"].split("/")[1]
        changed = self.api.request("PATCH", path, {"source_hash": original["source_hash"],
            "content": "Maestro remote conflict " + marker}, "maestro-conflict-update-" + marker)
        self.conflict_fixture = {"original": original, "changed": changed}
        write_result(self.output / "conflict.json", self.conflict_fixture)

    def verify_conflict(self):
        fixture = self.conflict_fixture
        if fixture is None:
            return {}
        original, changed = fixture["original"], fixture["changed"]
        path = "/api/app/v1/notes/" + original["name"].split("/")[1]
        try:
            self.api.request("PATCH", path, {"source_hash": original["source_hash"], "content": original["content"]},
                             "maestro-stale-" + uuid.uuid4().hex)
        except FixtureError as error:
            assert (error.status, error.code) == (409, "SOURCE_CHANGED"), "stale_source_error"
        else:
            raise AssertionError("stale_source_accepted")
        assert self.api.note(changed["name"])["content"] == changed["content"], "conflict_overwrote_remote"
        return {"stale_source_rejected": True, "remote_content_preserved": True, "fixture": str(self.output / "conflict.json")}

    def verify_result(self, flow, values, result):
        if flow not in SERVER_FLOWS:
            result["server_verification"] = "not_applicable"
            return
        try:
            if result["ui_status"] == "passed":
                observed = self.wait_verify(flow, values)
                if flow == "editing" and self.conflict_fixture:
                    observed["conflict"] = self.verify_conflict()
                result["server_assertions"] = observed
            else:
                result["server_observation"] = {"status": "passed", "assertions": self.verify(flow, values)}
            result["server_verification"] = "passed"
        except (AssertionError, FixtureError, ValueError, KeyError, TypeError) as error:
            if isinstance(error, AssertionError):
                status, case, details = "failed", "server_assertion", {"assertion": str(error)}
            elif isinstance(error, FixtureError):
                status, case, details = "environment_blocked", "fixture_request", self.failure_details(error)
            else:
                status, case, details = "harness_error", "server_response", self.failure_details(error)
            observed = {"failed": "not_satisfied", "environment_blocked": "environment_blocked",
                        "harness_error": "environment_blocked"}[status]
            result["server_observation"] = {"status": observed, **details}
            result["server_verification"] = observed if observed != "not_satisfied" else "failed"
            if result["status"] == "passed":
                result.update(status=status, cause=case, **details)

    def restore_pending_channel(self):
        path = self.channel_checkpoint()
        if path.exists():
            self.channel_fixture = json.loads(path.read_text(encoding="utf-8"))
            self.restore_channel(self.channel_fixture)

    def login_batch(self, on_source_blocked=None):
        if self.batch_identity is None:
            try:
                self.batch_identity = self.api.login() or {}
            except FixtureError:
                if not on_source_blocked or not on_source_blocked():
                    raise
                self.batch_identity = self.api.login() or {}
        return self.batch_identity

    def run_batch(self, flows, on_result=None, select=None, clean_editor=None, on_source_blocked=None):
        results = {}
        self.batch_cleanup_errors = []
        self.batch_identity = None
        step = "session_login"
        phone_ready, editor_error = False, None

        def checkpoint(flow, result):
            result.setdefault("ui_status", result["status"])
            result.setdefault("server_verification", "pending" if flow in SERVER_FLOWS else "not_applicable")
            result.setdefault("manual_status", "not_applicable")
            results[flow] = result
            directory = Path(result["log"]).parent if result.get("log") else self.output / flow
            if result["status"] not in ("reused_pass", "reused_baseline"):
                write_result(directory / "result.json", result)
            return on_result(flow, result) if on_result else False

        try:
            if not self.phone or self.channel_checkpoint().exists():
                self.login_batch(on_source_blocked)
                step = "channel_recovery"
                self.restore_pending_channel()
            for flow in flows:
                step = "session_login"
                result = select(flow, self) if select else None
                if result is None and self.phone and not phone_ready:
                    if self.on_phone_access:
                        step = "phone_access"
                        self.on_phone_access()
                    step = "session_login"
                    identity = self.login_batch(on_source_blocked)
                    for attempt in range(2):
                        step = "phone_offline_identity"
                        preflight = self.probe_phone_offline(self.resume_values(identity))
                        if preflight["status"] == "passed":
                            if preflight.get("resumed"):
                                self.start_app()
                            else:
                                step = "test_source_data"
                                probe = self.prepare_data("test-source", self.catalog["flows"]["test-source"])
                                probe[0]["EXPECTED_ACCOUNT"] = self.display_account(identity)
                                step = "test_source_ui"
                                preflight = self.run_flow("test-source", prepared=probe, deferred=True)
                                write_result(self.output / "test-source-result.json", preflight)
                                if preflight["status"] == "passed" and self.on_source_verified:
                                    self.on_source_verified({**self.source_identity(identity),
                                        "note_name": probe[0]["NOTE_NAME"], "note_text": probe[0]["NOTE_TEXT"]})
                        if preflight["status"] == "passed":
                            break
                        if attempt or not on_source_blocked or not on_source_blocked():
                            return self.block_unverified_phone((name for name in flows if name not in results),
                                                               results, checkpoint, preflight, step)
                    phone_ready = True
                    recovery = self.recover_editor_state()
                    editor_error = next((item for item in recovery if item["status"] != "passed"), None)
                    if recovery:
                        write_result(self.output / "editor-recovery.json", recovery)
                requires_clean = clean_editor is None or flow in clean_editor
                if result is None and editor_error and requires_clean:
                    result = {"status": "skipped", "ui_status": "not_run", "cause": editor_error["cause"],
                              "editor_recovery": editor_error}
                if result is not None:
                    if checkpoint(flow, result):
                        break
                    continue
                step = flow + ".data_preparation"
                values, remote = None, None
                self.remote_file = None
                self.conflict_fixture = None
                cleanup_errors = []
                try:
                    if flow in CHANNEL_FLOWS:
                        step = "channel_preparation"
                        self.prepare_channel()
                    if flow == "editing":
                        step = "conflict_preparation"
                        self.prepare_conflict()
                    step = flow + ".data_preparation"
                    values, remote = self.prepare_data(flow, self.catalog["flows"][flow])
                    if self.phone:
                        self.remember_drafts(values)
                    step = flow + ".ui"
                    result = self.run_flow(flow, prepared=(values, remote), deferred=True)
                    self.remote_file = None
                    remote = None
                    result.setdefault("ui_status", result["status"])
                    if self.phone and not editor_error:
                        recovery = self.recover_editor_state()
                        editor_error = next((item for item in recovery if item["status"] != "passed"), None)
                        if editor_error:
                            result["editor_recovery"] = editor_error
                            if result["status"] == "passed":
                                result.update(status="environment_blocked", cause=editor_error["cause"])
                    self.verify_result(flow, values, result)
                except (FixtureError, OSError, ValueError, RuntimeError, KeyError) as error:
                    result = {"status": "environment_blocked" if isinstance(error, FixtureError) else "harness_error",
                              "ui_status": "not_run", "cause": step.split(".")[-1], **self.failure_details(error, step)}
                except KeyboardInterrupt:
                    partial = getattr(self, "interrupted_result", None)
                    checkpoint(flow, {**(partial or {"status": "inconclusive", "ui_status": "not_run",
                                                    "cause": "interrupted", "preparation_step": step}), "attempted": True})
                    raise
                finally:
                    if remote or self.remote_file:
                        try:
                            self.adb.run("shell", "rm", "-f", remote or self.remote_file)
                        except Exception:
                            cleanup_errors.append("test_file_remove")
                    if flow in CHANNEL_FLOWS and self.channel_fixture is not None:
                        try:
                            self.restore_channel(self.channel_fixture)
                        except Exception:
                            cleanup_errors.append("channel_settings_restore")
                result.setdefault("cleanup_errors", []).extend(cleanup_errors)
                if result["cleanup_errors"] and result["status"] == "passed":
                    result.update(status="harness_error", cause="cleanup_error")
                if checkpoint(flow, {**result, "attempted": True}):
                    break
                if "channel_settings_restore" in result["cleanup_errors"]:
                    break
        except (FixtureError, OSError, ValueError, RuntimeError, KeyError, subprocess.SubprocessError) as error:
            if step == "phone_access":
                raise
            blocked = isinstance(error, FixtureError) or (step == "phone_offline_identity" and getattr(error, "code", None))
            for flow in flows:
                if flow not in results:
                    checkpoint(flow, {"status": "environment_blocked" if blocked else "harness_error",
                                      "ui_status": "not_run", "cause": "batch_preparation", **self.failure_details(error, step)})
        finally:
            if self.channel_fixture is not None:
                try:
                    self.restore_channel(self.channel_fixture)
                except Exception:
                    self.batch_cleanup_errors.append("channel_settings_restore")
            if self.batch_identity is not None:
                try:
                    self.api.close()
                except Exception:
                    self.batch_cleanup_errors.append("host_session_logout")
            write_result(self.output / "results.json", results)
        return results

    def source_identity(self, identity):
        return {"origin": normalized_origin(self.connection["origin"]), "instance_id": identity.get("instance_id"),
                "user": identity.get("user", {}).get("name")}

    def display_account(self, identity):
        user = identity.get("user", {})
        return (user.get("display_name", "").strip() or user.get("name", "")).removeprefix("users/")

    def resume_values(self, identity):
        receipt = self.source_receipt or {}
        source = self.source_identity(identity)
        if (not source["instance_id"] or not source["user"]
                or any(receipt.get(key) != value for key, value in source.items())):
            return None
        return {"APP_ID": self.app, "SCREENSHOT_PREFIX": "test-source-resume", "INPUT_SUFFIX": UNICODE_INPUT_SUFFIX,
                "NOTE_NAME": receipt["note_name"], "NOTE_TEXT": receipt["note_text"], "QUERY_TEXT": receipt["note_text"],
                "EXPECTED_ACCOUNT": self.display_account(identity)}

    def probe_phone_offline(self, resume=None):
        if self.phone_isolation is None:
            raise ValueError("正式包真机需要离线账号探针")
        with self.phone_isolation():
            self.start_app()
            artifact = self.output / "source-offline-window.xml"
            try:
                def visible_tags():
                    xml = fresh_ui_dump(self.adb, artifact, stop_drivers=False)
                    return {node.get("resource-id", "").split(":id/", 1)[-1]
                            for node in ET.fromstring(xml).iter("node") if node.get("package") == self.app}
                tags = visible_tags()
                if "welcome_enter" in tags:
                    welcome = self.run_flow("test-source-welcome",
                        prepared=({"APP_ID": self.app, "SCREENSHOT_PREFIX": "test-source-welcome"}, None), deferred=True)
                    if welcome["status"] != "passed":
                        return welcome
                    tags = visible_tags()
                if "open_menu" in tags:
                    if resume:
                        result = self.run_flow("test-source-resume", prepared=(resume, None), deferred=True)
                        if result["status"] == "passed":
                            return {**result, "resumed": True}
                        return {**result, "cause": "cached_test_source_unverified"}
                    return {"status": "environment_blocked", "cause": "selected_account_present"}
                if {"server_login", "server_origin_empty"} <= tags and "welcome_enter" not in tags:
                    return {"status": "passed"}
            except (RuntimeError, OSError, ValueError, ET.ParseError):
                pass
            finally:
                artifact.unlink(missing_ok=True)
            return {"status": "environment_blocked", "cause": "empty_login_form_not_observed"}

    def start_app(self):
        attempted = datetime.now(timezone.utc).isoformat()
        if self.on_app_launch_attempt:
            self.on_app_launch_attempt(attempted)
        activity = self.app + "/io.github.fermin214.moerise.MainActivity"
        output = self.adb.run("shell", "am", "start", "-W", "-n", activity, timeout=60)
        if self.phone:
            shown = re.search(r"(?m)^Activity:\s+(\S+)\s*$", output)
            actual = shown[1].replace("/.", "/" + self.app + ".", 1) if shown else None
            if not re.search(r"(?m)^Status:\s+ok\s*$", output) or actual != activity:
                raise RuntimeError("未能确认目标 Activity 已启动")
        if self.on_app_launch:
            self.on_app_launch(attempted)

    def block_unverified_phone(self, flows, results, checkpoint, preflight, step):
        for flow in list(flows):
            checkpoint(flow, {**preflight, "status": "environment_blocked", "ui_status": "not_run",
                              "server_verification": "pending", "cause": "phone_test_source_unverified",
                              "preparation_step": step, "log": None, "probe_log": preflight.get("log"),
                              "probe_cause": preflight.get("cause")})
        return results

    def source_checkpoint(self, kind, prefix=""):
        raw = self.connection["origin"]
        canonical = normalized_origin(raw)
        default_port = 443 if canonical.startswith("https:") else 80
        explicit_default = f"{canonical}:{default_port}" if urllib.parse.urlsplit(canonical).port is None else canonical
        def filename(origin):
            key = hashlib.sha256((prefix + origin).encode()).hexdigest()[:16]
            return f"maestro-{kind}-{key}.json"
        aliases = {raw, raw.rstrip("/"), canonical + "/", explicit_default, explicit_default + "/"} - {canonical}
        return shared_checkpoint(self.root, filename(canonical), tuple(map(filename, aliases)))

    def channel_checkpoint(self):
        return self.source_checkpoint("channel")

    def prepare_channel(self):
        checkpoint = self.channel_checkpoint()
        self.restore_pending_channel()
        configuration = self.api.request("GET", "/api/app/v1/settings")
        channel = configuration["settings"]["channel"]
        if channel.get("qq_app_id") or configuration["secrets"]["qq_app_secret"]["configured"]:
            raise ValueError("导航场景需要未配置 QQ 的隔离服务")
        saved = {"channel": {"kind": channel["kind"], "qq_app_id": ""},
                 "notifications": {"enabled": configuration["settings"]["notifications"]["enabled"]}}
        self.channel_fixture = saved
        write_result(checkpoint, saved)
        self.api.request("PATCH", "/api/app/v1/settings", {
            "version": configuration["version"],
            "settings": {"channel": {"kind": "none", "qq_app_id": "100000019"}, "notifications": {"enabled": False}},
            "secrets": {"qq_app_secret": {"action": "replace", "value": uuid.uuid4().hex}},
        })
        return saved

    def restore_channel(self, saved):
        configuration = self.api.request("GET", "/api/app/v1/settings")
        if configuration["settings"]["channel"].get("qq_app_id") == "100000019":
            self.api.request("PATCH", "/api/app/v1/settings", {
                "version": configuration["version"], "settings": saved,
                "secrets": {"qq_app_secret": {"action": "clear"}},
            })

        self.channel_checkpoint().unlink(missing_ok=True)
        self.channel_fixture = None

    def run_group(self, group):
        flows = self.catalog["groups"][group]
        results = {}
        for flow in flows:
            results[flow] = self.run_flow(flow)
            if results[flow]["status"] in ("environment_blocked", "harness_error", "inconclusive"):
                break
        write_result(self.output / "results.json", results)
        return results
