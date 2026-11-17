"""Shared Maestro execution for persistent phones and isolated CI devices."""
from datetime import datetime, timezone
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
    def __init__(self, connection):
        self.connection = connection
        origin = urllib.parse.urlsplit(connection["origin"])
        if (origin.scheme != "http" or origin.hostname not in ("127.0.0.1", "localhost", "::1")
                or origin.path not in ("", "/") or origin.query or origin.fragment
                or origin.username or origin.password or not origin.port
                or connection.get("username") != "android-owner" or not connection.get("password")):
            raise ValueError("Maestro 仅连接 HTTP 回环隔离服务与 android-owner 测试账号")
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
                if error.code in (429, 503) and attempt < 2 and (method == "GET" or key):
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

    def notes(self):
        token, seen, notes = "", set(), []
        for _ in range(100):
            query = urllib.parse.urlencode({"state": "NORMAL", "page_size": 100,
                                            **({"page_token": token} if token else {})})
            page = self.request("GET", "/api/app/v1/notes?" + query)
            notes.extend(page["notes"])
            token = page.get("next_page_token", "")
            if not token:
                return notes
            if token in seen:
                raise ValueError("隔离笔记分页游标重复")
            seen.add(token)
        raise ValueError("隔离笔记分页超过上限")


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
    def __init__(self, root, adb, fixture, output, executable="maestro"):
        self.root, self.adb, self.output = Path(root), adb, Path(output)
        self.connection = json.loads(Path(fixture).read_text(encoding="utf-8-sig"))
        self.api = FixtureAPI(self.connection)
        self.executable = executable
        self.catalog = catalog(self.root)
        self.remote_file = None
        self.output.mkdir(parents=True, exist_ok=True)

    def prepare_data(self, flow, entry, note_name=None):
        marker = uuid.uuid4().hex
        values = {
            "APP_ID": APP, "SCREENSHOT_PREFIX": flow,
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
            matches = [note for note in self.api.notes() if note["content"] == values["NEW_NOTE_TEXT"]]
            assert len(matches) == 1, "created_note_count"
            observed.update(edited_note=True, cancelled_edit_not_uploaded=True, created_note_count=len(matches))
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
            matches = [note for note in self.api.notes() if note["content"] == values["ATTACHMENT_NOTE_TEXT"]]
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
            assert not matches[0]["turns"], "unexpected_model_request"
            observed.update(independent_chat_with_exact_material=True, model_request_count=0)
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

    def run_flow(self, flow, note_name=None):
        if flow not in self.catalog["flows"]:
            raise ValueError("未知 Maestro 场景")
        entry = self.catalog["flows"][flow]
        directory = self.output / flow
        directory.mkdir(parents=True, exist_ok=False)
        result = {"kind": "maestro", "flow": flow, "status": "harness_error",
                  "log": str(directory / "console.log"), "report": str(directory / "report.xml"),
                  "flow_sha256": flow_digest(self.root, flow),
                  "started_at": datetime.now(timezone.utc).isoformat()}
        before, remote = None, None
        self.remote_file = None
        try:
            validation = subprocess.run(
                [sys.executable, "-B", str(self.root / "scripts/android/maestro/validate.py")],
                capture_output=True, timeout=20)
            (directory / "validation.log").write_bytes(validation.stdout + validation.stderr)
            if validation.returncode:
                raise ValueError("Maestro 流程静态校验失败")
            self.api.login()
            values, remote = self.prepare_data(flow, entry, note_name)
            parameters = {key: value for key, value in values.items() if key != "ATTACHMENT_SHA256"}
            write_result(directory / "test-data.json", parameters)
            before = set(self.adb.run("forward", "--list").splitlines())
            result["drivers_preexisting"] = {package: bool(self.adb.run(
                "shell", "pm", "path", package, check=False).strip()) for package in DRIVERS}
            # Complete the first Activity launch before attaching the instrumentation driver.
            self.adb.run("shell", "am", "start", "-W", "-n", ACTIVITY, timeout=60)
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
            code = execute(args, result["log"], entry["timeout_seconds"], environment)
            result.update(junit_result(result["report"], code))
            if result["status"] == "passed":
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
            try:
                self.api.close()
            except Exception:
                result["cleanup_errors"].append("host_session_logout")
            if result["cleanup_errors"] and result["status"] == "passed":
                result.update(status="harness_error", cause="cleanup_error")
            redact(directory, [self.connection["password"], self.connection.get("sourceToken")])
            result["last_step"] = last_failed_step(directory)
            serialized = json.dumps(result, ensure_ascii=False)
            for secret in (self.connection["password"], self.connection.get("sourceToken")):
                if secret:
                    serialized = serialized.replace(secret, "[redacted]")
            result = json.loads(serialized)
            result["finished_at"] = datetime.now(timezone.utc).isoformat()
            write_result(directory / "result.json", result)
        return result

    def run_group(self, group):
        flows = self.catalog["groups"][group]
        results = {}
        for flow in flows:
            results[flow] = self.run_flow(flow)
            if results[flow]["status"] in ("environment_blocked", "harness_error", "inconclusive"):
                break
        write_result(self.output / "results.json", results)
        return results
