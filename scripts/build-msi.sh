#!/bin/sh
# Builds the Windows agent installer with wixl (msitools) from the release zip.
# usage: scripts/build-msi.sh <lanscape-agent_*_windows_amd64.zip> <version> <out-dir>
set -eu
zip=$1
version=${2#v}
out=$3
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
unzip -q "$zip" -d "$work"
cp "$root/packaging/windows/lanscape-agent.wxs" "$work/"
# MSI versions are numeric: 1.2.3-beta.1 -> 1.2.3
msiver=$(echo "$version" | sed 's/[-+].*//')
mkdir -p "$out"
(cd "$work" && wixl -a x64 -D Version="$msiver" -o "lanscape-agent_${version}_windows_amd64.msi" lanscape-agent.wxs)
mv "$work/lanscape-agent_${version}_windows_amd64.msi" "$out/"
