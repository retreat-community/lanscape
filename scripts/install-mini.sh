#!/bin/sh
# One-command installer for Lanscape Mini on Linux:
#   curl -fsSL https://raw.githubusercontent.com/retreat-community/lanscape/main/scripts/install-mini.sh | \
#     sudo sh -s -- agent --server 192.168.1.10 --token SECRET
#   curl -fsSL .../install-mini.sh | sudo sh -s -- server --token SECRET
set -eu
REPO=retreat-community/lanscape
role=${1:-}
shift || true
server= token= version=${LSM_VERSION:-latest}
while [ $# -gt 0 ]; do
    case $1 in
    --server) server=$2; shift 2 ;;
    --token) token=$2; shift 2 ;;
    --version) version=$2; shift 2 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
    esac
done
case $role in agent | server) ;; *) echo "usage: install-mini.sh agent|server --token T [--server HOST]" >&2; exit 2 ;; esac
[ -n "$token" ] || { echo "--token is required" >&2; exit 2; }
[ "$role" = server ] || [ -n "$server" ] || { echo "--server is required for the agent" >&2; exit 2; }

case $(uname -m) in
x86_64) t=linux-amd64 ;; i?86) t=linux-386 ;; aarch64 | arm64) t=linux-arm64 ;;
armv7*) t=linux-armv7 ;; armv6*) t=linux-armv6 ;; armv5*) t=linux-armv5 ;;
mips) t=linux-mips ;; mipsel | mipsle) t=linux-mipsle ;; mips64) t=linux-mips64 ;; mips64el) t=linux-mips64le ;;
riscv64) t=linux-riscv64 ;; ppc64le) t=linux-ppc64le ;;
*) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
if [ "$version" = latest ]; then
    version=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
fi
name="lanscape-mini_${version#v}_$t"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "https://github.com/$REPO/releases/download/$version/$name.tar.gz" | tar -C "$tmp" -xz
install -m 0755 "$tmp/$name/lsm-$role" /usr/bin/lsm-$role
mkdir -p /etc/lsm
conf=/etc/lsm/$role.conf
if [ ! -f "$conf" ]; then
    sed "s|^token=.*|token=$token|" "$tmp/$name/$role.conf" > "$conf"
    [ "$role" = agent ] && sed -i "s|^server=.*|server=$server|" "$conf"
    chmod 0644 "$conf"
fi
if command -v systemctl > /dev/null 2>&1 && [ -d /run/systemd/system ]; then
    install -m 0644 "$tmp/$name/systemd/lsm-$role.service" /etc/systemd/system/
    systemctl daemon-reload
    systemctl enable --now "lsm-$role"
elif command -v rc-update > /dev/null 2>&1; then
    install -m 0755 "$tmp/$name/openrc/lsm-$role" /etc/init.d/
    rc-update add "lsm-$role" default
    rc-service "lsm-$role" restart
else
    echo "installed /usr/bin/lsm-$role; start it with: lsm-$role -c $conf"
fi
echo "lsm-$role $version installed"
