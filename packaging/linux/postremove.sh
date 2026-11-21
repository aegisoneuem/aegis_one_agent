#!/bin/sh
# Reload systemd after the unit is gone. NOTE: /var/opt/aegisone (data) and
# /var/lib/aegisone (durable identity) are intentionally left so the device keeps its
# identity across a reinstall. Remove them manually for a full wipe.
systemctl daemon-reload >/dev/null 2>&1 || true
