#!/bin/sh
set -e
mkdir -p /var/lib/lanscape-agent
chmod 0700 /var/lib/lanscape-agent
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
fi
echo "Set LANSCAPE_SERVER and LANSCAPE_TOKEN in /etc/lanscape/agent.env, then: systemctl enable --now lanscape-agent"
