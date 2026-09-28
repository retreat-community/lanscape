#!/bin/sh
# Prints a markdown changelog grouped by Conventional Commit type.
# usage: changelog.sh <tag>   (range: previous tag .. <tag>)
set -eu
tag=${1:-HEAD}
prev=$(git describe --tags --abbrev=0 "$tag^" 2>/dev/null || true)
range=${prev:+$prev..}$tag
log=$(git log --no-merges --format='%s' "$range")
section() {
    title=$1
    pattern=$2
    lines=$(printf '%s\n' "$log" | grep -E "^($pattern)(\([^)]*\))?!?: " || true)
    [ -n "$lines" ] || return 0
    printf '### %s\n\n' "$title"
    printf '%s\n' "$lines" | sed -E 's/^[a-z]+(\(([^)]*)\))?!?: (.*)$/- **\2** \3/; s/^- \*\*\*\* /- /'
    printf '\n'
}
section "Features" feat
section "Bug fixes" fix
section "Performance" perf
section "Refactoring" refactor
section "Tests" test
section "Documentation" docs
section "Build and CI" "build|ci"
section "Chores" chore
