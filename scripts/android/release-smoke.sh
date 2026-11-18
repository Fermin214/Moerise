#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/environment.sh"
cd "$android_repo_root"
export ANDROID_SERIAL="${ANDROID_SERIAL:-emulator-5554}"
[[ "$ANDROID_SERIAL" =~ ^emulator-[0-9]+$ ]]
android_release_min_sdk="$(sed -n 's/^moerise\.minSdk=//p' mobile/gradle.properties)"
[[ "$android_release_min_sdk" =~ ^[0-9]+$ ]]
[[ "$(timeout 15 adb shell getprop ro.build.version.sdk | tr -d '\r')" == "$android_release_min_sdk" ]]
android_release_apk="${1:?需要指定已签名的 APK}"
test -s "$android_release_apk"
mkdir -p .local/android
android_release_fixture="$(mktemp -d "$android_repo_root/.local/android/release-fixture-XXXXXXXX")"
chmod 700 "$android_release_fixture"
android_release_fixture_pid="" android_release_tls_pid=""
android_release_device_ca=""
cleanup_android_release_smoke() {
    if [[ -n "$android_release_device_ca" ]]; then
        timeout 20 adb shell sh -s -- "$android_release_device_ca" <<'DEVICE' || true
set -eu
staging=$1
ca_store=/system/etc/security/cacerts
setenforce 0
trap 'setenforce 1' EXIT
for zygote in $(pidof zygote64 zygote); do
    if nsenter -t "$zygote" -m -- sh -c 'test -f "$1"' sh "$ca_store/${staging##*/}.0"; then
        nsenter -t "$zygote" -m -- umount "$ca_store"
    fi
done
rm -rf "$staging"
DEVICE
    fi
    if [[ -f "$android_release_fixture/lifecycle.json" ]]; then
        if python3 - "$android_release_fixture/lifecycle.json" <<'PY'
import json, sys, urllib.request
state = json.load(open(sys.argv[1]))
request = urllib.request.Request(state["origin"] + "/dispose", method="POST",
    headers={"Authorization": "Bearer " + state["token"]})
with urllib.request.urlopen(request, timeout=5):
    pass
PY
        then
            wait "$android_release_fixture_pid" 2>/dev/null || true
            android_release_fixture_pid=""
        fi
    fi
    for android_release_pid in "$android_release_tls_pid" "$android_release_fixture_pid"; do
        if [[ -n "$android_release_pid" ]]; then
            kill -TERM -- "-$android_release_pid" 2>/dev/null || true
            wait "$android_release_pid" 2>/dev/null || true
        fi
    done
    timeout 10 adb reverse --remove tcp:28443 >/dev/null 2>&1 || true
    mkdir -p .local/android/release-smoke
    for android_release_log in fixture core tls; do
        if [[ -f "$android_release_fixture/$android_release_log.log" ]]; then
            cp "$android_release_fixture/$android_release_log.log" ".local/android/release-smoke/$android_release_log.log"
        fi
    done
    rm -rf "$android_release_fixture"
}
trap cleanup_android_release_smoke EXIT
umask 077
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 1 \
    -subj '/CN=Moerise isolated release CA' \
    -keyout "$android_release_fixture/ca.key" -out "$android_release_fixture/ca.pem" \
    > "$android_release_fixture/openssl.log" 2>&1
openssl req -newkey rsa:2048 -nodes -sha256 -subj '/CN=localhost' \
    -keyout "$android_release_fixture/tls.key" -out "$android_release_fixture/tls.csr" \
    >> "$android_release_fixture/openssl.log" 2>&1
cat > "$android_release_fixture/tls.ext" <<'EOF'
subjectAltName=DNS:localhost
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
EOF
openssl x509 -req -sha256 -days 1 -in "$android_release_fixture/tls.csr" \
    -CA "$android_release_fixture/ca.pem" -CAkey "$android_release_fixture/ca.key" \
    -CAcreateserial -extfile "$android_release_fixture/tls.ext" \
    -out "$android_release_fixture/tls.pem" >> "$android_release_fixture/openssl.log" 2>&1
android_release_ca_hash="$(openssl x509 -in "$android_release_fixture/ca.pem" -subject_hash_old -noout)"
echo '为新启动的应用加载隔离服务 CA'
timeout 20 adb root
timeout 30 adb wait-for-device
[[ "$(timeout 10 adb shell id -u | tr -d '\r')" == 0 ]]
android_release_device_ca="/data/local/tmp/$android_release_ca_hash"
timeout 10 adb shell mkdir "$android_release_device_ca"
timeout 15 adb push "$android_release_fixture/ca.pem" "$android_release_device_ca/$android_release_ca_hash.0"
timeout 45 adb shell sh -s -- "$android_release_device_ca" <<'DEVICE'
set -ex
staging=$1
ca_store=/system/etc/security/cacerts
cert=${staging##*/}.0
test ! -e "$ca_store/$cert"
cp "$ca_store"/* "$staging/"
setenforce 0
trap 'setenforce 1' EXIT
context=$(ls -Zd "$ca_store")
chown -R root:root "$staging"
chmod 755 "$staging"
chmod 644 "$staging"/*
chcon -R "${context%% *}" "$staging"
zygotes=$(pidof zygote64 zygote)
test -n "$zygotes"
for zygote in $zygotes; do
    nsenter -t "$zygote" -m -- sh -c '
        set -ex
        if ! test -f "$2/$3"; then
            mount -o bind "$1" "$2"
        fi
        cmp "$1/$3" "$2/$3"
    ' sh "$staging" "$ca_store" "$cert"
done
DEVICE
[[ "$(timeout 10 adb shell getenforce | tr -d '\r')" == Enforcing ]]
timeout 10 adb shell input keyevent 82
timeout 15 adb reverse tcp:28443 tcp:28443
export MOERISE_ANDROID_FIXTURE_ROOT="$android_release_fixture"
export MOERISE_ANDROID_FIXTURE_ORIGIN=https://localhost:28443
export MOERISE_ANDROID_FIXTURE_PORT=28442
setsid scripts/android/start-fixture.sh > "$android_release_fixture/fixture.log" 2>&1 &
android_release_fixture_pid=$!
python3 - "$android_release_fixture/connection.json" "$android_release_fixture_pid" <<'PY'
from pathlib import Path
import os, sys, time
deadline = time.monotonic() + 180
while not Path(sys.argv[1]).exists():
    os.kill(int(sys.argv[2]), 0)
    if time.monotonic() >= deadline:
        raise SystemExit("HTTPS 隔离服务启动超时")
    time.sleep(1)
PY
setsid node scripts/android/usb-https.mjs --origin "$MOERISE_ANDROID_FIXTURE_ORIGIN" \
    --upstream http://127.0.0.1:28442 --cert "$android_release_fixture/tls.pem" \
    --key "$android_release_fixture/tls.key" --proxy-port 28444 \
    > "$android_release_fixture/tls.log" 2>&1 &
android_release_tls_pid=$!
export SSL_CERT_FILE="$android_release_fixture/ca.pem"
python3 - <<'PY'
import time, urllib.request
deadline = time.monotonic() + 20
while True:
    try:
        with urllib.request.urlopen("https://localhost:28443/health/ready", timeout=5) as response:
            assert response.status == 200
        break
    except OSError:
        if time.monotonic() >= deadline:
            raise
        time.sleep(.5)
PY
python3 scripts/android/release-smoke.py --serial "$ANDROID_SERIAL" \
    --fixture "$android_release_fixture/connection.json" --apk "$android_release_apk" \
    --output .local/android/release-smoke
