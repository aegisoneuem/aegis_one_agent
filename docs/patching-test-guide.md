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

### 2.7 Agent on-disk layout
Everything the agent owns lives under one install root (like BigFix's `BES Client\`):
```
C:\Program Files\AegisOne\Client\
  AegisOneClient.exe            ← service binary (Task Manager: "AegisOne Agent")
  SKVC\                         ← all agent state (secure key vault + cache); per-device, never copied between machines
    config.json                 ← optional; absent by default (settings come from the registry)
    identity.json               ← persisted device identity (hardware-bound)
    wsusscn2.cab                ← offline compliance catalog (delivered via peer-cache)
    installed-kbs.json          ← authoritative installed-KB set from the last full scan (install pre-check)
    reboot-pending.json         ← KBs the agent installed that await a reboot
    certs\                      ← ca.crt, client.crt, client.key.enc (DPAPI-sealed key)
    cache\                      ← staged (pre-cached) encrypted patches
    download\                   ← server-delivered runtime patch files (just-in-time)
    buffer\patch-buffer.jsonl   ← durable outbound report buffer (survives disconnect)
    logs\agent-YYYY-MM-DD.log   ← daily rotating agent log (kept AEGIS_LOG_MAX_AGE_DAYS)
```
Maps to BigFix: `SKVC\` ≈ `__BESData\`, `certs\` ≈ `KeyStorage\`/`TrustedDownloadCerts\`,
`logs\` ≈ the root `*.log`, the exe at the root like `BESClient.exe`. On uninstall the MSI
removes the program files; `SKVC\` (identity/certs/cache) is left by policy — delete it
separately for a full decommission wipe (keeping it across reinstall is fine; the server
reconciles by hardware fingerprint).

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

### UC-5 — Reinstall (idempotency — reliable, two layers)
Send an install for a KB that is already installed → `phase=NOT_APPLICABLE`, no DISM/wusa run. Two independent guards make this reliable:
1. **Pre-check** against the authoritative last-scan installed set (`SKVC\installed-kbs.json`, refreshed by every full scan). If the KB is there → `reason="already installed (skipped)"` — the installer is never invoked. (The cheap CBS registry check is a fallback; it alone misses some updates, which is why the scan set is authoritative.)
2. **Installer-level** catch — if the pre-check misses it (e.g. no scan yet), DISM/wusa itself reports "already installed" (wusa exit `2359302` / `0x240006`) → `reason="already installed (nothing to do)"`. So a redundant install of an already-present patch is **always** reported as NOT_APPLICABLE, never a misleading SUCCESS.

A redelivered command after a reboot/reconnect (the mock resends on every connect) therefore reports NOT_APPLICABLE, not a second install.

### UC-6 — Uninstall (command) + auto-detect
**Agent-driven** (console/server): send an uninstall via the mock server —
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_UNINSTALL_KB=KB5062170
mockserver.exe
```
→ `action status type=patch_uninstall RUNNING→SUCCESS` (NOT_APPLICABLE if the KB isn't
installed; reboot_required if a reboot is needed to finalize). **Instant** feedback.

The agent removes updates via the **Windows Update Agent (WUA) COM API**, NOT `wusa.exe`
— wusa's silent `/uninstall /quiet` returns `ERROR_INVALID_PARAMETER (0x57)` from a
non-interactive LocalSystem service, even though it works in an interactive admin
session. WUA works in session 0, needs no internet, and uses no PowerShell.

> **⚠ Known limitation — cumulative / baked-in updates may be non-removable.**
> Latest **cumulative updates (LCU)**, **servicing-stack updates (SSU)**, and updates
> **baked into the OS image** often cannot be removed programmatically — Windows has no
> removal data for them. The agent reports this **fail-loud and clear**, e.g.
> `WU_E_ALL_UPDATES_FAILED (0x80240022)` → *"commonly a cumulative/baked-in update WUA
> cannot remove. If removal is required, try Control Panel → Installed Updates (manual)
> which uses a different (wusa/CBS) path."* In testing, Control Panel's manual uninstall
> (which shells out to wusa interactively) can succeed where the programmatic path
> cannot. **Server team: treat a failed uninstall of a cumulative/SSU as expected; the
> message tells the operator to fall back to the manual Control-Panel path.** A more
> complete DISM `/Remove-Package` based uninstall is a planned follow-up (see
> [server-team-handoff.md](server-team-handoff.md)).

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
- **Insufficient disk** → `FAILED` "insufficient disk space …" (DISM exit 112) — CBS rolls
  back; the system stays unpatched (not broken).
- **Genuinely wrong-OS patch** → `FAILED` with a classified reason — fails fast, no retry.

### UC-14b — DISM → wusa install fallback (Server 2022 especially)
Some `.msu` (seen on Windows Server 2022) **cannot be installed by DISM** — DISM returns
`exit 50 / 0x80070032 "The request is not supported"`. This is **not** a real failure of
the patch: the agent automatically **falls back to `wusa.exe`** (the manual "install this
update" path) and the install succeeds. The logs make the sequence explicit:
```
deploy: DISM attempt failed ... exit_code=50 "DISM does not support installing this .msu (exit 50 …) — handled by the wusa fallback"
install_patch: DISM could not install this .msu — falling back to wusa (the manual install path)
deploy: wusa install succeeded exit_code=0|3010 reboot_required=..
```
So a DISM exit-50 WARN in the log is expected on some packages — look for the wusa line
right after. (The disk pre-flight failure is NOT retried via wusa — wusa needs the same
free space.)

### UC-14c — Scan ↔ install never overlap (serialization)
The heavy compliance scan and an install/uninstall both hammer Windows Update /
TrustedInstaller, so the agent **serializes** them (only one touches WU at a time):
- If a scan is **in flight** when an install arrives, the install logs
  `install_patch: waiting for an in-flight compliance scan to finish before installing`,
  then runs once the scan completes.
- If an install/uninstall is **in progress** when a scan would run, the scan logs
  `FULL-SCAN (cab): deferred — an install/uninstall is in progress; will re-scan after it settles`
  and the post-op CBS watch re-triggers it afterwards.

You should **never** see `wusa install` and `offline WUA search` both logging "still
working" at the same time. This keeps installs fast and prevents the scan from reading a
half-applied state (wrong installed/missing counts).

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
| `install_patch: finished ... reason="installed — completed, no restart needed"` | SUCCESS, no reboot → console shows **Completed** |
| `install_patch: finished ... reboot_required=true reason="installed — RESTART REQUIRED to complete"` | SUCCESS, exit 3010 → console shows **Pending Restart** |
| `deploy: running DISM/wusa command=...` (DEBUG) | exact command per stage — set `AEGIS_LOG_LEVEL=debug` |
| full DISM/wusa output (TRACE) | deepest detail — set `AEGIS_LOG_LEVEL=trace` |

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
| install `NOT_APPLICABLE` unexpectedly | that KB is already installed (per the last scan's `installed-kbs.json` or the installer itself); use a genuinely-missing KB / behind snapshot. |
| install logs `DISM attempt failed ... exit 50` | **expected on some .msu** — the agent falls back to wusa automatically; look for `wusa install succeeded` right after (UC-14b). |
| install `exit 112 "insufficient disk"` | free disk (≥ ~15 GB), or lower `AEGIS_INSTALL_DISK_MULTIPLIER`. |
| uninstall `FAILED 0x80240022` / "not uninstallable" | cumulative/SSU/baked-in update — expected; use Control Panel → Installed Updates manually (UC-6 note). |
| fast lane "not running" | it is — silent when unchanged; `AEGIS_LOG_LEVEL=debug` to see the ticks. |
| compliance scan error "cab not present" | copy `wsusscn2.cab` to `SKVC\`, restart. |

For deeper mock-server scenarios + the full env-var reference see
[mockserver-guide.md](mockserver-guide.md).

---

## 9. Third-party installed-apps inventory (30 October 2026)

### What this covers

Agent reports every registration from the Windows registry Uninstall hive
(HKLM native + WOW6432Node) inside each `DeviceStateReport`. The server team
matches that inventory against its third-party patch catalog (Chrome,
Firefox, Adobe, Java, ...) and dispatches signed `software_deploy` tasks to
update outdated apps — exactly the same pipeline used in SoftDist blocks E/F.

**No agent-side cap on inventory size.** However many apps are installed on
the endpoint, the full list is reported. BigFix limits to ~350 apps on its
catalog side; the Aegis server team targets 1000+ apps on their catalog, and
the agent must not pre-filter.

### What's NOT in this block (deferred, tracked in project memory)

- **Office 365 Click-to-Run** (OfficeC2RClient.exe) — separate update
  mechanism; not WUA-scannable; different task type when added.
- **Windows 10/11 Feature Updates** — version jumps (24H2 to 25H2). WUA scan
  excludes feature-updates today by design; separate scope.
- **Patch corruption detection** — WUA has corruption APIs; not wired.
- **Language-aware applicability** — not wired.

### How the end-to-end flow works

1. Agent collects installed-apps on each `DeviceStateReport` cycle
   (`AEGIS_DEVICE_STATE_INTERVAL`, default 5 minutes). Sent only on CHANGE —
   adding/removing/upgrading an app triggers an immediate resend.
2. Mock prints the installed_apps count on every received report:
   ```
   Received patch state report -> device=..., installed_apps: 47
   ```
3. Server team (real server) stores the inventory per device and runs its
   catalog matcher: "Chrome 118 installed; latest catalog version 120 →
   dispatch signed software_deploy with Chrome 120 bundle".
4. Agent runs the software_deploy via the existing SoftDist pipeline — no
   new task type needed.

### UC-18 run record

**2026-11-01 VM PASS** on WIN-TUES4V46T7I (Windows Server 2022 Standard
Evaluation, device_id b3dea9f7-5df8-4765-ad8b-621307ef5f41):

- UC-18A baseline: `inventory apps` showed 14 installed registrations with
  DisplayName, DisplayVersion, Publisher, Hive — Chrome 156.0, Firefox ESR
  153.6.0, Microsoft Edge 156.0, 7-Zip 26.04, Notepad++ 8.9.8.1, VMware
  Tools 13.0.10.0, Visual C++ runtimes (x86 and x64), AegisOne Client,
  Mozilla Maintenance Service.
- UC-18B SVSH filter: `inventory apps microsoft`, `inventory apps mozilla`,
  `inventory apps google` all narrowed the table correctly;
  `inventory apps 7-zip` showed exactly 1 row before uninstall, 0 rows after.
- UC-18C change detection with real 7-Zip MSI: after `msiexec /x` the agent
  logged
  `FAST-LANE patch-state: CHANGED -> sending ... installed_apps=13` within
  one AEGIS_DEVICE_STATE_INTERVAL cycle; after reinstall the next cycle
  logged `installed_apps=14`. No agent service restart was required.
- UC-18D suppression: between the two CHANGED sends above, multiple 30-sec
  cycles passed with no DeviceStateReport sent (debug-level
  "no change this cycle (skip send)"), proving the fingerprint hash
  suppresses redundant sends.
- UC-18E hive separation: UC-18A output showed Google Chrome and Microsoft
  Edge as HKLM32 (WOW6432Node); 7-Zip, Firefox, Notepad++, VMware Tools,
  AegisOne Client as HKLM64 (native).
- UC-18F multi-device fleet summary: pending (only one agent active during
  this test run).

**Env var caveat caught during the run:** `AEGIS_DEVICE_STATE_INTERVAL` is
parsed as integer SECONDS by the agent config, not Go duration format.
Setting `30s` falls back to the 5-minute default; use `setx
AEGIS_DEVICE_STATE_INTERVAL 30 /M` (plain integer) and restart the
AegisOneClient service for the new interval to take effect.

### UC-18 — Fast-lane installed-apps inventory

**Purpose:** prove the agent reports every HKLM Uninstall hive entry and
that an install or uninstall triggers a resend without waiting for the
weekly reconciliation scan.

**MOCKSERVER:**
```cmd
set AEGIS_SVSH=1
mockserver.exe
```

Wait for the first `Received patch state report` line (within one
`AEGIS_DEVICE_STATE_INTERVAL` after the agent connects). Note the
`installed_apps` count.

**SVSH REPL:**
```
svsh> cd \\<hostname>
<hostname>> inventory apps
<hostname>> inventory apps chrome
<hostname>> inventory apps mozilla
```

**ENDPOINT** (admin CMD): install a small app and wait for the next
`DeviceStateReport` interval:
```cmd
msiexec /i C:\deploy\7z<version>-x64.msi /qn
```

**Expected:**
- First `Received patch state report` shows `installed_apps: N` where N is
  the count of all HKLM + WOW6432Node entries with a `DisplayName`.
- `inventory apps` prints a table of DisplayName, DisplayVersion, Publisher,
  Hive for every reported app.
- `inventory apps chrome` (or `mozilla`, `adobe`, etc.) filters by
  case-insensitive substring match against DisplayName or Publisher.
- After the 7-Zip install, the next FAST-LANE patch-state log line shows
  `installed_apps: N+1` and the mock logs a fresh `Received patch state
  report` with the bumped count — proof that the fingerprint caught the
  app list change and triggered a resend.

### What the real server catalog service must do

1. **Store the inventory** per device from the DeviceStateReport stream.
2. **Match against a catalog** of known-outdated apps:
   - BigFix hosts ~350 fixlets on their 3rd Party Windows Application site.
   - Aegis server team maintains a 1000+ app catalog (vendor name, latest
     version, download URL, silent args, detection rule).
3. **Dispatch SoftDist tasks** for outdated apps — nothing new on the agent
   side. Vendor installer EXE / MSI → `software_deploy` with the right
   silent args. Agent runs via the existing SoftDist pipeline (A-F / J
   blocks in the SoftDist testing guide).
4. **Reboot handling** — if a vendor installer returns 3010, the agent
   reports `reboot_required=true` via the SoftDist action status (same as
   J1). Server team schedules reboot per policy.

### Agent-side proto additions (for the server team)

```proto
message DeviceStateReport {
  // ... existing fields ...
  repeated InstalledApp installed_apps = 11;
}

message InstalledApp {
  string display_name = 1;
  string display_version = 2;
  string publisher = 3;
  string install_date = 4;       // yyyymmdd; empty when the vendor didn't set it
  string uninstall_key = 5;      // registry subkey name — stable id for later actions
  string hive = 6;               // "HKLM" | "HKLM\WOW6432Node" | "HKU\<SID>" | "HKU\<SID>\WOW6432Node"
  int64 estimated_size_kb = 7;
  string uninstall_string = 8;   // vendor-registered uninstall command
}
```

## UC-19 — HKU per-user Uninstall parity + on-demand `inventory refresh`

**Why:** UC-18 shipped machine-wide (HKLM) inventory, matching the common
vendor install path. appwiz.cpl and Settings > Apps also show per-user
installs — Chrome, Teams, Zoom and similar often register under the LOADED
user profile hive, not HKLM. UC-19 closes that parity gap and adds a
dev / admin pull to force a fresh DeviceStateReport without waiting for the
next on-change send.

### UC-19A — HKU per-user inventory present

1. Note the current app count from `inventory apps` after an `inventory refresh`.
2. Install an app that registers per-user (Chrome / Teams / Spotify, or
   `MSIEXEC /i file.msi ALLUSERS=""`).
3. Wait one `AEGIS_DEVICE_STATE_INTERVAL` cycle. Then from SVSH:
   `inventory refresh` followed by `inventory apps <name>`.
4. **Expected:** the new entry appears with `HIVE = HKU\S-1-5-21-...`
   (or `HKU\S-1-5-21-...\WOW6432Node` for a 32-bit per-user installer).

### UC-19B — On-demand refresh without any change

1. Make no change on the device.
2. From SVSH: `cd <hostname>` → `inventory refresh`
3. **Expected (within 5s):** `fresh inventory (N apps, collected_at=...)` and
   `refresh complete — 1/1 received, 0 timed out`. Agent log shows
   `on-demand refresh requested — resetting fingerprint` and
   `FAST-LANE patch-state: CHANGED -> sending`.

### UC-19C — Offline target handled

1. Stop the agent service on the target: `sc stop AegisOneClient`.
2. From SVSH: `inventory refresh`
3. **Expected (immediate, no wait):** `OFFLINE (no live stream) — refresh skipped`.

### UC-19D — `--timeout` flag

1. `inventory refresh --timeout 15` succeeds against a slow / cross-WAN target.
2. `inventory refresh --timeout abc` prints
   `--timeout expects a positive integer (seconds)` and exits cleanly.
3. `inventory refresh --fast` prints `unknown argument "--fast"` and exits.

### UC-19E — 100-target cap (SVSH is a dev / admin tool)

1. If the mock supports simulating >100 fleet targets, `cd` into them and run
   `inventory refresh`.
2. **Expected:** `refuses more than 100 targets ... use the server-side
   scheduled fleet refresh instead`. For single-VM test setups this case is
   verified by code review rather than execution.

### VM run record

- 2026-11-02 PASS on Vijay's VM. UC-19A, UC-19B, UC-19C, UC-19D all green:
  per-user app visible with `HKU\S-1-5-21-...` hive; refresh landed in 1-3 s;
  offline refusal immediate; both bad-flag cases returned clean errors
  without crashing the REPL.

