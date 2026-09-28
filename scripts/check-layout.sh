#!/bin/sh
# Verifies that the fixed repository layout is present.
set -eu
cd "$(dirname "$0")/.."
missing=0
for p in go.mod cmd/lanscape cmd/lanscape-agent internal/proto internal/netio internal/testengine \
  internal/agent internal/server internal/store internal/discovery internal/fingerprint \
  internal/monitor internal/notify internal/pki internal/topo web mini/common mini/agent \
  mini/server mini/web mini/tools packaging/nfpm packaging/openwrt packaging/docker \
  deploy/helm/lanscape deploy/k8s/mini deploy/compose test/e2e docs/SPEC.md docs/decisions \
  LICENSE NOTICE README.md README.ru.md Makefile .github/workflows/ci.yml; do
  if [ ! -e "$p" ]; then
    echo "missing: $p" >&2
    missing=1
  fi
done
exit "$missing"
