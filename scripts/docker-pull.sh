#!/bin/sh
# Pulls an image with retries: registry timeouts on large images must not fail a build.
# usage: docker-pull.sh IMAGE
set -u
image=$1
delay=5
for attempt in 1 2 3 4 5; do
    docker pull -q "$image" && exit 0
    [ "$attempt" -eq 5 ] && break
    echo "pull of $image failed (attempt $attempt), retrying in ${delay}s" >&2
    sleep "$delay"
    delay=$((delay * 3))
done
echo "cannot pull $image" >&2
exit 1
