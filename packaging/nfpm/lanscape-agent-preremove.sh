#!/bin/sh
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl disable --now lanscape-agent >/dev/null 2>&1 || true
elif command -v rc-service >/dev/null 2>&1; then
    rc-service lanscape-agent stop >/dev/null 2>&1 || true
fi
exit 0
