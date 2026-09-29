#!/bin/sh
set -e
if ! getent group lanscape >/dev/null 2>&1; then
    (groupadd -r lanscape || addgroup -S lanscape) >/dev/null 2>&1 || true
fi
if ! getent passwd lanscape >/dev/null 2>&1; then
    (useradd -r -g lanscape -d /var/lib/lanscape -s /sbin/nologin lanscape ||
        adduser -S -G lanscape -h /var/lib/lanscape -s /sbin/nologin lanscape) >/dev/null 2>&1 || true
fi
mkdir -p /var/lib/lanscape
chown lanscape:lanscape /var/lib/lanscape 2>/dev/null || true
chmod 0750 /var/lib/lanscape
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
    systemctl enable lanscape >/dev/null 2>&1 || true
fi
echo "Lanscape installed. Edit /etc/lanscape/lanscape.env and run: systemctl start lanscape (or rc-service lanscape start)"
