#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root/mobile"
android_gradle :core:model:test :core:sync:test :core:markdown:test :androidApp:testDebugUnitTest :androidApp:lintDebug :androidApp:assembleDebug "$@"
python3 - <<'PY'
from pathlib import Path
import xml.etree.ElementTree as E
files = list(Path('androidApp/build/test-results/testDebugUnitTest').glob('TEST-*.xml'))
assert files, 'JVM 测试报告为空'
count = sum(int(E.parse(p).getroot().get('tests', 0)) for p in files)
assert count > 0, 'JVM 测试执行数量为零'
print(f'Android 单元检查执行 {count} 项测试。')
PY
