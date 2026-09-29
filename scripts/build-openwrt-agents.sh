#!/bin/sh
# Builds the Full agent for the OpenWrt package targets with the lanscape_small tag and checks
# the flash budget (8 MiB). Inlining is disabled except in the data plane and the runtime:
# about -10 % of size without slowing the test hot paths (docs/decisions/0008).
# usage: build-openwrt-agents.sh <out-dir> <version>
# <out-dir>/<target>/lanscape-agent (target names as in mini/Makefile)
set -eu
budget=8388608
m=github.com/retreat-community/lanscape/internal
out=$1
version=$2
ldflags="-s -w -X github.com/retreat-community/lanscape/internal/buildinfo.Version=$version"
build() {
    target=$1
    shift
    mkdir -p "$out/$target"
    env CGO_ENABLED=0 GOOS=linux "$@" go build -trimpath -tags lanscape_small -ldflags "$ldflags" \
        -gcflags=all=-l -gcflags=$m/testengine= -gcflags=$m/netio= -gcflags=runtime= -gcflags=syscall= \
        -gcflags=internal/poll= -gcflags=net= -o "$out/$target/lanscape-agent" ./cmd/lanscape-agent
    size=$(wc -c < "$out/$target/lanscape-agent")
    printf '| %s | %s |\n' "$target" "$size"
    if [ "$size" -gt "$budget" ]; then
        echo "$target: lanscape-agent is $size bytes, budget $budget" >&2
        exit 1
    fi
}
echo "| target | lanscape-agent bytes |"
echo "|---|---|"
build linux-amd64 GOARCH=amd64
build linux-386 GOARCH=386 GO386=softfloat
build linux-arm64 GOARCH=arm64
build linux-armv7 GOARCH=arm GOARM=7
build linux-armv6 GOARCH=arm GOARM=6
build linux-armv5 GOARCH=arm GOARM=5
build linux-mips GOARCH=mips GOMIPS=softfloat
build linux-mipsle GOARCH=mipsle GOMIPS=softfloat
build linux-mips64 GOARCH=mips64 GOMIPS64=softfloat
build linux-mips64le GOARCH=mips64le GOMIPS64=softfloat
build linux-riscv64 GOARCH=riscv64
