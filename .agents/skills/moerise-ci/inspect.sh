#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 2 || ! "$2" =~ ^[0-9]+$ || ! "$1" =~ ^(pr|run)$ ]]; then
    echo 'Usage: inspect.sh pr <number> | run <id>' >&2
    exit 2
fi

repo=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
if [[ $1 == pr ]]; then
    sha=$(gh pr view "$2" --repo "$repo" --json headRefOid --jq .headRefOid)
    echo "PR #$2  $sha"
    run=$(gh run list --repo "$repo" --workflow checks.yml --event pull_request --commit "$sha" \
        --limit 1 --json databaseId --jq '.[0].databaseId // empty')
    if [[ -z $run ]]; then
        echo '该提交没有 PR Checks 运行记录。' >&2
        exit 1
    fi
else
    run=$2
fi

api="repos/$repo/actions"
record=$(gh api "$api/runs/$run")
jq -r '[.name, .head_sha, .status, .conclusion, .html_url] | @tsv' <<< "$record"
gh api --paginate "$api/runs/$run/jobs?per_page=100" --jq '
    .jobs[] | [.name, (.conclusion // .status),
      ([.steps[]? | select(.conclusion == "failure") | .name][0] // "-"), .html_url] | @tsv'

echo '诊断产物'
gh api --paginate "$api/runs/$run/artifacts?per_page=100" --jq \
    '.artifacts[] | [.name, (if .expired then "expired" else "available" end)] | @tsv'
echo "日志 gh run view $run --repo $repo --log-failed"
echo "下载 gh run download $run --repo $repo -n <产物名> -D .local/ci/$run/<产物名>"

workflow=$(jq -r .workflow_id <<< "$record")
event=$(jq -r .event <<< "$record")
created=$(jq -r .created_at <<< "$record")
name=$(jq -r .name <<< "$record")
echo '上一次同名工作流成功记录'
gh api -X GET "$api/workflows/$workflow/runs" -f event="$event" -f status=success \
    -f created="<$created" -f per_page=100 |
    jq -r --arg name "$name" \
        '[.workflow_runs[] | select(.name == $name)][0] | if . then [.head_sha, .html_url] | @tsv else "最近 100 次成功记录中无匹配项" end'
