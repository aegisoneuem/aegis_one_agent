# Aegis One — Patching Test Guide (Server Team)

**Audience:** the Aegis One server/console team.
**Goal:** stand up the reference **mock server** + the **agent** on Windows VMs and run
every patching scenario — single/multiple install, pre-cache (peer-cache), cache
limits, reboot status, uninstall detection, on-demand & catalog-triggered scans,
failure cases — on **Windows 11 AND Windows Server 2022**, so you can see exactly
what the agent sends and build the production server to match.

> `mockserver.exe` is an **executable reference spec**, not the real server. It
> drives the agent the way production must, and prints what the agent reports.
> See [What the real server must implement](#what-the-real-server-must-implement).

---

## 0. What's in `dist/`

| File | Purpose |
|---|---|
| `AegisOneClient.msi` | Agent installer (interactive wizard or silent). Installs the `AegisOneClient` service. |
| `AegisOneClient.exe` | Agent binary (also embedded in the MSI) — for reference / manual swap. |
| `mockserver.exe` | Reference server. Drives the agent; prints what it receives. |
| `certgen.exe` | Generates the dev mTLS certificate set (`certs/`). |

All Windows x64. Run everything **as Administrator**.

---

## 1. Prerequisites

- **Two test VMs: Windows 11 and Windows Server 2022** (take a **snapshot** of each first — installs are real; revert after).
- Admin on each (agent runs as the `LocalSystem` service).
- **Disk:** a multi-GB cumulative update needs real free space. Keep **≥ 15 GB free** on the system drive, or installs fail with a disk-space error (expected/by design).
- A few **`.msu` patch files** that are *actually missing* on each VM, from the
  [Microsoft Update Catalog](https://www.catalog.update.microsoft.com):
  - a small **no-reboot** update (e.g. a .NET cumulative) and
  - a **reboot-required** OS cumulative (returns DISM 3010).
  - Use the **correct package per OS** (a Win11 package is "not applicable" on Server 2022 and vice-versa).
- Each `.msu`'s **SHA-256**:
  ```cmd
  certutil -hashfile C:\deploy\<patch>.msu SHA256
  ```

---

## 2. One-time setup

### 2.1 Generate the dev certificates (once, reuse everywhere)
From the folder holding the binaries:
```cmd
certgen.exe certs
```
Creates `certs\` = `ca.crt, ca.key, server.crt, server.key, client.crt, client.key`.
> **Use ONE `certs\` set for the server and all agents.** The agent's `ca.crt` must
> be the same CA that signed the server cert, or mTLS fails with
> `x509: ... ECDSA verification failure ("Aegis One Dev CA")`. Don't regenerate mid-test.

### 2.2 Start the mock server
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
mockserver.exe
```
Listens on `:50051` (all interfaces → LAN-reachable; open the firewall for cross-VM).
**It reads `AEGIS_*` env vars at startup** and sends matching commands when the agent
connects. **To run a new scenario: Ctrl+C, `set` the vars, start it again.**

### 2.3 Install the agent on a VM (interactive)
Run `AegisOneClient.msi`:

| Field | Value |
|---|---|
| Server address | `127.0.0.1:50051` (mock on same VM) **or** `<mockserver-LAN-IP>:50051` |
| Server cert name | `aegis-server` |
| Enrollment token | `tenant-abc-123` |
| CA certificate file | full path to your `certs\ca.crt` |

Silent alternative:
```cmd
msiexec /i AegisOneClient.msi /qn SERVERADDRESS=127.0.0.1:50051 ^
  SERVERNAME=aegis-server ENROLLTOKEN=tenant-abc-123 CACERTFILE=C:\deploy\certs\ca.crt
```

### 2.4 Put the catalog (cab) in place
Copy `wsusscn2.cab` to `C:\Program Files\AegisOne\Client\SKVC\wsusscn2.cab`, then
restart the service (next step). Without it the compliance scan logs a clear error.

### 2.5 Verify + logs
```cmd
sc query AegisOneClient                    REM STATE = RUNNING
curl http://127.0.0.1:50080/status         REM "connected": true, "enrolled": true
```
- Agent logs: `C:\Program Files\AegisOne\Client\SKVC\logs\agent-YYYY-MM-DD.log`
- Task Manager → Details: process **`AegisOneClient.exe`**, Description **"AegisOne Agent"**, under `C:\Program Files\AegisOne\Client\`.

### 2.6 Onboard a SECOND VM against the SAME mock server
No server-side change. On the 2nd VM's install, set:
- `AEGIS_SERVER_ADDRESS = <mockserver-LAN-IP>:50051` (not 127.0.0.1)
- `AEGIS_TLS_SERVER_NAME = aegis-server` (matches the cert; verified by name, not IP — works dialing by IP, no cert regen needed)
- same `AEGIS_ENROLL_TOKEN` + same `ca.crt`.
**Fresh install per VM** — never copy `identity.json`/client cert between VMs (would duplicate identity).

---

## 3. Config reference (how to change agent settings)

The MSI writes config to the **registry** (`HKLM\SOFTWARE\AegisOne\Client\...`); there
is **no config.json by default** ("no config file found" in the log is normal). The
agent resolves each setting **env > config.json > registry > default**.

Change a value in the registry, then restart the service:
```cmd
reg add "HKLM\SOFTWARE\AegisOne\Client\Settings" /v <NAME> /t REG_SZ /d <VALUE> /f
sc stop AegisOneClient & sc start AegisOneClient
```
(Or create `C:\Program Files\AegisOne\Client\SKVC\config.json` = `{"NAME":"VALUE", ...}`.)

| Setting | Meaning | Default | Notes |
|---|---|---|---|
| `AEGIS_DEVICE_STATE_INTERVAL` | fast-lane cadence (seconds) | `300` (5 min) | integer **seconds** (`20`, not `20s`) |
| `AEGIS_RECON_SCAN_INTERVAL` | full cab-scan cadence (seconds) | `604800` (weekly) | daily=`86400`, 30 min=`1800` |
| `AEGIS_INSTALL_DISK_MULTIPLIER` | free-disk pre-flight = fileSize × this | `2` | raise for safety, lower on tight volumes |
| `AEGIS_CACHE_MAX_BYTES` | staging cache cap (bytes) | `2147483648` (2 GB) | cache-full test |
| `AEGIS_LOG_LEVEL` | log verbosity | `info` | `debug` to SEE the fast-lane ticks |
| `AEGIS_HEALTH_ADDR` | local health endpoint | `127.0.0.1:50080` | `GET /status` |

---

## 4. The two detection lanes (read this before the scenarios)

| Lane | What it reports | Cost | When | Log prefix |
|---|---|---|---|---|
| **FAST LANE** (DeviceStateReport) | **build** (authoritative cumulative level) + **reboot_pending** | ~ms (registry) | every `AEGIS_DEVICE_STATE_INTERVAL`, send **on change** | `FAST-LANE patch-state:` |
| **FULL SCAN** (PatchScanResult) | **missing** / **superseded** / **installed** (full metadata, authoritative WUA) | ~2-4 min (WUA cab) | startup + weekly + **new cab** + **scan_now** + **CBS change (debounced)** | `FULL-SCAN (cab):` |

The authoritative **installed AND missing** lists both come from the FULL SCAN (WUA —
what the system/console shows). The fast lane carries only the cheap, authoritative
**build** + **reboot_pending** (the coarse CBS installed-KB list was dropped as
misleading). The server correlates by `device_id`. Every dispatched action (install/
uninstall/stage/reboot/scan) also streams a unified **`ActionStatus`**
(`QUEUED→DOWNLOADING→VERIFYING→RUNNING→SUCCESS/FAILED/NOT_APPLICABLE`), so per-patch
install/uninstall is known **instantly** (no scan needed); the full scan reconciles the
broader picture and is triggered automatically on any CBS servicing change (debounced).

---

## 5. Test scenarios (step by step)

Run each on **both Win11 and Server 2022**. For each: stop the mock server, `set` the
vars, start it, watch the mock output **and** the agent log.

### UC-1 — Enroll + connect + health
Fresh MSI install → agent generates identity, enrolls (CSR → mock signs cert), mTLS
connects. Verify `curl /status` = connected/enrolled; mock prints `Enroll OK` + `Agent connected!`.

### UC-2 — Single patch install (no reboot)
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_INSTALL_FILE=C:\deploy\<noreboot>.msu
set AEGIS_INSTALL_KB=KB_______
set AEGIS_INSTALL_HASH=<sha256>
mockserver.exe
```
Expect: `action status type=patch_install QUEUED→VERIFYING→RUNNING→SUCCESS exit_code=0 reboot_required=false`.

### UC-3 — Single patch install (reboot required) + reboot status
Use a reboot-required OS cumulative (DISM 3010):
```cmd
set AEGIS_INSTALL_FILE=C:\deploy\<cumulative>.msu
set AEGIS_INSTALL_KB=KB_______
set AEGIS_INSTALL_HASH=<sha256>
mockserver.exe
```
Expect `...SUCCESS exit_code=3010 reboot_required=true`, then the fast lane:
```
patch state report -> ... reboot_pending: true (sources=[CBS], awaiting_kbs=[KB_______])
```
**Reboot the VM** → next report `reboot_pending: false`, build advanced; the next full scan lists the KB under `installed`.

### UC-4 — Multiple patches (serial)
Comma-separate (same index across lists):
```cmd
set AEGIS_INSTALL_FILE=C:\deploy\a.msu,C:\deploy\b.msu
set AEGIS_INSTALL_KB=KB_A,KB_B
set AEGIS_INSTALL_HASH=<hashA>,<hashB>
mockserver.exe
```
Expect `install-001` runs to terminal, **then** `install-002` (patches install one at a
time — the servicing stack is serial); other traffic stays responsive meanwhile.

### UC-5 — Reinstall (idempotency)
Send an install for a KB already installed → `phase=NOT_APPLICABLE reason="already installed (skipped)"` (no DISM).

### UC-6 — Uninstall (command) + auto-detect
**Agent-driven** (console/server): send an uninstall via the mock server —
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_UNINSTALL_KB=KB5062170
mockserver.exe
```
→ `action status type=patch_uninstall RUNNING→SUCCESS` (NOT_APPLICABLE if the KB isn't
installed; reboot_required if a reboot is needed to finalize). **Instant** feedback.

**Manual / external** (Settings → Uninstall updates): no ActionStatus, but the agent's
**CBS servicing watch** detects the change and, after a short debounce (~1 min), triggers
a full scan that re-lists the KB as **missing** (and drops it from `installed`). So both
agent-driven and manual changes are detected — agent-driven instantly, manual within a
couple of minutes.

### UC-7 — Pre-cache (peer-cache staging) then install from cache
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_SERVE_PATCH=C:\deploy\<patch>.msu   REM server serves the bytes
set AEGIS_STAGE_KB=KB_______
set AEGIS_STAGE_HASH=<sha256>
set AEGIS_STAGE_FILE=<patch>.msu              REM filename hint (extension)
mockserver.exe
```
Expect `action status type=stage DOWNLOADING → SUCCESS "patch staged in cache"`. Then
run UC-2/UC-3 for the same KB → agent log `patch found in cache (staged), using it` (no
re-download).

### UC-8 — Peer-cache content delivery (FetchContent)
UC-7 already exercises it (the `AEGIS_SERVE_PATCH` bytes stream to the agent over mTLS,
SHA-256 verified, no internet). A new **cab** also comes this way — see UC-11.

### UC-9 — Cache size / cache-full
Shrink the cap, then stage something bigger than it:
```cmd
reg add "HKLM\SOFTWARE\AegisOne\Client\Settings" /v AEGIS_CACHE_MAX_BYTES /t REG_SZ /d 10 /f
sc stop AegisOneClient & sc start AegisOneClient
```
Stage a patch → agent sends a **CacheFullReport** (mock prints `CACHE-FULL report -> needed/cap/in_use`) + the stage `action status FAILED "cache full"`. (Reset the cap after.)

### UC-10 — Scan now (on-demand compliance scan)
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_SCAN_NOW=1
mockserver.exe
```
Agent log `scan_now: on-demand compliance scan requested` → full scan → mock `command result accepted` + `patch scan result` + scan `action status`.

### UC-11 — New catalog (cab) → auto full scan
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_CAB_VERSION=v2
set AEGIS_CAB_SHA256=<new cab sha256>
set AEGIS_SERVE_CAB=C:\deploy\wsusscn2-new.cab
mockserver.exe
```
Agent fetches the new cab → `FULL-SCAN (cab): triggered (new catalog / on-demand)` runs
immediately (not weekly). A local install/uninstall WITHOUT a cab change does **not**
trigger the heavy scan (it's handled by the fast lane) — protects endpoint load.

### UC-12 — Superseded patches in the scan
On a behind VM, the scan reports both missing and **superseded** updates (tagged); the
mock prints `[MISSING]` / `[SUPERSEDED]` with KB, MS IDs, severity, CVE, size (min–max),
and the supersedes list. Console uses these to categorize.

### UC-13 — Reboot / cancel reboot command
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_REBOOT_DELAY=90
mockserver.exe
```
`action status type=reboot SUCCESS "reboot scheduled in 90s"`. Cancel: a run with
`set AEGIS_CANCEL_REBOOT=1`.

### UC-14 — Failure scenarios (all fail **loud + clear**, no silent drop)
- **Wrong hash** → `FAILED` "sha256 mismatch" (DISM never runs).
- **File missing** → `FAILED` "file not found".
- **Wrong-OS / not-applicable patch** (e.g. a Win11 .msu on Server 2022) → `FAILED`
  "patch not applicable to this OS edition/build …" (DISM exit 50) — fails fast, no retry.
- **Insufficient disk** → `FAILED` "insufficient disk space …" (DISM exit 112) — CBS rolls
  back; the system stays unpatched (not broken).

### UC-15 — Disk pre-flight (fileSize × 2)
Before installing, the agent requires free disk ≥ `fileSize × AEGIS_INSTALL_DISK_MULTIPLIER`
(default **2**). A 4 GB CU needs ~8 GB free. Lower the multiplier on tight test VMs:
```cmd
reg add "HKLM\SOFTWARE\AegisOne\Client\Settings" /v AEGIS_INSTALL_DISK_MULTIPLIER /t REG_SZ /d 1 /f
```

### UC-16 — Cert renewal (long-lived certs)
Short-cert test: run the mock with `set AEGIS_CERT_TTL_SECONDS=60` and the agent with
`AEGIS_CERT_CHECK_INTERVAL=10` → agent renews the cert over mTLS (no token) at ~2/3 life.
(Production: 398-day certs, renewed automatically — no mass re-enroll.)

### UC-17 — Both OSes
Repeat UC-2…UC-6 on **Server 2022** with Server-2022-applicable patches. The agent binary
is identical across Win10/11 + Server 2016/2019/2022; only patch **applicability** differs
(use the right `.msu` per OS).

---

## 6. Log quick-reference (agent log)

| Line | Meaning |
|---|---|
| `FAST-LANE patch-state: CHANGED -> sending ... kbs=[...] reboot_pending=..` | cheap installed/build/reboot report (on change) |
| `FAST-LANE patch-state: no change this cycle (skip)` (DEBUG) | fast-lane tick, nothing changed (set `AEGIS_LOG_LEVEL=debug` to see) |
| `FULL-SCAN (cab): running at startup / periodic / triggered` | heavy cab scan + why it ran |
| `FULL-SCAN (cab): result buffered ... kbs=[...]` | missing KBs the scan found |
| `action status ... type=patch_install phase=..` | unified per-action status |
| `deploy: install succeeded exit_code=0|3010` | DISM result (3010 = reboot required) |

Mock-server side prints `Enroll OK`, `Agent connected!`, `patch state report`,
`patch scan result` (full missing detail), `action status`, `CACHE-FULL report`.

---

## What the real server must implement

The mock fakes these; production builds them for real:

1. **mTLS + enrollment PKI** — per-tenant token → validate (license-gated) → sign the
   agent CSR → per-device cert; long-lived (~398d) + a `Renew` RPC (no token).
2. **Signed commands** — every command is an Ed25519-signed envelope; configure agents
   with the server's public verify key (`AEGIS_CMD_VERIFY_KEY`).
3. **Content delivery (peer-cache)** — the agent never touches the internet; the server
   streams each `.msu`/cab over `FetchContent` (or places it at `file_path`).
4. **Catalog + diff** — maintain the catalog (incl. supersedence), push
   `PatchBaseline`/`CabManifest`, and **compute missing from the agent's reported
   installed-state + full-scan results**.
5. **Action lifecycle + scheduling** — assign `action_id`s, hold actions until their
   window (Scheduled/Pending is server-side), dispatch, and **persist the `ActionStatus`
   transitions** per device per action for the console.
6. **Device reconciliation + licensing + token lifecycle** — see
   [server-team-handoff.md](server-team-handoff.md) and
   [device-identity-and-lifecycle.md](device-identity-and-lifecycle.md).
7. **Console views** — per device: installed / missing / superseded / reboot-pending,
   categorize by severity/classification/CVE/MS-ID, build patch groups, and drive
   install / scan-now / reboot actions.

---

## 8. Troubleshooting

| Symptom | Fix |
|---|---|
| `x509: ECDSA verification failure ("Aegis One Dev CA")` | Agent `ca.crt` ≠ server CA. Use one `certs\` set; re-enroll: delete `SKVC\certs\client.*` (keep `identity.json`), copy matching `ca.crt`, re-add token, restart. |
| `token=<unset — enrollment will reject>` | `set AEGIS_ENROLL_TOKEN=tenant-abc-123` (plain, no quotes). |
| install `NOT_APPLICABLE` unexpectedly | that KB is already installed; use a genuinely-missing KB / behind snapshot. |
| install `exit 50 "not applicable"` | wrong `.msu` for this OS/build — use the correct per-OS package. |
| install `exit 112 "insufficient disk"` | free disk (≥ ~15 GB), or lower `AEGIS_INSTALL_DISK_MULTIPLIER`. |
| fast lane "not running" | it is — silent when unchanged; `AEGIS_LOG_LEVEL=debug` to see the ticks. |
| compliance scan error "cab not present" | copy `wsusscn2.cab` to `SKVC\`, restart. |

For deeper mock-server scenarios + the full env-var reference see
[mockserver-guide.md](mockserver-guide.md).
