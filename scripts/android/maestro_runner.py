"""Shared Maestro execution for persistent phones and isolated CI devices."""
from contextlib import contextmanager
from datetime import datetime, timezone
import base64
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[2]
APP = "io.github.fermin214.moerise.fixture"
ACTIVITY = APP + "/io.github.fermin214.moerise.MainActivity"
DRIVERS = ("dev.mobile.maestro", "dev.mobile.maestro.test")
MAESTRO_IME = DRIVERS[0] + "/.input.MaestroInputMethodService"
UNICODE_INPUT_SUFFIX = "。"


def catalog(root=ROOT):
    return json.loads((Path(root) / "scripts/android/maestro/catalog.json").read_text(encoding="utf-8"))


def write_result(path, value):
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    Path(path).write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def flow_digest(root, flow):
    """Include shared subflows and policy in the identity used for pass reuse."""
    digest = hashlib.sha256(flow.encode())
    for path in sorted((Path(root) / "scripts/android/maestro").rglob("*")):
        if path.is_file() and path.suffix in (".yaml", ".json", ".py"):
            digest.update(path.relative_to(root).as_posix().encode() + b"\0")
            digest.update(path.read_bytes())
    return digest.hexdigest()


class FixtureError(RuntimeError):
    def __init__(self, status, code):
        super().__init__(f"隔离服务请求失败：HTTP {status} / {code}")
        self.status, self.code = status, code


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
        headers = {"Origin": self.origin}
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
                    return data if binary else (json.loads(data) if data else None)
            except urllib.error.HTTPError as error:
                code = "request_failed"
                try:
                    code = json.loads(error.read()).get("error", {}).get("code", code)
                except (ValueError, AttributeError):
                    pass
                settings_busy = (method == "PATCH" and path == "/api/app/v1/settings"
                                 and error.code == 503 and code == "SETTINGS_BUSY")
                if error.code in (429, 503) and attempt < 2 and (method == "GET" or key or settings_busy):
                    retry_after = error.headers.get("Retry-After", "1")
                    time.sleep(min(5, max(1, int(retry_after) if retry_after.isdigit() else 1)))
                    continue
                raise FixtureError(error.code, code) from None
            except (OSError, urllib.error.URLError):
                raise FixtureError(0, "unreachable") from None

    def login(self):
        result = self.request("POST", "/api/app/v1/session", {
            key: self.connection[key] for key in ("username", "password")})
        self.csrf = result["csrf_token"]

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


def fresh_ui_dump(adb, output, timeout=15):
    output = Path(output)
    output.unlink(missing_ok=True)
    remote = "/sdcard/moerise-ui-" + uuid.uuid4().hex + ".xml"
    try:
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
        except subprocess.TimeoutExpired:
            if os.name == "nt":
                subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
            else:
                os.killpg(process.pid, signal.SIGKILL)
            if process.poll() is None:
                process.kill()
            process.wait(timeout=5)
            raise


def active_input_method(adb):
    return adb.run("shell", "settings", "get", "secure", "default_input_method").strip()


def input_method_checkpoint(root, serial):
    key = hashlib.sha256(serial.encode()).hexdigest()[:16]
    return Path(root) / ".local/android" / ("maestro-ime-" + key + ".json")


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


def last_failed_step(directory):
    failed = []
    for path in Path(directory).rglob("commands.json"):
        try:
            commands = json.loads(path.read_text(encoding="utf-8"))
            for command in commands:
                metadata = command.get("metadata", {})
                if metadata.get("status") not in ("FAILED", "ERROR"):
                    continue
                evaluated = metadata.get("evaluatedCommand") or command.get("command", {})
                kind, body = next(iter(evaluated.items()))
                target = body.get("selector", {})
                if kind == "assertConditionCommand":
                    condition = body.get("condition", {})
                    target = condition.get("visible") or condition.get("notVisible") or {}
                failed.append((metadata.get("depth", 0), metadata.get("sequenceNumber", 0),
                               {"command": kind, "label": body.get("label", kind),
                                "target_id": target.get("idRegex") or target.get("childOf", {}).get("idRegex"),
                                "sequence": metadata.get("sequenceNumber"),
                                "artifact": str(path)}))
        except (OSError, ValueError, AttributeError, StopIteration):
            continue
    return max(failed, key=lambda item: item[:2])[2] if failed else None


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
        self.batch_preparing = False
        self.executable = executable
        self.catalog = catalog(self.root)
        self.remote_file = None
        self.channel_fixture = None
        self.conflict_fixture = None
        self.output.mkdir(parents=True, exist_ok=True)

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
        if self.phone and self.batch_preparing:
            self.remember_drafts(values)
        if flow == "attachments":
            data = f"Moerise attachment {marker}\n".encode()
            local = self.output / values["ATTACHMENT_NAME"]
            local.write_bytes(data)
            remote = "/sdcard/Download/" + values["ATTACHMENT_NAME"]
            self.remote_file = remote
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
        elif flow == "navigation":
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
        elif flow == "insights":
            page = self.api.request("GET", "/api/app/v1/chats?page_size=100")
            matches = []
            for summary in page["chats"]:
                if summary["material_count"] != 1:
                    continue
                identifier = summary["id"]
                if not re.fullmatch(r"[a-f0-9-]{36}", identifier):
                    raise ValueError("隔离洞察会话 ID 无效")
                chat = self.api.request("GET", "/api/app/v1/chats/" + identifier)
                if [item["name"] for item in chat["materials"]] == [values["NOTE_NAME"]]:
                    matches.append(chat)
            assert len(matches) == 1, "insight_material_count"
            turns = matches[0]["turns"]
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
            "attachment_note_count", "attachment_count", "insight_material_count",
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
            if flow == "navigation" and not deferred:
                channel_fixture = self.prepare_channel()
            values, remote = prepared if prepared is not None else self.prepare_data(flow, entry, note_name)
            parameters = {key: value for key, value in values.items() if key != "ATTACHMENT_SHA256"}
            write_result(directory / "test-data.json", parameters)
            before = set(self.adb.run("forward", "--list").splitlines())
            result["drivers_preexisting"] = {package: bool(self.adb.run(
                "shell", "pm", "path", package, check=False).strip()) for package in DRIVERS}
            # Complete the first Activity launch before attaching the instrumentation driver.
            self.adb.run("shell", "am", "start", "-W", "-n",
                         self.app + "/io.github.fermin214.moerise.MainActivity", timeout=60)
            environment = dict(os.environ)
            environment.update({
                "MAESTRO_FIXTURE_ORIGIN": self.connection["origin"],
                "MAESTRO_FIXTURE_USERNAME": self.connection["username"],
                "MAESTRO_FIXTURE_PASSWORD": self.connection["password"],
                "MAESTRO_CLI_NO_ANALYTICS": "true", "MAESTRO_DISABLE_UPDATE_CHECK": "true"})
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
            with preserve_input_method(self.adb, input_method_checkpoint(self.root, self.adb.serial)), self.recording(directory):
                code = execute(args, result["log"], entry["timeout_seconds"], environment)
            result.update(junit_result(result["report"], code))
            if deferred:
                pass
            elif result["status"] == "passed":
                result["server_assertions"] = self.wait_verify(flow, values)
            elif flow in ("editing", "actions", "archive", "attachments", "insights"):
                try:
                    observed = self.verify(flow, values)
                    result["server_observation"] = {"status": "passed", "assertions": observed}
                except AssertionError as error:
                    result["server_observation"] = {"status": "not_satisfied", "assertion": str(error)}
                except FixtureError as error:
                    result["server_observation"] = {"status": "environment_blocked",
                                                    "http_status": error.status, "error_code": error.code}
        except subprocess.TimeoutExpired:
            result.update(status="inconclusive", cause="maestro_host_timeout")
        except FixtureError as error:
            result.update(status="environment_blocked", cause="fixture_request",
                          http_status=error.status, error_code=error.code)
        except AssertionError as error:
            result.update(status="failed", cause="server_assertion", assertion=str(error))
        except (OSError, ValueError, KeyError, RuntimeError) as error:
            result.update(status="harness_error", cause=type(error).__name__, harness_message=str(error))
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
            result["last_step"] = last_failed_step(directory)
            serialized = json.dumps(result, ensure_ascii=False)
            for secret in secrets:
                if secret:
                    serialized = serialized.replace(secret, "[redacted]")
            result = json.loads(serialized)
            result["finished_at"] = datetime.now(timezone.utc).isoformat()
            write_result(directory / "result.json", result)
        return result

    def draft_checkpoint(self):
        identity = hashlib.sha256((self.adb.serial + "\0" + self.connection["origin"]).encode()).hexdigest()[:16]
        return self.root / ".local/android" / ("maestro-drafts-" + identity + ".json")

    def remember_drafts(self, values):
        path = self.draft_checkpoint()
        saved = json.loads(path.read_text(encoding="utf-8")) if path.exists() else []
        for key in ("DRAFT_TEXT", "NEW_NOTE_TEXT", "ATTACHMENT_NOTE_TEXT"):
            if values[key] not in saved:
                saved.append(values[key])
        write_result(path, saved)

    def recover_drafts(self):
        try:
            return self.read_and_recover_drafts()
        except (RuntimeError, OSError, ValueError, ET.ParseError) as error:
            return [{"status": "harness_error", "cause": "draft_ui_collection", "error_type": type(error).__name__}]

    def read_and_recover_drafts(self):
        path = self.draft_checkpoint()
        if not path.exists():
            return []
        saved = json.loads(path.read_text(encoding="utf-8"))
        if not saved:
            path.unlink()
            return []
        if any(not re.fullmatch(r"Maestro (?:draft|created|file) [a-f0-9]{32}", text) for text in saved):
            raise ValueError("测试草稿检查点无效")
        values = {"APP_ID": self.app, "SCREENSHOT_PREFIX": "recover-draft", "DRAFT_TEXT": saved[0],
                  "INPUT_SUFFIX": "." if self.adb.serial.startswith("emulator-") else UNICODE_INPUT_SUFFIX}
        self.adb.run("shell", "am", "force-stop", self.app)
        result = self.run_flow("recover-draft", prepared=(values, None), deferred=True)
        if result["status"] != "passed":
            return [result]
        xml = fresh_ui_dump(self.adb, self.output / "draft-window.xml")
        editors = [node for node in ET.fromstring(xml).iter("node")
                   if node.get("resource-id", "").rsplit(":id/", 1)[-1] == "note_editor"]
        if len(editors) != 1:
            return [{"status": "harness_error", "cause": "composer_not_observed"}]
        text = editors[0].get("text", "")
        if text:
            if text not in saved:
                return [{"status": "environment_blocked", "cause": "unknown_draft_preserved"}]
            values["DRAFT_TEXT"] = text
            result = self.run_flow("recover-draft", prepared=(values, None), deferred=True)
            if result["status"] != "passed":
                return [result]
        path.unlink()
        return [result]

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

    def run_batch(self, flows, on_result=None):
        prepared, results, channel_fixture, cleanup_errors = {}, {}, None, []
        try:
            self.api.login()
            if self.phone and flows:
                probe = self.prepare_data("test-source", self.catalog["flows"]["test-source"])
                preflight = self.run_flow("test-source", prepared=probe, deferred=True)
                write_result(self.output / "test-source-result.json", preflight)
                if preflight["status"] != "passed":
                    for flow in flows:
                        results[flow] = {**preflight, "status": "environment_blocked", "cause": "phone_test_source_unverified"}
                    return results
            if self.phone:
                recovery = self.recover_drafts()
                if any(item["status"] != "passed" for item in recovery):
                    for flow in flows:
                        results[flow] = {"status": "environment_blocked", "cause": "test_draft_recovery", "recovery": recovery}
                    return results
            if "navigation" in flows:
                try:
                    channel_fixture = self.prepare_channel()
                except (FixtureError, ValueError, KeyError) as error:
                    results["navigation"] = {"status": "environment_blocked" if isinstance(error, FixtureError) else "harness_error",
                                             "cause": "channel_preparation", "error_type": type(error).__name__}
            if "editing" in flows:
                try:
                    self.prepare_conflict()
                except (FixtureError, ValueError, KeyError) as error:
                    results["editing"] = {"status": "environment_blocked" if isinstance(error, FixtureError) else "harness_error",
                                          "cause": "conflict_preparation", "error_type": type(error).__name__}
            self.batch_preparing = True
            for flow in flows:
                if flow in results:
                    continue
                try:
                    prepared[flow] = self.prepare_data(flow, self.catalog["flows"][flow])
                except (FixtureError, OSError, ValueError, RuntimeError) as error:
                    results[flow] = {"status": "environment_blocked" if isinstance(error, FixtureError) else "harness_error",
                                     "cause": "data_preparation", "error_type": type(error).__name__}
            self.batch_preparing = False
            for flow in flows:
                if flow not in results:
                    if self.phone:
                        self.remember_drafts(prepared[flow][0])
                        self.adb.run("shell", "am", "force-stop", self.app)
                    results[flow] = self.run_flow(flow, prepared=prepared[flow], deferred=True)
                    if self.phone:
                        recovery = self.recover_drafts()
                        if any(item["status"] != "passed" for item in recovery):
                            results[flow]["draft_recovery"] = recovery
                            if results[flow]["status"] == "passed":
                                results[flow].update(status="inconclusive", cause="test_draft_recovery")
                if on_result:
                    on_result(flow, results[flow])
            for flow, result in results.items():
                if flow not in prepared:
                    continue
                try:
                    observed = self.wait_verify(flow, prepared[flow][0])
                    if flow == "editing":
                        observed["conflict"] = self.verify_conflict()
                    result["server_assertions"] = observed
                except AssertionError as error:
                    result["server_observation"] = {"status": "not_satisfied", "assertion": str(error)}
                    if result["status"] == "passed":
                        result.update(status="failed", cause="server_assertion", assertion=str(error))
                except FixtureError as error:
                    result["server_observation"] = {"status": "environment_blocked", "http_status": error.status}
                    if result["status"] == "passed":
                        result.update(status="environment_blocked", cause="fixture_request", http_status=error.status)
                except (ValueError, KeyError, TypeError) as error:
                    result.update(status="harness_error", cause="server_response", error_type=type(error).__name__)
        except (FixtureError, OSError, ValueError, RuntimeError) as error:
            for flow in flows:
                results.setdefault(flow, {"status": "environment_blocked" if isinstance(error, FixtureError) else "harness_error",
                                          "cause": "batch_preparation", "error_type": type(error).__name__})
        finally:
            self.batch_preparing = False
            for values, remote in prepared.values():
                if remote:
                    try:
                        self.adb.run("shell", "rm", "-f", remote)
                    except Exception:
                        cleanup_errors.append("test_file_remove")
            if channel_fixture is not None or self.channel_fixture is not None:
                try:
                    self.restore_channel(channel_fixture or self.channel_fixture)
                except Exception:
                    cleanup_errors.append("channel_settings_restore")
            try:
                self.api.close()
            except Exception:
                cleanup_errors.append("host_session_logout")
            for flow, result in results.items():
                result.setdefault("cleanup_errors", []).extend(cleanup_errors)
                if result["cleanup_errors"] and result["status"] == "passed":
                    result.update(status="harness_error", cause="cleanup_error")
                write_result(self.output / flow / "result.json", result)
                if on_result:
                    on_result(flow, result)
            write_result(self.output / "results.json", results)
        return results

    def channel_checkpoint(self):
        identity = hashlib.sha256(self.connection["origin"].encode()).hexdigest()[:16]
        return self.root / ".local/android" / ("maestro-channel-" + identity + ".json")

    def prepare_channel(self):
        checkpoint = self.channel_checkpoint()
        if checkpoint.exists():
            self.channel_fixture = json.loads(checkpoint.read_text(encoding="utf-8"))
            self.restore_channel(self.channel_fixture)
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
