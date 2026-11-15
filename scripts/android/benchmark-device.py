#!/usr/bin/env python3
"""Install benchmark APKs explicitly, run instrumentation and collect device outputs."""
import argparse
import json
from pathlib import Path
import re
import subprocess
import uuid


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--serial", required=True)
    parser.add_argument("--adb", default="adb")
    parser.add_argument("--variant", choices=("benchmark", "profile"), required=True)
    parser.add_argument("--classes", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9_.:-]+", args.serial):
        parser.error("设备序列号无效")
    repo = Path(__file__).resolve().parents[2]
    args.output.mkdir(parents=True, exist_ok=True)
    adb = [args.adb, "-s", args.serial]
    remote = "/sdcard/Android/media/io.github.fermin214.moerise.benchmark.runner/moerise-" + uuid.uuid4().hex
    result = dict(install_succeeded=False, instrumentation_succeeded=False, output_collected=False)

    def run(commands, log, timeout=180):
        with (args.output / log).open("wb") as output:
            process = subprocess.run([*adb, *commands], stdout=output, stderr=subprocess.STDOUT, timeout=timeout)
        text = (args.output / log).read_text(encoding="utf-8", errors="replace")
        if process.returncode:
            raise RuntimeError(f"ADB {commands[0]} 失败，退出码 {process.returncode}：\n{text}")
        return text

    try:
        for module, log in (("androidApp", "target-install.log"), ("benchmark", "runner-install.log")):
            directory = repo / f"mobile/{module}/build/outputs/apk/{args.variant}"
            metadata = json.loads((directory / "output-metadata.json").read_text())
            assert len(metadata["elements"]) == 1, "需要单个 APK"
            apk = (directory / metadata["elements"][0]["outputFile"]).resolve()
            assert apk.parent == directory.resolve() and apk.suffix == ".apk" and apk.is_file()
            text = run(["install", "-r", "-t", str(apk)], log)
            assert re.search(r"^Success\s*$", text, re.M), f"安装未返回 Success：\n{text}"
        result["install_succeeded"] = True
        run(["shell", "mkdir", "-p", remote], "output-directory.log")
        text = run([
            "shell", "am", "instrument", "-w", "-r", "-e", "class", args.classes,
            "-e", "additionalTestOutputDir", remote,
            "io.github.fermin214.moerise.benchmark.runner/androidx.test.runner.AndroidJUnitRunner",
        ], "instrumentation.log", timeout=14400)
        if (not re.search(r"^OK \([1-9][0-9]* tests?\)", text, re.M)
                or re.search(r"FAILURES!!!|INSTRUMENTATION_FAILED|Process crashed", text)):
            raise RuntimeError("性能设备流程失败；查看 instrumentation.log")
        result["instrumentation_succeeded"] = True
    finally:
        if result["install_succeeded"]:
            raw = args.output / "raw/device"
            raw.mkdir(parents=True, exist_ok=True)
            try:
                run(["pull", remote + "/.", str(raw)], "pull.log")
                result["output_collected"] = any(raw.rglob("*benchmarkData.json")) or any(raw.rglob("*-prof.txt"))
            finally:
                run(["shell", "rm", "-rf", remote], "output-cleanup.log")
        (args.output / "instrumentation.json").write_text(json.dumps(result, indent=2) + "\n")
    assert result["output_collected"], "设备输出为空；查看 pull.log"


if __name__ == "__main__":
    main()
