#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root/mobile"
android_build_cpu="${MOERISE_ANDROID_CPU:-$(python3 -c 'import os; print(min(os.sched_getaffinity(0)))')}"
taskset -c "$android_build_cpu" nice -n 10 ./gradlew --no-daemon --max-workers=1 :shared:jvmTest :androidApp:lintDebug :androidApp:assembleDebug "$@"
python3 - <<'PY'
from pathlib import Path
import xml.etree.ElementTree as E
files = list(Path('shared/build/test-results/jvmTest').glob('TEST-*.xml'))
assert files, 'JVM 测试报告为空'
count = sum(int(E.parse(p).getroot().get('tests', 0)) for p in files)
assert count > 0, 'JVM 测试执行数量为零'
print(f'共享 JVM 检查执行 {count} 项测试。')
PY
