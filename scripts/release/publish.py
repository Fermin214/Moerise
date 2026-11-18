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


def android_release(root, sha):
    metadata, = (root / 'android/android-release').glob('*/moerise-android-*.apk.json')
    info = json.loads(metadata.read_text())
    value = version(info['version_name'])
    base = f'moerise-android-v{value}'
    apk = metadata.with_name(base + '.apk')
    digest = checksum(apk)
    smoke = json.loads((root / 'android/android-release-checks/result.json').read_text())
    if (info['commit'] != sha or info['signed'] is not True or info['sha256'] != digest
            or smoke['status'] != 'passed' or smoke['commit'] != sha
            or smoke['apk_sha256'] != digest or str(smoke['api']) != '26'):
        raise SystemExit('Android APK does not match the successful signed smoke test')
    files = [metadata.with_name(base + suffix) for suffix in ('.apk', '.apk.sha256', '.apk.json', '-notices.zip')]
    if files[1].read_text().split() != [digest, apk.name]:
        raise SystemExit('Android APK checksum file does not match the package')
    return f'android-v{value}', files, {}, ['--notes-file', str(metadata.with_name('INSTALL.md'))]


def prepare(repo, sha, tag, files):
    endpoint = f'repos/{repo}/releases'
    records = output('gh', 'api', '--paginate', endpoint, '--jq',
                     f'.[] | select(.tag_name == "{tag}") | @json').splitlines()
    if len(records) > 1:
        raise SystemExit(f'Multiple releases use {tag}')
    release = json.loads(records[0]) if records else None
    ref = f'refs/tags/{tag}'
    ref_lines = output('git', 'ls-remote', '--tags', 'origin', ref, ref + '^{}').splitlines()
    targets = {name: commit for commit, name in (line.split('\t') for line in ref_lines)}
    target = targets.get(ref + '^{}', targets.get(ref))
    if target and target != sha:
        raise SystemExit(f'{tag} already points to {target}')
    digests = {file.name: 'sha256:' + checksum(file) for file in files}
    if release and not release['draft']:
        assets = {asset['name']: asset['digest'] for asset in release['assets']}
        if target != sha or any(assets.get(name) != digest for name, digest in digests.items()):
            raise SystemExit(f'{tag} is already published with different packages')
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
    current = {asset['name']: asset['digest'] for asset in release['assets']}
    changed = [str(file) for file in files if current.get(file.name) != digests[file.name]]
    if changed:
        subprocess.run(['gh', 'release', 'upload', tag, '--clobber', *changed], check=True)
    confirmed = json.loads(output('gh', 'api', release_path))
    assets = {asset['name']: asset['digest'] for asset in confirmed['assets']}
    if not confirmed['draft'] or any(assets.get(name) != digest for name, digest in digests.items()):
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
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()
    repo = os.environ['GH_REPO']
    releases = []
    if args.component != 'android':
        releases.append(linux_release(args.root, repo))
    if args.component != 'core-web':
        releases.append(android_release(args.root, args.sha))
    prepared = [(tag, files, images, notes, *prepare(repo, args.sha, tag, files))
                for tag, files, images, notes in releases]
    for tag, files, images, notes, release, digests in prepared:
        print(f'{tag} from {args.sha}', flush=True)
        if args.dry_run or (release and not release['draft']):
            continue
        endpoint, android = upload(repo, args.sha, tag, files, notes, release, digests)
        for image, refs in images.items():
            promote_image(image, refs)
        published = json.loads(output('gh', 'api', '--method', 'PATCH', endpoint, '-F', 'draft=false',
                                      '-f', f'target_commitish={args.sha}',
                                      '-f', f'make_latest={str(not android).lower()}'))
        print(published['html_url'], flush=True)


if __name__ == '__main__':
    main()
