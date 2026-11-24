from pathlib import Path
import re
import subprocess
import threading

NETWORK_KEYS = (
    "http_proxy", "global_http_proxy_host", "global_http_proxy_port",
    "global_http_proxy_exclusion_list", "global_proxy_pac_url",
    "private_dns_mode", "private_dns_specifier",
)


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
    return old not in ("", "null", ":0") or host not in ("", "null") or pac not in ("", "null")


def network_settings(adb):
    return {key: adb.run("shell", "settings", "get", "global", key).strip() for key in NETWORK_KEYS}


def reverse_conflict(adb, mapping):
    for line in adb.run("reverse", "--list").splitlines():
        parts = line.split()
        if len(parts) >= 2 and parts[-2] == mapping and parts[-1] != mapping:
            return True
    return False
