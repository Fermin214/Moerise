"""Get the release commit from a completed Main CI push run."""
import json
import re
import sys


def release_sha(run, repo, workflow_id, run_id):
    if (str(run['id']) != run_id or str(run['workflow_id']) != workflow_id
            or run['head_repository']['full_name'] != repo or run['head_branch'] != 'main'
            or run['event'] != 'push' or run['status'] != 'completed' or run['conclusion'] != 'success'
            or not re.fullmatch('[0-9a-f]{40}', run['head_sha'])):
        raise SystemExit('Release requires a completed successful Main CI push run on main')
    return run['head_sha']


if __name__ == '__main__':
    print(release_sha(json.load(sys.stdin), *sys.argv[1:]))
