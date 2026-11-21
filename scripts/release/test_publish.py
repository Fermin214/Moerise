import json
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import patch

import publish
from publish_source import release_sha


SHA = 'a' * 40
REPO = 'owner/project'
RUN = '123'


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

    def test_only_matching_device_evidence_can_release_signed_apk(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            artifacts = root / 'android/android-release/v0.1.0'
            artifacts.mkdir(parents=True)
            apk = artifacts / 'moerise-android-v0.1.0.apk'
            apk.write_bytes(b'APK tested by the device')
            digest = publish.checksum(apk)
            apk.with_suffix('.apk.sha256').write_text(f'{digest}  {apk.name}\n')
            metadata = apk.with_suffix('.apk.json')
            info = dict(version_name='0.1.0', signed=True, commit=SHA, min_sdk=29, sha256=digest, apk=apk.name)
            metadata.write_text(json.dumps(info))
            result = root / 'android/android-release-checks/result.json'
            result.parent.mkdir()
            smoke = dict(run_id=RUN, status='passed', commit=SHA, api='29', apk_sha256=digest, secure_cookie=True,
                         session_restored_without_login=True,
                         flows={'release-smoke': {'status': 'passed', 'server_assertions': {'created_note_count': 1}}})
            upgrade_report = result.parent / 'upgrade-smoke/result.json'
            upgrade_report.parent.mkdir()
            upgrade_smoke = smoke | dict(flows={'upgrade-smoke': {'status': 'passed'}},
                upgrade={'from': '0.2.0', 'pending_write_count': 1, 'account': True, 'draft': True,
                         'keystore_session': True, 'local_database_restored': True})
            with patch.object(publish, 'output', return_value='moerise.minSdk=29\n'):
                with self.assertRaisesRegex(SystemExit, 'missing'):
                    publish.android_release(root, SHA, RUN)
                for changes in ({'status': 'failed'}, {'status': 'cancelled'}, {'apk_sha256': '0' * 64},
                                {'commit': 'b' * 40}, {'run_id': '122'}, {'api': '36'}, {'flows': {}},
                                {'session_restored_without_login': False}):
                    result.write_text(json.dumps(smoke | changes))
                    with self.subTest(changes=changes), self.assertRaises(SystemExit):
                        publish.android_release(root, SHA, RUN)
                result.write_text(json.dumps(smoke))
                with self.assertRaisesRegex(SystemExit, 'upgrade verification result is missing'):
                    publish.android_release(root, SHA, RUN)
                for changes in ({'status': 'failed'}, {'apk_sha256': '0' * 64}, {'commit': 'b' * 40},
                                {'run_id': '122'}, {'session_restored_without_login': False},
                                {'upgrade': upgrade_smoke['upgrade'] | {'local_database_restored': False}},
                                {'upgrade': upgrade_smoke['upgrade'] | {'pending_write_count': 2}}):
                    upgrade_report.write_text(json.dumps(upgrade_smoke | changes))
                    with self.subTest(upgrade_changes=changes), self.assertRaises(SystemExit):
                        publish.android_release(root, SHA, RUN)
                upgrade_report.write_text(json.dumps(upgrade_smoke))
                self.assertEqual(publish.android_release(root, SHA, RUN)[0], 'android-v0.1.0')
                for changes in ({'min_sdk': 26}, {'signed': False}):
                    metadata.write_text(json.dumps(info | changes))
                    with self.subTest(changes=changes), self.assertRaises(SystemExit):
                        publish.android_release(root, SHA, RUN)

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
            for arch in publish.ARCHES:
                linux = root / f'linux-{arch}'
                linux.mkdir()
                (linux / f'version-{arch}.txt').write_text('0.2.0')
                (linux / f'moerise_0.2.0_linux_{arch}.tar.gz').write_bytes(arch.encode())
                for component, suffix in [('core', ''), ('layout', '-layout')]:
                    (linux / f'{component}-{arch}.txt').write_text(f'ghcr.io/{REPO}{suffix}@sha256:' + 'a' * 64)
            android_files = [root / f'moerise-android-v0.2.0{suffix}'
                             for suffix in ('.apk', '.apk.sha256', '.apk.json', '-notices.zip')]
            guide = root / 'INSTALL.md'
            for asset in [*android_files, guide]:
                asset.write_bytes(asset.name.encode())
            with patch.dict('os.environ', GH_REPO=REPO), \
                    patch('sys.argv', ['publish.py', directory, SHA, 'both', '--run-id', RUN, '--dry-run']), \
                    patch.object(publish, 'prepare', return_value=(None, {})) as prepare, \
                    patch.object(publish, 'android_release') as android:
                android.return_value = 'android-v0.1.0', android_files, {}, ['--notes-file', str(guide)]
                with self.assertRaisesRegex(SystemExit, 'versions differ'):
                    publish.main()
                prepare.assert_not_called()
                android.return_value = 'android-v0.2.0', android_files, {}, ['--notes-file', str(guide)]
                publish.main()
                prepare.assert_called_once_with(REPO, SHA, 'v0.2.0', [
                    root / 'linux-amd64/moerise_0.2.0_linux_amd64.tar.gz',
                    root / 'linux-arm64/moerise_0.2.0_linux_arm64.tar.gz', *android_files, guide])
