#!/bin/sh
# Builds deb, rpm and apk packages of lsm-agent and lsm-server with nfpm.
# usage: package-mini-nfpm.sh <mini-bins-dir> <version> <out-dir>
set -eu
bins=$(cd "$1" && pwd)
version=${2#v}
out=$3
mkdir -p "$out"
cd "$(dirname "$0")/.."
for t in linux-amd64:amd64 linux-arm64:arm64 linux-armv7:arm7 linux-armv6:arm6 linux-386:386; do
    dir=${t%%:*}
    arch=${t##*:}
    [ -f "$bins/$dir/lsm-agent" ] || continue
    for c in agent server; do
        for f in deb rpm apk; do
            cfg=$(mktemp)
            sed -e "s|\${MINI_BIN}|$bins/$dir|g" -e "s|\${NFPM_ARCH}|$arch|g" -e "s|\${NFPM_VERSION}|$version|g" \
                "packaging/nfpm/lsm-$c.yaml" > "$cfg"
            nfpm package --config "$cfg" --packager "$f" --target "$out/" > /dev/null
            rm -f "$cfg"
        done
    done
done
ls -l "$out"
