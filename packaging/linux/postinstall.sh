#!/bin/sh
# Aegis One agent — post-install (deb/rpm). Creates the data + durable dirs, registers
# the service, and starts it. The agent retries connecting until the operator drops the
# CA cert + config (so install-then-configure, BigFix-style).
set -e
mkdir -p /var/opt/aegisone/certs /var/lib/aegisone
chmod 700 /var/lib/aegisone
systemctl daemon-reload >/dev/null 2>&1 || true
systemctl enable aegisone.service >/dev/null 2>&1 || true
systemctl start aegisone.service >/dev/null 2>&1 || true
cat <<'MSG'
Aegis One agent installed.
Next: provide the server config so it can enroll:
  - CA cert:  /var/opt/aegisone/certs/ca.crt
  - config :  /var/opt/aegisone/config.json
      { "AEGIS_SERVER_ADDRESS": "<host:50051>", "AEGIS_ENROLL_TOKEN": "<token>", "AEGIS_TLS_SERVER_NAME": "<name>" }
  then: systemctl restart aegisone
MSG
