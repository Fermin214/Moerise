#!/usr/bin/env python3
"""Persistent USB device acceptance; installation, checks and cleanup are separate actions."""
import argparse
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
import threading
import time
import urllib.parse
import urllib.error
import urllib.request
from maestro_runner import (
    DRIVERS, MaestroRunner, catalog, flow_digest, input_method_checkpoint,
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
NETWORK_KEYS = (
    "http_proxy", "global_http_proxy_host", "global_http_proxy_port",
    "global_http_proxy_exclusion_list", "global_proxy_pac_url",
    "private_dns_mode", "private_dns_specifier",
)
UI_GROUPS = tuple(catalog()["groups"])
DEFAULT_UI_GROUPS = UI_GROUPS
FLOWS = tuple(catalog()["flows"])


def now():
    return datetime.now(timezone.utc).isoformat()


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def write_json(path, value):
    path = Path(path)
    temporary = path.with_suffix(path.suffix + ".tmp")
    payload = json.dumps(value, ensure_ascii=False, indent=2) + "\n"
    for attempt in range(10):
        try:
            temporary.write_text(payload, encoding="utf-8")
            os.replace(temporary, path)
            return
        except OSError as error:
            if getattr(error, "winerror", None) not in (5, 32, 33) or attempt == 9:
                raise
            time.sleep(.05)


def read_json(path):
    return json.loads(Path(path).read_text(encoding="utf-8-sig"))


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


class CommandError(RuntimeError):
    def __init__(self, code, message, output=""):
        super().__init__(message)
        self.code, self.output = code, output


class Adb:
    def __init__(self, executable, serial):
        if not re.fullmatch(r"[A-Za-z0-9_.:-]+", serial):
            raise ValueError("无效的设备序列号")
        self.executable, self.serial = executable, serial

    def run(self, *args, timeout=30, log=None, binary=False, check=True):
        command = [self.executable, "-s", self.serial, *args]
        if not log:
            try:
                process = subprocess.run(command, capture_output=True, timeout=timeout)
            except subprocess.TimeoutExpired as error:
                raise CommandError("timeout", "ADB 操作超时", error.stdout or b"") from None
            if process.returncode and process.stderr:
                process.stdout += b"\n" + process.stderr
            result = process.stdout if binary else process.stdout.decode("utf-8", "replace")
            if check and process.returncode:
                raise CommandError("adb_error", f"ADB {args[0]} 失败", result)
            return result
        # Stream each status line immediately; a deadline must retain the last completed step.
        process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        lines = []
        with Path(log).open("wb") as stream:
            def drain():
                for line in iter(process.stdout.readline, b""):
                    stream.write(line)
                    stream.flush()
                    lines.append(line)
                    text = line.decode("utf-8", "replace").strip()
                    if text.startswith("INSTRUMENTATION_STATUS: acceptance_step="):
                        print(text.partition("acceptance_step=")[2], flush=True)

            thread = threading.Thread(target=drain, daemon=True)
            thread.start()
            timed_out = False
            try:
                process.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                timed_out = True
                process.kill()
                process.wait(timeout=5)
            thread.join(timeout=5)
        output = b"".join(lines).decode("utf-8", "replace")
        if timed_out:
            raise CommandError("timeout", f"ADB 操作超过 {timeout} 秒；保留已输出步骤", output)
        if check and process.returncode:
            raise CommandError("adb_error", f"ADB {args[0]} 失败", output)
        return output


def proxy_enabled(network):
    old = network.get("http_proxy", "").strip()
    host = network.get("global_http_proxy_host", "").strip()
    pac = network.get("global_proxy_pac_url", "").strip()
    return (old not in ("", "null", ":0") or host not in ("", "null") or pac not in ("", "null"))


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
        return {key: self.adb.run("shell", "settings", "get", "global", key).strip()
                for key in NETWORK_KEYS}

    def restore_device_input(self):
        try:
            restore_input_method(self.adb, input_method_checkpoint(self.repo, self.adb.serial))
        except RuntimeError as error:
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
        for line in self.adb.run("reverse", "--list").splitlines():
            parts = line.split()
            if len(parts) >= 2 and parts[-2] == mapping and parts[-1] != mapping:
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
        identity = hashlib.sha256(name.encode())
        scripts = ["phone-session.py"]
        if name in UI_GROUPS or name in FLOWS:
            identity.update(flow_digest(self.repo, name).encode())
            scripts.append("maestro_runner.py")
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
        self.adb.run("shell", "am", "start", "-W", "-n", self.app + "/io.github.fermin214.moerise.MainActivity")
        return {"status": "manual_ready", "app": self.app, "connection": "USB",
                "session": str(self.directory), "cleanup": "显式 cleanup 才卸载"}

    def record(self, group, outcome, note):
        if not self.state or self.state.get("status") == "cleaned":
            raise ValueError("会话不存在或已清理")
        self.state.setdefault("manual_results", {})[group] = {
            "kind": "manual", "status": outcome, "note": note, "recorded_at": now()}
        self.save()
        if self.release:
            self.summary()
        return self.state["manual_results"][group]

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
                        install=False, clear_data=False, uninstall=False):
        if self.adb.serial.startswith("emulator-"):
            raise ValueError("正式包模拟器使用 release-smoke.py；此入口只接收真机")
        if not confirmed:
            raise ValueError("先人工完成装包、首次系统确认，并确认 App 已退出生产账号，再传 --confirm-ready")
        data = read_json(fixture)
        FixtureAPI(data, trusted_https=True, test_origin=test_origin, allow_production=allow_production)
        if urllib.parse.urlsplit(data["origin"]).scheme != "https":
            raise ValueError("正式包真机需要独立 HTTPS 测试服务")
        app = APP.removesuffix(".fixture")
        artifact = {"path": str(Path(apk).resolve()), "sha256": digest(apk)}
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
            self.directory.mkdir(parents=True, exist_ok=True)
            self.state = {"schema": 1, "app": app, "mode": "release-phone", "serial": self.adb.serial,
                "commit": self.commit, "fixture": str(Path(fixture).resolve()), "test_origin": test_origin,
                "allow_production": allow_production, "port": urllib.parse.urlsplit(test_origin).port,
                "created_at": now(), "status": "preparing", "artifacts": {app: artifact}, "results": {},
                "files": [], "reverse_preexisting": True}
            self.save()
        if uninstall and self.package_paths(app):
            self.adb.run("uninstall", app, timeout=90)
        if install:
            self.adb.run("install", "-r", str(Path(apk).resolve()), timeout=120)
        if self.package_hash(app) != artifact["sha256"]:
            raise CommandError("installed_hash_mismatch", "先人工安装指定 APK，或显式传 --install")
        if clear_data:
            self.adb.run("shell", "pm", "clear", app)
        if uninstall or install or clear_data:
            self.state["results"] = {}
        self.state["status"] = "ready"
        self.save()

    def device_checkpoint(self):
        return {"wifi_on": self.adb.run("shell", "settings", "get", "global", "wifi_on").strip(),
                "mobile_data": self.adb.run("shell", "settings", "get", "global", "mobile_data").strip(),
                "stay_on_while_plugged_in": self.adb.run("shell", "settings", "get", "global", "stay_on_while_plugged_in").strip(),
                "screen_off_timeout": self.adb.run("shell", "settings", "get", "system", "screen_off_timeout").strip()}

    def restore_checkpoint(self):
        saved = self.state.get("device_before")
        errors = []
        if not saved:
            return errors
        for key, value in saved.items():
            try:
                namespace = "system" if key == "screen_off_timeout" else "global"
                current = self.adb.run("shell", "settings", "get", namespace, key).strip()
                if current == value:
                    continue
                if key in ("wifi_on", "mobile_data"):
                    if value not in ("0", "1"):
                        raise ValueError("原网络状态无效")
                    self.adb.run("shell", "svc", "wifi" if key == "wifi_on" else "data",
                                 "enable" if value == "1" else "disable")
                elif value == "null":
                    self.adb.run("shell", "settings", "delete", namespace, key)
                elif value.isdigit():
                    self.adb.run("shell", "settings", "put", namespace, key, value)
                else:
                    raise ValueError("原亮屏设置无效")
                if self.adb.run("shell", "settings", "get", namespace, key).strip() != value:
                    raise RuntimeError("设备状态恢复读回不一致")
            except (CommandError, OSError, ValueError, RuntimeError):
                errors.append(key)
        if not errors:
            self.state.pop("device_before", None)
        self.save()
        return errors

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
                    "last_step": item.get("last_step")})
        report = {"mode": "release-phone", "app": self.app, "apk": self.state["artifacts"][self.app],
                  "serial": self.adb.serial, "items": rows,
                  "cleanup_errors": self.state.get("cleanup_errors", []),
                  "manual_results": self.state.get("manual_results", {}),
                  "visual_review": "集中复核关键截图和录像中的日历、图标、流式残影及布局"}
        write_json(self.directory / "report.json", report)
        lines = ["# Android 正式包真机验收", "", "| 模块 | 场景 | 状态 | 分类 | 证据 |", "| --- | --- | --- | --- | --- |"]
        for row in rows:
            paths = [*row["screenshots"], *row["recordings"], *([row["log"]] if row["log"] else [])]
            links = " · ".join("[" + Path(path).name + "](" + Path(path).resolve().as_uri() + ")" for path in paths)
            lines.append(f"| {row['module']} | {row['flow']} | {row['status']} | {row['category']} | {links} |")
        for group, item in report["manual_results"].items():
            lines.append(f"| {group} | manual | {item['status']} | manual | {item['note'].replace('|', '/').replace(chr(10), ' ')} |")
        lines.extend(["", report["visual_review"], "", "设备恢复错误：" + ", ".join(report["cleanup_errors"])])
        (self.directory / "report.md").write_text("\n".join(lines) + "\n", encoding="utf-8")
        return report

    def run_release(self, groups, retry=False):
        if any(group not in UI_GROUPS for group in groups):
            raise ValueError("正式包使用 catalog 中的 UI 模块；instrumentation 仅用于 fixture")
        self.require_ready()
        self.verify_artifacts()
        errors = self.restore_checkpoint()
        if errors:
            raise CommandError("device_restore_failed", ", ".join(errors))
        self.state["device_before"] = self.device_checkpoint()
        if not self.state.get("resources_pending"):
            self.state["maestro_preexisting"] = {package: bool(self.package_paths(package)) for package in DRIVERS}
            self.state["forwards_before"] = self.adb.run("forward", "--list").splitlines()
        self.state["resources_pending"] = True
        self.save()
        outcome, pending = {}, []
        try:
            runner = MaestroRunner(self.repo, self.adb, self.state["fixture"], self.output / "maestro",
                self.maestro, release=True, phone=True,
                test_origin=self.state["test_origin"], allow_production=self.state.get("allow_production", False), record=self.record_video)
            for group in groups:
                previous = self.state["results"].get(group)
                if previous and previous["status"] == "passed" and not retry and previous.get("flow_sha256") == self.execution_digest(group):
                    outcome[group] = {**previous, "status": "reused_pass"}
                else:
                    pending.append(group)
            flows = list(dict.fromkeys(flow for group in pending for flow in runner.catalog["groups"][group]))
            def checkpoint(flow, result):
                self.remember_maestro(result)
                for group in pending:
                    if flow not in runner.catalog["groups"][group]:
                        continue
                    saved = self.state["results"].setdefault(group, {"status": "inconclusive", "flows": {}})
                    saved["flows"][flow] = result
                    saved["status"] = "inconclusive"
                    saved.pop("flow_sha256", None)
                self.save()
            results = runner.run_batch(flows, on_result=checkpoint) if flows else {}
            for group in pending:
                selected = {flow: results[flow] for flow in runner.catalog["groups"][group]}
                for result in selected.values():
                    self.remember_maestro(result)
                status = next((item["status"] for item in selected.values() if item["status"] != "passed"), "passed")
                outcome[group] = {"status": status, "flows": selected, "flow_sha256": self.execution_digest(group)}
                self.state["results"][group] = outcome[group]
            self.save()
        finally:
            self.state["cleanup_errors"] = self.restore_checkpoint()
            operations = [("input_restore", self.restore_device_input)]
            for package in reversed(DRIVERS):
                operations.append(("driver_stop", lambda package=package: self.adb.run("shell", "am", "force-stop", package)))
                if not self.state.get("maestro_preexisting", {}).get(package, True):
                    operations.append(("driver_remove", lambda package=package: self.adb.run("uninstall", package, timeout=90)))
            operations.append(("app_stop", lambda: self.adb.run("shell", "am", "force-stop", self.app)))
            try:
                existing = self.adb.run("forward", "--list", check=False).splitlines()
            except (CommandError, OSError):
                existing = []
                self.state["cleanup_errors"].append("forward_readback")
            for line in set(existing) - set(self.state.get("forwards_before", existing)):
                parts = line.split()
                if len(parts) == 3 and parts[0] == self.adb.serial:
                    self.state.setdefault("maestro_forward_bindings", {})[parts[1]] = parts[2]
            for mapping, target in self.state.get("maestro_forward_bindings", {}).items():
                if f"{self.adb.serial} {mapping} {target}" in existing:
                    operations.append(("forward_remove", lambda mapping=mapping: self.adb.run("forward", "--remove", mapping)))
            for label, operation in operations:
                try:
                    operation()
                except (CommandError, OSError):
                    self.state["cleanup_errors"].append(label)
            if not self.state["cleanup_errors"]:
                self.state.pop("resources_pending", None)
            self.save()
            self.summary()
        return outcome

    def cleanup(self, uninstall=False):
        if not self.state:
            raise ValueError("会话不存在")
        if self.state["serial"] != self.adb.serial:
            raise ValueError("会话与手机不匹配")
        if self.state.get("status") == "cleaned":
            return self.state["cleanup"]
        if self.adb.run("get-state").strip() != "device":
            raise CommandError("device_unavailable", "手机尚未连接")
        self.restore_device_input()
        restore_errors = self.restore_checkpoint() if self.release else []
        results = {}
        for package in self.state["artifacts"] if uninstall else ():
            if package not in (*APK_PATHS, APP.removesuffix(".fixture")):
                continue
            if self.package_paths(package):
                text = self.adb.run("uninstall", package, timeout=90)
                if not re.search(r"^Success\s*$", text, re.M):
                    raise CommandError("cleanup_failed", "卸载没有返回 Success")
            results[package] = not bool(self.package_paths(package))
        self.adb.run("shell", "rm", "-f", CREDENTIAL)
        for package, preexisting in self.state.get("maestro_preexisting", {}).items():
            if package in DRIVERS and not preexisting:
                if self.package_paths(package):
                    self.adb.run("uninstall", package, timeout=90)
                results[package] = not bool(self.package_paths(package))
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
        self.state["cleanup"] = {"packages_removed": results, "network_changed": changed, "restore_errors": restore_errors,
                                 "finished_at": now()}
        self.state["status"] = "cleaned" if all(results.values()) and not changed and not restore_errors else "cleanup_attention"
        self.save()
        return self.state["cleanup"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "accept", "run", "ui", "manual", "status", "record", "cleanup"))
    parser.add_argument("--repo", default=str(Path(__file__).resolve().parents[2]))
    parser.add_argument("--session", required=True)
    parser.add_argument("--output")
    parser.add_argument("--commit")
    parser.add_argument("--adb", default="adb")
    parser.add_argument("--serial", default=os.environ.get("ANDROID_SERIAL"))
    parser.add_argument("--release", action="store_true")
    parser.add_argument("--apk", type=Path)
    parser.add_argument("--test-origin")
    parser.add_argument("--confirm-ready", action="store_true")
    parser.add_argument("--allow-production", action="store_true")
    parser.add_argument("--record", action="store_true")
    parser.add_argument("--install", action="store_true")
    parser.add_argument("--clear-data", action="store_true")
    parser.add_argument("--uninstall", action="store_true")
    parser.add_argument("--manifest")
    parser.add_argument("--fixture")
    parser.add_argument("--specialists", action="store_true")
    parser.add_argument("--group", action="append", choices=(*UI_GROUPS, *CASES, "recovery"))
    parser.add_argument("--retry", action="store_true")
    parser.add_argument("--outcome", choices=("passed", "failed", "inconclusive"))
    parser.add_argument("--note", default="")
    parser.add_argument("--flow", choices=FLOWS)
    parser.add_argument("--note-name")
    parser.add_argument("--maestro", default=shutil.which("maestro") or shutil.which("maestro.bat") or "maestro")
    args = parser.parse_args()
    args.output = args.output or str(Path(args.session) / 'output' /
        datetime.now(timezone.utc).strftime(args.action + '-%Y%m%dT%H%M%S.%fZ'))
    args.commit = args.commit or subprocess.check_output(
        ['git', '-C', args.repo, 'rev-parse', 'HEAD'], text=True).strip()
    if args.release and not args.serial:
        parser.error("--release 必须显式指定 --serial")
    if args.action == "accept" and not args.release:
        parser.error("accept 需要 --release")
    if not args.serial:
        state = Path(args.session) / 'session.json'
        if state.exists():
            args.serial = read_json(state)['serial']
        else:
            devices = subprocess.check_output([args.adb, 'devices'], text=True)
            ready = re.findall(r'^([^\s]+)\s+device$', devices, re.M)
            if len(ready) != 1:
                parser.error('传入唯一真机的 --serial')
            args.serial = ready[0]
    session = PhoneSession(args.repo, args.session, args.output, Adb(args.adb, args.serial), args.commit)
    session.maestro = args.maestro
    session.record_video = args.record
    try:
        if session.release and not args.release:
            parser.error("正式包会话需要显式 --release")
        if args.release and session.state and not session.release:
            parser.error("fixture 会话不能使用 --release；选择独立目录")
        if args.action == "accept":
            if not args.apk or not args.fixture or not args.test_origin:
                parser.error("accept 需要 --apk、--fixture 和 --test-origin")
            session.prepare_release(args.apk, args.fixture, args.test_origin, args.confirm_ready,
                                    args.allow_production, args.install, args.clear_data, args.uninstall)
            result = session.run(args.group or list(DEFAULT_UI_GROUPS), args.retry)
        elif args.action == "prepare":
            if args.release:
                parser.error("正式包真机使用 accept")
            if not args.manifest or not args.fixture:
                parser.error("prepare 需要 --manifest 和 --fixture")
            result = session.prepare(args.manifest, args.fixture, args.specialists)
        elif args.action == "status":
            result = session.state or {"status": "absent"}
        elif args.action == "run":
            groups = args.group or list(DEFAULT_UI_GROUPS)
            result = session.run(groups, args.retry)
        elif args.action == "record":
            if not args.group or len(args.group) != 1 or not args.outcome:
                parser.error("record 需要一个 --group 和 --outcome")
            result = session.record(args.group[0], args.outcome, args.note)
        elif args.action == "ui":
            if not args.flow:
                parser.error("ui 需要 --flow")
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
