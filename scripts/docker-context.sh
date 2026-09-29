#!/bin/sh
# Assembles a Docker build context from goreleaser output:
#   <ctx>/linux-amd64/, linux-arm64/, linux-armv7/, linux-armv6/ with lanscape and lanscape-agent.
# usage: docker-context.sh <goreleaser-dist> <ctx>
set -eu
dist=$1
ctx=$2
mkdir -p "$ctx"
for d in "$dist"/*/; do
    base=$(basename "$d")
    case $base in
    lanscape_linux_amd64* | lanscape-agent_linux_amd64*) t=linux-amd64 ;;
    lanscape_linux_arm64* | lanscape-agent_linux_arm64*) t=linux-arm64 ;;
    lanscape_linux_arm_7 | lanscape-agent_linux_arm_7) t=linux-armv7 ;;
    lanscape-agent_linux_arm_6) t=linux-armv6 ;;
    *) continue ;;
    esac
    mkdir -p "$ctx/$t"
    cp "$d"/lanscape* "$ctx/$t/"
    chmod 0755 "$ctx/$t"/*
done
# the server is not built for armv6: the agent image covers it, the server image skips it
ls -R "$ctx"
