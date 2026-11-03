# Aegis One — Patching Tester Guide (Standalone)

**Audience:** QA engineer with no prior exposure to this codebase.
**Goal:** install the Aegis One agent on fresh Windows VMs, drive it with the reference `mockserver.exe`, and record PASS/FAIL for every patch-related test case (UC-1 … UC-19) — including UC-18 third-party installed-apps inventory and UC-19 HKU per-user parity + on-demand `inventory refresh`.

This guide is **self-contained**. You do not need to open any other document in the repository to execute it.

> `mockserver.exe` is the **reference server** — not the production backend. It drives the agent exactly the way the real server must, prints every message it receives, and takes operator commands from a built-in NSH (Network Shell) REPL. The production server team builds the real backend; the agent-side behavior is what you verify here.

---

## 1. Overview

- You will install the agent on **two** VMs (Windows 11 and Windows Server 2022), connect them to a mockserver running on your dev machine, and exercise install / uninstall / cache / scan / reboot / failure flows plus the two inventory lanes.
- Every test case is a self-contained RUN record: GOAL, STEPS, EXPECTED, PASS CRITERIA.
- Record outcomes in [Section 9](#9-record-sheet).

---

## 2. Pre-requisites

### 2.1 VMs

| Item | Minimum |
|---|---|
| OS | **Windows 11 Pro 22H2+** AND **Windows Server 2022** (two separate VMs) |
| CPU | 2 vCPU |
| RAM | 4 GB |
| **Disk free** | **≥ 15 GB** on the system drive — a real cumulative update needs that much to install; a tighter VM throws `exit 112 insufficient disk` by design |
| Admin rights | required — agent runs as `LocalSystem` and installs patches |
| Snapshot | **Take a snapshot of every VM BEFORE anything is installed** — tests apply real patches; revert when a test case needs a clean baseline |

### 2.2 Dev machine (where mockserver runs)

| Item | Purpose |
|---|---|
| Windows 10/11 or Server 2022 | run `mockserver.exe` |
| LAN reachable from VMs | VMs dial back to this host on TCP `50051` and `50080` is for local health probe |
| Firewall hole | Allow inbound TCP **50051** on the dev machine so VMs can reach the mock |

### 2.3 Binaries you need

All of these are under `dist/` in the repo:

| File | Purpose |
|---|---|
| `AegisOneClient.msi` | Agent installer (MSI, interactive or silent). Installs the `AegisOneClient` Windows service. |
| `AegisOneClient.exe` | Agent binary (also embedded in the MSI). For reference / manual swap. |
| `mockserver.exe` | Reference server. Drives the agent; prints what it receives. Hosts the NSH REPL. |
| `certgen.exe` | Generates the dev mTLS certificate set. |

All are **Windows x64**. Run everything **as Administrator**.

### 2.4 Patch files (`.msu`) you must prepare

Download **per OS** from the [Microsoft Update Catalog](https://www.catalog.update.microsoft.com):

1. One **small, no-reboot** update (e.g. a .NET cumulative) — call it `<noreboot>.msu`.
2. One **reboot-required** OS cumulative — call it `<cumulative>.msu`. DISM/wusa returns `exit code 3010` on success.

> Use the **correct package per OS**. A Win11 package is NOT applicable on Server 2022 and vice-versa.

Compute each file's **SHA-256** on the dev machine:
```cmd
certutil -hashfile C:\deploy\<noreboot>.msu SHA256
certutil -hashfile C:\deploy\<cumulative>.msu SHA256
```
Save them — mockserver env vars will reference these hashes.

### 2.5 `wsusscn2.cab` (compliance-scan catalog)

Needed for UC-10 (`scan_now`) and UC-11 (new cab).

```
https://go.microsoft.com/fwlink/?LinkId=76054
```
(Alternatively grab the WSUS offline cab from Microsoft. ~900 MB download.)

Copy the file to the VM at `C:\deploy\wsusscn2.cab`. Setup step 3.4 moves it into place.

### 2.6 Critical env-var gotcha

Multiple env vars control the agent. **One is a trap:**

> **`AEGIS_DEVICE_STATE_INTERVAL` is parsed as INTEGER SECONDS, not Go duration format.**
> `setx AEGIS_DEVICE_STATE_INTERVAL 30s /M` silently falls back to the **5-minute default**.
> You MUST write `setx AEGIS_DEVICE_STATE_INTERVAL 30 /M` (plain integer). Restart the service for it to take effect.

The handful of settings you may actually touch during testing:

| Setting | Meaning | Default | Notes |
|---|---|---|---|
| `AEGIS_DEVICE_STATE_INTERVAL` | fast-lane cadence (seconds) | `300` (5 min) | **integer seconds**, not Go duration |
| `AEGIS_RECON_SCAN_INTERVAL` | full cab-scan cadence (seconds) | `604800` (weekly) | daily = `86400` for test |
| `AEGIS_INSTALL_DISK_MULTIPLIER` | free-disk pre-flight = fileSize × this | `2` | lower on tight VMs |
| `AEGIS_CACHE_MAX_BYTES` | staging cache cap (bytes) | `2147483648` (2 GB) | shrink for UC-9 cache-full test |
| `AEGIS_LOG_LEVEL` | log verbosity | `info` | `debug` to SEE fast-lane ticks |
| `AEGIS_HEALTH_ADDR` | local health endpoint | `127.0.0.1:50080` | `curl /status` |

Change them in the registry:
```cmd
reg add "HKLM\SOFTWARE\AegisOne\Client\Settings" /v <NAME> /t REG_SZ /d <VALUE> /f
sc stop AegisOneClient & sc start AegisOneClient
```

---

## 3. One-time setup

### 3.1 Generate the dev mTLS certificate set (once)

On the **dev machine**, in the folder holding the binaries:
```cmd
certgen.exe certs
```
Produces `certs\ca.crt`, `certs\ca.key`, `certs\server.crt`, `certs\server.key`, `certs\client.crt`, `certs\client.key`.

> **⚠ DEV ONLY, NOT PRODUCTION.** `certgen.exe` writes private keys in cleartext for one-shot bring-up. Production uses a proper PKI with HSM / cert-manager. Do not reuse this certs/ folder outside of testing.

Use **ONE** `certs\` set for the mockserver AND all agents. The agent's `ca.crt` **must be the same CA** that signed the server cert, or mTLS fails with `x509: ECDSA verification failure ("Aegis One Dev CA")`.

### 3.2 Start the mockserver

Still on the dev machine:
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
mockserver.exe
```
The mock listens on `:50051` (all interfaces → LAN-reachable). The NSH REPL is a separate invocation (see 3.5).

> The mock reads `AEGIS_*` env vars at startup and sends matching commands when the agent connects. **To run a new scenario: Ctrl+C the mock, `set` new vars, start it again.** This is deliberate — the test cases below each list the env vars to set.

### 3.3 Install the agent on a VM (silent — recommended)

Copy `AegisOneClient.msi` and `certs\ca.crt` to the VM, e.g. `C:\deploy\`. From an **admin** CMD:
```cmd
msiexec /i C:\deploy\AegisOneClient.msi /qn ^
  SERVERADDRESS=<mockserver-LAN-IP>:50051 ^
  SERVERNAME=aegis-server ^
  ENROLLTOKEN=tenant-abc-123 ^
  CACERTFILE=C:\deploy\certs\ca.crt
```
- Replace `<mockserver-LAN-IP>` with the dev machine's IP. Use `127.0.0.1` only if the mock runs ON the same VM.
- `SERVERNAME=aegis-server` matches the cert; TLS verifies by name, not IP, so dialing by IP is fine.

**Interactive install** alternative — double-click `AegisOneClient.msi` and fill the wizard with the four fields above.

### 3.4 Put `wsusscn2.cab` in place

```cmd
copy C:\deploy\wsusscn2.cab "C:\Program Files\AegisOne\Client\SKVC\wsusscn2.cab"
sc stop AegisOneClient & sc start AegisOneClient
```
Without this file UC-10 / UC-11 cannot run — the agent logs a clear error ("cab not present").

### 3.5 Start the NSH REPL (dev operator shell)

NSH is the mockserver's built-in operator REPL — the equivalent of a BigFix WebUI / BladeLogic nexec session, pure text. On the **dev machine**, in a SECOND window:
```cmd
mockserver.exe nsh
```
You get a `nsh>` prompt. **This is the right invocation.** Do NOT set `AEGIS_NSH=1` — that was wrong guidance from an earlier version of this guide; use the `nsh` subcommand.

### 3.6 Verify the agent is enrolled and connected

On the VM:
```cmd
sc query AegisOneClient
rem   STATE = RUNNING

curl http://127.0.0.1:50080/status
rem   {"connected":true,"enrolled":true, ... }
```
Mock output (on the dev machine) must show:
```
Enroll OK device_id=<GUID> computer_id=<n>
Agent connected! device_id=<GUID>
```

### 3.7 Second VM against the SAME mockserver

Repeat 3.3 on the Server 2022 VM. **No server-side change required.**

- Use the same `ENROLLTOKEN=tenant-abc-123`.
- Use the same `ca.crt` (copied over).
- Fresh install per VM — **never copy `identity.json` or client certs between VMs** (would duplicate identity).

### 3.8 Agent on-disk layout (for reference while debugging)

Everything the agent owns lives under one install root:
```
C:\Program Files\AegisOne\Client\
  AegisOneClient.exe                      ← service binary (Task Manager: "AegisOne Agent")
  SKVC\                                   ← all agent state; per-device; never copy between machines
    config.json                           ← optional; absent by default
    identity.json                         ← persisted device identity (hardware-bound)
    wsusscn2.cab                          ← offline compliance catalog
    installed-kbs.json                    ← authoritative installed-KB set from the last full scan
    reboot-pending.json                   ← KBs awaiting a reboot
    certs\                                ← ca.crt, client.crt, client.key.enc (DPAPI-sealed)
    cache\                                ← staged (pre-cached) encrypted patches
    download\                             ← runtime server-delivered patch files
    buffer\patch-buffer.jsonl             ← durable outbound report buffer
    logs\agent-YYYY-MM-DD.log             ← rotating daily log (kept per AEGIS_LOG_MAX_AGE_DAYS)
    logs\softdist\<action_id>.transcript  ← per-action softdist execution transcript
    logs\run\<cmd_id>.transcript          ← per-action run_command transcript
    logs\shell\<session_id>.transcript    ← per-session shell transcript
```

The agent log is where every message in this guide quotes from:
```
C:\Program Files\AegisOne\Client\SKVC\logs\agent-YYYY-MM-DD.log
```

---

## 4. How to read this guide

### Prompts

| Prompt you see | Where you are |
|---|---|
| `C:\>` | Windows CMD on the dev machine or VM — regular shell |
| `mockserver>` or raw mockserver output | the mockserver console (started in 3.2) |
| `nsh>` | NSH REPL with no target selected |
| `<hostname>>` | NSH REPL with a single device target (after `cd <hostname>` or `cd \<hostname>`) |
| `[N hosts]>` | NSH REPL with multiple device targets (after `cd \some-group`) |
| `... [DELIM]>` | NSH heredoc continuation (inside a `script ... until DELIM` block) |

### Target selection in NSH

- `cd \` — clear targets, return to `nsh>` prompt.
- `cd <hostname>` — single target by hostname (what the VM reports as `hostname`).
- `cd \<group>` — group root (if the mock has a fleet group defined).

### Action phases

Every signed command the agent accepts streams an `ActionStatus` with a `phase`:
```
QUEUED → DOWNLOADING → VERIFYING → RUNNING → SUCCESS | FAILED | NOT_APPLICABLE
```
`SUCCESS`, `FAILED`, `NOT_APPLICABLE` are terminal. `NOT_APPLICABLE` is a correct outcome (not a failure) — e.g. "already installed".

### The two detection lanes (read before UC-10)

| Lane | What it reports | Cost | When | Log prefix |
|---|---|---|---|---|
| **FAST LANE** (`DeviceStateReport`) | OS **build** + **reboot_pending** + **installed_apps** list | ~ms (registry) | every `AEGIS_DEVICE_STATE_INTERVAL`, send **on change** | `FAST-LANE patch-state:` |
| **FULL SCAN** (`PatchScanResult`) | **missing** / **superseded** / **installed** updates (full metadata, authoritative WUA) | ~2-4 min (WUA scan vs cab) | startup + weekly + new-cab + `scan_now` + CBS change (debounced) | `FULL-SCAN (cab):` |

The fast-lane build + reboot state is cheap; the full scan is the authoritative WUA view (what Settings → Windows Update shows).

---

## 5. Test cases

Run each on **both Win11 and Server 2022**. Record results in Section 9.

> **For each case:** Ctrl+C the mock, `set` the env vars shown, start it again, watch mock output **and** the agent log.

### UC-1 — Enroll + connect + health

**GOAL:** confirm the agent can bring up its identity + mTLS cert and connect to the mock.

**STEPS:**
1. Install the agent (3.3). Fresh identity → agent generates a keypair, sends a CSR, mock signs the per-device cert.
2. On the VM: `curl http://127.0.0.1:50080/status`.

**EXPECTED:**
- Mock prints `Enroll OK ...` and `Agent connected! device_id=<GUID>`.
- `/status` returns `"connected":true,"enrolled":true`.
- Agent log has `enroll: success` and `grpc: connected`.

**PASS CRITERIA:** all three above.

---

### UC-2 — Single patch install (no reboot)

**GOAL:** install a small no-reboot update and see the full action lifecycle.

**STEPS:**
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_INSTALL_FILE=C:\deploy\<noreboot>.msu
set AEGIS_INSTALL_KB=<KB_NO_REBOOT>
set AEGIS_INSTALL_HASH=<sha256 of the no-reboot msu>
mockserver.exe
```

**EXPECTED:** mock streams
```
action status type=patch_install phase=QUEUED
action status type=patch_install phase=VERIFYING
action status type=patch_install phase=RUNNING
action status type=patch_install phase=SUCCESS exit_code=0 reboot_required=false
```

**PASS CRITERIA:** Terminal phase is `SUCCESS` with `exit_code=0`, `reboot_required=false`.

---

### UC-3 — Single patch install (reboot required) + reboot status

**GOAL:** confirm DISM 3010 is handled, agent reports reboot_pending, and a real reboot clears it.

**STEPS:**
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_INSTALL_FILE=C:\deploy\<cumulative>.msu
set AEGIS_INSTALL_KB=<KB_CUMULATIVE>
set AEGIS_INSTALL_HASH=<sha256>
mockserver.exe
```
Then **reboot the VM**.

**EXPECTED:**
- `SUCCESS exit_code=3010 reboot_required=true`.
- Next fast-lane report: `reboot_pending: true (sources=[CBS], awaiting_kbs=[<KB>])`.
- After reboot: next report shows `reboot_pending: false`, OS build advanced.
- The next full scan lists the KB under `installed`.

**PASS CRITERIA:** all four above.

---

### UC-4 — Multiple patches (serialized)

**GOAL:** confirm installs run one at a time (Windows servicing stack is serial).

**STEPS:** comma-separate the three env vars (same index across all three):
```cmd
set AEGIS_INSTALL_FILE=C:\deploy\a.msu,C:\deploy\b.msu
set AEGIS_INSTALL_KB=KB_A,KB_B
set AEGIS_INSTALL_HASH=<hashA>,<hashB>
mockserver.exe
```

**EXPECTED:** `install-001` runs to a terminal phase, then `install-002` begins. Both complete.

**PASS CRITERIA:** The two installs are serial (second does not start until the first terminates). Other mock traffic (heartbeats etc.) stays responsive.

---

### UC-5 — Reinstall (idempotency — two independent layers)

**GOAL:** confirm redundant install of an already-present KB is skipped loud-and-clear, never a misleading SUCCESS.

**STEPS:** Re-send UC-2 or UC-3 after the install completed.

**EXPECTED:**
```
action status type=patch_install phase=NOT_APPLICABLE reason="already installed (skipped)"
```
Or (if no full scan has run since the install, falls back to installer-level):
```
action status type=patch_install phase=NOT_APPLICABLE reason="already installed (nothing to do)"
```
(wusa exit `2359302` / `0x240006` is the installer-level signal.)

**PASS CRITERIA:** Phase is `NOT_APPLICABLE` and the reason clearly says "already installed". DISM/wusa was NOT re-invoked.

---

### UC-6 — Uninstall (server-driven) + manual uninstall auto-detect

**GOAL:** server can remove an installed KB via a signed `uninstall_patch`, AND the agent auto-detects a manual uninstall (Control Panel) via CBS watch.

**STEPS A — Server-driven:**
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_UNINSTALL_KB=<KB_to_remove>
mockserver.exe
```

**EXPECTED A:** `action status type=patch_uninstall RUNNING → SUCCESS` (or `NOT_APPLICABLE` if the KB isn't installed; `reboot_required=true` if needed to finalize). The agent uses the WUA COM API, not `wusa.exe`.

**STEPS B — Manual:** On the VM, Settings → Windows Update → Update history → Uninstall updates → pick a KB → uninstall.

**EXPECTED B:** no `ActionStatus` (there was no server command), but within ~1 minute the agent's CBS servicing watch triggers a full scan and the KB appears under `missing` again (and is dropped from `installed`).

**PASS CRITERIA:** Both paths work. Server-driven is instant; manual settles within ~2 minutes.

> **⚠ Known limitation — cumulative / baked-in updates may be non-removable.**
> LCUs, SSUs, and image-baked updates often cannot be removed programmatically.
> The agent reports this fail-loud as `WU_E_ALL_UPDATES_FAILED (0x80240022)` with the guidance "try Control Panel → Installed Updates (manual) which uses a different (wusa/CBS) path". This is EXPECTED, not a bug.

---

### UC-7 — Pre-cache (stage) a patch, then install from cache

**GOAL:** confirm peer-cache staging works (encrypted-at-rest) and the install finds the staged file instead of re-downloading.

**STEPS:**
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_SERVE_PATCH=C:\deploy\<patch>.msu   REM mock serves the bytes
set AEGIS_STAGE_KB=<KB>
set AEGIS_STAGE_HASH=<sha256>
set AEGIS_STAGE_FILE=<patch>.msu              REM filename hint (extension matters)
mockserver.exe
```
Then **separately** run UC-2 or UC-3 for the SAME `<KB>`.

**EXPECTED:**
- Staging phase: `action status type=stage DOWNLOADING → SUCCESS "patch staged in cache"`.
- Install phase (second run) agent log: `patch found in cache (staged), using it` — no re-download.

**PASS CRITERIA:** Both phases above.

---

### UC-8 — Peer-cache content delivery (FetchContent)

**GOAL:** confirm `.msu` bytes stream over mTLS peer-cache, not from the internet.

**EXPECTED:** UC-7 already exercises `FetchContent` for a patch. UC-11 does the same for a new cab. In both cases SHA-256 of the received bytes must match the mock-stated hash — a mismatch is a FAILED stage with a clear reason.

**PASS CRITERIA:** `FetchContent` streams land successfully. No outbound internet request from the agent in UC-7 / UC-11 (verify with a brief network capture on the VM if required).

---

### UC-9 — Cache size cap / cache-full handling

**GOAL:** confirm the agent fails loudly when the staging cache has no room.

**STEPS:**
```cmd
reg add "HKLM\SOFTWARE\AegisOne\Client\Settings" /v AEGIS_CACHE_MAX_BYTES /t REG_SZ /d 10 /f
sc stop AegisOneClient & sc start AegisOneClient
```
Then stage a patch (UC-7 steps) that is clearly bigger than 10 bytes.

**EXPECTED:** mock prints `CACHE-FULL report -> needed=<n> cap=<n> in_use=<n>` AND `action status type=stage FAILED "cache full"`.

Reset the cap afterwards:
```cmd
reg add "HKLM\SOFTWARE\AegisOne\Client\Settings" /v AEGIS_CACHE_MAX_BYTES /t REG_SZ /d 2147483648 /f
sc stop AegisOneClient & sc start AegisOneClient
```

**PASS CRITERIA:** both the CacheFullReport and the FAILED action status.

---

### UC-10 — On-demand compliance scan (`scan_now`)

**GOAL:** operator can trigger a full WUA cab scan on demand.

**STEPS:**
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_SCAN_NOW=1
mockserver.exe
```

**EXPECTED:**
- Agent log: `scan_now: on-demand compliance scan requested` → `FULL-SCAN (cab): triggered (on-demand)`.
- Mock prints the scan `action status`, the `command result accepted`, and the resulting `patch scan result` with `missing` / `installed` entries (full WUA metadata).

**PASS CRITERIA:** scan runs end-to-end and the result arrives at the mock within 2-4 minutes.

---

### UC-11 — New catalog (cab) → auto full scan

**GOAL:** when the server advertises a new `wsusscn2.cab`, the agent fetches it and immediately runs a scan.

**STEPS:** Prepare a NEW `wsusscn2.cab` (different bytes). Then:
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_CAB_VERSION=v2
set AEGIS_CAB_SHA256=<new cab sha256>
set AEGIS_SERVE_CAB=C:\deploy\wsusscn2-new.cab
mockserver.exe
```

**EXPECTED:**
- Agent log: fetches the new cab via `FetchContent` → `FULL-SCAN (cab): triggered (new catalog / on-demand)`.
- A local install/uninstall WITHOUT a cab change does **NOT** trigger a scan (that's a fast-lane concern) — this protects endpoint load.

**PASS CRITERIA:** new cab lands, scan runs with it.

---

### UC-12 — Superseded patches in the scan result

**GOAL:** confirm the scan reports superseded (replaced-by-newer) updates with the correct tag so the console can categorize them.

**STEPS:** Run UC-10 against a VM that is behind on patches.

**EXPECTED:** the mock prints `[MISSING]` and `[SUPERSEDED]` entries. Each entry carries KB, MS bulletin IDs, severity, CVE list, size range (min—max), and the supersedes list.

**PASS CRITERIA:** at least one `[SUPERSEDED]` entry present, with a non-empty `supersedes` list.

---

### UC-13 — Reboot / cancel reboot command

**GOAL:** server can schedule a reboot with a user-visible delay, and can cancel it.

**STEPS A — schedule:**
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_REBOOT_DELAY=90
mockserver.exe
```
**EXPECTED A:** `action status type=reboot SUCCESS "reboot scheduled in 90s"`. Any logged-in user sees the Windows reboot notification.

**STEPS B — cancel:** before the 90s elapses, Ctrl+C the mock and:
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_CANCEL_REBOOT=1
mockserver.exe
```
**EXPECTED B:** `action status type=reboot SUCCESS "reboot cancelled"`. The VM does NOT reboot.

**PASS CRITERIA:** A reboots the VM after ~90s; B cancels it cleanly.

---

### UC-14 — Failure scenarios (all fail LOUD + CLEAR)

**GOAL:** confirm every failure mode is a classified, actionable reason — never a silent drop.

| Sub | Trigger | Expected |
|---|---|---|
| 14A | `AEGIS_INSTALL_HASH` set to a wrong SHA | `FAILED` `"sha256 mismatch"` — DISM never runs |
| 14B | `AEGIS_INSTALL_FILE` points to a missing path | `FAILED` `"file not found"` |
| 14C | VM with <~5 GB free + CU | `FAILED` `"insufficient disk space ..."` (exit 112). CBS rolls back; VM stays unpatched |
| 14D | Wrong-OS patch (Win11 msu on Server 2022) | `FAILED` with classified reason — fails fast, no retry |

**PASS CRITERIA:** every sub lands at `FAILED` with the quoted reason text (or close equivalent). None silently SUCCESS.

---

### UC-14b — DISM → wusa install fallback

**GOAL:** some `.msu` files cannot be installed via DISM (Server 2022 especially). The agent must automatically fall back to `wusa.exe`.

**STEPS:** install any `.msu` on Server 2022 that triggers DISM `exit 50 / 0x80070032 "The request is not supported"`.

**EXPECTED (agent log):**
```
deploy: DISM attempt failed ... exit_code=50 "DISM does not support installing this .msu (exit 50 ...) — handled by the wusa fallback"
install_patch: DISM could not install this .msu — falling back to wusa (the manual install path)
deploy: wusa install succeeded exit_code=0|3010 reboot_required=..
```

**PASS CRITERIA:** DISM exit-50 is treated as "try wusa", NOT a terminal failure. Final action status is `SUCCESS` via wusa.

> Disk-pre-flight failures are NOT retried via wusa (same disk).

---

### UC-14c — Scan vs install serialization

**GOAL:** confirm compliance scan and install/uninstall never run concurrently (both hammer Windows Update / TrustedInstaller).

**STEPS:**
- A: start UC-10 (`scan_now`) and WHILE the scan is running, dispatch UC-2 (install).
- B: start UC-2 (install) and WHILE it's running, dispatch UC-10 (scan).

**EXPECTED A (log):**
```
install_patch: waiting for an in-flight compliance scan to finish before installing
```
Install begins once the scan terminates.

**EXPECTED B (log):**
```
FULL-SCAN (cab): deferred — an install/uninstall is in progress; will re-scan after it settles
```
Scan runs once the install terminates (post-install CBS watch triggers it).

**PASS CRITERIA:** you NEVER see `wusa install` and `offline WUA search` both logging "still working" simultaneously.

---

### UC-15 — Disk pre-flight (`fileSize × multiplier`)

**GOAL:** confirm the agent refuses to run DISM if free disk < `fileSize × AEGIS_INSTALL_DISK_MULTIPLIER`.

**STEPS:** On a VM near its disk cap, try to install a large CU. Then lower the multiplier:
```cmd
reg add "HKLM\SOFTWARE\AegisOne\Client\Settings" /v AEGIS_INSTALL_DISK_MULTIPLIER /t REG_SZ /d 1 /f
sc stop AegisOneClient & sc start AegisOneClient
```
Retry.

**EXPECTED:** at the default multiplier, `FAILED exit 112 insufficient disk` BEFORE DISM is invoked. At multiplier=1 the install may proceed (if disk is still enough for 1×).

**PASS CRITERIA:** pre-flight blocks the install at the right threshold.

---

### UC-16 — Cert renewal (long-lived certs)

**GOAL:** confirm the client cert renews over the existing mTLS channel without re-enrolling (no token).

**STEPS:**
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_CERT_TTL_SECONDS=60
mockserver.exe
```
On the VM:
```cmd
reg add "HKLM\SOFTWARE\AegisOne\Client\Settings" /v AEGIS_CERT_CHECK_INTERVAL /t REG_SZ /d 10 /f
sc stop AegisOneClient & sc start AegisOneClient
```

**EXPECTED:** at ~2/3 of 60s TTL (so ~40s in) the agent calls `Renew` over mTLS and receives a fresh cert. Mock log: `Renew OK`. Agent log: `cert: renewed via mTLS`.

**PASS CRITERIA:** renewal happens without a new enrollment token.

> Production uses 398-day certs; this short TTL is for testing only.

---

### UC-17 — Both OSes

**GOAL:** the agent binary is single-source for Win10/11 + Server 2016/2019/2022; only patch applicability differs per OS.

**STEPS:** repeat UC-2 through UC-6 on **Server 2022** using Server-2022-applicable `.msu` files.

**PASS CRITERIA:** no OS-specific agent code path shows up; failures are patch-applicability failures (correct, not agent bugs).

---

### UC-18 — Third-party installed-apps inventory (fast-lane)

**GOAL:** confirm the agent reports every HKLM Uninstall hive entry (and HKLM\WOW6432Node) in each `DeviceStateReport`, and that an install or uninstall of a tracked app triggers an immediate resend (fingerprint hash caught the change).

**MOCKSERVER:**
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
mockserver.exe
```
Open the NSH REPL in a second window:
```cmd
mockserver.exe nsh
```

**NSH REPL:**
```
nsh> cd <vm-hostname>
<vm-hostname>> inventory refresh
<vm-hostname>> inventory apps
<vm-hostname>> inventory apps chrome
<vm-hostname>> inventory apps mozilla
```

**ENDPOINT (admin CMD on the VM):**
```cmd
msiexec /i C:\deploy\7z<version>-x64.msi /qn
```
Wait one `AEGIS_DEVICE_STATE_INTERVAL` cycle (set `30` seconds for test).

**EXPECTED:**
- First `Received patch state report` on the mock shows `installed_apps: N`.
- `inventory apps` prints a table of DisplayName, DisplayVersion, Publisher, Hive for every reported app.
- `inventory apps chrome` (or `mozilla`, `adobe`, etc.) filters by case-insensitive substring against DisplayName or Publisher.
- After the 7-Zip install, the next FAST-LANE log line shows `installed_apps: N+1` and a fresh `Received patch state report` lands within one interval — fingerprint caught the change.

**UC-18A** — baseline inventory present: expect at least Chrome / Firefox / Edge / 7-Zip / Notepad++ / VMware Tools / Visual C++ runtimes / AegisOne Client / Mozilla Maintenance Service entries (vary per VM).

**UC-18B** — filter: substring match works on DisplayName AND Publisher.

**UC-18C** — change detection: install → `installed_apps=N+1`; uninstall (`msiexec /x`) → `installed_apps=N`. No agent service restart required.

**UC-18D** — suppression: between CHANGED sends, multiple interval cycles pass with no `DeviceStateReport` (fingerprint suppresses the no-change case). Set `AEGIS_LOG_LEVEL=debug` to see `no change this cycle (skip send)`.

**UC-18E** — hive separation: Google Chrome and Microsoft Edge usually show `Hive=HKLM32` (WOW6432Node); 7-Zip, Firefox, Notepad++, VMware Tools, AegisOne Client show `Hive=HKLM64` (native). Verify both views are represented.

**UC-18F** — multi-device fleet summary (requires ≥ 2 live agents on the mock):
```
nsh> cd \                      # clear targets (no cd)
nsh> inventory apps            # fleet-wide summary: HOST | INVENTORY_SIZE | LAST_REPORT_AT
```
Both agents listed with their most recent inventory count. Skip if only one VM is online.

**PASS CRITERIA:** 18A, 18B, 18C, 18D, 18E all green. 18F green if two agents are live, N/A otherwise.

---

### UC-19 — HKU per-user Uninstall parity + on-demand `inventory refresh`

**GOAL:** per-user installs (Chrome / Teams / Spotify) that appear in **Settings → Apps** but under `HKEY_USERS\<SID>\...\Uninstall` must be reported too. Also: an admin can PULL a fresh inventory on demand, without waiting for a change event.

#### UC-19A — HKU per-user inventory present

**STEPS:**
1. Record the current app count from `inventory apps`.
2. Install an app per-user (any of):
   - Google Chrome standalone installer run in a user context (chooses per-user install if admin consent is declined).
   - Microsoft Teams (consumer / per-user build).
   - Spotify desktop.
   - Any MSI with: `msiexec /i file.msi ALLUSERS=""` (empty = per-user).
3. Wait one `AEGIS_DEVICE_STATE_INTERVAL`.
4. NSH:
   ```
   <vm-hostname>> inventory refresh
   <vm-hostname>> inventory apps <name>
   ```

**EXPECTED:** the new app appears with `HIVE = HKU\S-1-5-21-...` (or `HKU\S-1-5-21-...\WOW6432Node` for a 32-bit per-user installer).

**PASS CRITERIA:** HKU\<SID> hive is set correctly for the per-user install.

#### UC-19B — On-demand refresh without any change

**STEPS:** change nothing on the device. From NSH:
```
<vm-hostname>> inventory refresh
```

**EXPECTED (within 5 seconds):**
- Mockserver: `fresh inventory (N apps, collected_at=<ts>)`, `refresh complete — 1/1 received, 0 timed out`.
- Agent log: `on-demand refresh requested — resetting fingerprint`, then `FAST-LANE patch-state: CHANGED -> sending`.

**PASS CRITERIA:** inventory refresh works even when nothing changed since the last send.

#### UC-19C — Offline target handled

**STEPS:**
1. On the VM: `sc stop AegisOneClient`.
2. NSH: `inventory refresh`.

**EXPECTED (immediate, no 5-second wait):** `OFFLINE (no live stream) — refresh skipped`.

**PASS CRITERIA:** refusal is immediate and the message is clear.

#### UC-19D — `--timeout` flag parsing

**STEPS from NSH:**
1. `inventory refresh --timeout 15` → succeeds against a healthy target with `waiting up to 15s`.
2. `inventory refresh --timeout abc` → `--timeout expects a positive integer (seconds)`.
3. `inventory refresh --fast` → `unknown argument "--fast"`.

**PASS CRITERIA:** all three outcomes clean (no REPL crash).

#### UC-19E — 100-target cap

**STEPS (only if the mock supports a fake fleet of >100 targets):** `cd \big-group` → `inventory refresh`.

**EXPECTED:** `refuses more than 100 targets ... use the server-side scheduled fleet refresh instead`.

**PASS CRITERIA:** cap enforced. On single-VM test setups this case is verified by code review of `cmd/mockserver/nsh.go` (`maxTargets = 100`) rather than execution.

> NSH is a **dev/admin tool**, not a fleet-ops surface. Fleet-wide refresh belongs in a server-side scheduled operation that batches waves and rate-limits. The agent handler is identical, only the dispatcher differs.

---

## 6. Agent log — quick reference

| Line | Meaning |
|---|---|
| `FAST-LANE patch-state: CHANGED -> sending ... build=.. reboot_pending=.. installed_apps=..` | fast-lane report sent (something changed) |
| `FAST-LANE patch-state: no change this cycle (skip send)` (DEBUG) | fast-lane tick, suppressed by fingerprint (set `AEGIS_LOG_LEVEL=debug` to see) |
| `FAST-LANE patch-state: on-demand refresh requested — resetting fingerprint` | NSH `inventory refresh` triggered |
| `FULL-SCAN (cab): running at startup / periodic / triggered` | heavy cab scan + why it ran |
| `FULL-SCAN (cab): result buffered ... kbs=[..]` | missing KBs from the scan |
| `action status ... type=patch_install phase=..` | unified per-action lifecycle |
| `install_patch: finished ... reason="installed — completed, no restart needed"` | SUCCESS, no reboot |
| `install_patch: finished ... reboot_required=true reason="installed — RESTART REQUIRED to complete"` | SUCCESS, exit 3010 |
| `install_patch: waiting for an in-flight compliance scan to finish before installing` | UC-14c serialization |
| `deploy: running DISM/wusa command=..` (DEBUG) | exact command text per stage |
| `deploy: DISM attempt failed ... exit_code=50 ... — handled by the wusa fallback` | UC-14b auto-fallback |

Mockserver side prints: `Enroll OK`, `Agent connected!`, `patch state report`, `patch scan result`, `action status`, `CACHE-FULL report`, and (NSH) the inventory tables.

---

## 7. Troubleshooting

| Symptom | Root cause / fix |
|---|---|
| `x509: ECDSA verification failure ("Aegis One Dev CA")` | Agent `ca.crt` ≠ server CA. Use one `certs\` set. Re-enroll: delete `SKVC\certs\client.*` (keep `identity.json`), copy matching `ca.crt`, re-add token, restart. |
| `token=<unset — enrollment will reject>` | `set AEGIS_ENROLL_TOKEN=tenant-abc-123` (plain, no quotes). |
| Install `NOT_APPLICABLE` unexpectedly | that KB is already installed (per the last scan or installer); use a genuinely-missing KB or revert to a behind snapshot. |
| Install logs `DISM attempt failed ... exit 50` | **expected on some .msu** — agent falls back to wusa automatically (UC-14b). Look for `wusa install succeeded` right after. |
| Install `exit 112 "insufficient disk"` | free disk (≥ ~15 GB), or lower `AEGIS_INSTALL_DISK_MULTIPLIER`. |
| Uninstall `FAILED 0x80240022` / "not uninstallable" | cumulative / SSU / baked-in update — expected; use Control Panel → Installed Updates manually (UC-6 note). |
| Fast lane "not running" | it IS; silent when unchanged. `AEGIS_LOG_LEVEL=debug` to see ticks. |
| Compliance scan error "cab not present" | copy `wsusscn2.cab` to `SKVC\`, restart (step 3.4). |
| `inventory apps` shows `no DeviceStateReport received yet` | agent sends on-change only; run `inventory refresh` to pull one now (UC-19B). |
| `inventory refresh` times out | agent is live but busy — raise `--timeout 15` or `--timeout 30`. Check VM clock skew (unix seconds comparison). |
| `AEGIS_DEVICE_STATE_INTERVAL` ignored (still 5 min) | integer SECONDS, not `30s`. `setx AEGIS_DEVICE_STATE_INTERVAL 30 /M` and restart service. |

---

## 8. What the real server must implement (reference only)

The mock fakes these for test; the production server team builds them for real:

1. **mTLS + enrollment PKI** — per-tenant token → validate (license-gated) → sign agent CSR → per-device cert; long-lived (~398d) + a `Renew` RPC (no token).
2. **Signed commands** — every command is an Ed25519-signed envelope; configure agents with the server's public verify key (`AEGIS_CMD_VERIFY_KEY`).
3. **Content delivery (peer-cache)** — the agent never touches the internet; the server streams `.msu` / cab bytes over `FetchContent`.
4. **Catalog + diff** — maintain the catalog (including supersedence), push `PatchBaseline` / `CabManifest`, compute missing from the agent-reported installed state + full-scan results.
5. **Action lifecycle + scheduling** — assign `action_id`s, hold actions until their window, dispatch, persist the `ActionStatus` transitions per device per action.
6. **Third-party catalog match** — store `DeviceStateReport.installed_apps`, match against the server's catalog of outdated apps, dispatch `software_deploy` via the existing SoftDist pipeline. BigFix lists ~350 fixlets; Aegis targets 1000+. Agent reports facts; server owns the catalog.
7. **Device reconciliation + licensing + token lifecycle** — identity dedup across re-enrollment, license counting, token issue / revoke.
8. **Console views** — per device: installed / missing / superseded / reboot-pending; categorize by severity / classification / CVE / MS-ID; drive install, scan-now, reboot actions.

---

## 9. Record sheet

Fill one row per sub-case per OS. Attach the agent log excerpt on any FAIL.

| UC | Sub | OS | Date | Tester | PASS / FAIL | Notes |
|---|---|---|---|---|---|---|
| UC-1 |  | Win11 |  |  |  |  |
| UC-1 |  | Srv2022 |  |  |  |  |
| UC-2 |  | Win11 |  |  |  |  |
| UC-2 |  | Srv2022 |  |  |  |  |
| UC-3 |  | Win11 |  |  |  |  |
| UC-3 |  | Srv2022 |  |  |  |  |
| UC-4 |  | Win11 |  |  |  |  |
| UC-4 |  | Srv2022 |  |  |  |  |
| UC-5 |  | Win11 |  |  |  |  |
| UC-5 |  | Srv2022 |  |  |  |  |
| UC-6 | A server-driven | Win11 |  |  |  |  |
| UC-6 | A server-driven | Srv2022 |  |  |  |  |
| UC-6 | B manual | Win11 |  |  |  |  |
| UC-6 | B manual | Srv2022 |  |  |  |  |
| UC-7 |  | Win11 |  |  |  |  |
| UC-7 |  | Srv2022 |  |  |  |  |
| UC-8 |  | Win11 |  |  |  |  |
| UC-8 |  | Srv2022 |  |  |  |  |
| UC-9 |  | Win11 |  |  |  |  |
| UC-9 |  | Srv2022 |  |  |  |  |
| UC-10 |  | Win11 |  |  |  |  |
| UC-10 |  | Srv2022 |  |  |  |  |
| UC-11 |  | Win11 |  |  |  |  |
| UC-11 |  | Srv2022 |  |  |  |  |
| UC-12 |  | Win11 |  |  |  |  |
| UC-12 |  | Srv2022 |  |  |  |  |
| UC-13 | A schedule | Win11 |  |  |  |  |
| UC-13 | B cancel | Win11 |  |  |  |  |
| UC-13 | A schedule | Srv2022 |  |  |  |  |
| UC-13 | B cancel | Srv2022 |  |  |  |  |
| UC-14 | A wrong hash | Win11 |  |  |  |  |
| UC-14 | B missing file | Win11 |  |  |  |  |
| UC-14 | C disk | Win11 |  |  |  |  |
| UC-14 | D wrong OS | Win11 |  |  |  |  |
| UC-14 | A wrong hash | Srv2022 |  |  |  |  |
| UC-14 | B missing file | Srv2022 |  |  |  |  |
| UC-14 | C disk | Srv2022 |  |  |  |  |
| UC-14 | D wrong OS | Srv2022 |  |  |  |  |
| UC-14b | DISM→wusa | Srv2022 |  |  |  |  |
| UC-14c | A scan-then-install | Win11 |  |  |  |  |
| UC-14c | B install-then-scan | Win11 |  |  |  |  |
| UC-14c | A scan-then-install | Srv2022 |  |  |  |  |
| UC-14c | B install-then-scan | Srv2022 |  |  |  |  |
| UC-15 |  | Win11 |  |  |  |  |
| UC-15 |  | Srv2022 |  |  |  |  |
| UC-16 |  | Win11 |  |  |  |  |
| UC-17 | repeat UC-2..UC-6 | Srv2022 |  |  |  |  |
| UC-18 | A baseline | Win11 |  |  |  |  |
| UC-18 | B filter | Win11 |  |  |  |  |
| UC-18 | C change-detect | Win11 |  |  |  |  |
| UC-18 | D suppression | Win11 |  |  |  |  |
| UC-18 | E hive-sep | Win11 |  |  |  |  |
| UC-18 | F fleet-summary | Win11+Srv2022 |  |  |  |  |
| UC-19 | A HKU present | Win11 |  |  |  |  |
| UC-19 | B refresh no-change | Win11 |  |  |  |  |
| UC-19 | C offline | Win11 |  |  |  |  |
| UC-19 | D --timeout | Win11 |  |  |  |  |
| UC-19 | E 100-cap | n/a | n/a | n/a | by code review | `cmd/mockserver/nsh.go` maxTargets = 100 |

---

## UC-21 — Foundation fields + Tier-3 full inventory

**Why:** DeviceStateReport (UC-18/UC-19) is Tier-1 — fast, sends on change,
small payload. UC-21 adds TWO new pieces to the foundation:

1. **Foundation fields on Heartbeat + EnrollRequest** — `schema_version`,
   `agent_capabilities[]`, `device_type`. The server gates feature rollouts
   on these instead of parsing free-form `agent_version` strings; adding
   them NOW makes future protocol changes non-breaking.
2. **Tier-3 FullInventoryReport** — a 6-hour-interval FULL HW + SW snapshot
   (CPU / RAM / disks / NICs / BIOS / chassis + installed_apps). This is the
   SAFETY NET report: even if a Tier-2 change event was missed, one Tier-3
   cycle self-corrects the server's view. Also sent ONCE on
   reconnect-after-offline, with a flap floor so a flapping NIC does not DoS
   the server.

### UC-21 — Pre-reqs

- Agent build from this handoff (binary ≤ 25 MB).
- For speed, set `AEGIS_FULL_INVENTORY_INTERVAL=60` (plain integer SECONDS,
  60 = every 1 min; default is 21600 = 6 h) and
  `AEGIS_RECONNECT_SNAPSHOT_MIN_INTERVAL=30` on the VM. Restart the service.
  **Same env-var gotcha as `AEGIS_DEVICE_STATE_INTERVAL`: INTEGER SECONDS,
  not Go duration format.**

### UC-21A — Foundation fields on Heartbeat

**Goal:** confirm every heartbeat carries `schema_version`,
`agent_capabilities`, `device_type`.

**Steps:**
1. Start mockserver.
2. Start agent on the VM.
3. Within 60 s, mockserver console shows:
   ```
   Received heartbeat -> device: <guid>, hostname: <vm>, version: <v>, os: windows, schema_version: 1, device_type: endpoint, capabilities: 22
   ```

**PASS CRITERIA:**
- `schema_version: 1`
- `device_type: endpoint`
- `capabilities:` count ≥ 15 (dispatch handler count + feature flags)

### UC-21B — Foundation fields on EnrollRequest

**Goal:** confirm the SAME fields on first-enroll (fresh install).

**Steps:**
1. Uninstall agent on VM.
2. Delete `<SKVC>` folder so enrollment runs fresh.
3. Install agent.
4. On the mockserver console (if your mock prints enrollment payload — check
   it does), confirm `schema_version=1`, `device_type=endpoint`, non-empty
   capabilities list appear in the enrollment record.

**PASS CRITERIA:** same three fields present at enrollment. (This is the
"before the first heartbeat" moment the server uses to store the
capability profile atomically with the issued cert.)

### UC-21C — Tier-3 scheduled snapshot

**Goal:** on the configured interval, a FULL inventory snapshot arrives.

**Steps:**
1. Start fresh (mock + agent).
2. Within ONE `AEGIS_FULL_INVENTORY_INTERVAL` cycle (60 s with the test
   override), mockserver console shows:
   ```
   Received FULL inventory -> <device> | trigger: startup, cpu: GenuineIntel <model> (<cores>/<logical> cores), ram: NNNNN MB, disks: N, nics: N, apps: N, system: <vendor> <product>
   ```
3. ~60 s later, another line with `trigger: scheduled`.

**PASS CRITERIA:**
- `trigger` shows `startup` first, then `scheduled` thereafter.
- CPU / RAM / disks / NICs / apps counts are non-zero on a real VM.
- `cpu_model` is a non-empty string (e.g. "Intel(R) Xeon(R) ..." or similar).
- `system_vendor` and `system_product` are non-empty on a real OEM machine
  (may be empty on a stripped VM — not a FAIL in that case).

### UC-21D — Agent log inventory line

**Goal:** the agent itself logs the Tier-3 send with proof of a few-ms cost.

**On the VM:**
```powershell
Select-String -Path "<SKVC>\logs\agent-*.log" -Pattern "TIER-3 full-inventory: snapshot buffered"
```

**Expected line:**
```
INFO TIER-3 full-inventory: snapshot buffered trigger=startup apps=N disks=N nics=N cpu_model="..." ram_bytes=NNNN took_ms=12
```

**PASS CRITERIA:** `took_ms` ≤ 100 (should typically be <20 ms on a modern
VM). If it ever goes above 500 ms, flag — the collector would be eating
too much CPU.

### UC-21E — Reconnect-triggered snapshot (and flap floor)

**Goal:** disconnecting / reconnecting the agent triggers a snapshot;
rapid reconnects within the flap floor are SUPPRESSED.

**Steps:**
1. Stop mockserver on the dev laptop. The agent will disconnect.
2. Wait 10 s.
3. Start mockserver.
4. Within 5 s of mockserver restart, the console shows a FULL inventory
   line with `trigger: reconnect`.
5. Now stop + start mockserver RAPIDLY (within 30 s). The second reconnect
   should NOT trigger another full snapshot (flap floor engaged).
6. Agent log should show:
   ```
   DEBUG TIER-3 full-inventory: reconnect trigger suppressed by flap floor age=... floor=30s
   ```

**PASS CRITERIA:**
- First reconnect: `trigger: reconnect` snapshot arrives.
- Second rapid reconnect: NO second snapshot; `suppressed by flap floor` in
  agent log.

### UC-21F — Binary size budget

**Goal:** confirm the agent binary stays under the 25 MB budget Vijay set.

```powershell
(Get-Item "<path>\dist\AegisOneClient.exe").Length
```

**PASS CRITERIA:** value ≤ 26,214,400 (25 MB). Should be around 21-22 MB
on this handoff.

### UC-21G — No WMI / no COM check

**Goal:** confirm the HW collector did not pull in WMI / COM dependencies
(binary-size and runtime-cost rule from Vijay).

**On the VM, while the agent is running:**
```powershell
Get-Process AegisOneClient | Format-List HandleCount, ProcessName, Modules
# Inspect loaded modules; wbemprox.dll, wbemcore.dll, oleaut32.dll should NOT appear
```

(For a stronger check, run SysInternals `Process Explorer` → View → Lower
Pane → DLLs. No WMI DLLs should be loaded on top of the baseline Windows
services.)

**PASS CRITERIA:** No WMI / wbem DLLs loaded by AegisOneClient.exe.

### UC-21 — Record sheet

| Case | Sub | Env | VM | Start | End | Result | Notes |
|---|---|---|---|---|---|---|---|
| UC-21 | A heartbeat fields | Win11 |  |  |  |  |  |
| UC-21 | B enroll fields | Win11 |  |  |  |  |  |
| UC-21 | C scheduled snapshot | Win11 |  |  |  |  |  |
| UC-21 | D agent log took_ms | Win11 |  |  |  |  |  |
| UC-21 | E reconnect + flap | Win11 |  |  |  |  |  |
| UC-21 | F binary size | n/a |  |  |  | by `Get-Item` | ≤ 26,214,400 |
| UC-21 | G no WMI loaded | Win11 |  |  |  |  |  |

---

## 10. End

If a case FAILs with no clear classification in the agent log, capture:
1. The exact mockserver env vars you set.
2. The mockserver console output from start to the failure.
3. The agent log file for the day (`agent-YYYY-MM-DD.log`).
4. The VM OS build (`winver` → screenshot).

Email the bundle to the dev team.
