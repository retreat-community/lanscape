#!/bin/sh
# Runs the e2e suite inside a privileged container (CI runners use Docker-in-Docker).
# usage (from the repository root):
#   docker run --rm --privileged -v "$PWD:/src" -w /src mcr.microsoft.com/playwright:v1.56.1-noble \
#     test/e2e/ci.sh mini|full|all
set -eu
suite=${1:-all}
GO_VERSION=${GO_VERSION:-1.23.12}
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq iproute2 iptables curl ca-certificates iperf3 > /dev/null
if ! command -v go > /dev/null; then
    arch=$(dpkg --print-architecture)
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" | tar -C /usr/local -xz
    export PATH=/usr/local/go/bin:$PATH
fi
# iptables in containers may need the legacy backend
iptables -t nat -L > /dev/null 2>&1 || update-alternatives --set iptables /usr/sbin/iptables-legacy > /dev/null 2>&1 || true
modprobe 8021q 2> /dev/null || true
corepack enable > /dev/null 2>&1 || npm install -g pnpm@9 > /dev/null
export E2E_OUT=$PWD/test/e2e/out
# Full binaries: use prebuilt ones when given, otherwise build them (the web UI is embedded
# only when internal/server/webdist was built before)
if [ -z "${LANSCAPE_BIN:-}" ] || [ ! -x "${LANSCAPE_BIN}/lanscape" ]; then
    export LANSCAPE_BIN=$PWD/test/e2e/out/bin
    mkdir -p "$LANSCAPE_BIN"
    CGO_ENABLED=0 go build -o "$LANSCAPE_BIN/" ./cmd/lanscape ./cmd/lanscape-agent
fi
mkdir -p "$E2E_OUT" test/e2e/screenshots
(cd test/ui && pnpm install --frozen-lockfile > /dev/null)
status=0
run_suite() {
    name=$1
    test=$2
    spec=$3
    stop=$4
    E2E_KEEP=1 go test -tags e2e -count=1 -timeout 40m -v -run "$test" ./test/e2e/ || status=1
    if [ $status -eq 0 ]; then
        (cd test/ui && pnpm exec playwright test "$spec") || status=1
    fi
    sh -c "$stop" || true
    test/e2e/topo.sh down || true
    echo "== $name: $([ $status -eq 0 ] && echo ok || echo FAILED)"
}
case $suite in
mini | all) run_suite mini TestMini mini.spec.ts "test/e2e/mini.sh stop" ;;
esac
case $suite in
full | all) run_suite full TestFull full.spec.ts "test/e2e/full.sh stop" ;;
esac
chmod -R a+rwX test/e2e/out test/e2e/screenshots 2> /dev/null || true
exit $status
