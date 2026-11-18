#!/usr/bin/env bash
set -euo pipefail

version=${1:?Supply the release version}
: "${GITHUB_SERVER_URL:?}" "${GITHUB_REPOSITORY:?}" "${GITHUB_SHA:?}"

build_image() {
    docker build --label "org.opencontainers.image.source=$GITHUB_SERVER_URL/$GITHUB_REPOSITORY" \
        --label "org.opencontainers.image.revision=$GITHUB_SHA" \
        --label "org.opencontainers.image.version=$version" --tag "moerise-ci-$1" "$2"
}

smoke_http() (
    local component=$1 port=$2 route=$3 expected=$4 container address attempt response
    container=$(docker run -d -p "127.0.0.1::$port" "moerise-ci-$component")
    trap 'docker rm --force "$container" >/dev/null' EXIT
    address=$(docker port "$container" "$port/tcp")
    for ((attempt = 1; attempt <= 30; attempt++)); do
        if response=$(curl -fsS --connect-timeout 1 --max-time 2 "http://$address$route" 2>&1) &&
            jq -e --arg expected "$expected" '.status == $expected' <<< "$response" >/dev/null 2>&1; then
            if [[ "$component" == web ]]; then
                curl -fsS --max-time 5 -o /dev/null "http://$address/"
            fi
            printf '%s HTTP smoke passed\n' "$component"
            return 0
        fi
        if [[ "$(docker inspect --format '{{.State.Running}}' "$container")" != true ]]; then
            break
        fi
        sleep 1
    done
    printf '%s health check failed: %s\n' "$component" "$response" >&2
    docker logs "$container" >&2
    return 1
)

build_image core .
test "$(docker run --rm moerise-ci-core version)" = "$version"
build_image web web
smoke_http web 3000 /health/live ok
build_image layout services/layout
smoke_http layout 8766 /health ready
