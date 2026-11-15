#!/usr/bin/env python3
"""Build or verify notices for the resolved release runtime, using cached Maven POMs."""
import argparse
import io
import os
from pathlib import Path
import re
import xml.etree.ElementTree as E
import zipfile

parser = argparse.ArgumentParser()
parser.add_argument("--check", action="store_true")
args = parser.parse_args()
root = Path(__file__).resolve().parents[2]
cache = Path(os.environ.get("GRADLE_USER_HOME", Path.home() / ".gradle")) / "caches/modules-2/files-2.1"
inventory = root / "mobile/androidApp/build/reports/release-dependencies.tsv"
destination = root / "docs/android/THIRD_PARTY_NOTICES.md"
namespace = {"m": "http://maven.apache.org/POM/4.0.0"}

def pom_info(coordinate, seen=()):
    assert coordinate not in seen, f"POM parent cycle: {coordinate}"
    group, artifact, version = coordinate.split(":")
    poms = sorted((cache / group / artifact / version).glob("*/*.pom"))
    assert poms, f"缺少已解析 POM：{coordinate}"
    doc = E.parse(poms[0]).getroot()
    licenses = [(item.findtext("m:name", "", namespace), item.findtext("m:url", "", namespace))
                for item in doc.findall("m:licenses/m:license", namespace)]
    if not licenses:
        parent = doc.find("m:parent", namespace)
        if parent is not None:
            parts = [parent.findtext("m:" + tag, "", namespace) for tag in ("groupId", "artifactId", "version")]
            licenses = pom_info(":".join(parts), (*seen, coordinate))
    assert licenses and all(name and url for name, url in licenses), f"缺少许可信息：{coordinate}"
    return licenses

def embedded_notices(blob, prefix=""):
    result = []
    with zipfile.ZipFile(blob) as archive:
        for name in sorted(archive.namelist()):
            if name == "classes.jar":
                result += embedded_notices(io.BytesIO(archive.read(name)), prefix + "classes.jar/")
            elif re.fullmatch(r"(LICENSE|NOTICE|COPYING)([._-].*)?", Path(name).name, re.I) and archive.getinfo(name).file_size < 256 * 1024:
                try:
                    text = archive.read(name).decode("utf-8")
                except UnicodeDecodeError:
                    continue
                result.append((prefix + name, text.replace("\r\n", "\n").replace("\r", "\n").rstrip() + "\n"))
    return result

rows = []
notices = {}
for line in sorted(set(inventory.read_text().splitlines())):
    coordinate, path = line.split("\t")
    licenses = pom_info(coordinate)
    rows.append("| `" + coordinate + "` | " + "；".join(f"[{name}]({url})" for name, url in licenses) + " |")
    for name, text in embedded_notices(Path(path)):
        safe = re.sub(r"[^A-Za-z0-9._-]", "_", coordinate + "-" + name)
        notices[safe + ".txt"] = text
assert rows, "发行运行时依赖清单为空"
body = """# Android 第三方许可

本清单来自 `androidApp` 的 `releaseRuntimeClasspath`。更新依赖后运行
`:androidApp:exportReleaseDependencies` 与 `python3 scripts/android/licenses.py`。

Apache License 2.0 全文见 [licenses/Apache-2.0.txt](licenses/Apache-2.0.txt)。
SQLite 原始引擎属于公有领域，说明见 [licenses/SQLite.txt](licenses/SQLite.txt)。
依赖归档内附带的许可和声明保存在 `licenses/artifacts/`。

| Maven 坐标 | 依赖声明的许可 |
| --- | --- |
""" + "\n".join(rows) + "\n"
expected = {destination: body}
expected.update({destination.parent / "licenses/artifacts" / name: text for name, text in notices.items()})
if args.check:
    for path, text in expected.items():
        assert path.is_file() and path.read_text() == text, f"第三方许可需要更新：{path.relative_to(root)}"
else:
    artifact_dir = destination.parent / "licenses/artifacts"
    for stale in artifact_dir.glob("*.txt"):
        if stale not in expected:
            stale.unlink()
    for path, text in expected.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
print(f"发行依赖：{len(rows)}；归档许可与声明：{len(notices)}。")
