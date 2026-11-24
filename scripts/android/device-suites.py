#!/usr/bin/env python3
import argparse
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
UI = list(json.loads((ROOT / 'maestro/catalog.json').read_text())['groups'])
NATIVE = ['lifecycle', 'ime', 'recovery', 'stars']
GROUPS = json.loads((ROOT / 'device-suites.json').read_text())
ALIASES = {'records': ['records-edits', 'records-browse']}
members = [suite for suites in GROUPS.values() for suite in suites]
if sorted(members) != sorted(UI + NATIVE):
    raise SystemExit('设备分组必须各覆盖一次全部 UI 和专项流程')


def select(suite):
    if suite == 'all':
        return members
    if suite == 'ui':
        return UI
    if suite in ALIASES:
        return [member for group in ALIASES[suite] for member in GROUPS[group]]
    if suite in GROUPS:
        return GROUPS[suite]
    if suite in UI + NATIVE:
        return [suite]
    raise ValueError('未知设备检查组：' + suite)


def matrix(suite):
    if suite == 'all':
        return list(GROUPS)
    if suite == 'ui':
        return UI
    return ALIASES.get(suite, [suite])


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('suite')
    parser.add_argument('--matrix', action='store_true')
    args = parser.parse_args()
    try:
        selected = select(args.suite)
    except ValueError as error:
        parser.error(str(error))
    print(json.dumps(matrix(args.suite)) if args.matrix else '\n'.join(selected))
