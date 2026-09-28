#!/bin/sh
# Packs Lanscape Mini binaries into per-target tar.gz archives.
# usage: package-mini.sh <bins-dir> <version> <out-dir>
set -eu
bins=$1
version=$2
out=$3
root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out"
for d in "$bins"/*/; do
    t=$(basename "$d")
    [ -f "$d/lsm-agent" ] || continue
    name="lanscape-mini_${version#v}_${t}"
    stage=$(mktemp -d)
    mkdir -p "$stage/$name/systemd" "$stage/$name/openrc"
    cp "$d"/lsm-* "$stage/$name/"
    chmod 0755 "$stage/$name"/lsm-*
    cp "$root/LICENSE" "$root/NOTICE" "$stage/$name/"
    cp "$root/packaging/mini/agent.conf" "$root/packaging/mini/server.conf" "$stage/$name/"
    cp "$root/packaging/systemd/lsm-"*.service "$stage/$name/systemd/"
    cp "$root/packaging/openrc/lsm-"* "$stage/$name/openrc/"
    tar -C "$stage" --owner=0 --group=0 --sort=name -czf "$out/$name.tar.gz" "$name"
    rm -rf "$stage"
done
ls -l "$out"
