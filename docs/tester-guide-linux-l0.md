# Linux Agent — L0 Foundation Test Guide

Target: **Ubuntu Server 24.04 LTS** VM. Goal: prove the agent installs as a systemd
service, resolves a hardware-stable identity (DMI/machine-id), enrolls, connects over
mTLS, reports device-state, and keeps its identity across reinstall.

## Prerequisites
- Ubuntu Server VM with **network access to the mockserver** (e.g. `192.168.95.129:50051`).
- The Linux binary `build/aegisone-linux-amd64` (built on the dev box) — scp it over.
- The same **`ca.crt`** used in the Windows tests (`C:\deploy\certs\ca.crt`) — scp it over.
- mockserver running (same enroll token `tenant-abc-123`).

Check reachability first: `nc -vz 192.168.95.129 50051` (or `curl -v telnet://...`).

---

## Step 1 — Place the binary + CA cert
```bash
sudo mkdir -p /opt/aegisone /var/opt/aegisone/certs
sudo cp aegisone-linux-amd64 /opt/aegisone/aegisone
sudo chmod +x /opt/aegisone/aegisone
sudo cp ca.crt /var/opt/aegisone/certs/ca.crt
```

## Step 2 — Foreground smoke test (identity + enroll + connect)
```bash
sudo AEGIS_SERVER_ADDRESS=192.168.95.129:50051 \
     AEGIS_ENROLL_TOKEN=tenant-abc-123 \
     AEGIS_TLS_SERVER_NAME=aegis-server \
     /opt/aegisone/aegisone
```
**Expect (stdout):**
- `device identity resolved ... source=generated fingerprint=smbios:<PRODUCT_UUID> signals=2` (2 = smbios + machineid). First run = `generated`; later = `persisted`.
- `enrollment: ...` → client cert obtained (first run).
- `connected to server`
- `sent device-state report to server` / `sent full-inventory report` (full inventory is still minimal on Linux until L1 — that's expected).
- mockserver side: a new device appears (new device_id + computer_id — this is a new machine).

Stop with **Ctrl+C** → `agent stopped`.

**Verify files written:**
```bash
sudo ls -l /var/opt/aegisone /var/opt/aegisone/certs      # identity.json, client.crt, client.key
sudo cat /var/opt/aegisone/identity.json                   # device_id + fingerprint=smbios:... + signals
cat /sys/class/dmi/id/product_uuid                          # matches the smbios: signal (upper-case)
```

## Step 3 — Install as a systemd service
Create the config so the service (which doesn't inherit your shell env) has the settings:
```bash
sudo tee /var/opt/aegisone/config.json >/dev/null <<'EOF'
{
  "AEGIS_SERVER_ADDRESS": "192.168.95.129:50051",
  "AEGIS_ENROLL_TOKEN": "tenant-abc-123",
  "AEGIS_TLS_SERVER_NAME": "aegis-server"
}
EOF

sudo /opt/aegisone/aegisone install
```
**Expect:** `installed + started systemd service "aegisone.service" (/opt/aegisone/aegisone)`

**Verify:**
```bash
systemctl status aegisone          # active (running), enabled
journalctl -u aegisone -n 40 -f    # same connect/identity lines as Step 2
```

## Step 4 — Stop / start
```bash
sudo /opt/aegisone/aegisone stop     # systemctl stop
systemctl is-active aegisone         # inactive
sudo /opt/aegisone/aegisone start
systemctl is-active aegisone         # active
```

## Step 5 — Identity survives reinstall (the key test)
```bash
# note the current id first
sudo grep device_id /var/opt/aegisone/identity.json

sudo /opt/aegisone/aegisone uninstall   # stop + remove unit (data + /var/lib identity remain)
sudo rm -rf /var/opt/aegisone           # simulate a full data wipe (re-add ca.crt + config after)

# re-stage ca.crt + config, then reinstall
sudo mkdir -p /var/opt/aegisone/certs && sudo cp ca.crt /var/opt/aegisone/certs/ca.crt
sudo tee /var/opt/aegisone/config.json >/dev/null <<'EOF'
{ "AEGIS_SERVER_ADDRESS": "192.168.95.129:50051", "AEGIS_ENROLL_TOKEN": "tenant-abc-123", "AEGIS_TLS_SERVER_NAME": "aegis-server" }
EOF
sudo /opt/aegisone/aegisone install

# verify SAME device_id (restored from /var/lib/aegisone)
journalctl -u aegisone | grep -E "restored from durable store|device identity resolved"
sudo grep device_id /var/opt/aegisone/identity.json
```
**Expect:** log `identity: restored from durable store (survived reinstall)` and the
**same `device_id`/`computer_id`** as before the wipe. (This is the Linux analogue of the
Windows HKLM mirror — `/var/lib/aegisone/identity.json` survives the data wipe.)

## Step 6 — Cleanup (optional)
```bash
sudo /opt/aegisone/aegisone uninstall
sudo rm -rf /var/opt/aegisone /var/lib/aegisone /opt/aegisone   # full removal incl. durable id
```

---

## Pass criteria
| # | Check | Pass |
|---|---|---|
| 1 | Foreground run resolves `fingerprint=smbios:<uuid>` + `signals=2` | identity is hardware-stable |
| 2 | Enrolls + `connected to server` + device-state sent | mTLS + enroll work on Linux |
| 3 | `aegisone install` → `systemctl status aegisone` active/enabled | systemd service works |
| 4 | stop/start toggles the service | lifecycle works |
| 5 | After uninstall + data wipe + reinstall → **same device_id** | durable identity survives reinstall |

> Not yet on Linux (expected, coming in L1+): rich inventory (packages/HW), patching,
> DEX, tamper. Full-inventory is minimal until L1.
