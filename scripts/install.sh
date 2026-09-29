#!/bin/sh
# One-command installer for Lanscape (Full) on Linux:
#   curl -fsSL https://raw.githubusercontent.com/retreat-community/lanscape/main/scripts/install.sh | \
#     sudo sh -s -- agent --server lanscape.lan:8443 --token lsr_... --ca-fingerprint ...
#   curl -fsSL .../install.sh | sudo sh -s -- server [--admin-password ...]
set -eu
REPO=retreat-community/lanscape
role=${1:-}
shift || true
server="" token="" fp="" name="" admin="" version=${LANSCAPE_VERSION:-latest}
while [ $# -gt 0 ]; do
    case $1 in
    --server) server=$2; shift 2 ;;
    --token) token=$2; shift 2 ;;
    --ca-fingerprint) fp=$2; shift 2 ;;
    --name) name=$2; shift 2 ;;
    --admin-password) admin=$2; shift 2 ;;
    --version) version=$2; shift 2 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
    esac
done
case $role in
agent) bin=lanscape-agent; svc=lanscape-agent; env=/etc/lanscape/agent.env ;;
server) bin=lanscape; svc=lanscape; env=/etc/lanscape/lanscape.env ;;
*) echo "usage: install.sh agent|server [options]" >&2; exit 2 ;;
esac
[ "$role" = server ] || [ -n "$server" ] || { echo "--server is required" >&2; exit 2; }
case $(uname -m) in
x86_64) arch=amd64 ;; i?86) arch=386 ;; aarch64 | arm64) arch=arm64 ;;
armv7*) arch=armv7 ;; armv6*) arch=armv6 ;; armv5*) arch=armv5 ;;
mips) arch=mips_softfloat ;; mipsel | mipsle) arch=mipsle_softfloat ;;
mips64) arch=mips64_softfloat ;; mips64el) arch=mips64le_softfloat ;;
riscv64) arch=riscv64 ;; ppc64le) arch=ppc64le ;; s390x) arch=s390x ;;
*) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
if [ "$version" = latest ]; then
    version=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
fi
v=${version#v}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "https://github.com/$REPO/releases/download/$version/${bin}_${v}_linux_${arch}.tar.gz" | tar -C "$tmp" -xz
install -m 0755 "$tmp/$bin" "/usr/bin/$bin"
mkdir -p /etc/lanscape
if [ ! -f "$env" ]; then
    : > "$env"
    chmod 0600 "$env"
    if [ "$role" = agent ]; then
        {
            echo "LANSCAPE_SERVER=$server"
            [ -n "$token" ] && echo "LANSCAPE_TOKEN=$token"
            [ -n "$fp" ] && echo "LANSCAPE_CA_FINGERPRINT=$fp"
            [ -n "$name" ] && echo "LANSCAPE_NAME=$name"
        } >> "$env"
    elif [ -n "$admin" ]; then
        echo "LANSCAPE_ADMIN_PASSWORD=$admin" >> "$env"
    fi
fi
if command -v systemctl > /dev/null 2>&1 && [ -d /run/systemd/system ]; then
    if [ "$role" = server ] && ! getent passwd lanscape > /dev/null; then
        useradd -r -d /var/lib/lanscape -s /sbin/nologin lanscape 2> /dev/null || true
        chgrp lanscape "$env" 2> /dev/null && chmod 0640 "$env"
    fi
    install -m 0644 "$tmp/systemd/$svc.service" /etc/systemd/system/
    systemctl daemon-reload
    systemctl enable --now "$svc"
elif command -v rc-update > /dev/null 2>&1; then
    install -m 0755 "$tmp/openrc/$svc" "/etc/init.d/$svc"
    rc-update add "$svc" default
    rc-service "$svc" restart
else
    echo "installed /usr/bin/$bin; start it manually"
fi
echo "$bin $version installed"
