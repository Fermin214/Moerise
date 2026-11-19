#!/usr/bin/env bash
set -euo pipefail

if [[ ${1:-} == scope && ( $# == 3 || $# == 4 ) ]]; then
    base=$(git rev-parse --verify --end-of-options "$2^{commit}")
    first=$(git rev-parse --verify --end-of-options "$3^{commit}")
    candidate=$(git rev-parse --verify --end-of-options "${4:-HEAD}^{commit}")
    printf '目标 %s (%s)\n功能首提交 %s\n候选提交 %s\n' "$2" "$base" "$first" "$candidate"
    if ! git merge-base --is-ancestor "$first" "$candidate" || git merge-base --is-ancestor "$first" "$base"; then
        echo '功能首提交必须在候选提交历史中，且尚未进入目标分支。' >&2
        exit 1
    fi
    commits=$(git rev-list --reverse --topo-order "$base..$candidate")
    echo '目标分支以外的提交'
    git log --reverse --topo-order --oneline "$base..$candidate"
    echo 'PR 文件差异'
    git diff --name-status "$base...$candidate"
    foreign=()
    while IFS= read -r commit; do
        if ! git merge-base --is-ancestor "$first" "$commit"; then
            foreign+=("$commit")
        fi
    done <<< "$commits"
    if (( ${#foreign[@]} )); then
        echo '以下提交既未进入目标分支，也不源于功能首提交：' >&2
        git show -s --format='%h %s' "${foreign[@]}" >&2
        exit 1
    fi
    exit 0
fi

if [[ $# != 2 || ! ( $1 == workflow || ( $1 =~ ^(pr|run)$ && $2 =~ ^[0-9]+$ ) ) ]]; then
    echo 'Usage: inspect.sh scope <base-ref> <first-feature-commit> [candidate] | workflow <base-branch> | pr <number> | run <id>' >&2
    exit 2
fi

repo=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
actions_rules() {
    local branch
    branch=$(jq -rn --arg branch "$1" '$branch | @uri')
    gh api --paginate "repos/$repo/rules/branches/$branch?per_page=100" |
        jq -sc '[.[][] | select(.type == "required_status_checks")
            | .parameters.required_status_checks[] | select(.integration_id == 15368)
            | .context] | unique'
}

if [[ $1 == workflow ]]; then
    rules=$(actions_rules "$2")
    python3 "$(dirname "$0")/check-names.py" "$rules" < .github/workflows/checks.yml
    exit
fi

if [[ $1 == pr ]]; then
    pr=$(gh pr view "$2" --repo "$repo" --json headRefOid,baseRefName,state,mergeStateStatus)
    sha=$(jq -r .headRefOid <<< "$pr")
    base=$(jq -r .baseRefName <<< "$pr")
    jq -r --arg number "$2" '["PR #\($number)", .headRefOid, .baseRefName, .state, .mergeStateStatus] | @tsv' <<< "$pr"
    if rules=$(actions_rules "$base"); then
        echo "$base 生效的 ruleset 要求 / 此提交上 GitHub Actions 上报的结果"
        gh api --paginate "repos/$repo/commits/$sha/check-runs?per_page=100" |
            jq -sr --argjson rules "$rules" '
                [.[] | .check_runs[] | select(.app.id == 15368)] as $checks
                | if $rules == [] then "无绑定 GitHub Actions 的必需检查" else
                    $rules[] as $name | [$name, ([$checks[] | select(.name == $name)
                      | .conclusion // .status] | unique | join(", ") | if . == "" then "缺失" else . end)] | @tsv end'
    else
        echo "无法读取 $base 的 ruleset，必需检查未知。" >&2
    fi
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
latest=$(jq -r .run_attempt <<< "$record")
for ((attempt=1; attempt<=latest; attempt++)); do
    echo "Attempt $attempt/$latest  任务 / 结果 / 耗时 / 最慢步骤 / 首个异常步骤 / 日志"
    gh api --paginate "$api/runs/$run/attempts/$attempt/jobs?per_page=100" | jq -r --argjson latest "$((attempt == latest))" '
    def seconds: (.completed_at | fromdateiso8601) - (.started_at | fromdateiso8601);
    def abnormal: . != null and . != "success" and . != "skipped" and . != "neutral";
    .jobs[] | select($latest == 1 or (.conclusion | abnormal)) | [.name, (.conclusion // .status),
      (if .started_at and .completed_at then "\(seconds)s" else "-" end),
      ([.steps[]? | select(.started_at and .completed_at and .conclusion != "skipped")]
        | max_by(seconds) | if . then "\(.name) (\(seconds)s)" else "-" end),
      ([.steps[]? | select(.conclusion | abnormal) | .name][0] // "-"), .html_url] | @tsv'
    echo "日志 gh run view $run --repo $repo --attempt $attempt --log-failed"
done

echo '诊断产物'
gh api --paginate "$api/runs/$run/artifacts?per_page=100" --jq \
    '.artifacts[] | [.id, .name, .created_at, (if .expired then "expired" else "available" end)] | @tsv'
echo "按 ID 下载 gh api repos/$repo/actions/artifacts/<ID>/zip > <ID>.zip"

workflow=$(jq -r .workflow_id <<< "$record")
event=$(jq -r .event <<< "$record")
created=$(jq -r .created_at <<< "$record")
name=$(jq -r .name <<< "$record")
echo '上一次同名工作流成功记录'
gh api -X GET "$api/workflows/$workflow/runs" -f event="$event" -f status=success \
    -f created="<$created" -f per_page=100 |
    jq -r --arg name "$name" \
        '[.workflow_runs[] | select(.name == $name)][0] | if . then [.head_sha, .html_url] | @tsv else "最近 100 次成功记录中无匹配项" end'
