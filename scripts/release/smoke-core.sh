#!/usr/bin/env bash
set -euo pipefail
smoke_root=$(mktemp -d)
smoke_port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
smoke_pid=
trap 'if [[ -n "$smoke_pid" ]]; then kill "$smoke_pid" 2>/dev/null || true; wait "$smoke_pid" 2>/dev/null || true; fi; rm -rf "$smoke_root"' EXIT
chmod 755 "$smoke_root"
node --input-type=module - "$smoke_root" "$smoke_port" <<'JS'
import { readFileSync, writeFileSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
const [root, port] = process.argv.slice(2);
const config = JSON.parse(readFileSync('config.example.json'));
config.state_dir = `${root}/state`;
config.index_dir = `${root}/index`;
config.cursor_secret = randomBytes(32).toString('hex');
config.access_tokens = [];
config.web = { enabled: true, public_origin: `http://127.0.0.1:${port}`, state_encryption_key: randomBytes(32).toString('base64') };
config.telemetry_forwarding = false;
config.notifications.enabled = false;
writeFileSync(`${root}/config.json`, JSON.stringify(config));
JS
if [[ "$1" == --image ]]; then
    docker run --rm --network host --user "$(id -u):$(id -g)" \
        --mount "type=bind,src=$smoke_root,dst=$smoke_root" "$2" \
        serve --config "$smoke_root/config.json" --port "$smoke_port" >"$smoke_root/server.log" 2>&1 &
else
    "$1" serve --config "$smoke_root/config.json" --host 127.0.0.1 --port "$smoke_port" >"$smoke_root/server.log" 2>&1 &
fi
smoke_pid=$!
if ! curl -fsS --retry 15 --retry-delay 1 --retry-connrefused --max-time 3 \
    "http://127.0.0.1:$smoke_port/health/live"; then
    cat "$smoke_root/server.log"
    exit 1
fi
curl -fsS --max-time 5 -o "$smoke_root/index.html" "http://127.0.0.1:$smoke_port/"
grep -q 'id="app"' "$smoke_root/index.html"
curl -fsS --compressed --max-time 5 -o /dev/null "http://127.0.0.1:$smoke_port/settings"
