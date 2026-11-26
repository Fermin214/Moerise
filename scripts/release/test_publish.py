import json
import os
from pathlib import Path
import re
import subprocess
import sys
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import patch

import publish
from publish_source import release_sha


SHA = 'a' * 40
REPO = 'owner/project'
RUN = '123'
ROOT = Path(__file__).resolve().parents[2]


def write_android_artifact(root, sha, version='0.2.0'):
    apk_dir = root / 'android/android-release' / f'v{version}-code123-{sha[:7]}'
    apk_dir.mkdir(parents=True)
    apk = apk_dir / f'moerise-android-v{version}.apk'
    apk.write_bytes(b'APK')
    digest = publish.checksum(apk)
    apk.with_suffix('.apk.sha256').write_text(f'{digest}  {apk.name}\n')
    properties = publish.output('git', '-C', str(ROOT), 'show', f'{sha}:mobile/gradle.properties')
    min_sdk = int(re.search(r'^moerise\.minSdk=(\d+)$', properties, re.M)[1])
    metadata = apk.with_suffix('.apk.json')
    info = dict(version_name=version, signed=True, commit=sha, min_sdk=min_sdk,
                version_code=123, sha256=digest, apk=apk.name)
    metadata.write_text(json.dumps(info))
    for name in (f'{apk.stem}-notices.zip', 'INSTALL.md', 'mapping.txt'):
        (apk_dir / name).write_bytes(name.encode())
    return metadata, info


def write_linux_artifacts(root, version='0.2.0'):
    for arch in publish.ARCHES:
        artifact = root / f'linux-{arch}'
        artifact.mkdir(exist_ok=True)
        (artifact / f'version-{arch}.txt').write_text(version)
        (artifact / f'moerise_{version}_linux_{arch}.tar.gz').write_bytes(arch.encode())
        for component, suffix in [('core', ''), ('layout', '-layout')]:
            (artifact / f'{component}-{arch}.txt').write_text(f'ghcr.io/{REPO}{suffix}@sha256:' + 'a' * 64)


def verify_release(root, sha, component):
    return subprocess.run(
        [sys.executable, '-B', str(ROOT / 'scripts/release/publish.py'), str(root), sha, component, '--verify-only'],
        cwd=ROOT, env=dict(os.environ, GH_REPO=REPO), capture_output=True, text=True,
    )


class PublishTests(unittest.TestCase):
    def test_only_successful_main_push_run_can_supply_packages(self):
        run = dict(id=123, workflow_id=45, head_repository={'full_name': REPO},
                   head_branch='main', event='push', status='completed', conclusion='success', head_sha=SHA)
        self.assertEqual(release_sha(run, REPO, '45', RUN), SHA)
        for changes in ({'conclusion': 'cancelled'}, {'conclusion': 'failure'}, {'status': 'in_progress'},
                        {'event': 'pull_request'}, {'head_branch': 'topic'}, {'head_repository': {'full_name': 'fork/project'}},
                        {'workflow_id': 56}, {'id': 124}):
            with self.subTest(changes=changes), self.assertRaises(SystemExit):
                release_sha(run | changes, REPO, '45', RUN)

    def test_verify_only_requires_downloaded_android_artifact_layout(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            sha = publish.output('git', '-C', str(ROOT), 'rev-parse', 'HEAD')
            metadata, _ = write_android_artifact(root, sha)
            write_linux_artifacts(root)
            _, android_files, _, _ = publish.release_package(root, REPO, sha, 'android')
            self.assertEqual(android_files, [metadata.with_suffix('')])
            for component in ('android', 'both'):
                with self.subTest(component=component):
                    result = verify_release(root, sha, component)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    for asset in sorted(metadata.parent.iterdir()):
                        with self.subTest(missing=asset.name):
                            moved = asset.rename(root / asset.name)
                            try:
                                result = verify_release(root, sha, component)
                                self.assertEqual(result.returncode, 1, result.stderr)
                                expected = 'Android APK metadata file' if asset == metadata else asset.name
                                self.assertIn(expected, result.stderr)
                            finally:
                                moved.rename(asset)
                    shallow = metadata.parent.rename(root / 'android' / metadata.parent.name)
                    try:
                        result = verify_release(root, sha, component)
                        self.assertEqual(result.returncode, 1, result.stderr)
                        self.assertIn('Android APK metadata file', result.stderr)
                    finally:
                        shallow.rename(metadata.parent)

    def test_signed_android_metadata_must_match_package(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            sha = publish.output('git', '-C', str(ROOT), 'rev-parse', 'HEAD')
            metadata, info = write_android_artifact(root, sha)
            for changes in (
                {'min_sdk': info['min_sdk'] + 1}, {'signed': False}, {'signed': 1},
                {'commit': 'b' * 40}, {'sha256': '0' * 64}, {'apk': 'other.apk'},
                {'version_code': 0}, {'version_code': -1}, {'version_code': True},
                {'version_code': '123'}, {'version_code': 123.0},
            ):
                with self.subTest(changes=changes), self.assertRaises(SystemExit):
                    metadata.write_text(json.dumps(info | changes))
                    publish.android_release(root, sha)
            metadata.write_text(json.dumps(info))
            metadata.with_suffix('.sha256').write_text('wrong checksum')
            with self.assertRaisesRegex(SystemExit, 'checksum file'):
                publish.android_release(root, sha)

    def test_reject_existing_version_on_another_commit_before_upload(self):
        tag = 'android-v0.1.0'
        old = 'b' * 40
        for draft in (True, False):
            release = json.dumps(dict(draft=draft, target_commitish=old, assets=[]))
            for tag_exists in (True, False):
                tag_output = f'{old}\trefs/tags/{tag}' if tag_exists else ''
                with self.subTest(draft=draft, tag_exists=tag_exists), self.assertRaises(SystemExit), \
                        patch.object(publish, 'output', side_effect=[release, tag_output, old]):
                    publish.prepare(REPO, SHA, tag, [])
        with patch.object(publish, 'output', side_effect=['', f'{old}\trefs/tags/{tag}']), self.assertRaises(SystemExit):
            publish.prepare(REPO, SHA, tag, [])

    def test_joint_release_prepares_one_tag_with_both_platforms(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            sha = publish.output('git', '-C', str(ROOT), 'rev-parse', 'HEAD')
            metadata, _ = write_android_artifact(root, sha)
            with patch.dict('os.environ', GH_REPO=REPO), \
                    patch('sys.argv', ['publish.py', directory, sha, 'both', '--dry-run']), \
                    patch.object(publish, 'prepare', return_value=(None, {})) as prepare:
                write_linux_artifacts(root, '0.1.0')
                with self.assertRaisesRegex(SystemExit, 'versions differ'):
                    publish.main()
                prepare.assert_not_called()
                write_linux_artifacts(root)
                publish.main()
                prepare.assert_called_once_with(REPO, sha, 'v0.2.0', [
                    root / 'linux-amd64/moerise_0.2.0_linux_amd64.tar.gz',
                    root / 'linux-arm64/moerise_0.2.0_linux_arm64.tar.gz', metadata.with_suffix('')])

    def test_release_notes_skip_author_attribution_and_pr_suffix(self):
        tags = '\n'.join(['v0.3.0', 'android-v0.1.0', 'v0.2.0', 'v0.1.2'])
        log = '\n'.join([
            '修复 Android 离线发现 (#22)',
            '内嵌 Web 与统一通知渠道 (#20)',
        ])
        with patch.object(publish, 'output', side_effect=[tags, log]):
            notes = publish.release_notes(REPO, SHA, 'v0.3.0')
        self.assertNotIn('by @', notes)
        self.assertEqual(
            notes,
            '- 修复 Android 离线发现\n'
            '- 内嵌 Web 与统一通知渠道\n\n'
            f'完整提交对比：https://github.com/{REPO}/compare/v0.2.0...v0.3.0',
        )

    def test_upload_creates_draft_with_generated_notes_instead_of_github_auto_notes(self):
        draft = json.dumps(dict(id=99, draft=True, tag_name='v0.3.0', assets=[]))
        with patch.object(publish, 'release_notes', return_value='- 变更说明'), \
                patch.object(publish, 'output', side_effect=[draft, draft]), \
                patch.object(publish.subprocess, 'run') as run:
            publish.upload(REPO, SHA, 'v0.3.0', [], [], None, {})
        command = run.call_args.args[0]
        self.assertNotIn('--generate-notes', command)
        self.assertEqual(command[-2:], ['--notes', '- 变更说明'])
