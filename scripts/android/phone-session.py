#!/usr/bin/env python3
"""Persistent USB device acceptance; installation, checks and cleanup are separate actions."""
import argparse
from collections import Counter
from contextlib import contextmanager
import csv
from datetime import datetime, timezone
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.parse
import urllib.error
import urllib.request
import uuid
from adb_common import Adb, CommandError, NETWORK_KEYS, network_settings, proxy_enabled, reverse_conflict
from maestro_runner import (
    DRIVERS, SERVER_FLOWS, MaestroRunner, catalog, common_repo, flow_digest, git, input_method_checkpoint, worktree_roots,
    junit_result as maestro_result, restore_input_method, FixtureAPI, FixtureError,
)

APP = "io.github.fermin214.moerise.fixture"
RUNNER_APP = APP + ".test"
RUNNER = RUNNER_APP + "/androidx.test.runner.AndroidJUnitRunner"
ACTIVITY = APP + "/io.github.fermin214.moerise.MainActivity"
CREDENTIAL = "/data/local/tmp/moerise-android-fixture.json"
APK_PATHS = {
    APP: "mobile/androidApp/build/outputs/apk/fixture/androidApp-fixture.apk",
    RUNNER_APP: "mobile/androidApp/build/outputs/apk/androidTest/fixture/androidApp-fixture-androidTest.apk",
}
CASES = {
    "lifecycle": "QuickNoteTest#loginWriteRecreateSaveAndReadBack",
    "ime": "ImeLifecycleTest#composingCommitCursorEditsAndDraftSurviveRecreation",
    "stars": "StarRenderingTest#thousandAndFiveThousandStarsRenderSelectPanZoomAndRefresh",
}
UI_GROUPS = tuple(catalog()["groups"])
DEFAULT_UI_GROUPS = UI_GROUPS
FLOWS = tuple(catalog()["flows"])
REUSABLE = {"passed", "reused_pass", "reused_baseline"}
LEGACY_APK = "c713ba5f5ef5b81ce85a4989ef7588c8f04f6d93942652031f314b352bd9fd59"
LEGACY_FLOW_DIGESTS = {
    "language": "cc041447aa42b525eba1d979f25e141c3bf66254d37d33e010428d708459c837",
    "navigation-review": "a982b2bcdf00ab729ea14a9e7509f1b526f5ae8567a4f83b84369aab0fa0856b",
    "navigation-walk": "2b9b2ceec0011d4878d4f1e767d9e7e7c41220e5930afaec0ed355ea38108adb",
    "navigation-map": "4a41e74cc8e0d29bdb2624bc4774bbf564422f934323e5a76f2cfa43f8ded495",
}


def now():
    return datetime.now(timezone.utc).isoformat()


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def write_json(path, value):
    write_atomic(path, json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def write_atomic(path, payload):
    path = Path(path)
    temporary = path.with_suffix(path.suffix + "." + uuid.uuid4().hex + ".tmp")
    try:
        temporary.write_text(payload, encoding="utf-8")
        for attempt in range(10):
            try:
                os.replace(temporary, path)
                return
            except OSError as error:
                if getattr(error, "winerror", None) not in (5, 32, 33) or attempt == 9:
                    raise
                time.sleep(.05)
    finally:
        temporary.unlink(missing_ok=True)


def read_json(path):
    return json.loads(Path(path).read_text(encoding="utf-8-sig"))


@contextmanager
def lock_handle(path):
    if os.name == "nt":
        import ctypes
        from ctypes import wintypes
        import msvcrt

        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        create = kernel.CreateFileW
        create.argtypes = [wintypes.LPCWSTR, wintypes.DWORD, wintypes.DWORD, wintypes.LPVOID,
                           wintypes.DWORD, wintypes.DWORD, wintypes.HANDLE]
        create.restype = wintypes.HANDLE
        handle = create(str(path), 0xC0000000, 0, None, 4, 0x80, None)
        if handle == ctypes.c_void_p(-1).value:
            error = ctypes.get_last_error()
            if error in (32, 33):
                raise CommandError("device_busy", "设备操作锁已被占用") from None
            raise ctypes.WinError(error)
        with os.fdopen(msvcrt.open_osfhandle(handle, os.O_RDWR | os.O_BINARY), "r+b") as stream:
            yield stream
    else:
        import fcntl

        with path.open("a+b") as stream:
            try:
                fcntl.flock(stream.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                raise CommandError("device_busy", "设备操作锁已被占用") from None
            yield stream


@contextmanager
def device_lock(repo, action, commit):
    path = common_repo(repo) / ".local/android/device-build.lock"
    path.parent.mkdir(parents=True, exist_ok=True)
    with lock_handle(path) as stream:
        started = now()
        value = {"task": action, "owner": "phone-session", "pid": os.getpid(),
                 "started_at": started, "process_start_utc": started,
                 "branch": git(repo, "rev-parse", "--abbrev-ref", "HEAD"), "commit": commit}
        stream.seek(0)
        stream.truncate()
        stream.write((json.dumps(value) + "\n").encode("utf-8"))
        stream.flush()
        yield


def phone_checks(repo):
    return {item["id"]: item for item in read_json(Path(repo) / "scripts/android/phone-checks.json")["checks"]}


def create_release_state(session, artifact, fixture, origin, allow_production):
    session.directory.mkdir(parents=True, exist_ok=True)
    session.state = {"schema": 1, "app": APP.removesuffix(".fixture"), "mode": "release-phone",
        "serial": session.adb.serial, "commit": session.commit, "fixture": str(Path(fixture).resolve()),
        "test_origin": origin, "allow_production": allow_production, "port": urllib.parse.urlsplit(origin).port,
        "created_at": now(), "status": "preparing", "artifacts": {APP.removesuffix(".fixture"): artifact},
        "results": {}, "files": [], "reverse_preexisting": True}
    session.save()


def evidence_paths(item, *, include_input=False):
    paths = []
    for key in ("evidence_paths", "screenshots", "recordings"):
        values = item.get(key)
        if isinstance(values, (list, tuple)):
            paths.extend(values)
    paths.extend(item.get(key) for key in ("log", "probe_log"))
    if include_input:
        paths.append(item.get("test_data_path"))
    return list(dict.fromkeys(path for path in paths if isinstance(path, str) and path.strip() and "\0" not in path))


def has_evidence(item):
    input_path = item.get("test_data_path")
    source = Path(input_path) if isinstance(input_path, str) and input_path else None
    return any(path != source and path.is_file() for path in map(Path, evidence_paths(item)))


def manual_evidence(record, phase):
    if record.get("status") != "passed":
        return record
    if record.get("reuse") == "reused_baseline" and not has_evidence(record):
        return {**record, "status": "pending", "reuse": None, "note": "人工基线证据已缺失，需重新取证"}
    if phase == "pre_launch" and not record.get("evidence_captured_at"):
        return {**record, "status": "inconclusive", "reuse": None}
    return record


def compatible_pass(item, flow, fingerprint):
    expected = "passed" if flow in SERVER_FLOWS else "not_applicable"
    return (item.get("status") in REUSABLE and item.get("ui_status") == "passed"
            and item.get("server_verification") == expected and item.get("flow_sha256") == fingerprint
            and not item.get("cleanup_errors") and has_evidence(item))


def validate_coverage(checks, selected, current, stopped, rows):
    if Counter(row["id"] for row in rows) != Counter(checks.keys()):
        raise ValueError("覆盖报告的检查项缺失或重复")
    automatic = [name for name in selected if name in checks and checks[name]["kind"] == "auto"]
    if (len(set(selected)) != len(selected) or set(selected) - checks.keys()
            or set(current) - set(automatic) or (stopped and stopped not in current)):
        raise ValueError("覆盖报告的选中项、结算项或停止点不一致")
    after_stop = set(automatic[automatic.index(stopped) + 1:]) if stopped else set()
    if after_stop & current.keys():
        raise ValueError("覆盖报告在停止点之后结算了所选检查项")
    for row in rows:
        name, status = row["id"], row["status"]
        item = checks[name]
        if item["kind"] == "auto":
            valid = row["manual_status"] == "not_applicable"
            if name not in current:
                valid &= (status == row["ui_status"] == "not_run" and bool(row["not_run_reason"])
                          and row["reuse"] is None)
            elif status == "not_run":
                valid = False
            elif status in ("reused_baseline", "reused_pass"):
                result = current[name]
                fingerprint = result.get("flow_sha256")
                valid &= (row["reuse"] == status and bool(fingerprint)
                          and compatible_pass(result, item["flow"], fingerprint))
            elif row["cause"] == "capability_missing":
                valid &= (status == "skipped" and row["ui_status"] == "not_run"
                          and bool(item.get("requires_capability")) and row["reuse"] is None)
        else:
            valid = (status == row["manual_status"] and status in {
                "pending", "passed", "failed", "inconclusive", "not_applicable", "environment_blocked"
            } and row["ui_status"] == row["server_verification"] == "not_applicable"
                and row["not_run_reason"] is None and row["existing_status"] is None)
            if row["reuse"] is not None:
                valid &= status == "passed" and row["reuse"] == "reused_baseline" and has_evidence(row)
        if not valid:
            raise ValueError(f"覆盖报告检查项状态或证据不一致：{name}")


def prelaunch_evidence(paths, launch, *, window_missed=False):
    if window_missed and not launch:
        return None
    cutoff = datetime.fromisoformat(launch) if launch else datetime.now(timezone.utc)
    if cutoff.tzinfo is None or cutoff > datetime.now(timezone.utc):
        return None
    for filename in paths:
        path = Path(filename)
        try:
            captured = datetime.fromtimestamp(path.stat().st_mtime, timezone.utc)
            if path.suffix.lower() == ".csv" and (captured < cutoff if launch else captured <= cutoff):
                with path.open(encoding="utf-8-sig", newline="") as stream:
                    header = next(csv.reader(stream), [])
                if {"week_ending_utc", "platform", "client_version", "sample_count"} <= set(header):
                    return captured.isoformat()
        except (OSError, UnicodeError, csv.Error):
            continue
    return None


def setting_matches(key, actual, expected):
    return actual == expected or (key == "wifi_on" and any(actual in pair and expected in pair
                                                          for pair in (("0", "3"), ("1", "2"))))


def artifact_links(paths):
    return " · ".join(f"[{Path(path).name}]({Path(path).resolve().as_uri()})" for path in dict.fromkeys(paths))


def markdown(value):
    return str(value).replace("|", "/").replace("\n", " ").replace("\r", " ")


def run_logged(command, log, timeout):
    """Keep subprocess output and stop this command's descendants at its deadline."""
    with Path(log).open("wb") as stream:
        process = subprocess.Popen(command, stdout=stream, stderr=subprocess.STDOUT)
        try:
            return process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            # A .bat launcher or recovery Python process can outlive its immediate parent.
            if os.name == "nt":
                subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
            if process.poll() is None:
                process.kill()
            process.wait(timeout=5)
            raise


def instrument_result(text):
    steps = re.findall(r"^INSTRUMENTATION_STATUS: acceptance_step=(.+)$", text, re.M)
    result = {"last_step": steps[-1].strip() if steps else None}
    if re.search(r"^OK \([1-9][0-9]* tests?\)", text, re.M) and not re.search(
            r"FAILURES!!!|INSTRUMENTATION_FAILED", text):
        return {**result, "status": "passed", "cause": None}
    if "UiAutomationService already registered" in text:
        return {**result, "status": "harness_error", "cause": "uiautomation_conflict"}
    if re.search(r"INSTRUMENTATION_FAILED|Unable to find instrumentation|ClassNotFoundException", text):
        return {**result, "status": "harness_error", "cause": "runner_unavailable"}
    if "TestTimedOutException" in text:
        return {**result, "status": "inconclusive", "cause": "instrumentation_timeout"}
    if "SignIntoFixture" in text or "登录后主页未就绪" in text:
        return {**result, "status": "environment_blocked", "cause": "login_connection"}
    if "ComposeTimeoutException" in text or "Condition still not satisfied" in text:
        return {**result, "status": "inconclusive", "cause": "ui_wait_timeout"}
    if not re.search(r"FAILURES!!!|INSTRUMENTATION_STATUS: (?:stack=|failure=)", text):
        return {**result, "status": "harness_error", "cause": "runner_result_incomplete"}
    return {**result, "status": "failed", "cause": "automation_assertion"}


def probe_fixture(origin):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(origin + "/api/app/v1/capabilities", timeout=5) as response:
            payload = json.load(response)
            if response.status != 200 or not isinstance(payload, dict):
                raise ValueError("invalid capabilities")
    except (OSError, ValueError, urllib.error.URLError):
        raise CommandError("fixture_unavailable", "电脑隔离服务未就绪；先启动服务，不安装或重装手机 App") from None


class PhoneSession:
    def __init__(self, repo, directory, output, adb, commit):
        self.repo, self.directory, self.output = Path(repo), Path(directory), Path(output)
        self.adb, self.commit = adb, commit
        self.path = self.directory / "session.json"
        self.state = read_json(self.path) if self.path.exists() else None
        self.output.mkdir(parents=True, exist_ok=True)
        self.record_video = False
        self.maestro = shutil.which("maestro") or shutil.which("maestro.bat") or "maestro"

    @property
    def app(self):
        return self.state.get("app", APP) if self.state else APP

    @property
    def release(self):
        return self.app == APP.removesuffix(".fixture")

    def save(self):
        self.state["updated_at"] = now()
        write_json(self.path, self.state)

    def network(self):
        return network_settings(self.adb)

    def restore_device_input(self):
        try:
            if not self.adb.serial.startswith("emulator-"):
                restore_input_method(self.adb, input_method_checkpoint(self.repo, self.adb.serial))
        except (RuntimeError, ValueError) as error:
            raise CommandError("ime_restore_failed", str(error)) from error

    def require_ready(self, preparing=False):
        if not self.state or self.state.get("status") == "cleaned":
            raise ValueError("先执行 prepare，或使用尚未清理的会话")
        if not preparing and self.state.get("status") != "ready":
            raise ValueError("会话准备尚未完成；对同一会话执行 prepare")
        if self.state["serial"] != self.adb.serial:
            raise ValueError("会话与手机不匹配")
        if self.adb.run("get-state").strip() != "device":
            raise CommandError("device_unavailable", "手机尚未连接")
        self.restore_device_input()
        network = self.network()
        if proxy_enabled(network):
            raise CommandError("proxy_present", "手机存在全局代理；USB 验收不会改写手机网络设置")
        return network

    def connection(self):
        data = read_json(self.state["fixture"])
        if self.release:
            FixtureAPI(data, trusted_https=True, test_origin=self.state["test_origin"],
                       allow_production=self.state.get("allow_production", False))
            return data
        origin = urllib.parse.urlsplit(data["origin"])
        if (origin.scheme != "http" or origin.hostname not in ("localhost", "127.0.0.1", "::1")
                or origin.path not in ("", "/") or origin.query or origin.fragment or origin.username
                or not origin.port
                or data.get("username") != "android-owner" or not data.get("password")):
            raise ValueError("使用 HTTP 回环隔离服务连接档案")
        if origin.port != self.state["port"]:
            raise ValueError("服务端口已改变")
        return data

    def connect(self):
        self.connection()
        if self.release:
            return
        mapping = f"tcp:{self.state['port']}"
        if reverse_conflict(self.adb, mapping):
            raise CommandError("reverse_conflict", "该手机端口已有其他 USB 映射；选择独立服务端口")
        self.adb.run("reverse", mapping, mapping)

    def package_paths(self, package):
        text = self.adb.run("shell", "pm", "path", package, check=False)
        matches = [re.fullmatch(r"package:(/[^\r\n]+\.apk)", line)
                   for line in text.splitlines() if line.strip()]
        if not all(matches):
            raise CommandError("package_query_failed", "无法读取设备安装包路径", text)
        paths = [match[1] for match in matches]
        if not paths:
            if self.adb.run("get-state").strip() != "device":
                raise CommandError("device_unavailable", "手机尚未连接")
        return paths

    def package_hash(self, package):
        paths = self.package_paths(package)
        if not paths:
            return None
        if len(paths) != 1:
            raise CommandError("split_apk", "设备包为拆分安装，不能自动认定与单 APK 相同")
        with tempfile.TemporaryDirectory(prefix="moerise-installed-apk-") as folder:
            path = Path(folder) / "base.apk"
            self.adb.run("pull", paths[0], str(path), timeout=60)
            return digest(path)

    def prepare(self, manifest, fixture, specialists=False):
        manifest = read_json(manifest)
        if not isinstance(manifest.get("commit"), str) or not manifest["commit"].strip():
            raise ValueError("构建清单缺少 APK 对应的源码提交")
        data = read_json(fixture)
        port = urllib.parse.urlsplit(data["origin"]).port
        if self.state:
            self.require_ready(preparing=True)
            if Path(self.state["fixture"]).resolve() != Path(fixture).resolve():
                raise ValueError("会话已绑定其他隔离服务")
        else:
            self.directory.mkdir(parents=True, exist_ok=True)
            self.state = {"schema": 1, "commit": manifest["commit"], "serial": self.adb.serial,
                          "fixture": str(Path(fixture).resolve()), "port": port,
                          "created_at": now(), "status": "preparing",
                          "artifacts": {}, "results": {}, "installs": {},
                          "reverse_preexisting": False, "files": []}
        self.require_ready(preparing=True)
        self.connection()
        probe_fixture(data["origin"])
        entries = {entry["path"].replace("\\", "/"): entry for entry in manifest.get("artifacts", [])}
        if not entries and "apks" in manifest:
            entries = {path.replace("\\", "/"): {"sha256": sha256}
                       for path, sha256 in manifest["apks"].items()}
        artifacts = {}
        requested = APK_PATHS if specialists else {APP: APK_PATHS[APP]}
        for package, path in requested.items():
            if path not in entries or entries[path]["sha256"] != digest(self.repo / path):
                raise ValueError("APK 与构建清单摘要不一致：" + path)
            artifacts[package] = {"path": path, "sha256": entries[path]["sha256"]}
        if any(package in self.state["artifacts"] and self.state["artifacts"][package] != artifact
               for package, artifact in artifacts.items()):
            raise ValueError("会话的 APK 已改变；创建独立会话")
        self.state["artifacts"].update(artifacts)
        self.state["runner_commit"] = self.commit
        mapping = f"tcp:{port}"
        if "network_before" not in self.state:
            self.state["network_before"] = self.network()
            self.state["reverse_preexisting"] = any(
                line.split()[-2:] == [mapping, mapping]
                for line in self.adb.run("reverse", "--list").splitlines())
        self.save()
        self.connect()
        for package, artifact in artifacts.items():
            if self.package_hash(package) == artifact["sha256"]:
                self.state["installs"][package] = "reused"
                self.save()
                continue
            if package == APP:
                self.state["status"] = "preparing"
                self.state["results"] = {}
                self.save()
            log = self.output / (package + "-install.log")
            try:
                text = self.adb.run("install", "-r", "-t", str(self.repo / artifact["path"]),
                                    timeout=120, log=log)
            except CommandError as error:
                self.state["installs"][package] = "confirmation_required" if (
                    "INSTALL_FAILED_USER_RESTRICTED" in str(error.output)) else error.code
                self.save()
                raise CommandError("installation_blocked", "安装未完成；手机确认后对同一会话重试 prepare", error.output)
            if not re.search(r"^Success\s*$", text, re.M):
                raise CommandError("installation_blocked", "安装没有返回 Success", text)
            if self.package_hash(package) != artifact["sha256"]:
                raise CommandError("installed_hash_mismatch", "设备 APK 摘要核对失败")
            self.state["installs"][package] = "installed"
            self.save()
        if self.state["status"] == "ready":
            return {"status": "ready", "session": str(self.directory), "manual": "Moerise Fixture"}
        self.state["status"] = "installed"
        self.save()
        result = self.ui("login", preparing=True)
        if result["status"] not in ("passed", "reused_pass"):
            raise CommandError("login_blocked", "登录准备未通过；先处理这一项，不运行后续业务组")
        self.state["status"] = "ready"
        self.save()
        self.adb.run("shell", "am", "start", "-W", "-n", ACTIVITY)
        return {"status": "ready", "session": str(self.directory), "manual": "Moerise Fixture"}

    def verify_artifacts(self, specialists=False):
        required = (APP, RUNNER_APP) if specialists else (self.app,)
        for package in required:
            if package not in self.state["artifacts"]:
                raise CommandError("prepare_required", "专项测试需要先执行 prepare -Specialists")
            artifact = self.state["artifacts"][package]
            if self.package_hash(package) != artifact["sha256"]:
                raise CommandError("prepare_required", "设备 APK 发生变化；先执行 prepare，不自动安装")

    def instrument(self, name, method, visual=False):
        data = self.connection()
        log = self.output / (name + "-instrumentation.log")
        result = {"started_at": now(), "log": str(log), "kind": "automation"}
        # Only the login fields enter the device; source API tokens stay on the host.
        try:
            # The last Maestro CLI can exit while its device-side instrumentation is still registered.
            for package in reversed(DRIVERS):
                if self.package_paths(package):
                    self.adb.run("shell", "am", "force-stop", package)
            with tempfile.TemporaryDirectory(prefix="moerise-device-login-") as folder:
                credential = Path(folder) / "connection.json"
                write_json(credential, {key: data[key] for key in ("origin", "username", "password")})
                self.adb.run("push", str(credential), CREDENTIAL)
                self.adb.run("shell", "am", "force-stop", APP)
                args = ["shell", "am", "instrument", "-w", "-e", "class",
                        "io.github.fermin214.moerise." + method,
                        "-e", "timeout_msec", "360000"]
                if visual:
                    args += ["-e", "visualSetup", "true"]
                args.append(RUNNER)
                text = self.adb.run(*args, timeout=390, log=log)
                result.update(instrument_result(text))
        except CommandError as error:
            result.update(instrument_result(str(error.output)))
            if error.code == "timeout":
                result.update(status="inconclusive", cause="host_timeout")
            elif result["cause"] in (None, "runner_result_incomplete"):
                result.update(status="harness_error", cause=error.code)
        except OSError:
            result.update(status="harness_error", cause="host_io_error")
        finally:
            result["cleanup_errors"] = []
            if result.get("status") != "passed":
                try:
                    self.adb.run("shell", "am", "force-stop", APP, check=False)
                except (CommandError, OSError):
                    result["cleanup_errors"].append("force_stop")
                self.collect_diagnostics(name)
            try:
                self.adb.run("shell", "rm", "-f", CREDENTIAL)
            except (CommandError, OSError):
                result["cleanup_errors"].append("credential_remove")
            if result["cleanup_errors"] and result.get("status") == "passed":
                result.update(status="harness_error", cause="cleanup_error")
        result["finished_at"] = now()
        result["runner_commit"] = self.commit
        self.state["results"][name] = result
        self.save()
        return result

    def collect_diagnostics(self, name):
        try:
            archive = self.adb.run("exec-out", "run-as", APP, "tar", "-C", "cache",
                                   "-cf", "-", "acceptance-diagnostics", binary=True)
            # A missing diagnostics directory is a collection issue, never a product failure.
            with tarfile.open(fileobj=io.BytesIO(archive)):
                (self.output / (name + "-diagnostics.tar")).write_bytes(archive)
        except (CommandError, ValueError, OSError, tarfile.TarError) as error:
            write_json(self.output / (name + "-diagnostics-error.json"),
                       {"kind": "collection_error", "error_type": type(error).__name__})

    def execution_digest(self, name):
        if name in FLOWS and (self.repo / "scripts/android/maestro/catalog.json").is_file():
            return flow_digest(self.repo, name)
        identity = hashlib.sha256(name.encode())
        scripts = ["phone-session.py"] if name not in (*UI_GROUPS, *FLOWS) else []
        if name in (*CASES, "recovery"):
            scripts.append("adb_common.py")
        if name in UI_GROUPS and (self.repo / "scripts/android/maestro/catalog.json").is_file():
            for flow in catalog(self.repo)["groups"][name]:
                identity.update(flow_digest(self.repo, flow).encode())
        if name == "recovery":
            scripts.append("device-recovery.py")
        for script in scripts:
            path = self.repo / "scripts/android" / script
            if path.is_file():
                identity.update(script.encode() + b"\0" + path.read_bytes())
        return identity.hexdigest()

    def run(self, groups, retry=False):
        if self.release:
            return self.run_release(groups, retry)
        self.require_ready()
        self.verify_artifacts(specialists=any(group in (*CASES, "recovery") for group in groups))
        self.connect()
        outcome = {}
        for group in groups:
            previous = self.state["results"].get(group)
            identity = self.execution_digest(group)
            if (previous and previous["status"] == "passed" and not retry
                    and previous.get("flow_sha256") == identity):
                outcome[group] = {"status": "reused_pass", "log": previous["log"]}
                continue
            if group in UI_GROUPS:
                runner = MaestroRunner(self.repo, self.adb, self.state["fixture"],
                    self.output / ("ui-" + group), shutil.which("maestro") or shutil.which("maestro.bat") or "maestro")
                results = runner.run_group(group)
                for result in results.values():
                    self.remember_maestro(result)
                status = next((value["status"] for value in results.values()
                               if value["status"] != "passed"), "passed")
                outcome[group] = {"kind": "maestro", "status": status, "flows": results,
                                  "runner_commit": self.commit,
                                  "flow_sha256": identity, "log": str(runner.output / "results.json")}
                self.state["results"][group] = outcome[group]
                self.save()
            elif group == "recovery":
                outcome[group] = self.recovery()
            else:
                outcome[group] = self.instrument(group, CASES[group])
            outcome[group]["flow_sha256"] = identity
            self.state["results"][group] = outcome[group]
            self.save()
            if outcome[group]["status"] in ("environment_blocked", "harness_error"):
                # Shared prerequisites stop the batch; an isolated UI wait does not.
                break
        return outcome

    def recovery(self):
        # Recovery needs an independent login, never a previous suite's side effects.
        login = self.instrument("recovery-login", "QuickNoteTest#loginWriteRecreateSaveAndReadBack", visual=True)
        if login["status"] != "passed":
            self.state["results"]["recovery"] = login
            self.save()
            return login
        report = self.output / "recovery.json"
        command = [sys.executable, "-X", "utf8", str(self.repo / "scripts/android/device-recovery.py"),
                   "--serial", self.adb.serial, "--adb", self.adb.executable,
                   "--fixture", self.state["fixture"], "--source-root", str(self.repo),
                   "--output", str(self.output)]
        result = {"kind": "automation", "started_at": now(), "log": str(self.output / "recovery.log"),
                  "runner_commit": self.commit, "cleanup_errors": []}
        try:
            report.unlink(missing_ok=True)
            data = self.connection()
            with tempfile.TemporaryDirectory(prefix="moerise-recovery-login-") as folder:
                credential = Path(folder) / "connection.json"
                write_json(credential, {key: data[key] for key in ("origin", "username", "password")})
                try:
                    self.adb.run("push", str(credential), CREDENTIAL)
                    returncode = run_logged(command, result["log"], timeout=600)
                finally:
                    try:
                        self.adb.run("shell", "rm", "-f", CREDENTIAL)
                    except (CommandError, OSError):
                        result["cleanup_errors"].append("credential_remove")
            if report.exists():
                raw = read_json(report)
                failure_kind = raw.get("failure", {}).get("category")
                status = ("passed" if raw["status"] == "succeeded" else
                          "failed" if failure_kind == "assertion" else
                          "harness_error" if failure_kind == "harness-error" or raw.get("cleanup_errors")
                          else "inconclusive")
                result.update(status="passed" if raw["status"] == "succeeded" else "inconclusive",
                              cause=None if raw["status"] == "succeeded" else "recovery_evidence",
                              stages=raw.get("stages", []))
                result["status"] = status
            else:
                result.update(status="harness_error", cause="recovery_report_missing")
            if returncode and result["status"] == "passed":
                result.update(status="harness_error", cause="recovery_exit_code")
        except subprocess.TimeoutExpired:
            result.update(status="inconclusive", cause="recovery_host_timeout")
        except (CommandError, OSError, ValueError, KeyError) as error:
            result.update(status="harness_error", cause=getattr(error, "code", type(error).__name__))
        if result["cleanup_errors"] and result["status"] == "passed":
            result.update(status="harness_error", cause="cleanup_error")
        result["finished_at"] = now()
        self.state["results"]["recovery"] = result
        self.save()
        return result

    def manual(self):
        self.require_ready()
        self.verify_artifacts()
        self.connect()
        if self.release:
            self.capture_resources()
            try:
                runner = MaestroRunner(self.repo, self.adb, self.state["fixture"], self.output / "maestro",
                    self.maestro, release=True, phone=True, test_origin=self.state["test_origin"],
                    allow_production=self.state.get("allow_production", False))
                self.configure_phone_runner(runner)
                result = runner.probe_phone_offline()
                if result["status"] != "passed":
                    raise CommandError(result["cause"], "先退出已登录账号并停在空白登录页")
            finally:
                if self.restore_resources():
                    raise CommandError("device_restore_failed", ", ".join(self.state["cleanup_errors"]))
        self.adb.run("shell", "am", "start", "-W", "-n", self.app + "/io.github.fermin214.moerise.MainActivity")
        return {"status": "manual_ready", "app": self.app, "connection": "USB",
                "session": str(self.directory), "cleanup": "显式 cleanup 才卸载"}

    def record(self, group, outcome, note, evidence=(), first_launch_at=None, refresh_report=True):
        if not self.state or self.state.get("status") == "cleaned":
            raise ValueError("会话不存在或已清理")
        item = phone_checks(self.repo).get(group) if self.release else None
        before_launch = item and item["phase"] == "pre_launch"
        manual = self.state.setdefault("manual_results", {})
        existing = manual.get(group, {})
        result = {
            "kind": "manual", "status": outcome, "note": note, "recorded_at": now(),
            "evidence_paths": [str(Path(path).resolve()) for path in evidence]}
        if before_launch and outcome == "passed":
            launch = first_launch(self.state)
            observed = launch if isinstance(launch, str) else None
            if first_launch_at:
                reported = datetime.fromisoformat(first_launch_at)
                if reported.tzinfo is None or reported > datetime.now(timezone.utc) or (
                        observed and reported > datetime.fromisoformat(observed)):
                    raise ValueError("--first-launch-at 必须是早于已记录启动时间的带时区时间")
                observed = reported.isoformat()
            captured = prelaunch_evidence(result["evidence_paths"], observed, window_missed=bool(launch))
            if not captured:
                raise CommandError("pre_launch_evidence_required",
                    "传入首次启动前的遥测 CSV：--evidence <文件>；若入口未观测首启，另给出 --first-launch-at <带时区时间>")
            result["evidence_captured_at"] = captured
            if first_launch_at:
                self.state["first_app_launch_at"] = observed
                self.state.pop("app_launch_time_unknown", None)
        if before_launch and outcome == "inconclusive":
            result["note"] = note or existing.get("note") or "缺少首次启动前的遥测 CSV"
            if existing.get("window_missed"):
                result["window_missed"] = True
        manual[group] = result
        self.save()
        if self.release and refresh_report:
            self.coverage_report() if self.state.get("coverage") else self.summary()
        return result

    def remember_maestro(self, result):
        original = self.state.setdefault("maestro_preexisting", {})
        for package, existed in result.get("drivers_preexisting", {}).items():
            original.setdefault(package, existed)
        forwards = self.state.setdefault("maestro_forwards", [])
        for line in result.get("added_forwards", []):
            parts = line.split()
            if len(parts) == 3 and parts[0] == self.adb.serial and parts[1].startswith("tcp:"):
                if parts[1] not in forwards:
                    forwards.append(parts[1])
                self.state.setdefault("maestro_forward_bindings", {})[parts[1]] = parts[2]
        remote = result.get("remote_test_file")
        if remote and remote not in self.state["files"]:
            self.state["files"].append(remote)

    def ui(self, flow, executable=None, note_name=None, retry=False, preparing=False):
        if self.release:
            raise ValueError("正式包真机使用 accept 或 run --group")
        self.require_ready(preparing=preparing)
        self.verify_artifacts()
        key = "maestro:" + flow
        directory = self.output / ("maestro-" + flow)
        if flow not in FLOWS:
            raise ValueError("未知 Maestro 场景")
        identity = self.execution_digest(flow)
        previous = self.state["results"].get(key)
        if (previous and previous["status"] == "passed" and not retry
                and previous.get("note_name") == note_name and previous.get("flow_sha256") == identity):
            return {"status": "reused_pass", "log": previous["log"]}
        self.connect()
        runner = MaestroRunner(self.repo, self.adb, self.state["fixture"], directory,
            executable or shutil.which("maestro") or shutil.which("maestro.bat") or "maestro")
        result = runner.run_flow(flow, note_name)
        result.update(note_name=note_name, flow_sha256=identity, runner_commit=self.commit)
        self.remember_maestro(result)
        self.state["results"][key] = result
        self.save()
        return result

    def prepare_release(self, apk, fixture, test_origin, confirmed=False, allow_production=False,
                        install=False, clear_data=False, uninstall=False, artifact=None, installed=None):
        if self.adb.serial.startswith("emulator-"):
            raise ValueError("正式包模拟器使用 release-smoke.py；此入口只接收真机")
        if not confirmed:
            raise ValueError("先人工完成装包、首次系统确认，并确认 App 已退出生产账号，再传 --confirm-ready")
        data = read_json(fixture)
        FixtureAPI(data, trusted_https=True, test_origin=test_origin, allow_production=allow_production)
        if urllib.parse.urlsplit(data["origin"]).scheme != "https":
            raise ValueError("正式包真机需要独立 HTTPS 测试服务")
        app = APP.removesuffix(".fixture")
        artifact = artifact or {"path": str(Path(apk).resolve()), "sha256": digest(apk)}
        if self.state and self.state.get("status") == "cleaned":
            raise ValueError("会话已清理；使用独立会话目录")
        if self.state and (self.state.get("app") != app or self.state["serial"] != self.adb.serial or
                           self.state["artifacts"].get(app) != artifact or
                           self.state.get("test_origin") != test_origin or
                           self.state.get("fixture") != str(Path(fixture).resolve())):
            raise ValueError("设备、APK 或服务已改变；使用独立会话")
        if self.adb.run("get-state").strip() != "device":
            raise CommandError("device_unavailable", "手机尚未连接")
        if proxy_enabled(self.network()):
            raise CommandError("proxy_present", "手机存在全局代理")
        if uninstall and not install:
            raise ValueError("卸载后需要显式 --install 重新安装")
        if self.state is None:
            create_release_state(self, artifact, fixture, test_origin, allow_production)
        if uninstall and self.package_paths(app):
            self.adb.run("uninstall", app, timeout=90)
        if install:
            self.adb.run("install", "-r", str(Path(apk).resolve()), timeout=120)
        if (self.package_hash(app) if installed is None or install else installed) != artifact["sha256"]:
            raise CommandError("installed_hash_mismatch", "先人工安装指定 APK，或显式传 --install")
        if clear_data:
            self.adb.run("shell", "pm", "clear", app)
        if uninstall or install or clear_data:
            self.state["results"] = {}
            for key in ("flow_results", "check_results", "source_receipt"):
                self.state.pop(key, None)
        self.state["status"] = "ready"
        self.save()

    def device_checkpoint(self):
        return {"wifi_on": self.adb.run("shell", "settings", "get", "global", "wifi_on").strip(),
                "mobile_data": self.adb.run("shell", "settings", "get", "global", "mobile_data").strip(),
                "stay_on_while_plugged_in": self.adb.run("shell", "settings", "get", "global", "stay_on_while_plugged_in").strip(),
                "screen_off_timeout": self.adb.run("shell", "settings", "get", "system", "screen_off_timeout").strip()}

    def restore_settings(self, saved):
        errors = []
        for key, value in saved.items():
            try:
                namespace = "system" if key == "screen_off_timeout" else "global"
                current = self.adb.run("shell", "settings", "get", namespace, key).strip()
                if setting_matches(key, current, value):
                    continue
                if key in ("wifi_on", "mobile_data"):
                    if value not in (("0", "1", "2", "3") if key == "wifi_on" else ("0", "1")):
                        raise ValueError("原网络状态无效")
                    self.adb.run("shell", "svc", "wifi" if key == "wifi_on" else "data",
                                 "enable" if value in ("1", "2") else "disable")
                elif key == "airplane_mode_on" and value in ("0", "1"):
                    self.adb.run("shell", "cmd", "connectivity", "airplane-mode", "enable" if value == "1" else "disable")
                elif value == "null":
                    self.adb.run("shell", "settings", "delete", namespace, key)
                elif value.isdigit():
                    self.adb.run("shell", "settings", "put", namespace, key, value)
                else:
                    raise ValueError("原亮屏设置无效")
                actual = self.adb.run("shell", "settings", "get", namespace, key).strip()
                until = time.monotonic() + (5 if key in ("wifi_on", "airplane_mode_on") else 0)
                while not setting_matches(key, actual, value) and time.monotonic() < until:
                    time.sleep(.2)
                    actual = self.adb.run("shell", "settings", "get", namespace, key).strip()
                if not setting_matches(key, actual, value):
                    raise RuntimeError("设备状态恢复读回不一致")
            except (CommandError, OSError, ValueError, RuntimeError):
                errors.append(key)
        return errors

    def restore_checkpoint(self):
        errors = self.restore_settings(self.state.get("device_before", {}))
        if not errors:
            self.state.pop("device_before", None)
        self.save()
        return errors

    def restore_source_network(self):
        saved = self.state.get("source_network_before")
        if not saved:
            return []
        try:
            self.adb.run("shell", "am", "force-stop", self.app)
        except (CommandError, OSError):
            return ["source_app_stop"]
        errors = self.restore_settings(saved)
        if not errors:
            self.state.pop("source_network_before", None)
        self.save()
        return errors

    @contextmanager
    def isolate_source(self):
        if ":" in self.adb.serial or "_adb-tls-" in self.adb.serial:
            raise CommandError("usb_required", "离线来源探针需要 USB ADB")
        if self.adb.run("reverse", "--list").strip():
            raise CommandError("source_network_not_isolated", "设备存在 USB 反向转发；关闭转发后再运行来源探针")
        airplane = self.adb.run("shell", "settings", "get", "global", "airplane_mode_on").strip()
        wifi = self.adb.run("shell", "settings", "get", "global", "wifi_on").strip()
        if airplane not in ("0", "1") or wifi not in ("0", "1", "2", "3"):
            raise CommandError("network_state_unknown", "无法读取并恢复设备的飞行模式与 Wi-Fi 状态")
        self.state["source_network_before"] = {"airplane_mode_on": airplane, "wifi_on": wifi}
        self.save()
        try:
            self.adb.run("shell", "cmd", "connectivity", "airplane-mode", "enable")
            self.adb.run("shell", "svc", "wifi", "disable")
            until = time.monotonic() + 12
            while True:
                mode = self.adb.run("shell", "settings", "get", "global", "airplane_mode_on").strip()
                networks = self.adb.run("shell", "dumpsys", "connectivity")
                current = re.search(r"^([ \t]*)Current Networks:[ \t]*\r?\n(.*?)(?=^\1\S|\Z)", networks, re.S | re.M)
                if (mode == "1" and current and not current[2].strip()
                        and re.search(r"(?m)^\s*Active default network:\s*none\s*$", networks)):
                    break
                if time.monotonic() >= until:
                    raise CommandError("source_network_not_isolated", "设备仍有网络连接或无法确认离线；断开有线网与其它设备网络")
                time.sleep(.3)
            self.adb.run("shell", "am", "force-stop", self.app)
            yield
        finally:
            errors = self.restore_source_network()
            if errors:
                raise CommandError("source_network_restore_failed", ", ".join(errors))

    def restore_resources(self):
        errors = self.restore_source_network()
        if not errors:
            errors = self.restore_checkpoint()
        operations = [("input_restore", self.restore_device_input)]
        if self.state.get("resources_pending"):
            for package in reversed(DRIVERS):
                operations.append(("driver_stop", lambda package=package: self.adb.run("shell", "am", "force-stop", package)))
                if not self.state.get("maestro_preexisting", {}).get(package, True):
                    operations.append(("driver_remove", lambda package=package: self.remove_package(package)))
            try:
                existing = self.adb.run("forward", "--list").splitlines()
            except (CommandError, OSError):
                existing = []
                errors.append("forward_readback")
            for line in set(existing) - set(self.state.get("forwards_before", existing)):
                parts = line.split()
                if len(parts) == 3 and parts[0] == self.adb.serial:
                    self.state.setdefault("maestro_forward_bindings", {})[parts[1]] = parts[2]
            for mapping, target in self.state.get("maestro_forward_bindings", {}).items():
                if f"{self.adb.serial} {mapping} {target}" in existing:
                    operations.append(("forward_remove", lambda mapping=mapping: self.adb.run("forward", "--remove", mapping)))
            for remote in self.state.get("files", []):
                if re.fullmatch(r"/sdcard/Download/moerise-maestro-[a-f0-9]{32}\.txt", remote):
                    operations.append(("test_file_remove", lambda remote=remote: self.adb.run("shell", "rm", "-f", remote)))
        for label, operation in operations:
            try:
                operation()
            except (CommandError, OSError):
                errors.append(label)
        if not errors:
            for field in ("resources_pending", "forwards_before", "maestro_preexisting", "maestro_forwards", "maestro_forward_bindings"):
                self.state.pop(field, None)
            self.state["files"] = []
        self.state["cleanup_errors"] = errors
        self.save()
        if not errors:
            owner = self.resource_owner
            if owner.exists() and Path(read_json(owner)["session"]) == self.path.resolve():
                owner.unlink()
        return errors

    @property
    def resource_owner(self):
        return device_resource_owner(self.repo, self.adb.serial)

    @property
    def owns_resources(self):
        return any(self.state.get(key) for key in ("resources_pending", "device_before", "source_network_before"))

    def capture_resources(self):
        resume = self.owns_resources
        if resume:
            self.save()
        roots = session_history(self.repo, self.directory.parent)
        recover_device_sessions(self.repo, self.adb, self.commit, [self.path, *roots])
        if resume:
            self.state = read_json(self.path)
        self.state["device_before"] = self.device_checkpoint()
        self.state["maestro_preexisting"] = {package: bool(self.package_paths(package)) for package in DRIVERS}
        self.state["forwards_before"] = self.adb.run("forward", "--list").splitlines()
        owner = self.resource_owner
        owner.parent.mkdir(parents=True, exist_ok=True)
        write_json(owner, {"serial": self.adb.serial, "session": str(self.path.resolve())})
        self.state["resources_pending"] = True
        self.save()

    def summary(self):
        categories = {"failed": "product", "harness_error": "tool", "environment_blocked": "environment",
                      "inconclusive": "unconfirmed", "passed": "none", "reused_pass": "none"}
        rows = []
        for group, result in self.state["results"].items():
            flows = result.get("flows", {group: result})
            for flow, item in flows.items():
                rows.append({"module": group, "flow": flow, "status": item["status"],
                    "category": categories.get(item["status"], "unconfirmed"),
                    "cause": item.get("cause"), "screenshots": item.get("screenshots", []),
                    "recordings": item.get("recordings", []), "log": item.get("log"),
                    "last_step": item.get("last_step"),
                    **{key: item[key] for key in ("preparation_step", "request", "http_status", "error_code",
                                                  "error_type", "error_message") if key in item}})
        report = {"mode": "release-phone", "app": self.app, "apk": self.state["artifacts"][self.app],
                  "serial": self.adb.serial, "items": rows,
                  "cleanup_errors": self.state.get("cleanup_errors", []),
                  "manual_results": self.state.get("manual_results", {}),
                  "visual_review": "集中复核关键截图和录像中的日历、图标、流式残影及布局"}
        write_json(self.directory / "report.json", report)
        lines = ["# Android 正式包真机验收", "", "| 模块 | 场景 | 状态 | 分类 | 失败步骤与原因 | 证据 |", "| --- | --- | --- | --- | --- | --- |"]
        for row in rows:
            paths = [*row["screenshots"], *row["recordings"], *([row["log"]] if row["log"] else [])]
            links = " · ".join("[" + Path(path).name + "](" + Path(path).resolve().as_uri() + ")" for path in paths)
            failure = " · ".join(str(row[key]) for key in ("preparation_step", "error_message") if row.get(key))
            failure = failure.replace("|", "/").replace("\n", " ").replace("\r", " ")
            lines.append(f"| {row['module']} | {row['flow']} | {row['status']} | {row['category']} | {failure} | {links} |")
        for group, item in report["manual_results"].items():
            lines.append(f"| {group} | manual | {item['status']} | manual | {item['note'].replace('|', '/').replace(chr(10), ' ')} | |")
        lines.extend(["", report["visual_review"], "", "设备恢复错误：" + ", ".join(report["cleanup_errors"])])
        (self.directory / "report.md").write_text("\n".join(lines) + "\n", encoding="utf-8")
        return report

    def previous_check(self, item):
        name, flow = item["id"], item.get("flow")
        latest = self.state.get("flow_results", {}).get(flow)
        if latest is not None:
            return latest
        previous = self.state.get("check_results", {}).get(name)
        if not flow:
            return previous or {}
        candidates = [previous] if previous else []
        for group, record in self.state["results"].items():
            candidate = record.get("flows", {group: record}).get(flow)
            if candidate:
                candidates.append(candidate)
        return max(candidates, key=lambda result: result.get("started_at", ""), default={})

    def save_flow_result(self, flow, result):
        if result.get("attempted") or result["status"] in ("passed", "reused_baseline"):
            self.state.setdefault("flow_results", {})[flow] = result

    def baseline_records(self, path):
        if not path:
            return {}
        path = Path(path).resolve()
        baseline = read_json(path)
        identity = {"apk_sha256": self.state["artifacts"][self.app]["sha256"],
                    "serial": self.adb.serial, "test_origin": self.state["test_origin"]}
        if any(baseline.get(field) != value for field, value in identity.items()):
            raise CommandError("baseline_identity_mismatch", "基线的 APK、设备或测试来源不匹配")
        legacy = baseline.get("legacy_handoff") is True
        if legacy and identity["apk_sha256"] != LEGACY_APK:
            raise CommandError("baseline_legacy_apk", "历史交接映射仅适用于指定的 0.3.0 APK")
        records = baseline.get("checks")
        if not isinstance(records, dict):
            raise ValueError("基线需要 checks 对象")
        checks = phone_checks(self.repo)
        imported = {}
        for name, record in records.items():
            if (name not in checks or not isinstance(record, dict)
                    or record.get("ui_status") not in {"passed", "failed", "pending", "not_applicable", "not_run", "inconclusive"}
                    or record.get("server_verification") not in {"passed", "failed", "pending", "not_applicable", "environment_blocked"}
                    or record.get("manual_status") not in {"passed", "failed", "pending", "not_applicable", "inconclusive"}
                    or not isinstance(record.get("evidence_paths"), list) or not record["evidence_paths"]
                    or not all(isinstance(entry, str) and entry for entry in record["evidence_paths"])):
                raise ValueError(f"基线检查记录不完整或状态无效：{name}")
            evidence = [(path.parent / entry).resolve() for entry in record["evidence_paths"]]
            item = checks[name]
            value = {**record, "evidence_paths": [str(entry) for entry in evidence]}
            if record.get("test_data_path"):
                value["test_data_path"] = str((path.parent / record["test_data_path"]).resolve())
            if all(entry.is_file() for entry in evidence):
                value["legacy_handoff"] = legacy
                imported[name] = value
            if (item["kind"] != "auto" and (not legacy or name == "editing-keyboard")
                    and name in imported and record["manual_status"] == "passed"):
                if item["phase"] == "pre_launch":
                    launch = first_launch(self.state)
                    cutoff = launch if isinstance(launch, str) else None
                    captured = prelaunch_evidence(evidence, cutoff,
                                                  window_missed=bool(launch))
                    if not captured:
                        continue
                    value["evidence_captured_at"] = captured
                local = self.state.setdefault("manual_results", {})
                if manual_evidence(local.get(name, {}), item["phase"]).get("status") != "passed":
                    local[name] = {**value, "status": "passed", "reuse": "reused_baseline"}
        self.save()
        return imported

    def baseline_pass(self, name, flow, record, fingerprint, runner, *, local_only=False):
        if record.get("ui_status") != "passed":
            return None
        expected = "passed" if flow in SERVER_FLOWS else "not_applicable"
        if record.get("legacy_handoff"):
            if LEGACY_FLOW_DIGESTS.get(name) != fingerprint or expected != "not_applicable":
                return None
        elif record.get("flow_sha256") != fingerprint:
            return None
        result = {**record, "status": "reused_baseline", "flow_sha256": fingerprint}
        if (result["server_verification"] == "pending" and result.get("test_data_path") and expected == "passed"):
            if local_only or flow in ("editing", "navigation", "navigation-settings"):
                return None
            try:
                values = read_json(result["test_data_path"])
                if flow == "attachments" and not (
                        re.fullmatch(r"[a-f0-9]{64}", values.get("ATTACHMENT_SHA256", ""))
                        and re.fullmatch(r"[A-Za-z0-9_-]+\.txt", values.get("ATTACHMENT_NAME", ""))
                        and values.get("ATTACHMENT_NOTE_TEXT")):
                    return None
                if flow == "actions" and (values.get("NOTE_NAME") == values.get("PEER_NOTE_NAME")
                        or not all(re.fullmatch(r"memos/[A-Za-z0-9_-]+", values.get(key, ""))
                                   for key in ("NOTE_NAME", "PEER_NOTE_NAME"))):
                    return None
                runner.login_batch()
                observed = runner.verify(flow, values)
                if observed:
                    result.update(server_verification="passed", server_assertions=observed)
            except (AssertionError, FixtureError, OSError, KeyError, ValueError, TypeError):
                return None
        return result if compatible_pass(result, flow, fingerprint) else None

    def render_coverage_report(self, checks=None, selected=None):
        checks = phone_checks(self.repo) if checks is None else checks
        view = self.state.get("coverage", {})
        selected = view.get("selected", []) if selected is None else selected
        current = view.get("results", {})
        manual = {name: manual_evidence(record, checks.get(name, {}).get("phase"))
                  for name, record in self.state.get("manual_results", {}).items()}
        categories = {"failed": "assertion", "harness_error": "tool", "environment_blocked": "environment",
                      "inconclusive": "unconfirmed", "skipped": "dependency", "not_run": "not_run"}
        rows, cards = [], ["# Android 真机操作卡", ""]
        for name, item in checks.items():
            automatic = item["kind"] == "auto"
            settled = current.get(name)
            previous = self.previous_check(item)
            result = settled or previous
            human = manual.get(name, {})
            manual_status = human.get("status", "not_applicable" if automatic else "pending")
            if not human and item["phase"] == "pre_install" and view.get("install_needed") is False:
                manual_status = "not_applicable"
            status = (result.get("status", "not_run") if settled else "not_run") if automatic else manual_status
            paths = evidence_paths(result, include_input=True) + evidence_paths(human)
            row = {"id": name, "module": item["module"], "flow": item.get("flow"), "kind": item["kind"],
                   "phase": item["phase"], "handoff_items": item["handoff_items"], "status": status,
                   "ui_status": result.get("ui_status", "not_run") if settled else ("not_run" if automatic else "not_applicable"),
                   "server_verification": result.get("server_verification", "pending" if item.get("flow") in SERVER_FLOWS else "not_applicable"),
                   "manual_status": manual_status, "reuse": human.get("reuse") or (
                       result["status"] if settled and result["status"] in ("reused_baseline", "reused_pass") else None),
                   "category": categories.get(status), "cause": result.get("cause") or human.get("note"),
                   "existing_status": previous.get("status") if automatic and not settled else None,
                   "not_run_reason": ("stopped_at_boundary" if view.get("stopped") and name in selected
                                      else "not_selected_or_interrupted") if automatic and not settled else None,
                   "last_step": result.get("last_step"), "evidence_paths": list(dict.fromkeys(paths)),
                   **{key: result[key] for key in ("preparation_step", "request", "http_status", "error_code",
                                                   "error_type", "error_message", "probe_cause") if key in result}}
            rows.append(row)
            if automatic or manual_status in ("passed", "not_applicable"):
                continue
            card = item["manual_card"]
            linked = [path for auto in checks.values() if auto["kind"] == "auto" and auto["module"] == item["module"]
                      for path in evidence_paths(self.previous_check(auto), include_input=True)]
            quoted = str(self.directory).replace("'", "''")
            outcome = "inconclusive" if item["phase"] == "pre_launch" else "passed"
            command = (f"python -B scripts/android/phone-session.py record --release --session '{quoted}' "
                       f"--check {name} --outcome {outcome}")
            cards.extend([f"## {name} · {item['phase']}", "", card["start_state"], "",
                          *(f"{index}. {step}" for index, step in enumerate(card["steps"], 1)), "",
                          f"通过条件　{card['pass_criteria']}", f"证据　{card['evidence_hint']}",
                          *([f"取证状态　{human['note']}"] if human.get("window_missed") else []), "",
                          artifact_links([*paths, *linked]), "", "```powershell", command, "```", ""])
        validate_coverage(checks, selected, current, view.get("stopped"), rows)
        report = {"app": self.app, "apk": self.state["artifacts"][self.app], "serial": self.adb.serial,
                  "test_origin": self.state["test_origin"], "run_id": view.get("run_id"),
                  "status": view.get("status"), "stopped_at": view.get("stopped"), "error": view.get("error"),
                  "cleanup_errors": self.state.get("cleanup_errors", []), "items": rows, "manual_results": manual}
        title = ["# Android 正式包真机验收", "", f"设备 `{self.adb.serial}` · 来源 `{report['test_origin']}`",
                 f"APK `{report['apk']['sha256']}` · 运行 `{report['status']}`", "",
                 "[待确认操作卡](manual-cards.md)", "", "设备恢复错误　" + ", ".join(report["cleanup_errors"]), ""]
        if report["error"]:
            title.extend([markdown(report["error"]), ""])
        title.extend(["| 检查项 | 交接编号 | 模块 | 类型 | UI | 服务端 | 人工 | 结果与复用 | 分类与原因 | 最后一步及来源 | 证据 |",
                      "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |"])
        for row in rows:
            step = row["last_step"] or {}
            values = [row["id"], ", ".join(map(str, row["handoff_items"])), row["module"],
                      row["kind"], row["ui_status"], row["server_verification"], row["manual_status"],
                      " · ".join(filter(None, (row["status"], row["reuse"], row["existing_status"]))),
                      " · ".join(filter(None, (row["category"], row["not_run_reason"], row["cause"],
                                               row.get("probe_cause"), row.get("error_message")))),
                      " · ".join(filter(None, (step.get("source"), step.get("kind"), step.get("label") or step.get("stage")))),
                      artifact_links(row["evidence_paths"])]
            title.append("| " + " | ".join(map(markdown, values)) + " |")
        return report, "\n".join(title) + "\n", "\n".join(cards) + "\n"

    def publish_coverage_report(self, report, markdown_report, manual_cards):
        write_atomic(self.directory / "report.md", markdown_report)
        write_atomic(self.directory / "manual-cards.md", manual_cards)
        write_json(self.directory / "report.json", report)
        return report

    def coverage_report(self):
        return self.publish_coverage_report(*self.render_coverage_report())

    def configure_phone_runner(self, runner):
        runner.on_app_launch = self.mark_app_launch
        runner.on_app_launch_attempt = self.mark_app_launch_attempt
        runner.on_source_verified = self.mark_source_verified
        runner.source_receipt = self.state.get("source_receipt")
        runner.phone_isolation = self.isolate_source

    def run_checks(self, items, baseline, args, run_id, prompt, before_phone):
        runner = MaestroRunner(self.repo, self.adb, self.state["fixture"], self.output / "maestro",
            self.maestro, release=True, phone=True, test_origin=self.state["test_origin"],
            allow_production=self.state["allow_production"], record=self.record_video)
        runner.fail_marker = args.fail_marker
        self.configure_phone_runner(runner)
        runner.on_test_file = self.mark_test_file
        runner.on_phone_access = before_phone
        by_flow = {item["flow"]: item for item in items}
        view = self.state["coverage"]
        marker = self.directory / "STOP_AFTER_CURRENT"

        def select(flow, engine, *, local_only=False):
            item = by_flow[flow]
            name = item["id"]
            if item.get("requires_capability") and not getattr(args, item["requires_capability"]):
                return {"status": "skipped", "cause": "capability_missing", "ui_status": "not_run"}
            fingerprint = self.execution_digest(flow)
            if not args.retry:
                previous = self.previous_check(item)
                if compatible_pass(previous, flow, fingerprint):
                    return {**previous, "status": "reused_pass"}
                if previous.get("attempted"):
                    return None
                return self.baseline_pass(name, flow, baseline.get(name, {}), fingerprint, engine, local_only=local_only)
            return None

        def stop_requested():
            try:
                return marker.read_text(encoding="utf-8-sig").strip() == run_id
            except FileNotFoundError:
                return False

        def should_stop(flow):
            item = by_flow.get(flow)
            return (item is not None and args.stop_after in (item["id"], flow)) or stop_requested()

        def retry_source(flow):
            return not should_stop(flow) and prompt(
                "若离线账号核验失败，保持设备断网并退出账号，停在空白登录页后恢复测试网络。输入 y 重试一次。"
            ) and not should_stop(flow)

        def checkpoint(flow, result):
            name = by_flow[flow]["id"]
            self.remember_maestro(result)
            previous = self.state.get("flow_results", {}).get(flow)
            try:
                view["results"][name] = result
                self.save_flow_result(flow, result)
                self.save()
            except OSError:
                view["results"].pop(name, None)
                if not result.get("attempted"):
                    history = self.state.get("flow_results", {})
                    if previous is None:
                        history.pop(flow, None)
                    else:
                        history[flow] = previous
                raise
            if result.get("cause") != "interrupted" and should_stop(flow):
                view["stopped"] = name
                try:
                    self.save()
                except OSError:
                    view.pop("stopped", None)
                    raise
                return True
            return False

        flows = list(by_flow)
        try:
            return runner.run_batch(flows, on_result=checkpoint, select=select,
                clean_editor={flow for flow, item in by_flow.items() if item["requires_clean_editor"]},
                on_source_blocked=retry_source)
        finally:
            self.state["batch_cleanup_errors"] = getattr(runner, "batch_cleanup_errors", [])

    def mark_source_verified(self, receipt):
        self.state["source_receipt"] = receipt
        self.save()

    def mark_app_launch_attempt(self, attempted):
        if not first_launch(self.state):
            self.state["last_app_launch_attempt_at"] = attempted
            self.save()

    def mark_app_launch(self, attempted):
        if not first_launch(self.state):
            self.state["first_app_launch_at"] = attempted
            self.save()

    def mark_test_file(self, path):
        if path not in self.state["files"]:
            self.state["files"].append(path)
            self.save()

    def run_release(self, groups, retry=False):
        if any(group not in UI_GROUPS for group in groups):
            raise ValueError("正式包使用 catalog 中的 UI 模块；instrumentation 仅用于 fixture")
        self.require_ready()
        self.verify_artifacts()
        outcome = {}
        runner = None
        self.state["cleanup_errors"] = []
        try:
            runner = MaestroRunner(self.repo, self.adb, self.state["fixture"], self.output / "maestro",
                self.maestro, release=True, phone=True,
                test_origin=self.state["test_origin"], allow_production=self.state.get("allow_production", False), record=self.record_video)
            self.configure_phone_runner(runner)
            runner.on_phone_access = self.capture_resources
            flows = list(dict.fromkeys(flow for group in groups for flow in runner.catalog["groups"][group]))
            by_flow = {item["flow"]: item for item in phone_checks(self.repo).values() if item.get("flow")}
            for flow in flows:
                previous = self.previous_check(by_flow.get(flow, {"id": flow, "flow": flow}))
                if previous:
                    self.state.setdefault("flow_results", {}).setdefault(flow, previous)

            def select(flow, _engine, *, local_only=False):
                previous = self.state.get("flow_results", {}).get(flow, {})
                if not retry and compatible_pass(previous, flow, self.execution_digest(flow)):
                    return {**previous, "status": "reused_pass"}
                return None

            def checkpoint(flow, result):
                self.remember_maestro(result)
                self.save_flow_result(flow, result)
                for group in groups:
                    if flow in runner.catalog["groups"][group]:
                        saved = self.state["results"].setdefault(group, {"flows": {}})
                        saved["flows"][flow] = result
                        saved["status"] = "inconclusive"
                        saved.pop("flow_sha256", None)
                self.save()

            results = runner.run_batch(flows, on_result=checkpoint, select=select) if flows else {}
            for group in groups:
                selected = {flow: results[flow] for flow in runner.catalog["groups"][group] if flow in results}
                complete = len(selected) == len(runner.catalog["groups"][group])
                status = next((item["status"] for item in selected.values() if item["status"] not in REUSABLE),
                              "passed" if complete else "inconclusive")
                outcome[group] = {"status": status, "flows": selected, "flow_sha256": self.execution_digest(group)}
                self.state["results"][group] = outcome[group]
                if complete and all(item["status"] == "reused_pass" for item in selected.values()):
                    outcome[group] = {**outcome[group], "status": "reused_pass"}
            self.save()
        finally:
            batch_errors = getattr(runner, "batch_cleanup_errors", [])
            if self.owns_resources:
                self.restore_resources()
            if isinstance(batch_errors, list):
                self.state["cleanup_errors"].extend(batch_errors)
            self.save()
            self.summary()
        return outcome

    def remove_package(self, package):
        if self.package_paths(package):
            text = self.adb.run("uninstall", package, timeout=90)
            if not re.search(r"^Success\s*$", text, re.M):
                raise CommandError("cleanup_failed", "卸载没有返回 Success")
            if self.package_paths(package):
                raise CommandError("cleanup_failed", "卸载后安装包仍存在")
        return True

    def cleanup(self, uninstall=False):
        if not self.state:
            raise ValueError("会话不存在")
        if self.state["serial"] != self.adb.serial:
            raise ValueError("会话与手机不匹配")
        if self.state.get("status") == "cleaned":
            return self.state["cleanup"]
        if self.adb.run("get-state").strip() != "device":
            raise CommandError("device_unavailable", "手机尚未连接")
        if self.release and self.owns_resources:
            errors = self.restore_resources()
            if errors:
                raise CommandError("device_restore_failed", ", ".join(errors))
        if not self.release:
            self.restore_device_input()
        results = {}
        for package in self.state["artifacts"] if uninstall else ():
            if package not in (*APK_PATHS, APP.removesuffix(".fixture")):
                continue
            results[package] = self.remove_package(package)
        if not self.release:
            self.adb.run("shell", "rm", "-f", CREDENTIAL)
            for package, preexisting in self.state.get("maestro_preexisting", {}).items():
                if package in DRIVERS and not preexisting:
                    results[package] = self.remove_package(package)
            if self.state.get("maestro_forwards"):
                existing = self.adb.run("forward", "--list").splitlines()
                for mapping, target in self.state.get("maestro_forward_bindings", {}).items():
                    if f"{self.adb.serial} {mapping} {target}" in existing:
                        self.adb.run("forward", "--remove", mapping)
            for remote in self.state.get("files", []):
                if re.fullmatch(r"/sdcard/Download/moerise-maestro-[a-f0-9]{32}\.txt", remote):
                    self.adb.run("shell", "rm", "-f", remote)
        mapping = f"tcp:{self.state['port']}"
        if not self.release and not self.state["reverse_preexisting"]:
            lines = self.adb.run("reverse", "--list").splitlines()
            if any(line.split()[-2:] == [mapping, mapping] for line in lines):
                self.adb.run("reverse", "--remove", mapping)
        network = self.network()
        baseline = self.state.get("network_before", network)
        changed = [key for key in NETWORK_KEYS if network.get(key) != baseline.get(key)]
        self.state["cleanup"] = {"packages_removed": results, "network_changed": changed, "restore_errors": [],
                                 "finished_at": now()}
        self.state["status"] = "cleaned" if all(results.values()) and not changed else "cleanup_attention"
        self.save()
        return self.state["cleanup"]


def discover_serial(executable):
    output = subprocess.check_output([executable, "devices", "-l"], encoding="utf-8", timeout=30)
    devices = [fields[0] for line in output.splitlines() if len(fields := line.split()) >= 2
               and fields[1] == "device" and not fields[0].startswith("emulator-")]
    if len(devices) != 1:
        raise CommandError("device_selection", f"检测到 {len(devices)} 台在线真机；连接并授权一台或指定 --serial")
    return devices[0]


def profile_args(args):
    relative = Path(".local/phone/profile.json")
    path = Path(args.profile) if args.profile else Path(args.repo) / relative
    profile = read_json(path) if args.profile or path.exists() else {}
    for field in ("apk", "fixture", "sessions_root", "baseline"):
        if getattr(args, field) is None and profile.get(field):
            setattr(args, field, (path.parent / profile[field]).resolve())
    for field in ("test_origin", "record", "fail_marker", "serial", "confirm_ready", "already_launched",
                  "non_interactive", "install", "adb", "maestro"):
        if getattr(args, field) is None and field in profile:
            setattr(args, field, profile[field])
    if not args.apk or not args.fixture:
        raise ValueError("在验收档案设置 apk 和 fixture，或传入 --apk 与 --fixture")
    args.sessions_root = Path(args.sessions_root or Path(args.repo) / ".local/phone/sessions").resolve()
    connection = read_json(args.fixture)
    args.test_origin = (args.test_origin or connection["origin"]).rstrip("/")
    if args.fail_marker and not re.fullmatch(r"[A-Za-z0-9_-]+(?: [A-Za-z0-9_-]+)*", args.fail_marker):
        raise ValueError("fail_marker 需使用可直接输入的 ASCII 标记")
    if args.fail_marker and len(args.fail_marker) > 90:
        raise ValueError("fail_marker 最多 90 字符")
    FixtureAPI(connection, trusted_https=True, test_origin=args.test_origin, allow_production=args.allow_production)
    if urllib.parse.urlsplit(args.test_origin).scheme != "https":
        raise ValueError("正式包真机需要独立 HTTPS 测试服务")


def active_directory(root):
    index = root / "active-session.json"
    if not index.exists():
        return None
    name = read_json(index)["session"]
    if not re.fullmatch(r"session-[A-Za-z0-9.-]+", name):
        raise ValueError("活动会话目录无效")
    return root / name


def session_history(repo, root):
    worktrees = [tree / ".local/phone" for tree in worktree_roots(repo)]
    roots = {root, *(tree / "sessions" for tree in worktrees)}
    legacy = (tree / "session/session.json" for tree in worktrees)
    return sorted({metadata for folder in roots for metadata in folder.glob("session-*/session.json")}
                  | {metadata for metadata in legacy if metadata.is_file()})


def device_resource_owner(repo, serial):
    key = hashlib.sha256(serial.encode()).hexdigest()[:16]
    return common_repo(repo) / ".local/android" / f"phone-resources-{key}.json"


def recover_device_sessions(repo, adb, commit, paths):
    owner = device_resource_owner(repo, adb.serial)
    indexed = None
    if owner.exists():
        entry = read_json(owner)
        indexed = Path(entry["session"])
        if entry["serial"] != adb.serial or not indexed.is_file():
            raise CommandError("previous_session_missing", "设备存在未归还的资源记录；先恢复对应的会话档案")
    for path in dict.fromkeys(path.resolve() for path in ([indexed] if indexed else []) + list(paths)):
        state = read_json(path)
        if state.get("serial") != adb.serial:
            if indexed and path == indexed.resolve():
                raise CommandError("previous_session_mismatch", "设备资源记录与会话序列号不一致")
            continue
        if not any(state.get(key) for key in ("resources_pending", "device_before", "source_network_before")):
            if indexed and path == indexed.resolve():
                owner.unlink(missing_ok=True)
            continue
        old = PhoneSession(repo, path.parent, path.parent, adb, commit)
        errors = old.restore_resources()
        if old.state.get("coverage"):
            old.coverage_report()
        elif old.release:
            old.summary()
        if errors:
            raise CommandError("previous_session_restore_failed", ", ".join(errors))


def select_session(args, adb, artifact):
    root = args.sessions_root
    root.mkdir(parents=True, exist_ok=True)
    directory = active_directory(root)
    paths = session_history(args.repo, root)
    recover_device_sessions(args.repo, adb, args.commit, paths)
    previous = read_json(directory / "session.json") if directory else {}
    app = APP.removesuffix(".fixture")
    same_device = previous.get("serial") == adb.serial
    same_apk = previous.get("artifacts", {}).get(app, {}).get("sha256") == artifact["sha256"]
    matching = (same_device and same_apk and previous.get("test_origin") == args.test_origin
                and previous.get("fixture") == str(Path(args.fixture).resolve()) and previous.get("status") != "cleaned")
    past_launch = first_launch(previous) if same_device and same_apk else None
    if matching:
        session = PhoneSession(args.repo, directory, Path(args.output or directory), adb, args.commit)
        session.state["artifacts"][app] = artifact
        session.state["allow_production"] = args.allow_production
        return session, past_launch
    directory = root / datetime.now(timezone.utc).strftime("session-%Y%m%dT%H%M%S.%fZ")
    session = PhoneSession(args.repo, directory, Path(args.output or directory), adb, args.commit)
    create_release_state(session, artifact, args.fixture, args.test_origin, args.allow_production)
    if not past_launch:
        for metadata in paths:
            history = read_json(metadata)
            launched = first_launch(history)
            if (history.get("serial") == adb.serial and launched
                    and history.get("artifacts", {}).get(app, {}).get("sha256") == artifact["sha256"]):
                past_launch = launched
                break
    write_json(root / "active-session.json", {"session": directory.name})
    return session, past_launch


def first_launch(state):
    if state.get("first_app_launch_at"):
        return state["first_app_launch_at"]
    observed = state.get("app_launch_attempted")
    if state.get("app_launch_time_verified"):
        return observed
    return bool(observed or state.get("app_launch_time_unknown"))


def ask(interactive, text):
    print(text, file=sys.stderr, flush=True)
    if not interactive:
        return ""
    try:
        return input("> ").strip()
    except EOFError:
        return ""


def complete_phase(session, checks, phase, prompt, missed=False):
    for item in checks.values():
        if item["phase"] != phase:
            continue
        name = item["id"]
        manual = session.state.setdefault("manual_results", {})
        previous = manual_evidence(manual.get(name, {}), phase)
        if previous.get("status") == "passed":
            continue
        if missed:
            manual[name] = {"status": "inconclusive", "window_missed": True,
                            "note": previous.get("note") if previous.get("window_missed") else
                            "此 APK 已启动，首次启动前的取证窗口已错过"}
            session.save()
            continue
        card = item["manual_card"]
        instruction = ("输入遥测 CSV 路径；如果已启动，输入 launched。" if phase == "pre_launch"
                       else "采证后输入证据文件路径，或输入 y 确认已采证。")
        reply = prompt(f"{name} · {card['start_state']}\n" + "\n".join(card["steps"]) +
                       f"\n{card['pass_criteria']}\n{card['evidence_hint']}\n{instruction}")
        if phase == "pre_launch" and reply == "launched":
            manual[name] = {"status": "inconclusive", "window_missed": True,
                            "note": "机主确认首次启动已发生，缺少启动前证据"}
            session.state["app_launch_time_unknown"] = True
        elif phase != "pre_launch" and reply.lower() == "y":
            manual[name] = {"status": "passed", "note": "机主确认已采证", "recorded_at": now()}
        elif reply and Path(reply.strip('"')).is_file():
            session.record(name, "passed", "", (reply.strip('"'),), refresh_report=False)
        else:
            raise CommandError(phase + "_evidence_pending", f"完成 {name} 取证并回填后续跑")
        session.save()


def verify(args, adb):
    if adb.serial.startswith("emulator-"):
        raise CommandError("physical_device_required", "正式包真机验收需要物理设备")
    checks = phone_checks(args.repo)
    selected = [item for name, item in checks.items()
                if (not args.check or name in args.check)
                and (not args.group or item["module"] in args.group)]
    if not selected:
        raise ValueError("所选分组与检查项没有交集")
    auto = [item for item in selected if item["kind"] == "auto"]
    if args.stop_after and not any(args.stop_after in (item["id"], item["flow"]) for item in auto):
        raise ValueError("--stop-after 必须匹配已选自动检查项或场景")
    if args.clear_data or args.uninstall:
        raise ValueError("verify 支持保留数据的覆盖安装")
    artifact = {"path": str(Path(args.apk).resolve()), "sha256": digest(args.apk)}
    session, past_launch = select_session(args, adb, artifact)
    session.maestro, session.record_video = args.maestro, args.record
    run_id = uuid.uuid4().hex
    marker = session.directory / "STOP_AFTER_CURRENT"
    marker.unlink(missing_ok=True)
    if not args.output:
        session.output = session.directory / "output" / ("verify-" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ"))
        session.output.mkdir(parents=True)
    view = {"run_id": run_id, "status": "running", "selected": [item["id"] for item in selected], "results": {}}
    session.state["coverage"] = view
    session.state["active_run_id"] = run_id
    session.state["cleanup_errors"] = []
    session.state.pop("batch_cleanup_errors", None)
    session.save()
    interactive = sys.stdin.isatty() and not args.non_interactive
    prompt = lambda text: ask(interactive, text)
    code = 1

    def report_failed(error):
        previous_error = view.get("error")
        view.update(status="failed", error={"cause": "coverage_report_failed", "message": str(error)})
        if previous_error:
            view["error"]["previous_error"] = previous_error

    def before_phone():
        if not (args.confirm_ready or session.state.get("source_receipt") or (interactive and prompt(
                f"若有未核验的登录账号，先在设备断网退出，停在空白登录页并恢复测试网络。"
                f"入口将仅登录 {args.test_origin}。确认后输入 y。").lower() == "y")):
            raise CommandError("test_account_confirmation_pending", "确认测试账号与来源后使用 --confirm-ready")
        missed = first_launch(session.state)
        complete_phase(session, checks, "pre_launch", prompt, missed=missed)
        session.capture_resources()

    try:
        if past_launch and not first_launch(session.state):
            if past_launch is True:
                session.state["app_launch_time_unknown"] = True
            else:
                session.state["first_app_launch_at"] = past_launch
        if adb.run("get-state").strip() != "device":
            raise CommandError("device_unavailable", "手机尚未连接")
        if proxy_enabled(session.network()):
            raise CommandError("proxy_present", "手机存在全局代理")
        installed = session.package_hash(session.app)
        if args.already_launched and installed == artifact["sha256"] and not first_launch(session.state):
            session.state["app_launch_time_unknown"] = True
        baseline = session.baseline_records(args.baseline)
        install = bool(args.install and installed != artifact["sha256"])
        view["install_needed"] = installed != artifact["sha256"]
        if view["install_needed"] and not install:
            if interactive and args.install is None:
                install = prompt("已装 APK 与档案不同。输入 y 授权覆盖安装，随后在设备 10 秒内点系统确认。").lower() == "y"
            if not install:
                raise CommandError("installed_hash_mismatch", "在手机安装指定 APK 并确认安装弹窗，或传 --install")
        if install:
            complete_phase(session, checks, "pre_install", prompt)
            print("安装时请在设备 10 秒内确认 USB 安装。", file=sys.stderr, flush=True)
        try:
            session.prepare_release(args.apk, args.fixture, args.test_origin, confirmed=True,
                                    allow_production=args.allow_production, install=install,
                                    artifact=artifact, installed=installed)
        except CommandError as error:
            if not install or error.code not in ("adb_error", "timeout", "installed_hash_mismatch"):
                raise
            if prompt("检查设备上的 USB 安装确认和倒计时，准备好后输入 y；入口仅重试一次。").lower() != "y":
                raise CommandError("installation_confirmation_pending", "安装未完成，检查设备安装确认后续跑") from None
            current = session.package_hash(session.app)
            session.prepare_release(args.apk, args.fixture, args.test_origin, confirmed=True,
                                    allow_production=args.allow_production, install=current != artifact["sha256"],
                                    artifact=artifact, installed=current)
        if any(item["phase"] == "pre_launch" for item in selected):
            complete_phase(session, checks, "pre_launch", prompt, missed=first_launch(session.state))
        if auto:
            results = session.run_checks(auto, baseline, args, run_id, lambda text: prompt(text).lower() == "y", before_phone)
            incomplete = len(results) != len(auto) and not view.get("stopped")
            code = int(bool(incomplete) or any(result["status"] not in REUSABLE and result.get("cause") != "capability_missing"
                                              for result in results.values()))
        else:
            code = 0
    except KeyboardInterrupt:
        view.update(status="interrupted", error={"cause": "interrupted"})
    except (CommandError, FixtureError, ValueError, OSError, KeyError, subprocess.SubprocessError) as error:
        view["error"] = {"cause": getattr(error, "code", type(error).__name__), "message": str(error)}
        if isinstance(error, FixtureError):
            view["error"].update(error.details())
    finally:
        if session.owns_resources:
            session.restore_resources()
        session.state.setdefault("cleanup_errors", []).extend(session.state.pop("batch_cleanup_errors", []))
        if session.state["cleanup_errors"]:
            code = 1
        if code == 0 and view.get("stopped"):
            code = 2
        if view["status"] == "running":
            view["status"] = {0: "completed", 1: "failed", 2: "stopped"}[code]
        session.state.pop("active_run_id", None)
        try:
            rendered = session.render_coverage_report(checks, view["selected"])
        except (ValueError, OSError) as error:
            report_failed(error)
            raise
        finally:
            session.save()
            if marker.exists() and marker.read_text(encoding="utf-8-sig").strip() == run_id:
                marker.unlink(missing_ok=True)
        try:
            report = session.publish_coverage_report(*rendered)
        except OSError as error:
            report_failed(error)
            session.save()
            raise
        print(json.dumps({"status": report["status"], "session": str(session.directory),
                          "report": str(session.directory / "report.md"), "error": report["error"],
                          "cleanup_errors": report["cleanup_errors"]}, ensure_ascii=False, indent=2))
    return code


def request_stop(directory):
    path = directory / "session.json"
    run_id = read_json(path).get("active_run_id")
    if not run_id:
        raise CommandError("no_active_run", "会话没有可停止的运行")
    write_atomic(directory / "STOP_AFTER_CURRENT", run_id + "\n")
    return {"status": "stop_requested", "run_id": run_id}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("verify", "stop", "prepare", "accept", "run", "ui", "manual", "status", "record", "cleanup"))
    parser.add_argument("--repo", default=str(Path(__file__).resolve().parents[2]))
    parser.add_argument("--session")
    parser.add_argument("--output")
    parser.add_argument("--commit")
    parser.add_argument("--adb")
    parser.add_argument("--serial", default=os.environ.get("ANDROID_SERIAL"))
    parser.add_argument("--release", action="store_true")
    parser.add_argument("--apk", type=Path)
    parser.add_argument("--test-origin")
    parser.add_argument("--confirm-ready", action=argparse.BooleanOptionalAction)
    parser.add_argument("--allow-production", action="store_true")
    parser.add_argument("--record", action=argparse.BooleanOptionalAction)
    parser.add_argument("--install", action=argparse.BooleanOptionalAction)
    parser.add_argument("--clear-data", action="store_true")
    parser.add_argument("--uninstall", action="store_true")
    parser.add_argument("--manifest")
    parser.add_argument("--fixture")
    parser.add_argument("--specialists", action="store_true")
    parser.add_argument("--group", action="append", choices=(*UI_GROUPS, *CASES, "recovery"))
    parser.add_argument("--check", action="append", choices=tuple(phone_checks(parser.get_default("repo"))))
    parser.add_argument("--retry", action="store_true")
    parser.add_argument("--outcome", choices=("passed", "failed", "inconclusive", "not_applicable", "environment_blocked"))
    parser.add_argument("--note", default="")
    parser.add_argument("--evidence", action="append", default=[])
    parser.add_argument("--first-launch-at")
    parser.add_argument("--flow", choices=FLOWS)
    parser.add_argument("--note-name")
    parser.add_argument("--maestro")
    parser.add_argument("--profile")
    parser.add_argument("--sessions-root")
    parser.add_argument("--baseline")
    parser.add_argument("--fail-marker")
    parser.add_argument("--already-launched", action=argparse.BooleanOptionalAction)
    parser.add_argument("--non-interactive", action=argparse.BooleanOptionalAction)
    parser.add_argument("--stop-after")
    args = parser.parse_args()
    if args.action != "verify" and not args.session and args.action not in ("status", "stop"):
        parser.error("需要 --session")
    if args.action == "verify" and args.session:
        parser.error("verify 通过 --sessions-root 管理活动会话")
    if args.action == "accept" and not args.release:
        parser.error("accept 需要 --release")
    if args.action == "record" and (len(args.check or []) + len(args.group or []) != 1 or not args.outcome):
        parser.error("record 需要一个 --check 或 --group，以及 --outcome")
    try:
        if args.action == "verify":
            profile_args(args)
        if args.action in ("stop", "status"):
            root = Path(args.sessions_root or Path(args.repo) / ".local/phone/sessions")
            directory = Path(args.session) if args.session else active_directory(root)
            path = directory / "session.json" if directory else None
            if args.action == "stop" and (not path or not path.exists()):
                raise CommandError("no_active_run", "找不到活动会话；指定 --session")
            result = request_stop(directory) if args.action == "stop" else (
                read_json(path) if path and path.exists() else {"status": "absent"})
            print(json.dumps(result, ensure_ascii=False, indent=2))
            return 0
        args.commit = args.commit or git(args.repo, "rev-parse", "HEAD")
        args.adb = args.adb or "adb"
        args.maestro = args.maestro or shutil.which("maestro") or shutil.which("maestro.bat") or "maestro"
        with device_lock(args.repo, args.action, args.commit):
            if args.action == "verify":
                return verify(args, Adb(args.adb, args.serial or discover_serial(args.adb)))
            return dispatch(args)
    except (CommandError, FixtureError, ValueError, OSError, KeyError, subprocess.SubprocessError) as error:
        report = {"status": "environment_blocked" if isinstance(error, CommandError) else "harness_error",
                  "cause": getattr(error, "code", type(error).__name__), "message": str(error)}
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 1


def dispatch(args):
    args.output = args.output or str(Path(args.session) / 'output' /
        datetime.now(timezone.utc).strftime(args.action + '-%Y%m%dT%H%M%S.%fZ'))
    state = Path(args.session) / 'session.json'
    if args.action == "record" and state.exists():
        args.serial = read_json(state)["serial"]
    if not args.serial:
        if state.exists():
            args.serial = read_json(state)['serial']
        else:
            args.serial = discover_serial(args.adb)
    session = PhoneSession(args.repo, args.session, args.output, Adb(args.adb, args.serial), args.commit)
    session.maestro = args.maestro
    session.record_video = args.record
    try:
        if session.release and not args.release:
            raise ValueError("正式包会话需要显式 --release")
        if args.release and session.state and not session.release:
            raise ValueError("fixture 会话不能使用 --release；选择独立目录")
        if args.action == "accept":
            if not args.apk or not args.fixture or not args.test_origin:
                raise ValueError("accept 需要 --apk、--fixture 和 --test-origin")
            session.prepare_release(args.apk, args.fixture, args.test_origin, args.confirm_ready,
                                    args.allow_production, args.install, args.clear_data, args.uninstall)
            result = session.run(args.group or list(DEFAULT_UI_GROUPS), args.retry)
        elif args.action == "prepare":
            if args.release:
                raise ValueError("正式包真机使用 accept")
            if not args.manifest or not args.fixture:
                raise ValueError("prepare 需要 --manifest 和 --fixture")
            result = session.prepare(args.manifest, args.fixture, args.specialists)
        elif args.action == "run":
            groups = args.group or list(DEFAULT_UI_GROUPS)
            result = session.run(groups, args.retry)
        elif args.action == "record":
            result = session.record((args.check or args.group)[0], args.outcome, args.note, args.evidence, args.first_launch_at)
        elif args.action == "ui":
            if not args.flow:
                raise ValueError("ui 需要 --flow")
            result = session.ui(args.flow, args.maestro, args.note_name, args.retry)
        elif args.action == "cleanup":
            result = session.cleanup(uninstall=args.uninstall)
        else:
            result = getattr(session, args.action)()
        print(json.dumps(result, ensure_ascii=False, indent=2))
        if args.action in ("run", "accept") and (session.state.get("cleanup_errors") or any(
                value.get("status") not in ("passed", "reused_pass") for value in result.values())):
            return 1
        if args.action == "ui" and result["status"] not in ("passed", "reused_pass"):
            return 1
        if args.action == "cleanup" and session.state["status"] != "cleaned":
            return 1
        return 0
    except (CommandError, FixtureError, ValueError, OSError) as error:
        report = {"status": "environment_blocked" if isinstance(error, CommandError) else "harness_error",
                  "cause": getattr(error, "code", type(error).__name__), "message": str(error)}
        write_json(Path(args.output) / "session-error.json", report)
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
