#!/usr/bin/env python3
import argparse
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
UI = list(json.loads((ROOT / 'maestro/catalog.json').read_text())['groups'])
NATIVE = ['lifecycle', 'ime', 'recovery', 'stars']
GROUPS = json.loads((ROOT / 'device-suites.json').read_text())
members = [suite for suites in GROUPS.values() for suite in suites]
if sorted(members) != sorted(UI + NATIVE):
    raise SystemExit('设备分组必须各覆盖一次全部 UI 和专项流程')


def select(suite):
    if suite == 'all':
        return members
    if suite == 'ui':
        return UI
    if suite in GROUPS:
        return GROUPS[suite]
    if suite in UI + NATIVE:
        return [suite]
    raise ValueError('未知设备检查组：' + suite)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('suite')
    parser.add_argument('--matrix', action='store_true')
    args = parser.parse_args()
    try:
        selected = select(args.suite)
        suites = (list(GROUPS) if args.suite == 'all' else selected if args.suite == 'ui' else [args.suite]) if args.matrix else selected
    except ValueError as error:
        parser.error(str(error))
    print(json.dumps(suites) if args.matrix else '\n'.join(suites))
