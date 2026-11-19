"""Publish packages and registry digests from a verified main run."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess

ARCHES = ('amd64', 'arm64')


def output(*args):
    return subprocess.check_output(args, text=True).strip()


def checksum(path):
    with path.open('rb') as file:
        return hashlib.file_digest(file, 'sha256').hexdigest()


def version(value):
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', value):
        raise SystemExit(f'Invalid release version: {value}')
    return value


def linux_release(root, repo):
    value = version((root / 'linux-amd64/version-amd64.txt').read_text().strip())
    if (root / 'linux-arm64/version-arm64.txt').read_text().strip() != value:
        raise SystemExit('Linux package versions differ')
    files = [root / f'linux-{arch}/moerise_{value}_linux_{arch}.tar.gz' for arch in ARCHES]
    images = {}
    for component, suffix in [('core', ''), ('web', '-web'), ('layout', '-layout')]:
        image = f'ghcr.io/{repo.lower()}{suffix}'
        refs = []
        for arch in ARCHES:
            path = root / f'linux-{arch}/{component}-{arch}.txt'
            if not path.is_file():
                path = root / f'image-{component}-{arch}/{component}-{arch}.txt'
            refs.append(path.read_text().strip())
        if any(not re.fullmatch(re.escape(image) + r'@sha256:[0-9a-f]{64}', ref) for ref in refs):
            raise SystemExit(f'Invalid image digest for {image}')
        images[f'{image}:{value}'] = refs
    return f'v{value}', files, images, ['--generate-notes']


def android_release(root, sha, run_id):
    metadata, = (root / 'android/android-release').glob('*/moerise-android-*.apk.json')
    info = json.loads(metadata.read_text())
    value = version(info['version_name'])
    base = f'moerise-android-v{value}'
    apk = metadata.with_name(base + '.apk')
    digest = checksum(apk)
    report = root / 'android/android-release-checks/result.json'
    if not report.is_file():
        raise SystemExit('Android device verification result is missing')
    smoke = json.loads(report.read_text())
    properties = output('git', 'show', f'{sha}:mobile/gradle.properties')
    min_sdk, = re.findall(r'^moerise\.minSdk=(\d+)$', properties, re.M)
    flow = smoke.get('flows', {}).get('release-smoke', {})
    expected_info = dict(commit=sha, sha256=digest, apk=apk.name, min_sdk=int(min_sdk))
    expected_report = dict(status='passed', commit=sha, run_id=run_id, apk_sha256=digest)
    if (info.get('signed') is not True or any(info.get(key) != value for key, value in expected_info.items())
            or any(smoke.get(key) != value for key, value in expected_report.items())
            or str(smoke.get('api')) != min_sdk or smoke.get('secure_cookie') is not True
            or smoke.get('session_restored_without_login') is not True or flow.get('status') != 'passed'
            or flow.get('server_assertions', {}).get('created_note_count') != 1):
        raise SystemExit('Android APK does not match the successful signed smoke test')
    files = [metadata.with_name(base + suffix) for suffix in ('.apk', '.apk.sha256', '.apk.json', '-notices.zip')]
    if files[1].read_text().split() != [digest, apk.name]:
        raise SystemExit('Android APK checksum file does not match the package')
    return f'android-v{value}', files, {}, ['--notes-file', str(metadata.with_name('INSTALL.md'))]


def release_package(root, repo, sha, run_id, component):
    if component == 'android':
        return android_release(root, sha, run_id)
    tag, files, images, notes = linux_release(root, repo)
    if component == 'both':
        android_tag, android_files, _, android_notes = android_release(root, sha, run_id)
        if android_tag != f'android-{tag}':
            raise SystemExit(f'Linux and Android versions differ: {tag}, {android_tag}')
        files.extend([*android_files, Path(android_notes[1])])
    return tag, files, images, notes


def asset_digests(repo, release):
    digests = {}
    for asset in release['assets']:
        digest = asset.get('digest')
        if not digest:
            endpoint = f"repos/{repo}/releases/assets/{asset['id']}"
            with subprocess.Popen(['gh', 'api', '-H', 'Accept: application/octet-stream', endpoint],
                                  stdout=subprocess.PIPE) as download:
                digest = 'sha256:' + hashlib.file_digest(download.stdout, 'sha256').hexdigest()
                if download.wait():
                    raise SystemExit(f"Could not verify {asset['name']}")
        digests[asset['name']] = digest
    return digests


def tag_target(tag):
    ref = f'refs/tags/{tag}'
    ref_lines = output('git', 'ls-remote', '--tags', 'origin', ref, ref + '^{}').splitlines()
    targets = {name: commit for commit, name in (line.split('\t') for line in ref_lines)}
    return targets.get(ref + '^{}', targets.get(ref))


def prepare(repo, sha, tag, files):
    endpoint = f'repos/{repo}/releases'
    records = output('gh', 'api', '--paginate', endpoint, '--jq',
                     f'.[] | select(.tag_name == "{tag}") | @json').splitlines()
    if len(records) > 1:
        raise SystemExit(f'Multiple releases use {tag}')
    release = json.loads(records[0]) if records else None
    target = tag_target(tag)
    if target and target != sha:
        raise SystemExit(f'{tag} already points to {target}')
    if release and release['draft']:
        draft_target = release['target_commitish']
        if draft_target != sha:
            resolved = output('gh', 'api', f'repos/{repo}/commits/{draft_target}', '--jq', '.sha')
            if resolved != sha:
                raise SystemExit(f'Draft {tag} targets {draft_target}, expected {sha}')
    digests = {file.name: 'sha256:' + checksum(file) for file in files}
    if release and not release['draft']:
        if target != sha or asset_digests(repo, release) != digests:
            raise SystemExit(f'{tag} is already published with different packages')
    if release and {item['name'] for item in release['assets']} - digests.keys():
        raise SystemExit(f'{tag} contains assets outside the verified release')
    return release, digests


def upload(repo, sha, tag, files, notes, release, digests):
    android = tag.startswith('android-')
    endpoint = f'repos/{repo}/releases'
    if not release:
        title = f'Moerise Android {tag.removeprefix("android-v")}' if android else f'Moerise {tag}'
        subprocess.run(['gh', 'release', 'create', tag, '--draft', '--target', sha, '--title', title, *notes], check=True)
        release = json.loads(output('gh', 'api', '--paginate', endpoint, '--jq',
                                    f'.[] | select(.tag_name == "{tag}" and .draft) | @json'))
    release_path = f"{endpoint}/{release['id']}"
    current = asset_digests(repo, release)
    changed = [str(file) for file in files if current.get(file.name) != digests[file.name]]
    if changed:
        subprocess.run(['gh', 'release', 'upload', tag, '--clobber', *changed], check=True)
    confirmed = json.loads(output('gh', 'api', release_path))
    if not confirmed['draft'] or confirmed['tag_name'] != tag or asset_digests(repo, confirmed) != digests:
        raise SystemExit(f'Draft assets changed while preparing {tag}')
    return release_path, android


def promote_image(tag, refs):
    inspected = subprocess.run(['docker', 'buildx', 'imagetools', 'inspect', tag, '--raw'],
                               capture_output=True, text=True)
    if inspected.returncode == 0:
        actual = {item['digest'] for item in json.loads(inspected.stdout).get('manifests', [])}
        if actual != {ref.split('@')[1] for ref in refs}:
            raise SystemExit(f'{tag} is already published with different image digests')
        return
    if not re.search(r'not found|manifest unknown', inspected.stderr, re.I):
        raise SystemExit(inspected.stderr.strip())
    subprocess.run(['docker', 'buildx', 'imagetools', 'create', '--tag', tag, *refs], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('root', type=Path)
    parser.add_argument('sha')
    parser.add_argument('component', choices=['core-web', 'android', 'both'])
    parser.add_argument('--run-id', required=True)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--dry-run', action='store_true')
    mode.add_argument('--verify-only', action='store_true')
    args = parser.parse_args()
    repo = os.environ.get('GH_REPO') or os.environ.get('GITHUB_REPOSITORY')
    if not repo and (not args.verify_only or args.component != 'android'):
        parser.error('Set GH_REPO or GITHUB_REPOSITORY')
    tag, files, images, notes = release_package(args.root, repo, args.sha, args.run_id, args.component)
    required = files + ([Path(notes[1])] if notes[0] == '--notes-file' else [])
    if any(not path.is_file() or not path.stat().st_size for path in required):
        raise SystemExit(f'Incomplete release files for {tag}')
    if args.verify_only:
        return
    release, digests = prepare(repo, args.sha, tag, files)
    print(f'{tag} from {args.sha}', flush=True)
    if args.dry_run or (release and not release['draft']):
        return
    endpoint, android = upload(repo, args.sha, tag, files, notes, release, digests)
    for image, refs in images.items():
        promote_image(image, refs)
    target = tag_target(tag)
    if not target:
        output('gh', 'api', '--method', 'POST', f'repos/{repo}/git/refs',
               '-f', f'ref=refs/tags/{tag}', '-f', f'sha={args.sha}')
    elif target != args.sha:
        raise SystemExit(f'{tag} already points to {target}')
    published = json.loads(output('gh', 'api', '--method', 'PATCH', endpoint, '-F', 'draft=false',
                                  '-f', f'target_commitish={args.sha}',
                                  '-f', f'make_latest={str(not android).lower()}'))
    print(published['html_url'], flush=True)


if __name__ == '__main__':
    main()
