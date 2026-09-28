#!/bin/sh
# Starts/stops lsm-server (root namespace) and lsm-agent in every node namespace of topo.sh.
# usage: mini.sh start|stop
# env: LSM_BIN (directory with lsm-server/lsm-agent), E2E_OUT (logs), E2E_HTTP (server listen)
set -eu
cd "$(dirname "$0")"
P=${E2E_PREFIX:-lse}
BIN=$(cd "${LSM_BIN:-../../mini/build/linux-amd64}" && pwd)
OUT=${E2E_OUT:-$PWD/out}
HTTP=${E2E_HTTP:-127.0.0.1:18080}
CTL=${E2E_CTL:-192.168.250.1:47701}
TOKEN=e2e-mini-token

stop() {
    [ -f "$OUT/mini.pids" ] || return 0
    while read -r pid; do kill "$pid" 2>/dev/null || true; done < "$OUT/mini.pids"
    rm -f "$OUT/mini.pids"
}

start() {
    stop
    mkdir -p "$OUT"
    rm -f "$OUT/mini-runs.json"
    cat > "$OUT/mini-server.conf" <<CONF
token=$TOKEN
listen=$HTTP
ctl_listen=$CTL
data_file=$OUT/mini-runs.json
duration_ms=${E2E_DURATION_MS:-2000}
streams=4
ping_count=5
expect=10.30.0.0/24:1000,10.31.0.0/24:100
webhook=${E2E_WEBHOOK:-}
CONF
    "$BIN/lsm-server" -c "$OUT/mini-server.conf" > "$OUT/mini-server.log" 2>&1 &
    echo $! > "$OUT/mini.pids"
    for n in n1 n2 n3 rt pod; do
        ip netns exec "$P-$n" "$BIN/lsm-agent" -s "${CTL%:*}" -t "$TOKEN" -n "$n" -x 'mgmt*' \
            > "$OUT/mini-agent-$n.log" 2>&1 &
        echo $! >> "$OUT/mini.pids"
    done
}

case ${1:-} in
start) start ;;
stop) stop ;;
*) echo "usage: $0 start|stop" >&2; exit 2 ;;
esac
