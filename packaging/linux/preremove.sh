#!/bin/sh
# Stop + deregister the service before files are removed.
systemctl stop aegisone.service >/dev/null 2>&1 || true
systemctl disable aegisone.service >/dev/null 2>&1 || true
