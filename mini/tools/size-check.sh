#!/bin/sh
# Checks Mini binary budgets and prints a markdown table row per artefact.
# usage: size-check.sh <build-dir> <target>
set -eu
dir=$1
target=$2
fail=0
check() {
    file=$1
    limit=$2
    if [ ! -f "$file" ]; then
        echo "missing: $file" >&2
        fail=1
        return
    fi
    size=$(wc -c < "$file" | tr -d ' ')
    status=ok
    if [ "$size" -gt "$limit" ]; then
        status=OVER
        fail=1
    fi
    printf '| %s | %s | %s | %s | %s |\n' "$target" "$(basename "$file")" "$size" "$limit" "$status"
}
check "$dir/lsm-agent" 131072
check "$dir/lsm-server" 524288
check "$dir/../gen/index.html.gz" 40960
exit $fail
