#!/usr/bin/env bash
set -euo pipefail

# 与官方 scripts/install.sh 使用相同的 CLI ZIP 和 maestro/bin 目录。
android_maestro_version=2.12.1
android_maestro_sha256=54dbb0716425626604973cbb374c585ca30272375d9ce15177a1152765ddd9d7
android_maestro_cache="$HOME/.cache/moerise-maestro/$android_maestro_version"
android_maestro_archive="$android_maestro_cache/maestro.zip"
mkdir -p "$android_maestro_cache"
if [[ ! -f "$android_maestro_archive" ]]; then
    android_maestro_download="$(mktemp "$android_maestro_cache/download-XXXXXXXX")"
    trap 'rm -f "$android_maestro_download"' EXIT
    curl --fail --location --retry 3 --connect-timeout 20 --max-time 300 \
        "https://github.com/mobile-dev-inc/Maestro/releases/download/cli-$android_maestro_version/maestro.zip" \
        --output "$android_maestro_download"
    printf '%s  %s\n' "$android_maestro_sha256" "$android_maestro_download" | sha256sum --check
    mv "$android_maestro_download" "$android_maestro_archive"
fi
printf '%s  %s\n' "$android_maestro_sha256" "$android_maestro_archive" | sha256sum --check
android_maestro_install="$(mktemp -d "${RUNNER_TEMP:?需要 GitHub Actions RUNNER_TEMP}/maestro-XXXXXXXX")"
unzip -q "$android_maestro_archive" -d "$android_maestro_install"
chmod +x "$android_maestro_install/maestro/bin/maestro"
export MAESTRO_CLI_NO_ANALYTICS=true
export MAESTRO_DISABLE_UPDATE_CHECK=true
android_maestro_actual="$("$android_maestro_install/maestro/bin/maestro" --version)"
if ! printf '%s\n' "$android_maestro_actual" | tr -d '\r' | grep -Fxq "$android_maestro_version"; then
    echo "Maestro CLI 版本不匹配：$android_maestro_actual" >&2
    exit 1
fi
printf '%s\n' "$android_maestro_install/maestro/bin" >> "${GITHUB_PATH:?需要 GitHub Actions GITHUB_PATH}"
