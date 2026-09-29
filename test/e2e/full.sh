#!/bin/sh
# Starts/stops the Lanscape server (root namespace) and agents in the topo.sh namespaces:
# Full agents on n1, n2, n3 and rt; the pod runs a Lanscape Mini agent connected to the
# Mini-compatible port of the Full server (lite node).
# usage: full.sh server | agents <registration-token> | stop
set -eu
cd "$(dirname "$0")"
P=${E2E_PREFIX:-lse}
BIN=$(cd "${LANSCAPE_BIN:-../../dist/full}" && pwd)
MINI=$(cd "${LSM_BIN:-../../mini/build/linux-amd64}" && pwd)
OUT=${E2E_OUT:-$PWD/out}
GW=192.168.250.1:18443
MINI_TOKEN=e2e-mini-token

stop() {
    [ -f "$OUT/full.pids" ] || return 0
    while read -r pid; do kill "$pid" 2>/dev/null || true; done < "$OUT/full.pids"
    rm -f "$OUT/full.pids"
}

server() {
    stop
    mkdir -p "$OUT"
    rm -rf "$OUT/full-data"
    LANSCAPE_ADMIN_PASSWORD=e2e-admin-password "$BIN/lanscape" serve --data-dir "$OUT/full-data" \
        --listen 127.0.0.1:18090 --gateway-listen "$GW" --mini-listen 192.168.250.1:47711 \
        --mini-token "$MINI_TOKEN" --gateway-hosts 192.168.250.1 \
        --expect 10.30.0.0/24=1000,10.31.0.0/24=100 --log-level debug > "$OUT/full-server.log" 2>&1 &
    echo $! > "$OUT/full.pids"
}

agents() {
    token=$1
    for n in n1 n2 n3 rt; do
        rm -rf "$OUT/full-agent-$n"
        ip netns exec "$P-$n" "$BIN/lanscape-agent" --server "$GW" --token "$token" --name "$n" \
            --data-dir "$OUT/full-agent-$n" --exclude 'mgmt*' --log-level debug > "$OUT/full-agent-$n.log" 2>&1 &
        echo $! >> "$OUT/full.pids"
    done
    ip netns exec "$P-pod" "$MINI/lsm-agent" -s 192.168.250.1:47711 -t "$MINI_TOKEN" -n pod -x 'mgmt*' \
        > "$OUT/full-mini-pod.log" 2>&1 &
    echo $! >> "$OUT/full.pids"
}

case ${1:-} in
server) server ;;
agents) agents "$2" ;;
stop) stop ;;
*) echo "usage: $0 server|agents TOKEN|stop" >&2; exit 2 ;;
esac
