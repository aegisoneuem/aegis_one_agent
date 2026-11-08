# Aegis One — SoftDist Tester Guide

**Audience:** QA engineer with no prior Aegis codebase context.
**Purpose:** validate the agent's Software Distribution pipeline end to end on a Windows VM, using the mockserver as the signed-command source. Hinglish/English OK; copy-paste commands exactly.

**Last updated:** 2026-11-08 (handoff `9f0b147`). Blocks G/H/I/K VM-PASS; added Block M (resumable download / checkpoint restart); G now uses two-box server delivery; I redirect fixed.

---

## 1. Overview

**SoftDist** is Aegis's BigFix-equivalent software distribution feature: the server dispatches a signed `software_deploy` (or variant) command to one or more agents. The agent fetches the payload, verifies integrity, runs the installer silently as LocalSystem (or an explicit account), reports phase lifecycle (`QUEUED → DOWNLOADING → VERIFYING → RUNNING → SUCCESS / FAILED / NOT_APPLICABLE`) via a streamed `ActionStatus`, and optionally runs pre / post / rollback commands around the main action.

What this guide covers:

| Block | Theme | Test count |
|---|---|---|
| A | Multi-file ZIP bundles (deploy + custom retention + wrong-hash refusal) | 3 |
| B | Pre / post commands (success, pre-fail blocks main, post-fail is honest) | 3 |
| C | Package dependencies (missing, installed-skip, failed) | 3 |
| D | Advanced detection (file / version / registry ALL / ANY) | 3 |
| E | Real 7-Zip MSI lifecycle (install → upgrade → uninstall) | 5 |
| F | Real Firefox EXE + JSON config + active policy | 1 (+recovery) |
| G | Rollback-on-failure orchestration | 3 |
| H | Crash recovery + durable journal | 2 |
| I | Descendant termination on timeout | 1 |
| J | Reboot-sensitive 3010 + MSI/MST + config-only parameter update | 3 |
| K | Bandwidth throttling (fetch, upload, lower-wins policy) | 3 |
| M | Resumable download / checkpoint restart (disconnect resumes from offset) | 1 (+1 automated) |
| L | Run-As + secure parameters + per-user detection | 5 |
| UE | Uninstall edge cases (ambiguity, HKCU refusal, timeout, tampering, hash pin, …) | 34 |
| UC-18 | Third-party installed-apps inventory (DeviceStateReport) | brief; depth in patching guide |
| UC-19 | HKU per-user Uninstall parity + on-demand `inventory refresh` | brief; depth in patching guide |

Everything is signed-command driven: the agent REFUSES any unsigned or tampered input, and the server (here: mockserver) is the trusted dispatcher. A failed negative case means the protection worked.

---

## 2. Pre-requisites

### 2.1 Hosts

- **Endpoint VM** — fresh Windows 10 or 11 Pro, 2 vCPU / 4 GB RAM minimum. Local admin rights on the VM. Snapshots strongly recommended between blocks.
- **Mockserver host** — your Windows dev machine; needs CMD and the Aegis mockserver binary. Can also be a second VM.
- Both machines must reach each other on TCP 50051 (default agent port). Allow it in Windows Firewall on both.

### 2.2 Binaries

Everything you need is in `dist/`:

| File | Purpose |
|---|---|
| `dist/AegisOneClient.exe` | Agent binary (optional direct replacement) |
| `dist/AegisOneClient.msi` | **Agent installer — use this on the VM** |
| `dist/mockserver.exe` | Mockserver (runs on your dev machine) |
| `dist/certgen.exe` | Dev-only CA + cert generator (see §2.4) |
| `dist/softdist-handoff.json` | Handoff manifest — SHA256 of each binary, test coverage summary |

**Verify the hashes** on the machines you copy to. Example on PowerShell:

```powershell
Get-FileHash <MSI_PATH> -Algorithm SHA256
```

Compare to the `sha256` fields in `dist/softdist-handoff.json`. Hash mismatch = don't test, you have a stale/corrupt file.

### 2.3 Test packages

Download these BEFORE starting block E/F/J:

| Package | URL | Save as |
|---|---|---|
| 7-Zip 26.04 x64 MSI | https://github.com/ip7z/7zip/releases/download/26.04/7z2604-x64.msi | `C:\temp\AegisRealTests\7z2604-x64.msi` on mockserver |
| 7-Zip 26.05 x64 MSI | https://github.com/ip7z/7zip/releases/download/26.05/7z2605-x64.msi | `C:\temp\AegisRealTests\7z2605-x64.msi` on mockserver |
| Firefox ESR x64 EXE | https://www.firefox.com/en-US/download/all/desktop-esr/ (choose Windows 64-bit full installer) | `C:\temp\AegisRealTests\FirefoxPackage\FirefoxSetup.exe` on mockserver |
| ORCA (for J2 MST authoring) | https://learn.microsoft.com/en-us/windows/win32/msi/platform-sdk-components-for-windows-installer-developers | install on mockserver only |
| Fixture pack | `docs/fixtures/softdist/` in the repo | copy whole folder to `C:\temp\AegisSoftDistTests\` on BOTH machines (UE block) |

### 2.4 Env vars on the VM

Before installing the agent, decide these endpoint-wide policies. All optional unless noted.

| Env var | Purpose | Example | Notes |
|---|---|---|---|
| `AEGIS_DEVICE_STATE_INTERVAL` | Layer-1 inventory cycle | `30` | **Integer SECONDS, NOT Go-style `30s`** — a lot of testers trip here. See §4.4. |
| `AEGIS_FETCH_MAX_BYTES_PER_SECOND` | Admin-wide download cap | `500000` | 0 = unlimited (default). K3 uses this to prove "lower wins". |
| `AEGIS_UPLOAD_MAX_BYTES_PER_SECOND` | Admin-wide upload cap | `1000000` | 0 = unlimited (default). |
| `AEGIS_DOWNLOAD_DIR` | Override agent download root | `D:\AegisDownload` | Optional. If set, the `packages/` subfolder lives under this path. |

Set them per-machine as SYSTEM env vars (CMD as admin):

```cmd
setx AEGIS_DEVICE_STATE_INTERVAL 30 /M
```

Then stop/start the agent service for the service to pick them up:

```cmd
sc stop AegisOneClient
sc start AegisOneClient
```

### 2.5 Firewall

Open TCP 50051 inbound on the mockserver host (dev machine):

```cmd
netsh advfirewall firewall add rule name="Aegis mock 50051" dir=in action=allow protocol=TCP localport=50051
```

### 2.6 DEV-ONLY WARNINGS — read before you set anything up

- **`certgen.exe`** stores the CA private key on disk in plaintext. **DEV only.** Never use certgen output in a production environment.
- The mockserver signs commands with a hard-coded dev key. Agents enrolled against this dev CA will trust those signatures. **Do NOT point production agents at a mockserver.**
- `AEGIS_SERVE_SOFTWARE` is a mock-only convenience that lets the mockserver serve a local file as a "package catalog" fetch. The production server uses its own catalog service.

---

## 3. One-time setup

### 3.1 Mockserver host

1. Create a workspace:
   ```cmd
   mkdir C:\temp\mock
   mkdir C:\temp\AegisRealTests
   mkdir C:\temp\AegisBundleDemo-src\settings
   ```
2. Copy from `dist/`:
   ```cmd
   copy <REPO>\dist\mockserver.exe C:\temp\mock\mockserver.exe
   copy <REPO>\dist\certgen.exe    C:\temp\mock\certgen.exe
   ```
3. Generate dev CA + mock cert + a device enrollment cert for the VM (DEV ONLY):
   ```cmd
   cd /d C:\temp\mock
   certgen.exe --out .
   ```
   This writes `ca.crt`, `ca.key`, `mockserver.crt`, `mockserver.key`, `agent-enroll.crt`, `agent-enroll.key`. Keep this folder; the mockserver reads these on startup.
4. Verify the mockserver boots:
   ```cmd
   mockserver.exe
   ```
   Expected log line: `mockserver listening on :50051`. Ctrl+C to stop.

### 3.2 Endpoint VM

1. Copy `dist/AegisOneClient.msi` and the `installer/aegisone.cfg` from the repo to the VM (e.g. `C:\Setup\`).
2. Edit `aegisone.cfg` on the VM:
   ```
   SERVER=<mockserver-IP>:50051
   ENROLL_TOKEN=dev-token
   CA_PEM=C:\Setup\ca.crt
   ```
   (`ca.crt` = the mockserver `ca.crt` you created in §3.1. Copy it to the VM too.)
3. Install silently as admin:
   ```cmd
   msiexec /i C:\Setup\AegisOneClient.msi /qn /l*v C:\Setup\install.log ^
     SERVER=<mockserver-IP>:50051 ^
     ENROLL_TOKEN=dev-token ^
     CA_PEM=C:\Setup\ca.crt
   ```
4. Verify the service is running:
   ```cmd
   sc query AegisOneClient
   ```
   Expected: `STATE : 4 RUNNING`.
5. On the mockserver, start it again:
   ```cmd
   cd /d C:\temp\mock
   mockserver.exe
   ```
   Expected within 60s: `agent connected device_id=<UUID> hostname=<HOSTNAME>`. Note the UUID — you'll paste it into `AEGIS_TARGET_DEVICE` for every test.

### 3.3 Grab the softdist-handoff manifest

```cmd
copy <REPO>\dist\softdist-handoff.json C:\temp\mock\
```

Open it; the `artifacts[]` block gives you the authoritative SHA256 for the EXE / MSI / mockserver you installed. Record them in your test journal.

---

## 4. How to read this guide

### 4.1 Mockserver prompt forms

The mockserver has two run modes you'll see in these tests:

- **`mockserver.exe server`** — one-shot: picks up `AEGIS_*` env vars from the CMD, sends one signed command, prints the ActionStatus stream, exits (or holds the connection — Ctrl+C to stop).
- **`mockserver.exe nsh`** — interactive REPL. You'll see this in Block UC-18/19 and in NSH tests. See the separate `tester-guide-nsh.md`.

NSH prompt forms:

| Prompt | Meaning |
|---|---|
| `nsh> ` | no target selected |
| `<hostname>> ` | one target (after `cd <hostname>`) |
| `[N hosts]> ` | N targets selected (after `cd \fleet-group`) |
| `...> ` | continuation inside a `for` / `foreach` / `if` block |
| `... [DELIM]> ` | inside a heredoc |

### 4.2 ActionStatus phase lifecycle

Every SoftDist action streams an ActionStatus you'll see printed by the mockserver. Phases:

```
ACTION_PHASE_QUEUED        → accepted, waiting for the worker
ACTION_PHASE_DOWNLOADING   → fetching bundle / installer (if content_id)
ACTION_PHASE_VERIFYING     → decrypt + hash + signature check
ACTION_PHASE_RUNNING       → installer actually running
ACTION_PHASE_SUCCESS       → done; exit_code & reboot_required set
ACTION_PHASE_FAILED        → failed; exit_code + reason explain why
ACTION_PHASE_NOT_APPLICABLE → nothing to do (already installed, no uninstall match, …)
```

A terminal phase (`SUCCESS` / `FAILED` / `NOT_APPLICABLE`) carries the final outcome. `QUEUED` alone is NOT a terminal result — always wait for the terminal line before marking PASS/FAIL.

Each terminal line also prints:

- `exit_code` — installer / command exit code
- `reboot_required` — true when the installer returned 3010 or an equivalent signal
- `reason` — concise one-liner for the console status column
- `detail` — verbose detail (last few lines of installer output or structured JSON)
- `package_directory` — where the bundle was extracted
- `package_cleanup` — `removed`, `retained`, or `failed`

### 4.3 Env var discipline

Every test case prepends its own `set "AEGIS_*=..."` lines. Between tests, **run a clean-slate reset** in the SAME CMD before setting the next test's vars. The reset for each major block is included in that block's "Prepare once" section.

Rule of thumb: a stale env var from a previous test will silently join the new test and give confusing failures (`installer bundle needs entry_point and no bundle.command` is the classic one). If a test fails with an odd reason, close the CMD, open a NEW one, and re-run the block's `Prepare once` plus the case.

### 4.4 The `AEGIS_DEVICE_STATE_INTERVAL` gotcha

The agent parses this env var as **integer seconds** via `strconv.Atoi(value)`. It does NOT accept Go duration format like `30s` or `5m`.

- Wrong: `setx AEGIS_DEVICE_STATE_INTERVAL 30s /M` → silently falls back to the 5-minute default.
- Right: `setx AEGIS_DEVICE_STATE_INTERVAL 30 /M`.

Verify by checking the agent log at `<InstallDir>\logs\agent.log` for the line `device_state_interval=30s` (yes, the log emits Go duration form, but input is int seconds).

### 4.5 Record each test

Treat every case as atomic: capture the mockserver's terminal ActionStatus line AND the endpoint check output. Record both in your journal before moving on. The Record Sheet in §16 has a table template.

---

## 5. Block A — Multi-file packages

### 5.1 Prepare once (Block A)

**Mockserver CMD:**

```cmd
cd /d C:\temp\mock
mkdir C:\temp\AegisBundleDemo-src\settings
echo REGION=apac>C:\temp\AegisBundleDemo-src\settings\app.ini
echo Aegis multi-file package>C:\temp\AegisBundleDemo-src\readme.txt
mockserver.exe pack C:\temp\AegisBundleDemo-src C:\temp\AegisDemoBundle.zip
```

`pack` is a native mockserver command that creates a signed ZIP + `aegis-package.json` manifest + SHA256. Output ZIP must be outside the source folder. If recreating, `del C:\temp\AegisDemoBundle.zip` first.

Now reset env vars and set the device target:

```cmd
set "AEGIS_DEPLOY_PRE_COMMAND="
set "AEGIS_DEPLOY_PRE_ARGS="
set "AEGIS_DEPLOY_PRE_NAME="
set "AEGIS_DEPLOY_POST_COMMAND="
set "AEGIS_DEPLOY_POST_ARGS="
set "AEGIS_DEPLOY_POST_NAME="
set "AEGIS_DEPENDENCY_ID="
set "AEGIS_DEPENDENCY_NAME="
set "AEGIS_DEPENDENCY_FILE="
set "AEGIS_NSH="
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_HASH_ALGO=sha256"
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_SESSION_ID="
set "AEGIS_DEPLOY_SECRETS_FILE="
set "AEGIS_DEPLOY_PARAMS="
set "AEGIS_INSTALL_FILE="
set "AEGIS_UNINSTALL_KB="
set "AEGIS_STAGE_KB="
set "AEGIS_COLLECT_PATHS="
set "AEGIS_REBOOT_DELAY="
set "AEGIS_SCAN_NOW="
set "AEGIS_CANCEL_REBOOT="
set "AEGIS_TARGET_DEVICE=<DEVICE_UUID_FROM_SETUP>"
```

Replace `<DEVICE_UUID_FROM_SETUP>` with the UUID you noted in §3.2 step 5. **Keep this CMD open** for A1-A3.

### 5.2 Case A1 — Deploy multiple files, default cleanup

**Goal:** Deploy both files and the subfolder from the bundle, verify extraction, verify default staging cleanup after main command runs.

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_DETECT_COUNT=0"
set "AEGIS_DEPENDENCY_DETECT_COUNT=0"
set "AEGIS_DEPLOY_NAME=A1 multi-file deployment"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_CONTENT_ID=demo-multifile-package"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisDemoBundle.zip"
set "AEGIS_DEPLOY_AUTO_HASH=1"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_BUNDLE_ENTRY="
set "AEGIS_DEPLOY_BUNDLE_COMMAND=xcopy.exe"
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisBundleInstalled /E /I /Y"
mockserver.exe server
```

**Endpoint (Administrator CMD), after terminal result:**

```cmd
type C:\temp\AegisBundleInstalled\settings\app.ini
type C:\temp\AegisBundleInstalled\readme.txt
dir /ad "C:\Program Files\AegisOne\Client\SKVC\download\packages\aegis-package-*"
```

**Expected:**

- Terminal `ACTION_PHASE_SUCCESS`, `exit_code = 0`.
- `app.ini` prints `REGION=apac`; `readme.txt` prints `Aegis multi-file package`.
- Mockserver output contains `package_directory: ...` and `cleanup: removed`.
- The `aegis-package-*` directory is GONE (default cleanup removed it).
- The deployed files under `C:\temp\AegisBundleInstalled` remain intact.

**PASS criteria:** all four above.

### 5.3 Case A2 — Retain custom staging, then explicit delete

**Goal:** A custom package destination is retained after deployment; demonstrate exact cleanup syntax.

**Endpoint, BEFORE run:** confirm this destination does NOT exist (it will be created fresh):

```cmd
dir C:\temp\AegisRetainedDemo
```

Must say "File Not Found". A custom destination MUST be a NEW folder; existing folders are never merged or overwritten.

**Mockserver:** Ctrl+C the previous server. Keep the same CMD, then:

```cmd
set "AEGIS_DEPLOY_NAME=A2 retained custom package"
set "AEGIS_DEPLOY_BUNDLE_DEST=C:\temp\AegisRetainedDemo"
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisBundleInstalled /E /I /Y"
mockserver.exe server
```

**Endpoint, after terminal result:**

```cmd
dir /s C:\temp\AegisRetainedDemo
type C:\temp\AegisRetainedDemo\settings\app.ini
```

Delete one file manually (admin can do this later from the console):

```cmd
del /Q "C:\temp\AegisRetainedDemo\readme.txt"
dir C:\temp\AegisRetainedDemo
```

Delete the complete folder:

```cmd
rmdir /S /Q "C:\temp\AegisRetainedDemo"
```

**Expected:**

- Terminal `ACTION_PHASE_SUCCESS`; `cleanup: retained` and the exact custom path printed.
- Both files + subfolder exist in `C:\temp\AegisRetainedDemo` before manual cleanup.
- After `del`, `readme.txt` is gone but `settings\app.ini` remains.
- After `rmdir`, the whole directory is gone.
- Deployed files under `C:\temp\AegisBundleInstalled` from A1 remain unchanged.

### 5.4 Case A3 — Wrong hash is refused

**Goal:** A ZIP with the wrong signed SHA256 is refused before the command can touch the filesystem. Negative test — expected FAILED means protection worked.

**Endpoint, BEFORE run:** confirm this test destination does NOT exist:

```cmd
dir C:\temp\AegisBundleShouldNotRun
```

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_NAME=A3 wrong-hash refusal"
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_AUTO_HASH=0"
set "AEGIS_DEPLOY_HASH=0000000000000000000000000000000000000000000000000000000000000000"
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisBundleShouldNotRun /E /I /Y"
mockserver.exe server
```

**Endpoint, after terminal result:**

```cmd
dir C:\temp\AegisBundleShouldNotRun
```

**Expected:**

- Terminal `ACTION_PHASE_FAILED` with a hash/integrity mismatch reason.
- `C:\temp\AegisBundleShouldNotRun` remains absent — the command never ran.

**PASS criteria:** both above. This FAILED result means the protection PASSED.

Before the next normal test, reset the negative-hash settings:

```cmd
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_AUTO_HASH=1"
```

---

## 6. Block B — Pre / post commands

### 6.1 Prepare once (Block B)

**Mockserver:** open a FRESH CMD (not the one with A's vars). Then:

```cmd
cd /d C:\temp\mock
mkdir C:\temp\AegisWorkflowSource\settings
echo REGION=apac>C:\temp\AegisWorkflowSource\settings\app.ini
echo Configuration package>C:\temp\AegisWorkflowSource\readme.txt
mockserver.exe pack C:\temp\AegisWorkflowSource C:\temp\AegisWorkflow.zip
```

Set common envs (same clean-slate reset as A, plus workflow fields):

```cmd
set "AEGIS_DEPENDENCY_ID="
set "AEGIS_DEPENDENCY_NAME="
set "AEGIS_DEPENDENCY_FILE="
set "AEGIS_NSH="
set "AEGIS_INSTALL_FILE="
set "AEGIS_UNINSTALL_KB="
set "AEGIS_TARGET_DEVICE=<DEVICE_UUID>"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_HASH_ALGO=sha256"
set "AEGIS_DEPLOY_AUTO_HASH=1"
set "AEGIS_DEPLOY_CONTENT_ID=workflow-demo"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisWorkflow.zip"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_SESSION_ID="
set "AEGIS_DEPLOY_SECRETS_FILE="
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_VERSION="
set "AEGIS_DEPLOY_TIMEOUT=60"
set "AEGIS_DEPLOY_BUNDLE_ENTRY="
set "AEGIS_DEPLOY_BUNDLE_COMMAND=xcopy.exe"
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_PRE_COMMAND=findstr.exe"
set "AEGIS_DEPLOY_PRE_ARGS=/X /C:REGION=${REGION} settings\app.ini"
set "AEGIS_DEPLOY_PRE_NAME=Check configuration region"
set "AEGIS_DEPLOY_PRE_TIMEOUT_SECONDS=30"
set "AEGIS_DEPLOY_POST_COMMAND=fc.exe"
set "AEGIS_DEPLOY_POST_NAME=Verify deployed configuration"
set "AEGIS_DEPLOY_POST_TIMEOUT_SECONDS=30"
```

### 6.2 Case B1 — Check / deploy / verify successful workflow

**Goal:** Pre-step checks a staged file, main-step deploys it, post-step verifies deployed bytes against staged.

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_NAME=B1 workflow success"
set "AEGIS_DEPLOY_PARAMS=REGION=apac"
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisWorkflowInstalled /E /I /Y"
set "AEGIS_DEPLOY_POST_ARGS=/B settings\app.ini C:\temp\AegisWorkflowInstalled\settings\app.ini"
mockserver.exe server
```

**Endpoint, after terminal:**

```cmd
type C:\temp\AegisWorkflowInstalled\settings\app.ini
type C:\temp\AegisWorkflowInstalled\readme.txt
```

**Expected:**

- Progress stream prints `pre-deployment` → `main` → `post-deployment`.
- Terminal `ACTION_PHASE_SUCCESS`, `exit_code = 0`.
- `app.ini` prints `REGION=apac`; `readme.txt` prints `Configuration package`.
- `package_cleanup: removed`.
- Deployed files under `C:\temp\AegisWorkflowInstalled` remain.

### 6.3 Case B2 — Wrong configuration blocks main deployment

**Goal:** Pre-step detects a mismatch (`REGION=emea` requested but staged says `apac`), main command never runs.

**Endpoint, before:** confirm destination absent.

```cmd
dir C:\temp\AegisWorkflowBlocked
```

**Mockserver:** Ctrl+C previous, same CMD:

```cmd
set "AEGIS_DEPLOY_NAME=B2 pre-check blocks main"
set "AEGIS_DEPLOY_PARAMS=REGION=emea"
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisWorkflowBlocked /E /I /Y"
set "AEGIS_DEPLOY_POST_ARGS=/B settings\app.ini C:\temp\AegisWorkflowBlocked\settings\app.ini"
mockserver.exe server
```

**Endpoint, after:**

```cmd
dir C:\temp\AegisWorkflowBlocked
```

**Expected:**

- Terminal `ACTION_PHASE_FAILED`, `exit_code = 1` (findstr's exit).
- Reason identifies `pre-deployment step 1 (Check configuration region)`.
- Main and post never ran; destination absent.
- `package_cleanup: removed`.
- This expected refusal = PASS.

### 6.4 Case B3 — Post-check failure is honest, not silent success

**Goal:** Files copy successfully but a required `required.ini` is missing. Post-step fails → whole action FAILED.

**Endpoint, before:**

```cmd
dir C:\temp\AegisWorkflowPostFailed
```

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_NAME=B3 post-check refusal"
set "AEGIS_DEPLOY_PARAMS=REGION=apac"
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisWorkflowPostFailed /E /I /Y"
set "AEGIS_DEPLOY_POST_ARGS=/B settings\app.ini C:\temp\AegisWorkflowPostFailed\settings\required.ini"
mockserver.exe server
```

**Endpoint, after:**

```cmd
type C:\temp\AegisWorkflowPostFailed\settings\app.ini
dir C:\temp\AegisWorkflowPostFailed\settings\required.ini
```

**Expected:**

- Pre-check + main copy succeed, `app.ini` is `REGION=apac`.
- `required.ini` is absent; `fc.exe` returns exit 2.
- Terminal `ACTION_PHASE_FAILED` with reason `post-deployment step 1 (Verify deployed configuration)`.
- `package_cleanup: removed`; deployed files are kept (operator can inspect the failed deployment).
- This expected failure = PASS.

---

## 7. Block C — Package dependencies

### 7.1 Prepare once (Block C)

**Endpoint — Administrator CMD:**

```cmd
mkdir C:\temp\AegisDependencyDemo
```

**Mockserver:** FRESH CMD. Then:

```cmd
cd /d C:\temp\mock
set "AEGIS_TARGET_DEVICE=<DEVICE_UUID>"
set "AEGIS_DEPLOY_DETECT_COUNT=0"
set "AEGIS_DEPENDENCY_DETECT_COUNT=0"
set "AEGIS_DEPLOY_NAME=Dependency main-package test"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C echo MAIN-RAN>C:\temp\AegisDependencyDemo\main-ran.txt"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_TIMEOUT=60"
set "AEGIS_DEPLOY_PLAN_TIMEOUT=180"
set "AEGIS_DEPENDENCY_NAME=Aegis Dependency Demo"
set "AEGIS_DEPENDENCY_RUN_AS=system"
set "AEGIS_DEPENDENCY_DETECTION_SCOPE=machine"
set "AEGIS_DEPENDENCY_UNINSTALL_KEY=HKLM64\AegisDependencyDemo"
set "AEGIS_DEPENDENCY_TIMEOUT=60"
```

> `exit 7` in cases C2/C3 is a deliberate failure. In repeat/skip tests it must NOT execute — detection should already be satisfied.

### 7.2 Case C1 — Missing dependency installs then main runs

**Endpoint:** clear stale state

```cmd
reg delete HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisDependencyDemo /f /reg:64
del /Q C:\temp\AegisDependencyDemo\main-ran.txt
```

(A "not found" message is expected and OK.)

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_PACKAGE_ID=app-demo-r1"
set "AEGIS_DEPENDENCY_ID=runtime-demo-r1"
set "AEGIS_DEPENDENCY_TYPE=command"
set "AEGIS_DEPENDENCY_FILE=reg.exe"
set "AEGIS_DEPENDENCY_ARGS=add HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisDependencyDemo /v DisplayName /t REG_SZ /d "Aegis Dependency Demo" /f /reg:64"
mockserver.exe server
```

**Endpoint, after final `PLAN ... SUCCESS`:**

```cmd
reg query HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisDependencyDemo /v DisplayName /reg:64
type C:\temp\AegisDependencyDemo\main-ran.txt
```

**Expected:**

- Plan order prints `runtime-demo-r1 -> app-demo-r1`.
- Dependency `ACTION_PHASE_SUCCESS` after registration verification.
- Main dispatched afterward; final plan `SUCCESS`.
- Registry `DisplayName = Aegis Dependency Demo`; marker prints `MAIN-RAN`.

### 7.3 Case C2 — Installed dependency skips, main runs

**Endpoint:** mock the dependency as already-present:

```cmd
reg add HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisDependencyDemo /v DisplayName /t REG_SZ /d "Aegis Dependency Demo" /f /reg:64
del /Q C:\temp\AegisDependencyDemo\main-ran.txt
```

**Mockserver:** Ctrl+C previous. Same CMD:

```cmd
set "AEGIS_DEPLOY_PACKAGE_ID=app-demo-r2"
set "AEGIS_DEPENDENCY_ID=runtime-demo-r2"
set "AEGIS_DEPENDENCY_TYPE=command"
set "AEGIS_DEPENDENCY_FILE=cmd.exe"
set "AEGIS_DEPENDENCY_ARGS=/D /C exit 7"
mockserver.exe server
```

**Endpoint, after:**

```cmd
type C:\temp\AegisDependencyDemo\main-ran.txt
```

**Expected:**

- Dependency `ACTION_PHASE_NOT_APPLICABLE`, "already installed in the selected execution scope".
- `exit 7` DID NOT run (if it had, dependency would be FAILED and main would be blocked).
- Main `SUCCESS`; marker prints `MAIN-RAN`.
- Final plan `SUCCESS`.

### 7.4 Case C3 — Dependency failure prevents main

**Endpoint:**

```cmd
reg delete HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisDependencyDemo /f /reg:64
del /Q C:\temp\AegisDependencyDemo\main-ran.txt
```

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_PACKAGE_ID=app-demo-r3"
set "AEGIS_DEPENDENCY_ID=runtime-demo-r3"
set "AEGIS_DEPENDENCY_TYPE=command"
set "AEGIS_DEPENDENCY_FILE=cmd.exe"
set "AEGIS_DEPENDENCY_ARGS=/D /C exit 7"
mockserver.exe server
```

**Endpoint, after:**

```cmd
dir C:\temp\AegisDependencyDemo\main-ran.txt
```

**Expected:**

- Dependency `FAILED`, `exit_code = 7`.
- Final plan `FAILED`; reason mentions `runtime-demo-r3`.
- No main-package send line in mock output; marker file absent.
- Expected refusal = PASS.

### 7.5 Cleanup (Block C)

**Endpoint:**

```cmd
reg delete HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisDependencyDemo /f /reg:64
del /Q C:\temp\AegisDependencyDemo\main-ran.txt
```

---

## 8. Block D — Advanced detection

Advanced detection = post-install verification rules on the endpoint. ALL = all rules must match; ANY = at least one.

### 8.1 Prepare once (Block D)

**Endpoint:**

```cmd
mkdir C:\temp\AegisDetectionDemo
```

**Mockserver:** FRESH CMD:

```cmd
cd /d C:\temp\mock
set "AEGIS_TARGET_DEVICE=<DEVICE_UUID>"
set "AEGIS_DEPLOY_NAME=Advanced detection test"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE=cmd.exe"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_TIMEOUT=60"
set "AEGIS_DEPLOY_VERSION="
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME="
```

### 8.2 Case D1 — File created, verified, then repeat skipped

**Endpoint:**

```cmd
del /Q C:\temp\AegisDetectionDemo\installed.txt
```

**Mockserver (first run):**

```cmd
set "AEGIS_DEPLOY_DETECT_COUNT=1"
set "AEGIS_DEPLOY_DETECT_MODE=all"
set "AEGIS_DEPLOY_DETECT_1_KIND=file_exists"
set "AEGIS_DEPLOY_DETECT_1_PATH=C:\temp\AegisDetectionDemo\installed.txt"
set "AEGIS_DEPLOY_ARGS=/D /C echo INSTALLED>C:\temp\AegisDetectionDemo\installed.txt"
mockserver.exe server
```

**Endpoint, after:**

```cmd
type C:\temp\AegisDetectionDemo\installed.txt
```

**Mockserver (second run — repeat should skip):**

```cmd
set "AEGIS_DEPLOY_ARGS=/D /C exit 7"
mockserver.exe server
```

**Expected:**

- First run: `SUCCESS`, `exit 0`, file printed `INSTALLED`, verification reason noted.
- Second run: `NOT_APPLICABLE`, "advanced detection satisfied". The `exit 7` command never executes.

### 8.3 Case D2 — File presence alone can't satisfy a version rule

**Endpoint:**

```cmd
del /Q C:\temp\AegisDetectionDemo\app.exe
```

**Mockserver (text file as "exe"):**

```cmd
set "AEGIS_DEPLOY_DETECT_COUNT=1"
set "AEGIS_DEPLOY_DETECT_MODE=all"
set "AEGIS_DEPLOY_DETECT_1_KIND=file_version"
set "AEGIS_DEPLOY_DETECT_1_PATH=C:\temp\AegisDetectionDemo\app.exe"
set "AEGIS_DEPLOY_DETECT_1_MIN_VERSION=1.0.0.0"
set "AEGIS_DEPLOY_ARGS=/D /C echo NOT-A-VERSIONED-EXE>C:\temp\AegisDetectionDemo\app.exe"
mockserver.exe server
```

**Endpoint, after:**

```cmd
dir C:\temp\AegisDetectionDemo\app.exe
```

**Mockserver (replace with real versioned exe — Windows `cmd.exe`):**

```cmd
set "AEGIS_DEPLOY_ARGS=/D /C copy /Y C:\Windows\System32\cmd.exe C:\temp\AegisDetectionDemo\app.exe"
mockserver.exe server
```

**Expected:**

- Text-file run: command exits 0, file exists, action `FAILED` — advanced installed detection not satisfied (text file has no version resource). Expected refusal = PASS.
- Real exe run: unversioned prior output does NOT cause a skip, copy executes, file_version detection succeeds, `SUCCESS`.

### 8.4 Case D3 — ALL requires file AND registry; ANY permits either

**Endpoint — Administrator CMD:**

```cmd
reg delete HKLM\SOFTWARE\AegisOne\DetectionDemo /f /reg:64
del /Q C:\temp\AegisDetectionDemo\combined.txt
```

**Mockserver (ALL mode, missing registry):**

```cmd
set "AEGIS_DEPLOY_DETECT_COUNT=2"
set "AEGIS_DEPLOY_DETECT_MODE=all"
set "AEGIS_DEPLOY_DETECT_1_KIND=file_exists"
set "AEGIS_DEPLOY_DETECT_1_PATH=C:\temp\AegisDetectionDemo\combined.txt"
set "AEGIS_DEPLOY_DETECT_1_MIN_VERSION="
set "AEGIS_DEPLOY_DETECT_2_KIND=registry_value"
set "AEGIS_DEPLOY_DETECT_2_HIVE=HKLM"
set "AEGIS_DEPLOY_DETECT_2_KEY=SOFTWARE\AegisOne\DetectionDemo"
set "AEGIS_DEPLOY_DETECT_2_VIEW=64"
set "AEGIS_DEPLOY_DETECT_2_VALUE_NAME=Region"
set "AEGIS_DEPLOY_DETECT_2_VALUE_TYPE=string"
set "AEGIS_DEPLOY_DETECT_2_EXPECTED=apac"
set "AEGIS_DEPLOY_ARGS=/D /C echo COMBINED>C:\temp\AegisDetectionDemo\combined.txt"
mockserver.exe server
```

**Endpoint, after:**

```cmd
type C:\temp\AegisDetectionDemo\combined.txt
reg query HKLM\SOFTWARE\AegisOne\DetectionDemo /v Region /reg:64
```

**Mockserver (switch to ANY; set an exit-7 command — must not run):**

```cmd
set "AEGIS_DEPLOY_DETECT_MODE=any"
set "AEGIS_DEPLOY_ARGS=/D /C exit 7"
mockserver.exe server
```

**Endpoint — Administrator CMD (add the registry value for the final run):**

```cmd
reg add HKLM\SOFTWARE\AegisOne\DetectionDemo /v Region /t REG_SZ /d apac /f /reg:64
```

**Mockserver (back to ALL; exit-7 still set):**

```cmd
set "AEGIS_DEPLOY_DETECT_MODE=all"
mockserver.exe server
```

**Expected:**

- ALL + missing registry: `FAILED`, exit 0, verification failed. File printed `COMBINED`; registry absent. Expected = PASS.
- ANY with only file: `NOT_APPLICABLE`; `exit 7` not run.
- ALL after adding Region=apac: also `NOT_APPLICABLE`; both rules now match.

### 8.5 Cleanup (Block D)

```cmd
reg delete HKLM\SOFTWARE\AegisOne\DetectionDemo /f /reg:64
del /Q C:\temp\AegisDetectionDemo\installed.txt C:\temp\AegisDetectionDemo\app.exe C:\temp\AegisDetectionDemo\combined.txt
```

---

## 9. Block E — Real 7-Zip MSI lifecycle

### 9.1 Prepare once (Block E + F)

Target: endpoint with NO existing 7-Zip or Firefox. Replace stopped agent EXE with `dist/AegisOneClient.exe` (or use the matching MSI); replace stopped mockserver with `dist/mockserver.exe`.

Mockserver CMD, FRESH:

```cmd
cd /d C:\temp\mock
mkdir C:\temp\AegisRealTests
set "AEGIS_NSH="
set "AEGIS_INSTALL_FILE="
set "AEGIS_UNINSTALL_KB="
set "AEGIS_STAGE_KB="
set "AEGIS_COLLECT_PATHS="
set "AEGIS_REBOOT_DELAY="
set "AEGIS_SCAN_NOW="
set "AEGIS_CANCEL_REBOOT="
set "AEGIS_DEPENDENCY_ID="
set "AEGIS_DEPENDENCY_NAME="
set "AEGIS_DEPENDENCY_FILE="
set "AEGIS_DEPENDENCY_CONTENT_ID="
set "AEGIS_DEPLOY_PRE_COMMAND="
set "AEGIS_DEPLOY_PRE_ARGS="
set "AEGIS_DEPLOY_PRE_NAME="
set "AEGIS_DEPLOY_POST_COMMAND="
set "AEGIS_DEPLOY_POST_ARGS="
set "AEGIS_DEPLOY_POST_NAME="
set "AEGIS_DEPLOY_BUNDLE_ENTRY="
set "AEGIS_DEPLOY_BUNDLE_COMMAND="
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_SESSION_ID="
set "AEGIS_DEPLOY_SECRETS_FILE="
set "AEGIS_DEPLOY_PARAMS="
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_REQUIRE_SIG="
set "AEGIS_DEPLOY_SUCCESS_CODES="
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_HASH_ALGO=sha256"
set "AEGIS_DEPLOY_AUTO_HASH=1"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_DETECT_COUNT=0"
set "AEGIS_DEPLOY_TIMEOUT=300"
set "AEGIS_TARGET_DEVICE=<DEVICE_UUID>"
```

Confirm the MSIs are at `C:\temp\AegisRealTests\7z2604-x64.msi` and `7z2605-x64.msi` (see §2.3).

### 9.2 Case E1 — Fresh install (26.04)

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_PACKAGE_ID=7zip-x64-msi-26.04"
set "AEGIS_DEPLOY_NAME=7-Zip 26.04 real MSI install"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=msi"
set "AEGIS_DEPLOY_CONTENT_ID=7zip-x64-msi-26.04"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisRealTests\7z2604-x64.msi"
set "AEGIS_DEPLOY_DISPLAY_NAME=7-Zip"
set "AEGIS_DEPLOY_VERSION=26.04"
set "AEGIS_DEPLOY_ARGS=/qn /norestart ALLUSERS=1 /L*v C:\Windows\Temp\Aegis-7zip-install.log"
mockserver.exe server
```

**Endpoint, after terminal:**

```cmd
dir "C:\Program Files\7-Zip\7z.exe"
"C:\Program Files\7-Zip\7z.exe" | findstr /C:"7-Zip 26.04"
```

**Expected:**

- Terminal `SUCCESS`, `exit 0`, `reboot_required = false`, installed-version verification pass.
- `7z.exe` exists; its startup banner prints the 26.04 line.

MSI flags cheat-sheet: `/qn` = no UI, `/norestart` = don't reboot, `ALLUSERS=1` = machine-wide, `/L*v <path>` = verbose log on the endpoint.

### 9.3 Case E2 — Repeat install skips

**Mockserver:** Ctrl+C E1. Same CMD, no changes:

```cmd
mockserver.exe server
```

**Expected:**

- `NOT_APPLICABLE`, "already installed in the selected execution scope (skipped)".
- The current pipeline may still fetch the MSI before worker-side detection (optimization gap, not a failure).

### 9.4 Case E3 — Upgrade 26.04 → 26.05

**Mockserver:** Ctrl+C E2. Same CMD:

```cmd
set "AEGIS_DEPLOY_PACKAGE_ID=7zip-x64-msi-26.05"
set "AEGIS_DEPLOY_NAME=7-Zip upgrade 26.04 to 26.05"
set "AEGIS_DEPLOY_CONTENT_ID=7zip-x64-msi-26.05"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisRealTests\7z2605-x64.msi"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_VERSION=26.05"
set "AEGIS_DEPLOY_ARGS=/qn /norestart ALLUSERS=1 /L*v C:\Windows\Temp\Aegis-7zip-upgrade.log"
mockserver.exe server
```

**Endpoint, after:**

```cmd
"C:\Program Files\7-Zip\7z.exe" | findstr /C:"7-Zip 26.05"
```

**Expected:**

- `SUCCESS`, exit 0, verified 26.05.
- Does NOT skip because of 26.04 (newer version triggers upgrade).
- Action label stays `install`; the vendor MSI handles the upgrade.

### 9.5 Case E4 — Uninstall via registry registration (no MSI download needed)

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_NAME=7-Zip 26.05 uninstall"
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_CONTENT_ID="
set "AEGIS_SERVE_SOFTWARE="
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME=7-Zip"
set "AEGIS_DEPLOY_VERSION=26.05.00.0"
set "AEGIS_DEPLOY_ARGS=/qn /norestart /L*v C:\Windows\Temp\Aegis-7zip-uninstall.log"
mockserver.exe server
```

**Endpoint, after:**

```cmd
if exist "C:\Program Files\7-Zip\7z.exe" (echo FAIL: 7z.exe still exists) else (echo PASS: 7z.exe removed)
```

**Expected:**

- Agent resolves "7-Zip 26.05 (x64 edition)" in HKLM64 (observed ProductCode `{23170F69-40C1-2702-2605-000001000000}`).
- `SUCCESS`, exit 0, reason includes "verified removed".
- No installer fetch is attempted.
- `PASS: 7z.exe removed` prints.

### 9.6 Case E5 — Repeat uninstall skips absent product

**Mockserver:** Ctrl+C E4, same CMD, unchanged:

```cmd
mockserver.exe server
```

**Expected:** `NOT_APPLICABLE`, reason "not installed in the selected execution scope (nothing to uninstall)". No download, no uninstaller execution.

---

## 10. Block F — Real Firefox installer + policy

### 10.1 Case F1 — EXE + JSON config + active policy verification

**Prepare the Firefox bundle on the mockserver:**

```cmd
mkdir C:\temp\AegisRealTests\FirefoxPackage\distribution
```

Copy the downloaded Firefox ESR x64 setup EXE (from §2.3) to:

```
C:\temp\AegisRealTests\FirefoxPackage\FirefoxSetup.exe
```

In Notepad, paste this JSON and Save As **All Files, UTF-8**, name `policies.json` (NOT `policies.json.txt`) to:

```
C:\temp\AegisRealTests\FirefoxPackage\distribution\policies.json
```

```json
{
  "policies": {
    "Homepage": {
      "URL": "https://example.org",
      "Locked": true
    }
  }
}
```

**Mockserver:**

```cmd
cd /d C:\temp\mock
dir C:\temp\AegisRealTests\FirefoxPackage\FirefoxSetup.exe
type C:\temp\AegisRealTests\FirefoxPackage\distribution\policies.json
mockserver.exe pack C:\temp\AegisRealTests\FirefoxPackage C:\temp\AegisRealTests\FirefoxPackage.zip
set "AEGIS_DEPLOY_PACKAGE_ID=firefox-esr-config-v1"
set "AEGIS_DEPLOY_NAME=Firefox ESR with homepage configuration"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=exe"
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_CONTENT_ID=firefox-esr-config-v1"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisRealTests\FirefoxPackage.zip"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_HASH_ALGO=sha256"
set "AEGIS_DEPLOY_AUTO_HASH=1"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_DETECT_COUNT=0"
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME=Mozilla Firefox"
set "AEGIS_DEPLOY_VERSION="
set "AEGIS_DEPLOY_ARGS=/S"
set "AEGIS_DEPLOY_TIMEOUT=600"
set "AEGIS_DEPLOY_BUNDLE_ENTRY=FirefoxSetup.exe"
set "AEGIS_DEPLOY_BUNDLE_COMMAND="
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_POST_COMMAND=xcopy.exe"
set "AEGIS_DEPLOY_POST_ARGS=distribution "C:\Program Files\Mozilla Firefox\distribution" /E /I /Y"
set "AEGIS_DEPLOY_POST_NAME=Apply Firefox configuration"
set "AEGIS_DEPLOY_POST_TIMEOUT_SECONDS=60"
set "AEGIS_DEPLOY_POST_SUCCESS_CODES=0"
mockserver.exe server
```

> Before PASS, confirm the server sends `type: software_deploy_workflow` and the stream shows `pre-deployment` (if any) → `main` → `post-deployment step 1: Apply Firefox configuration`.

**Endpoint, after terminal:**

```cmd
dir "C:\Program Files\Mozilla Firefox\firefox.exe"
type "C:\Program Files\Mozilla Firefox\distribution\policies.json"
```

Then on the endpoint, LAUNCH Firefox, type `about:policies` in the address bar.

**Expected:**

- Terminal `SUCCESS`, workflow steps complete; Firefox installed-verification against DISPLAY_NAME passes; post-copy succeeded.
- `firefox.exe` exists; `policies.json` prints exactly the configured URL and `"Locked": true`.
- In `about:policies` → Active, Homepage shows `https://example.org`, `Locked: true`, no error for this policy.
- `package_cleanup: removed`; the extracted package directory is gone.

### 10.2 Firefox recovery pattern (if F1 fails first time)

If the first run installs Firefox but shows NO post-step progress (JSON missing), replace the mockserver EXE from fresh `dist/` AND re-enter the six POST settings in the same CMD. For the retry, deliberately clear the Firefox detection selectors:

```cmd
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_VERSION="
set "AEGIS_DEPLOY_DETECT_COUNT=0"
mockserver.exe server
```

This reruns the EXE + post-copy. Do NOT leave the selector-cleared recovery as a production detection policy.

---

## 11. Block G — Rollback-on-failure orchestration

### 11.1 Prepare once (Block G)

FRESH mockserver CMD:

```cmd
cd /d C:\temp\mock
if not exist C:\temp\AegisBundleDemo-src\settings mkdir C:\temp\AegisBundleDemo-src\settings
echo REGION=apac>C:\temp\AegisBundleDemo-src\settings\app.ini
echo Rollback sample bundle>C:\temp\AegisBundleDemo-src\readme.txt
if exist C:\temp\AegisRollbackBundle.zip del C:\temp\AegisRollbackBundle.zip
mockserver.exe pack C:\temp\AegisBundleDemo-src C:\temp\AegisRollbackBundle.zip
set "AEGIS_TARGET_DEVICE=<DEVICE_UUID>"
rem Two-box (endpoint + mockserver on SEPARATE hosts): deliver via content_id +
rem SERVE + AUTO_HASH, and leave AEGIS_DEPLOY_FILE EMPTY. A pre-staged
rem AEGIS_DEPLOY_FILE path only works when both run on the SAME machine.
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_CONTENT_ID=rollback-demo-v1"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisRollbackBundle.zip"
set "AEGIS_DEPLOY_AUTO_HASH=1"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_REQUIRE_SIG=0"
set "AEGIS_DEPLOY_TIMEOUT=60"
set "AEGIS_DEPLOY_BUNDLE_ENTRY="
set "AEGIS_DEPLOY_BUNDLE_COMMAND=xcopy.exe"
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_PRE_COMMAND="
set "AEGIS_DEPLOY_POST_COMMAND="
set "AEGIS_DEPLOY_ROLLBACK_COMMAND="
set "AEGIS_DEPLOY_DETECT_COUNT=0"
set "AEGIS_DEPENDENCY_DETECT_COUNT=0"
set "AEGIS_DEPENDENCY_ID="
set "AEGIS_DEPENDENCY_NAME="
set "AEGIS_NSH="
```

### 11.2 Case G1 — Main fails → rollback runs → marker file written

**Endpoint:**

```cmd
if exist C:\temp\AegisRollbackMarker.txt del C:\temp\AegisRollbackMarker.txt
```

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_NAME=G1 Rollback-on-main-failure"
set "AEGIS_DEPLOY_ARGS=/this-flag-does-not-exist"
set "AEGIS_DEPLOY_ROLLBACK_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ROLLBACK_ARGS=/D /C echo rollback-fired > C:\temp\AegisRollbackMarker.txt"
set "AEGIS_DEPLOY_ROLLBACK_NAME=Write rollback marker file"
set "AEGIS_DEPLOY_ROLLBACK_TIMEOUT_SECONDS=30"
mockserver.exe server
```

**Endpoint, after:**

```cmd
type C:\temp\AegisRollbackMarker.txt
```

**Expected:**

- Terminal `FAILED` with reason including `rollback after main failure completed (1 step(s))`.
- Marker file exists; contents `rollback-fired`.
- Package extraction directory removed.

### 11.3 Case G2 — Rollback itself fails: later rollback steps still run

**Endpoint:**

```cmd
if exist C:\temp\AegisRollbackSecondMarker.txt del C:\temp\AegisRollbackSecondMarker.txt
```

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_NAME=G2 Partial-rollback-visibility"
set "AEGIS_DEPLOY_ARGS=/this-flag-does-not-exist"
set "AEGIS_DEPLOY_ROLLBACK_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ROLLBACK_ARGS=/D /C exit 9"
set "AEGIS_DEPLOY_ROLLBACK_NAME=Rollback step that fails"
set "AEGIS_DEPLOY_ROLLBACK_TIMEOUT_SECONDS=30"
mockserver.exe server
```

> The mock exposes ONE rollback step for readable manual cases. The real protocol accepts up to 8 rollback steps; agent-side regression (`TestRollbackContinuesPastFailure`) covers multi-step partial behavior.

**Expected:**

- Terminal `FAILED`.
- Reason mentions `rollback after main failure partial — rollback step 1 (Rollback step that fails) exit 9`.
- Package directory removed; agent did NOT re-queue the job.

### 11.4 Case G3 — Successful deployment SKIPS rollback

**Endpoint:**

```cmd
if exist C:\temp\AegisRollbackMustNotFire.txt del C:\temp\AegisRollbackMustNotFire.txt
if exist C:\temp\AegisWorkflowSuccess rmdir /S /Q C:\temp\AegisWorkflowSuccess
```

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_NAME=G3 Success-skips-rollback"
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisWorkflowSuccess /E /I /Y"
set "AEGIS_DEPLOY_ROLLBACK_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ROLLBACK_ARGS=/D /C echo rollback-should-not-fire > C:\temp\AegisRollbackMustNotFire.txt"
set "AEGIS_DEPLOY_ROLLBACK_NAME=Must not fire"
set "AEGIS_DEPLOY_ROLLBACK_TIMEOUT_SECONDS=30"
mockserver.exe server
```

**Endpoint, after:**

```cmd
dir C:\temp\AegisRollbackMustNotFire.txt
dir C:\temp\AegisWorkflowSuccess
```

**Expected:**

- Terminal `SUCCESS`.
- `AegisRollbackMustNotFire.txt` does NOT exist.
- `AegisWorkflowSuccess` created by main xcopy.

---

## 12. Block H — Crash recovery + durable journal

The agent writes per-job state to `C:\Program Files\AegisOne\Client\SKVC\jobs\<command_id>.state` and deletes on terminal finish. A journal outliving the agent process is reported `FAILED` with reason "agent restarted while <state>; result unknown — not retried automatically" and any orphaned package directory is swept.

### 12.1 Case H1 — Agent restart during install is reported honestly

**Endpoint:**

```cmd
if exist "C:\Program Files\AegisOne\Client\SKVC\jobs\recovery-demo.state" del "C:\Program Files\AegisOne\Client\SKVC\jobs\recovery-demo.state"
```

Fabricate a fake in-flight record so the recovery sweep sees it on next start:

```cmd
powershell -NoProfile -Command "@{command_id='recovery-demo'; device_id='test'; name='Recovery demo'; action='install'; state='running'; started_at=1709999999; last_updated=1709999999} | ConvertTo-Json | Set-Content -Encoding ASCII 'C:\Program Files\AegisOne\Client\SKVC\jobs\recovery-demo.state'"
```

Restart the service:

```cmd
sc stop AegisOneClient
sc start AegisOneClient
```

**Expected:**

- Mock prints an ActionStatus for `action_id = recovery-demo` with phase `FAILED` and reason starting `agent restarted while running; result unknown — not retried automatically`.
- The `recovery-demo.state` file is DELETED.

### 12.2 Case H2 — Orphan package directory swept

**Endpoint:**

```cmd
set "AEGIS_PACKAGES_ROOT=C:\Program Files\AegisOne\Client\SKVC\download\packages"
if not exist "%AEGIS_PACKAGES_ROOT%" mkdir "%AEGIS_PACKAGES_ROOT%"
mkdir "%AEGIS_PACKAGES_ROOT%\aegis-package-recovery-h2"
echo leftover>"%AEGIS_PACKAGES_ROOT%\aegis-package-recovery-h2\leftover.txt"
powershell -NoProfile -Command "@{command_id='recovery-h2'; device_id='test'; name='Recovery H2'; action='install'; state='extracting'; package_dir='C:\Program Files\AegisOne\Client\SKVC\download\packages\aegis-package-recovery-h2'; started_at=1709999999; last_updated=1709999999} | ConvertTo-Json | Set-Content -Encoding ASCII 'C:\Program Files\AegisOne\Client\SKVC\jobs\recovery-h2.state'"
sc stop AegisOneClient
sc start AegisOneClient
```

**Expected:**

- `aegis-package-recovery-h2` directory and `leftover.txt` are GONE.
- Mock shows ActionStatus for `recovery-h2` with `package_cleanup = removed`.
- Journal file deleted.

---

## 13. Block I — Descendant kill on timeout

The agent wraps each installer in a Windows Job Object; on timeout, every descendant dies with the parent. The failure reason reports descendant kill count.

### 13.1 Case I1 — Timeout reports descendant count honestly

**Mockserver**, after G1 Prepare (reuse bundle):

```cmd
set "AEGIS_DEPLOY_NAME=I1 Descendant kill with diagnostic"
set "AEGIS_DEPLOY_BUNDLE_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C ping -n 30 127.0.0.1"
set "AEGIS_DEPLOY_TIMEOUT=5"
set "AEGIS_DEPLOY_ROLLBACK_COMMAND="
set "AEGIS_DEPLOY_ROLLBACK_TIMEOUT_SECONDS="
mockserver.exe server
```

**Endpoint**, during the 5-sec window:

```cmd
tasklist /FI "IMAGENAME eq ping.exe"
```

**Expected:**

- Terminal `FAILED` after ~5 seconds.
- Reason includes `installer timed out after 5s` AND either:
  - `; 1 descendant process(es) killed with parent (child installer helpers were still running)` — ping child alive at kill, or
  - `; no descendants were alive at kill (parent process was likely stuck)` — kernel race reaped the child first (retry if needed).
- After terminal line, `tasklist /FI "IMAGENAME eq ping.exe"` returns no rows.

---

## 14. Block J — Reboot-sensitive + MSI/MST + config-only

### 14.1 One-time setup for J

**Endpoint, as the LocalSystem agent will see it:**

1. 7-Zip MSI for J1/J2 (reuse Block E file).
2. **ORCA** for authoring the J2 MST (install from the Windows SDK link in §2.3).
3. PsExec is optional for cross-user retest; not required for J1/J2/J3.

### 14.2 Case J1 — 3010 reboot-required = SUCCESS + reboot_required

**Goal:** Deterministically test the agent's 3010 handling without depending on a specific vendor's installer.

**Mockserver:**

```cmd
if not exist C:\temp\AegisBundleDemo-src\settings mkdir C:\temp\AegisBundleDemo-src\settings
echo REGION=apac>C:\temp\AegisBundleDemo-src\settings\app.ini
echo J1 bundle>C:\temp\AegisBundleDemo-src\readme.txt
if exist C:\temp\AegisJ1Bundle.zip del C:\temp\AegisJ1Bundle.zip
mockserver.exe pack C:\temp\AegisBundleDemo-src C:\temp\AegisJ1Bundle.zip
set "AEGIS_DEPLOY_FILE=C:\temp\AegisJ1Bundle.zip"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_NAME=J1 deterministic 3010"
set "AEGIS_DEPLOY_BUNDLE_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C exit 3010"
set "AEGIS_DEPLOY_TIMEOUT=60"
mockserver.exe server
```

**Expected:**

- Terminal `SUCCESS`, `exit_code = 3010`, `reboot_required = true`.
- Reason mentions "restart required".
- Agent does NOT re-queue the signed command (no second RUNNING with the same action_id).
- `package_cleanup: removed`.

### 14.3 Case J2 — 7-Zip MSI with MST transform

**Author the MST once on mockserver:**

1. Open ORCA → `File > Open > 7z<version>-x64.msi`.
2. `Transform > New Transform`.
3. In the `Property` table, locate `INSTALLDIR` and change its value to `C:\7ZipMST`.
4. `Transform > Generate Transform`, save as `C:\temp\7zip-installdir.mst`.
5. Close ORCA without saving over the MSI.

**Endpoint, before:**

```cmd
wmic product where "Name like '7-Zip%%'" call uninstall /nointeractive
rmdir /S /Q C:\7ZipMST
```

**Mockserver:**

```cmd
mkdir C:\temp\AegisJ2-src
copy C:\temp\AegisRealTests\7z2604-x64.msi C:\temp\AegisJ2-src\7zip.msi
copy C:\temp\7zip-installdir.mst C:\temp\AegisJ2-src\
if exist C:\temp\AegisJ2Bundle.zip del C:\temp\AegisJ2Bundle.zip
mockserver.exe pack C:\temp\AegisJ2-src C:\temp\AegisJ2Bundle.zip
set "AEGIS_DEPLOY_FILE=C:\temp\AegisJ2Bundle.zip"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_NAME=J2 7-Zip MSI with MST transform"
set "AEGIS_DEPLOY_BUNDLE_COMMAND=msiexec.exe"
set "AEGIS_DEPLOY_ARGS=/i 7zip.msi TRANSFORMS=7zip-installdir.mst /qn /norestart /L*v C:\Windows\Temp\Aegis-7zip-mst.log"
set "AEGIS_DEPLOY_TIMEOUT=600"
mockserver.exe server
```

**Endpoint, after:**

```cmd
dir C:\7ZipMST
powershell -NoProfile -Command "Get-Content 'C:\Windows\Temp\Aegis-7zip-mst.log' -Tail 80"
```

**Expected:**

- Terminal `SUCCESS`, exit 0.
- `C:\7ZipMST` exists with `7z.exe`, `7z.dll`, `Lang\`, etc. — the transform took effect (default would have been `C:\Program Files\7-Zip`).
- MST log tail shows `TRANSFORMS=` set and install actions running under the transformed INSTALLDIR path.
- `package_cleanup: removed`.

### 14.4 Case J3 — Config-only parameter update

**Goal:** A signed `parameters` map drives a post-step that writes a product's config file, without reinstalling the main package.

**Endpoint, before:**

```cmd
if not exist C:\ProgramData\AegisVendor mkdir C:\ProgramData\AegisVendor
if exist C:\ProgramData\AegisVendor\settings.ini del C:\ProgramData\AegisVendor\settings.ini
```

> J3 setup caveat: `${CONFIG_PATH}`'s parent dir MUST exist, or the POST echo+redirect fails with exit 1. This pre-step `mkdir` is required for the first run.

**Mockserver:**

```cmd
if not exist C:\temp\AegisJ3-src mkdir C:\temp\AegisJ3-src
echo J3 bundle>C:\temp\AegisJ3-src\readme.txt
if exist C:\temp\AegisJ3Bundle.zip del C:\temp\AegisJ3Bundle.zip
mockserver.exe pack C:\temp\AegisJ3-src C:\temp\AegisJ3Bundle.zip
set "AEGIS_DEPLOY_FILE=C:\temp\AegisJ3Bundle.zip"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_NAME=J3 config-only parameter update"
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_PARAMS=REGION=emea,CONFIG_PATH=C:\ProgramData\AegisVendor\settings.ini"
set "AEGIS_DEPLOY_BUNDLE_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C echo config-only run on %COMPUTERNAME%"
set "AEGIS_DEPLOY_TIMEOUT=60"
set "AEGIS_DEPLOY_POST_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_POST_ARGS=/D /C echo REGION=${REGION}> ${CONFIG_PATH}"
set "AEGIS_DEPLOY_POST_NAME=Apply config-only update"
set "AEGIS_DEPLOY_POST_TIMEOUT_SECONDS=30"
mockserver.exe server
```

**Endpoint, after:**

```cmd
type C:\ProgramData\AegisVendor\settings.ini
```

**Expected:**

- Terminal `SUCCESS`, exit 0.
- `settings.ini` contents: `REGION=emea`.
- Progress stream: `main` → `post-deployment`.
- `package_cleanup: removed`.

Historical run: 2026-10-29 PASS on Vijay's WIN-TUES4V46T7I: `cmd /D /C exit 3010` returned SUCCESS/3010/reboot_required=true, mock did not re-queue; 7-Zip MSI + MST installed to `C:\7ZipMST`; J3 post-step wrote `REGION=emea` to `settings.ini`.

---

## 15. Block K — Bandwidth throttling (lower-wins)

Two sources of truth; "lower wins":

1. **Agent config cap** (admin policy, endpoint-wide) — `AEGIS_FETCH_MAX_BYTES_PER_SECOND`, `AEGIS_UPLOAD_MAX_BYTES_PER_SECOND`. 0 = unlimited.
2. **Signed per-task rate** (console operator per-package) — `SoftwareDeploySpec.max_fetch_bytes_per_second`, `CollectFileSpec.max_upload_bytes_per_second`. 0 = no cap, falls back to agent config.
3. **Effective** = `min(agent_cap, signed_rate)`, treating 0 as unlimited. Signed command CANNOT bypass admin cap.

Server-side cumulative throttle is independent server-team work; gRPC backpressure makes agent + server combine as min() at the network layer.

### 15.1 Preflight

**Endpoint, Admin CMD:**

```cmd
if not exist C:\temp\AegisThrottleOut mkdir C:\temp\AegisThrottleOut
if exist C:\temp\AegisThrottleOut\big.log del C:\temp\AegisThrottleOut\big.log
powershell -NoProfile -Command "$path='C:\temp\AegisThrottleOut\big.log'; $size=5MB; $buf=[byte[]]::new($size); (New-Object Random).NextBytes($buf); [IO.File]::WriteAllBytes($path, $buf)"
dir C:\temp\AegisThrottleOut\big.log
```

Expect ~5,242,880 bytes random-content file.

### 15.2 Case K1 — Throttled fetch (signed per-task rate)

**Goal:** cap a 1.9 MB bundle fetch at 500 KB/s → expect ~4 sec download.

**Mockserver:**

```cmd
set "AEGIS_FETCH_MAX_BYTES_PER_SECOND="
set "AEGIS_UPLOAD_MAX_BYTES_PER_SECOND="
set "AEGIS_DEPLOY_FETCH_MAX_BYTES_PER_SEC=500000"
set "AEGIS_COLLECT_UPLOAD_MAX_BYTES_PER_SEC="
if not exist C:\temp\AegisK1-src mkdir C:\temp\AegisK1-src
copy /Y C:\temp\AegisRealTests\7z2605-x64.msi C:\temp\AegisK1-src\7zip.msi
if exist C:\temp\AegisK1Bundle.zip del C:\temp\AegisK1Bundle.zip
mockserver.exe pack C:\temp\AegisK1-src C:\temp\AegisK1Bundle.zip
set "AEGIS_DEPLOY_FILE=C:\temp\AegisK1Bundle.zip"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisK1Bundle.zip"
set "AEGIS_DEPLOY_CONTENT_ID=k1-throttled-fetch-v1"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_NAME=K1 throttled fetch 500 KB/s"
set "AEGIS_DEPLOY_BUNDLE_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C echo throttled-fetch-run"
set "AEGIS_DEPLOY_TIMEOUT=60"
mockserver.exe server
```

**Expected:**

- Mock command line shows `fetch_rate=500000 B/s`.
- `DOWNLOADING` phase lasts ~4 seconds (not instant). Wall-clock between `fetching installer from server` and `preparing and verifying software bundle` ≈ `bundle_bytes / 500000`.
- Terminal `SUCCESS`, `exit 0`, `cleanup: removed`.

### 15.3 Case K2 — Throttled upload (signed per-task rate)

**Goal:** upload a 5 MB log at 1 MB/s → ~5 sec.

**Mockserver:**

```cmd
set "AEGIS_FETCH_MAX_BYTES_PER_SECOND="
set "AEGIS_UPLOAD_MAX_BYTES_PER_SECOND="
set "AEGIS_DEPLOY_FETCH_MAX_BYTES_PER_SEC="
set "AEGIS_COLLECT_UPLOAD_MAX_BYTES_PER_SEC=1000000"
set "AEGIS_COLLECT_PATHS=C:\temp\AegisThrottleOut\big.log"
mockserver.exe server
```

**Expected:**

- Mock shows `upload_rate=1000000 B/s`.
- Wall-clock between `collecting files` and terminal ≈ 5 seconds (not instant, not 10 s).
- Server stores file under `uploads/<computer_id>/collect-001/big.log` with `integrity: true`. On-disk size matches endpoint source.

### 15.4 Case K3 — Lower wins: admin cap overrides looser signed rate

**Goal:** signed 2 MB/s + agent cap 500 KB/s → effective 500 KB/s (admin policy wins).

**Endpoint:** set the endpoint-wide cap, restart service:

```cmd
sc stop AegisOneClient
setx AEGIS_FETCH_MAX_BYTES_PER_SECOND 500000 /M
sc start AegisOneClient
```

> To revert later: `setx AEGIS_FETCH_MAX_BYTES_PER_SECOND "" /M` + restart.

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_FETCH_MAX_BYTES_PER_SEC=2000000"
set "AEGIS_DEPLOY_FILE=C:\temp\AegisK1Bundle.zip"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisK1Bundle.zip"
set "AEGIS_DEPLOY_CONTENT_ID=k3-lower-wins-v1"
set "AEGIS_DEPLOY_NAME=K3 lower-wins policy enforcement"
set "AEGIS_DEPLOY_BUNDLE_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C echo lower-wins-run"
mockserver.exe server
```

**Expected:**

- Mock shows `fetch_rate=2000000 B/s` on send (the signed value).
- Actual DOWNLOADING wall-clock ≈ 4 seconds (NOT 1 second). Agent applied `min(500000 admin cap, 2000000 signed) = 500000 B/s`.
- Terminal `SUCCESS`, exit 0.

> **K3 note:** `setx /M` does NOT reliably reach a running service. Set the admin
> cap via `config.json` instead (the service reads it at startup): **Endpoint**
> `sc stop AegisOneClient`, then write
> `{ "AEGIS_FETCH_MAX_BYTES_PER_SECOND": "500000" }` (ASCII, no BOM) to
> `C:\Program Files\AegisOne\Client\SKVC\config.json`, then `sc start AegisOneClient`.
> Revert by deleting that file and restarting the service.

---

## 15A. Block M — Resumable download (checkpoint restart)

A mid-transfer disconnect no longer re-downloads a large package from zero. The
agent writes received bytes to a content-addressed partial file next to the
destination; on a dropped connection the partial is KEPT (for SHA-pinned
transfers), and when the server re-issues the command the next fetch resumes from
the partial's byte offset (`ContentRequest.offset`). The WHOLE file's SHA-256 is
re-verified before accept, so a resumed transfer is as trustworthy as a single
one. Applies to every `FetchContent` transfer (bundles, patch content, cab).

### 15A.1 Case M1 — disconnect mid-fetch resumes from the byte offset

Use a bundle big enough that a throttled fetch gives a wide interrupt window.

**Mockserver** — FRESH CMD; build a 6 MB bundle and serve it throttled at
200 KB/s (~30 s download) via server delivery:

```cmd
cd /d C:\temp\mock
powershell -NoProfile -Command "$p='C:\temp\AegisResumeSrc'; New-Item -ItemType Directory -Force $p | Out-Null; $b=[byte[]]::new(6MB); (New-Object Random).NextBytes($b); [IO.File]::WriteAllBytes(\"$p\big.bin\",$b)"
if exist C:\temp\AegisResumeBundle.zip del C:\temp\AegisResumeBundle.zip
mockserver.exe pack C:\temp\AegisResumeSrc C:\temp\AegisResumeBundle.zip
set "AEGIS_TARGET_DEVICE=<DEVICE_UUID>"
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_CONTENT_ID=resume-demo-v2"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisResumeBundle.zip"
set "AEGIS_DEPLOY_AUTO_HASH=1"
set "AEGIS_DEPLOY_FETCH_MAX_BYTES_PER_SEC=200000"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_NAME=M1 resume after disconnect"
set "AEGIS_DEPLOY_BUNDLE_ENTRY="
set "AEGIS_DEPLOY_BUNDLE_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_ARGS=/D /C echo resume-run"
set "AEGIS_DEPLOY_TIMEOUT=120"
set "AEGIS_DEPLOY_PRE_COMMAND="
set "AEGIS_DEPLOY_POST_COMMAND="
set "AEGIS_DEPLOY_ROLLBACK_COMMAND="
set "AEGIS_DEPLOY_ROLLBACK_TIMEOUT_SECONDS="
mockserver.exe server
```

When the endpoint reaches `ACTION_PHASE_DOWNLOADING`, wait ~8–10 s (so the fetch
is genuinely mid-transfer, NOT nearly done), press `Ctrl+C` on the mockserver,
wait ~5 s, then run `mockserver.exe server` again in the same CMD.

**Expected (PASS):**

- **Mockserver** on the re-issue prints
  `FetchContent: resuming from offset <N> (<rest> bytes remain)` and
  `FetchContent: done (<rest>)` — `<rest>` LESS than the full bundle size.
- **Endpoint agent log** (in order): `fetch: connection lost mid-transfer;
  partial kept for resume on re-issue have_bytes=<N>`, then `fetch: found a
  partial from an earlier interrupted transfer — will resume have_bytes=<N>`,
  then `content fetched and verified ... resumed_from=<N>` with `<N>` > 0.
- Terminal `ACTION_PHASE_SUCCESS`, `cleanup: removed`. Whole-file SHA-256 passed
  across two connections.

> **Pitfall:** if the binaries are not the current build, the test silently
> reverts to re-download-from-zero. Verify the service exe with
> `certutil -hashfile "C:\Program Files\AegisOne\Client\AegisOneClient.exe" SHA256`
> against `dist/softdist-handoff.json` before running. The success log MUST
> contain `resumed_from=` — a pre-resume build omits that field.

### 15A.2 Case M2 — unpinned fetch never resumes (hardening)

Resume only trusts a persisted partial when the transfer is SHA-pinned. This is
covered by the agent's automated tests (`TestFetchContent_UnpinnedDoesNotResume`)
— no manual VM step is required; it is listed here so the server team knows an
unpinned (no `expected_hash`) deploy always restarts from zero by design.

---

## 16. Block L — Run-As + secure parameters + per-user detection

### 16.1 Prepare once (Block L)

**Mockserver:** FRESH CMD. Common envs:

```cmd
cd /d C:\temp\mock
set "AEGIS_TARGET_DEVICE=<DEVICE_UUID>"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_AUTO_HASH=1"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_DETECT_COUNT=0"
set "AEGIS_DEPENDENCY_DETECT_COUNT=0"
```

### 16.2 Case L1 — Run as the logged-in user

**Goal:** The signed command runs under the active interactive user, not LocalSystem. Writes to that user's `%USERPROFILE%`.

**Endpoint:** sign in to the VM with a non-admin test user (e.g. `aegisqa`). Confirm `%USERPROFILE%` → `C:\Users\aegisqa`.

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_NAME=L1 Run as logged-in user"
set "AEGIS_DEPLOY_RUN_AS=logged_in_user"
set "AEGIS_DEPLOY_FILE=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C echo %%USERNAME%%>%%USERPROFILE%%\aegis-runas-l1.txt"
set "AEGIS_DEPLOY_TIMEOUT=30"
mockserver.exe server
```

**Endpoint (as aegisqa), after:**

```cmd
type %USERPROFILE%\aegis-runas-l1.txt
```

**Expected:**

- Terminal `SUCCESS`, exit 0.
- File contains `aegisqa` (the active user), not `SYSTEM`.
- File path is under `C:\Users\aegisqa\`.

### 16.3 Case L2 — Run as a specified account

**Goal:** The signed command runs under an explicit domain or local user with its own token. Credentials delivered via secure stdin, never argv/env/logs.

**Endpoint:** create a local test account:

```cmd
net user aegisqa2 "<STRONG_PASSWORD>" /ADD
net localgroup Users aegisqa2 /ADD
```

**Mockserver:** create a secrets file with the password. The agent reads it ONLY in-memory, never writes it anywhere persistent:

```cmd
echo <STRONG_PASSWORD> > C:\temp\mock\aegisqa2.secret
set "AEGIS_DEPLOY_NAME=L2 Run as specified account"
set "AEGIS_DEPLOY_RUN_AS=specified_account"
set "AEGIS_DEPLOY_RUN_AS_USERNAME=aegisqa2"
set "AEGIS_DEPLOY_RUN_AS_DOMAIN=."
set "AEGIS_DEPLOY_SECRETS_FILE=C:\temp\mock\aegisqa2.secret"
set "AEGIS_DEPLOY_FILE=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C echo %%USERNAME%%>C:\Users\Public\aegis-runas-l2.txt"
set "AEGIS_DEPLOY_TIMEOUT=30"
mockserver.exe server
```

**Endpoint, after:**

```cmd
type C:\Users\Public\aegis-runas-l2.txt
```

**Expected:**

- Terminal `SUCCESS`, exit 0.
- File contains `aegisqa2`.
- Agent log (grep agent.log) does NOT contain the password string in plaintext.
- Mockserver outbox log does NOT contain the password either.

### 16.4 Case L3 — Ordinary package parameter sets config

**Goal:** A non-secret `parameters` map lands in the command via `${name}` substitution AND as `AEGIS_PARAM_<NAME>` env vars for the child process.

**Mockserver:**

```cmd
set "AEGIS_DEPLOY_NAME=L3 Ordinary package parameter"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_PARAMS=REGION=apac,FEATURE_FLAG=enabled"
set "AEGIS_DEPLOY_FILE=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C echo region=${REGION} feature=${FEATURE_FLAG} env=%%AEGIS_PARAM_REGION%% > C:\temp\AegisL3.txt"
set "AEGIS_DEPLOY_TIMEOUT=30"
mockserver.exe server
```

**Endpoint, after:**

```cmd
type C:\temp\AegisL3.txt
```

**Expected:**

- Terminal `SUCCESS`, exit 0.
- File contains `region=apac feature=enabled env=apac`.

### 16.5 Case L4 — Secure parameter delivered over stdin

**Goal:** A `secure_parameters` map is NOT substituted into argv, NOT exported as env vars. The agent writes JSON `{name: value}` on stdin to the child; the child reads it with its own PowerShell/C# shim.

**Prepare a stdin-reader payload on the endpoint:**

```cmd
mkdir C:\temp\AegisL4
```

Create `C:\temp\AegisL4\reader.cmd` on the ENDPOINT:

```cmd
@echo off
for /f "delims=" %%A in ('more') do echo %%A>> C:\temp\AegisL4\stdin-captured.txt
exit /b 0
```

**Mockserver:**

```cmd
echo APIKEY=super-secret-value > C:\temp\mock\aegis-secret-params.secret
set "AEGIS_DEPLOY_NAME=L4 Secure parameter over stdin"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_PARAMS="
set "AEGIS_DEPLOY_SECURE_PARAMS=C:\temp\mock\aegis-secret-params.secret"
set "AEGIS_DEPLOY_FILE=C:\temp\AegisL4\reader.cmd"
set "AEGIS_DEPLOY_ARGS="
set "AEGIS_DEPLOY_TIMEOUT=30"
mockserver.exe server
```

**Endpoint, after:**

```cmd
type C:\temp\AegisL4\stdin-captured.txt
```

**Expected:**

- Terminal `SUCCESS`, exit 0.
- `stdin-captured.txt` contains `{"APIKEY":"super-secret-value"}` (JSON on stdin).
- Agent log and server log do NOT contain `super-secret-value` in plaintext.
- Mock output says `installer output suppressed for private deployment` — this is informational; secure deployments suppress diagnostic output to protect secrets.

### 16.6 Case L5 — Per-user detection + repeat protection + verified removal

**Goal:** Install into HKCU (per-user), confirm agent correctly scopes detection to a user context, prevents double-install, and verifies removal.

**Endpoint (as aegisqa):**

```cmd
reg delete HKCU\SOFTWARE\AegisOne\L5Demo /f
```

**Mockserver — install:**

```cmd
set "AEGIS_DEPLOY_NAME=L5 Per-user install"
set "AEGIS_DEPLOY_RUN_AS=logged_in_user"
set "AEGIS_DEPLOY_DETECTION_SCOPE=user"
set "AEGIS_DEPLOY_DETECT_COUNT=1"
set "AEGIS_DEPLOY_DETECT_MODE=all"
set "AEGIS_DEPLOY_DETECT_1_KIND=registry_value"
set "AEGIS_DEPLOY_DETECT_1_HIVE=HKCU"
set "AEGIS_DEPLOY_DETECT_1_KEY=SOFTWARE\AegisOne\L5Demo"
set "AEGIS_DEPLOY_DETECT_1_VIEW=64"
set "AEGIS_DEPLOY_DETECT_1_VALUE_NAME=Installed"
set "AEGIS_DEPLOY_DETECT_1_VALUE_TYPE=string"
set "AEGIS_DEPLOY_DETECT_1_EXPECTED=yes"
set "AEGIS_DEPLOY_FILE=reg.exe"
set "AEGIS_DEPLOY_ARGS=add HKCU\SOFTWARE\AegisOne\L5Demo /v Installed /t REG_SZ /d yes /f"
set "AEGIS_DEPLOY_TIMEOUT=30"
mockserver.exe server
```

**Expected:** `SUCCESS`, exit 0, "verified installed in user scope".

**Mockserver — repeat (should skip):**

```cmd
set "AEGIS_DEPLOY_ARGS=add HKCU\SOFTWARE\AegisOne\L5Demo /v Installed /t REG_SZ /d DIFFERENT /f"
mockserver.exe server
```

**Expected:** `NOT_APPLICABLE`, "already installed in user scope". Reg value stays `yes` (not overwritten with `DIFFERENT`).

**Mockserver — uninstall:**

```cmd
set "AEGIS_DEPLOY_NAME=L5 Per-user uninstall"
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_FILE=reg.exe"
set "AEGIS_DEPLOY_ARGS=delete HKCU\SOFTWARE\AegisOne\L5Demo /f"
mockserver.exe server
```

**Endpoint:**

```cmd
reg query HKCU\SOFTWARE\AegisOne\L5Demo /v Installed
```

**Expected:** uninstall `SUCCESS`, "verified removed". Reg query returns "not found".

---

## 17. UC Blocks — UC-18 (third-party inventory) & UC-19 (HKU parity + refresh)

These are inventory tests that intersect SoftDist (the catalog match drives SoftDist dispatch) but their detailed test cases live in `tester-guide-patching.md`. Brief overview here:

### 17.1 UC-18 — Third-party installed-apps inventory

Agent sends `DeviceStateReport.installed_apps[]` on every Layer-1 cycle (default 30 sec per `AEGIS_DEVICE_STATE_INTERVAL`), reading HKLM Uninstall hives. Each entry: display_name, display_version, publisher, install_date, uninstall_key, hive, estimated_size_kb, uninstall_string. Server team's catalog service matches these against its 1000+ app catalog and dispatches SoftDist `software_deploy` to update outdated apps.

Quick smoke (sanity check in this guide): install 7-Zip from Block E → agent `installed_apps` count increases by 1 within one cycle. Uninstall → count decreases. See `tester-guide-patching.md` UC-18A–F for full matrix.

### 17.2 UC-19 — HKU per-user parity + on-demand `inventory refresh`

As of 2026-11-02 the agent also enumerates `HKEY_USERS\<SID>\...\Uninstall` (native + WOW6432Node) for every LOADED user hive. Per-user installs (Chrome, Teams, Zoom, Spotify) now report with `hive = HKU\<SID>` or `HKU\<SID>\WOW6432Node`. See `tester-guide-patching.md` UC-19A–E for full matrix. The `inventory refresh` NSH verb (dev tool, capped at 100 targets) forces a fresh send regardless of change state.

---

## 18. Block UE — Uninstall edge cases (1-34)

### 18.1 Prepare once (Block UE)

1. **Copy fixtures to BOTH machines** — the `docs/fixtures/softdist/` folder from the repo must land at `C:\temp\AegisSoftDistTests\` on the endpoint VM AND on the mockserver host. Keep `server/`, `endpoint/`, payload scripts, and `.nsh` files together.
2. **Replace binaries** — endpoint: stop service, swap `AegisOneClient.exe` (or use MSI). Mockserver: Ctrl+C, swap `mockserver.exe`.
3. **Open two CMDs** — Admin CMD on the endpoint; CMD in `C:\temp\mock` on the mockserver host.
4. These fixtures are DUMMY registry products — they validate routing/detection and script execution, NOT real vendor acceptance.

### 18.2 Every test follows this 5-step pattern

For test number `NN` (01-34):

1. **Mockserver**: stop with Ctrl+C. If CMD asks `Terminate batch job (Y/N)?`, answer **Y**.
2. **Endpoint**: `call C:\temp\AegisSoftDistTests\endpoint-setup.cmd NN`.
   This resets ONLY the Aegis test registrations and marker files for THIS test. Earlier tests don't contaminate.
3. **Mockserver**: `call C:\temp\AegisSoftDistTests\server-test.cmd NN`.
   This clears stale SoftDist/NSH env vars and starts normal server mode. Keep your existing TLS/enrollment.
4. **Wait** for the intended endpoint to connect and reach a terminal `SUCCESS` / `FAILED` / `NOT_APPLICABLE`. `QUEUED` or `RUNNING` is NOT a final result. If the agent doesn't reconnect, restart its service.
5. **Endpoint**: `call C:\temp\AegisSoftDistTests\endpoint-check.cmd NN`.
   PASS requires BOTH the expected server status AND the endpoint check output matching.

**Two-agent note:** mock dispatches to each connected device. Check the line for YOUR intended hostname/device UUID. Other agents reporting different results (e.g. `NOT_APPLICABLE` because they have no fixtures) do not invalidate your run.

### 18.3 Test index (UE-01 to UE-34)

| # | Check | Expected terminal |
|---|---|---|
| 01 | Ambiguous display name | FAILED; ambiguous installed product (2 matches) |
| 02 | Remove only requested version 1.0 | SUCCESS; verified removed |
| 03 | Already absent requested version | NOT_APPLICABLE; not installed |
| 04 | Exact 64-bit key | SUCCESS; verified removed |
| 05 | Same key in both registry views | FAILED; ambiguous installed product (2 matches) |
| 06 | Remove only the 32-bit registration | SUCCESS; verified removed |
| 07 | Remove only the 64-bit registration | SUCCESS; verified removed |
| 08 | Exact name wins over similarly named companion | SUCCESS; verified removed |
| 09 | Key and display name disagree | NOT_APPLICABLE; not installed |
| 10 | No registered uninstall command | FAILED; no uninstall command registered |
| 11 | No quiet command, no author silent_args | FAILED; supply vendor-specific silent_args |
| 12 | Author silent_args with spaces and runtime variables | SUCCESS; verified removed |
| 13 | Zero exit is not enough when product remains | FAILED; verification failed; product still detected after uninstall; exit_code=0 |
| 14 | Registered executable is missing | FAILED; could not start installer |
| 15 | Unquoted spaced executable path | FAILED; quote paths with spaces |
| 16 | Malformed MSI ProductCode | FAILED; invalid MSI ProductCode |
| 17 | HKCU is explicitly refused until Run-As | FAILED; per-user/HKCU removal requires user-context support |
| 18 | Custom command overrides default registry uninstaller | SUCCESS; verified removed; exit_code=0 |
| 19 | Hash-pinned CMD script overrides default uninstaller | SUCCESS; verified removed |
| 20 | Tampered or wrong-hash file is refused | FAILED; integrity check failed (hash mismatch) |
| 21 | Malformed registry value is not absence | FAILED; registry detection failed; QuietUninstallString |
| 22 | Server-delivered custom script with detection IDs | SUCCESS; verified removed; FetchContent kind=package |
| 23 | Installer timeout | FAILED; installer timed out after 1s |
| 24 | Nonzero registered uninstaller exit | FAILED; installer failed (exit 19); exit_code=19 |
| 25 | Package-author custom success exit code | SUCCESS; verified removed; exit_code=19 |
| 26 | Install fixture and verify version | SUCCESS; verified installed (version 1.0) |
| 27 | Upgrade old 1.0 to 2.0 | SUCCESS; verified installed (version 2.0) |
| 28 | Same/newer version skips installation | NOT_APPLICABLE; already installed (version 2.0, skipped) |
| 29 | Missing pinned hash refused | FAILED; expected_hash (sha256) is required |
| 30 | Required publisher signature rejects unsigned script | FAILED; publisher signature check failed |
| 31 | CMD payload path contains spaces | SUCCESS; verified removed |
| 32 | Installed but requested version cannot be verified | FAILED; installed version is unknown (target 1.0) |
| 33 | NSH SoftDist job, parameters + fleet loop | JOB SUCCESS; per-target package/tag/hostname output |
| 34 | NSH job propagates native script failure | JOB FAILED; native command exit_code=7 |

### 18.4 Walk-through for UE-01 (template for all 34)

**Check:** Ambiguous display name — the mock registers TWO products matching the display_name, agent must refuse.

**Endpoint, Admin CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 01
```

**Mockserver, CMD in the mockserver folder:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 01
```

**Wait for terminal.** PASS line for your host: `FAILED; ambiguous installed product (2 matches)`.

**Endpoint, after:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 01
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` (view 64) PRESENT; `AegisSoftDistFixture-v2` (view 64) PRESENT (both still registered — the ambiguity was correctly refused, nothing uninstalled).

**If it fails:** save the host's complete ActionStatus (phase, exit_code, reason, detail) plus the endpoint-check output. Mark FAIL in the Record Sheet. Move to UE-02 — tests are independent.

Repeat the same 5-step pattern for UE-02 through UE-34. The index above gives the expected terminal result for each.

### 18.5 Real MSI / EXE acceptance (in addition to UE fixtures)

After UE-01 through UE-34, run these real-vendor acceptance checks:

- **MSI install from server** — reuse Block E Case 1 (7-Zip 26.04).
- **MSI uninstall by ProductCode** — like E4 but set `AEGIS_DEPLOY_PRODUCT_CODE={23170F69-40C1-2702-2605-000001000000}` explicitly, with empty `AEGIS_DEPLOY_DISPLAY_NAME`.
- **MSI uninstall by registry registration** — Block E Case 4.
- **EXE install and fileless uninstall** — Block F Case 1 install + an uninstall job that targets `AEGIS_DEPLOY_DISPLAY_NAME=Mozilla Firefox` with no file, no content_id.

---

## 18A. UC-20 — Script content audit (compliance / forensic)

**Why:** BigFix client logs show WHICH script file ran, not WHAT was inside it.
Customers in regulated verticals (healthcare, finance, defense) demand
end-to-end script visibility. Aegis One ab har SoftDist run jo
`.ps1 / .bat / .cmd / .vbs / .py` payload execute karta hai uske pehle:

- Default: tamper-evident **audit record** likhta hai `<SKVC>\logs\softdist\<action_id>.transcript` mein — SHA-256 digest + script size + first 500 bytes + last 500 bytes.
- Opt-in: full script content embedded in transcript via signed per-task flag `capture_script_content: true`.
- Policy override at endpoint: `AEGIS_LOG_SCRIPT_CONTENT` env.
    - `enabled` (default) — honor the per-task flag.
    - `disabled` — kill switch. Full content NEVER written, even if the signed flag says true. For regulated endpoints where raw script bytes must not land on disk.
    - `always` — force full content on every script, regardless of flag. Forensic mode.
- `SecureParameters` NEVER appear in the transcript — scripts are read verbatim from disk BEFORE `${name}` substitution; secrets flow via child stdin.

### 18A.1 Pre-reqs

- Agent built from this handoff.
- A tiny PowerShell script on the endpoint for the tests, e.g. `C:\tmp\audit-test.ps1`:
  ```powershell
  # audit-test.ps1 — benign script for script-audit testing
  Write-Host "hello from audit test"
  exit 0
  ```
- Agent mock server env ready (same as other UCs).

### 18A.2 UC-20A — Default (hash + preview only)

**Goal:** Confirm the agent writes the audit record EVERY time a script
runs, even when the admin asks for nothing special.

Mockserver side (dev CMD):
```cmd
set AEGIS_DEPLOY_NAME=audit-default
set AEGIS_DEPLOY_FILE=C:\tmp\audit-test.ps1
set AEGIS_DEPLOY_INSTALLER_TYPE=ps1
set AEGIS_DEPLOY_SILENT_ARGS=-NoProfile -ExecutionPolicy Bypass -File ${file_path}
set AEGIS_DEPLOY_HASH=<SHA256 of audit-test.ps1>
set AEGIS_DEPLOY_CAPTURE_SCRIPT_CONTENT=false
rem (or just do not set it — defaults to false)
mockserver.exe
```

Dispatch via NSH or let mock auto-send; agent runs the install.

**Expected on endpoint:**
```
C:\...\SKVC\logs\softdist\deploy-001.transcript
```
Open and verify:
```
# Aegis One SoftDist script audit transcript
# Written: 2026-11-02T...
# Action ID: deploy-001
# Script path: C:\<download or staged>\audit-test.ps1
# Script size (bytes): 93
# SHA-256: <64 hex>
# Endpoint policy: enabled
# Per-task CaptureScriptContent flag: false
# Full content captured: false

# ------ HEAD (first 500 bytes, verbatim) ------
# audit-test.ps1 — benign script for script-audit testing
Write-Host "hello from audit test"
exit 0

# ------ TAIL (last 500 bytes, verbatim) ------
...same content...
```

**Pass:** No `# ------ FULL CONTENT` section. Policy line reads `enabled`.
Hash matches `certutil -hashfile audit-test.ps1 SHA256` output.

### 18A.3 UC-20B — Per-task opt-in (full content capture)

**Goal:** Admin asks for full content on this one deploy.

```cmd
set AEGIS_DEPLOY_NAME=audit-full
set AEGIS_DEPLOY_FILE=C:\tmp\audit-test.ps1
set AEGIS_DEPLOY_INSTALLER_TYPE=ps1
set AEGIS_DEPLOY_SILENT_ARGS=-NoProfile -ExecutionPolicy Bypass -File ${file_path}
set AEGIS_DEPLOY_HASH=<SHA256>
set AEGIS_DEPLOY_CAPTURE_SCRIPT_CONTENT=true
mockserver.exe
```

Mock log line expected:
```
Sent command -> id: deploy-001, type: software_deploy, name=audit-full, file=C:\tmp\audit-test.ps1, capture_script_content=true (signed)
```

**Expected transcript header:**
```
# Per-task CaptureScriptContent flag: true
# Full content captured: true
...
# ------ FULL CONTENT (verbatim, policy-opt-in) ------
# audit-test.ps1 — benign script for script-audit testing
Write-Host "hello from audit test"
exit 0
```

**Pass:** `FULL CONTENT` block present and equal to the script on disk.

### 18A.4 UC-20C — Endpoint kill switch (`disabled`) wins

**Goal:** Regulated endpoint. Even if signed deploy asks for full
content, it MUST NOT be written to disk.

On the VM (admin cmd), set the policy:
```cmd
setx AEGIS_LOG_SCRIPT_CONTENT disabled /M
sc stop AegisOneClient && sc start AegisOneClient
```

Repeat UC-20B exactly (with `AEGIS_DEPLOY_CAPTURE_SCRIPT_CONTENT=true`).

**Expected transcript header:**
```
# Endpoint policy: disabled
# Per-task CaptureScriptContent flag: true
# Full content captured: false
```

**Pass:** No `FULL CONTENT` section even though the signed task flag was
true. Hash + preview are still written (audit is NEVER fully disabled).

### 18A.5 UC-20D — Endpoint force-on (`always`) wins

**Goal:** Forensic endpoint. Every script is fully captured regardless
of signed flag.

```cmd
setx AEGIS_LOG_SCRIPT_CONTENT always /M
sc stop AegisOneClient && sc start AegisOneClient
```

Run UC-20A (which does NOT set the flag). Transcript header:
```
# Endpoint policy: always
# Per-task CaptureScriptContent flag: false
# Full content captured: true
```

**Pass:** `FULL CONTENT` section present even though the signed task
flag was false.

### 18A.6 UC-20E — MSI / EXE skip (not audited)

**Goal:** Confirm non-script payloads are NOT audited (no transcript
file for MSI/EXE/ZIP).

Run any plain MSI deploy from Block E (e.g. 7-Zip MSI install). After the
install completes, check:
```cmd
dir C:\...\SKVC\logs\softdist\
```

**Pass:** No new `.transcript` file for the MSI's action_id. (The audit
is specifically for scripts; binaries have their hash captured in
`AEGIS_DEPLOY_HASH` already and content is not meaningful text.)

### 18A.7 UC-20F — Bundle entry_point script

**Goal:** When a bundle's `entry_point` is a `.ps1`/`.bat`/etc., the
staged entry file is audited.

Repeat Block A with a bundle whose `entry_point: install.ps1`. After
dispatch, transcript path:
```
C:\...\SKVC\logs\softdist\deploy-001.transcript
```
should name the resolved `install.ps1` path under the bundle extraction
directory (`<SecureDir>/download/packages/...`).

**Pass:** Transcript exists, `Script path:` header line names the
staged entry file, not the ZIP.

### 18A.8 UC-20G — Unreadable payload → fail-loud

**Goal:** If the agent CAN'T read what it's about to run, deploy FAILS;
blindly running an unaudited script defeats the whole feature.

Simulate: set `AEGIS_DEPLOY_FILE=C:\no-such-file.ps1` and dispatch.

**Expected:**
- Agent log:
  ```
  software_deploy: script audit read failed path=C:\no-such-file.ps1 error=open ...no such file or directory
  ```
- `ActionStatus.Reason` streamed to mock:
  `script audit read failed: ...`
- Phase: `ACTION_PHASE_FAILED`.

**Pass:** Deploy fails BEFORE the script could run. No partial side effects.

### 18A.9 UC-20H — SecureParameters leak check (critical)

**Goal:** Prove that `secure_parameters` values NEVER end up in the
transcript (would be a critical compliance failure).

Dispatch a `software_deploy_v2` with:
```
parameters: {} 
secure_parameters: { DB_PASS: "s3cret-NEVER-LOG-ME" }
file_path: C:\tmp\audit-test.ps1
capture_script_content: true
```

Agent runs the install, writes the transcript (full content captured).

**Verify:**
```powershell
Select-String -Path C:\...\SKVC\logs\softdist\deploy-001.transcript -Pattern "s3cret-NEVER-LOG-ME"
```

**Pass:** NO match. The secret appears nowhere in the transcript — not
in head, not in tail, not in full content. The script bytes on disk are
captured BEFORE any `${...}` substitution; the secret is delivered via
child stdin JSON (never substituted into the file).

### 18A.10 File permissions (defense in depth)

Transcript file mode is `0600` (SYSTEM read/write only; non-admin users
denied). Verify with:
```powershell
icacls C:\...\SKVC\logs\softdist\deploy-001.transcript
```
should show SYSTEM full control and no inherited user access.

### 18A.11 UC-20 record sheet

| Case | Date | Tester | PASS/FAIL | Notes |
|---|---|---|---|---|
| 20A default hash+preview | | | | |
| 20B per-task opt-in full | | | | |
| 20C kill switch wins | | | | |
| 20D force-on wins | | | | |
| 20E MSI/EXE skip | | | | |
| 20F bundle entry_point | | | | |
| 20G unreadable fail-loud | | | | |
| 20H secret leak check | | | | |

---

## 19. Common troubleshooting

### 19.1 "installer bundle needs entry_point and no bundle.command"

Old bundle options from a previous test leaked into the current CMD. Close the CMD, open a NEW one, re-run the block's "Prepare once", then the case.

### 19.2 "installer output suppressed for private deployment"

Informational. Run-As contexts (including SYSTEM) suppress diagnostic output to the server for secret safety. For non-secret packages, use `/L*v C:\Windows\Temp\<name>.log` MSI logging flags.

### 19.3 Mock sends `deploy-001` twice

If you restart mockserver while an installer is RUNNING, the agent's worker survives the connection loss and completes the original run. On reconnect, the mock may dispatch `deploy-001` again. You'll see `FAILED` followed by `RUNNING` for the same action_id. Wait for the SECOND terminal line before marking the test.

### 19.4 CAB scan errors during a SoftDist run

CAB scan is a SEPARATE patch reconciliation action. Scans do NOT take the SoftDist machine-install gate. A patch INSTALL can hold that gate; the agent's diagnostic log explicitly logs "waiting for install gate". Don't infer a CAB scan blocked an MSI just from QUEUED.

### 19.5 Tail the installer log

**Endpoint:**

```cmd
powershell -NoProfile -Command "Get-Content 'C:\Windows\Temp\Aegis-7zip-install.log' -Tail 60"
powershell -NoProfile -Command "Get-Content 'C:\Windows\Temp\Aegis-7zip-upgrade.log' -Tail 60"
powershell -NoProfile -Command "Get-Content 'C:\Windows\Temp\Aegis-7zip-uninstall.log' -Tail 60"
```

### 19.6 Agent log location

```
C:\Program Files\AegisOne\Client\SKVC\logs\agent.log
```

Key lines to grep for:

- `software_deploy: accepted` — command received
- `queue handoff` — passed to the worker
- `install gate` — serialized against another installer
- `FAST-LANE patch-state: CHANGED -> sending` — Layer-1 inventory sent
- `FAST-LANE patch-state: no change this cycle (skip send)` — fingerprint suppression

### 19.7 "ping child dead on timeout" (Block I)

If `tasklist` after I1's terminal shows no `ping.exe` rows but the reason says "no descendants were alive at kill", the kernel reaped the child before the Job Object kill. Retry; one of the two accepted reason variants is still PASS.

### 19.8 Firewall silently blocking the mockserver

Symptom: endpoint logs show `dial tcp <IP>:50051: connectex: A connection attempt failed`. Fix: §2.5 firewall rule on the mockserver host.

### 19.9 Hash mismatch after replacing a binary

Always use the SHA256 from `dist/softdist-handoff.json` as truth. If a copy picked up a stale cached version (OneDrive, antivirus write-back, etc.), re-copy from `dist/` and verify with `Get-FileHash`.

---

## 20. Record sheet

Copy this table into a Google Sheet / Excel / your QA tool. Mark one row per case.

| Block | Case | Date (ISO) | Tester | Agent SHA256 | Mockserver SHA256 | Terminal phase | exit_code | reboot_required | PASS/FAIL | Notes / Reason |
|---|---|---|---|---|---|---|---|---|---|---|
| A | A1 |  |  |  |  |  |  |  |  |  |
| A | A2 |  |  |  |  |  |  |  |  |  |
| A | A3 |  |  |  |  |  |  |  |  |  |
| B | B1 |  |  |  |  |  |  |  |  |  |
| B | B2 |  |  |  |  |  |  |  |  |  |
| B | B3 |  |  |  |  |  |  |  |  |  |
| C | C1 |  |  |  |  |  |  |  |  |  |
| C | C2 |  |  |  |  |  |  |  |  |  |
| C | C3 |  |  |  |  |  |  |  |  |  |
| D | D1 |  |  |  |  |  |  |  |  |  |
| D | D2 |  |  |  |  |  |  |  |  |  |
| D | D3 |  |  |  |  |  |  |  |  |  |
| E | E1 |  |  |  |  |  |  |  |  |  |
| E | E2 |  |  |  |  |  |  |  |  |  |
| E | E3 |  |  |  |  |  |  |  |  |  |
| E | E4 |  |  |  |  |  |  |  |  |  |
| E | E5 |  |  |  |  |  |  |  |  |  |
| F | F1 |  |  |  |  |  |  |  |  |  |
| G | G1 |  |  |  |  |  |  |  |  |  |
| G | G2 |  |  |  |  |  |  |  |  |  |
| G | G3 |  |  |  |  |  |  |  |  |  |
| H | H1 |  |  |  |  |  |  |  |  |  |
| H | H2 |  |  |  |  |  |  |  |  |  |
| I | I1 |  |  |  |  |  |  |  |  |  |
| J | J1 |  |  |  |  |  |  |  |  |  |
| J | J2 |  |  |  |  |  |  |  |  |  |
| J | J3 |  |  |  |  |  |  |  |  |  |
| K | K1 |  |  |  |  |  |  |  |  |  |
| K | K2 |  |  |  |  |  |  |  |  |  |
| K | K3 |  |  |  |  |  |  |  |  |  |
| L | L1 |  |  |  |  |  |  |  |  |  |
| L | L2 |  |  |  |  |  |  |  |  |  |
| L | L3 |  |  |  |  |  |  |  |  |  |
| L | L4 |  |  |  |  |  |  |  |  |  |
| L | L5 |  |  |  |  |  |  |  |  |  |
| UC-18 | smoke |  |  |  |  |  |  |  |  | depth in tester-guide-patching.md |
| UC-19 | smoke |  |  |  |  |  |  |  |  | depth in tester-guide-patching.md |
| UE | 01 |  |  |  |  |  |  |  |  |  |
| UE | 02 |  |  |  |  |  |  |  |  |  |
| UE | 03 |  |  |  |  |  |  |  |  |  |
| UE | 04 |  |  |  |  |  |  |  |  |  |
| UE | 05 |  |  |  |  |  |  |  |  |  |
| UE | 06 |  |  |  |  |  |  |  |  |  |
| UE | 07 |  |  |  |  |  |  |  |  |  |
| UE | 08 |  |  |  |  |  |  |  |  |  |
| UE | 09 |  |  |  |  |  |  |  |  |  |
| UE | 10 |  |  |  |  |  |  |  |  |  |
| UE | 11 |  |  |  |  |  |  |  |  |  |
| UE | 12 |  |  |  |  |  |  |  |  |  |
| UE | 13 |  |  |  |  |  |  |  |  |  |
| UE | 14 |  |  |  |  |  |  |  |  |  |
| UE | 15 |  |  |  |  |  |  |  |  |  |
| UE | 16 |  |  |  |  |  |  |  |  |  |
| UE | 17 |  |  |  |  |  |  |  |  |  |
| UE | 18 |  |  |  |  |  |  |  |  |  |
| UE | 19 |  |  |  |  |  |  |  |  |  |
| UE | 20 |  |  |  |  |  |  |  |  |  |
| UE | 21 |  |  |  |  |  |  |  |  |  |
| UE | 22 |  |  |  |  |  |  |  |  |  |
| UE | 23 |  |  |  |  |  |  |  |  |  |
| UE | 24 |  |  |  |  |  |  |  |  |  |
| UE | 25 |  |  |  |  |  |  |  |  |  |
| UE | 26 |  |  |  |  |  |  |  |  |  |
| UE | 27 |  |  |  |  |  |  |  |  |  |
| UE | 28 |  |  |  |  |  |  |  |  |  |
| UE | 29 |  |  |  |  |  |  |  |  |  |
| UE | 30 |  |  |  |  |  |  |  |  |  |
| UE | 31 |  |  |  |  |  |  |  |  |  |
| UE | 32 |  |  |  |  |  |  |  |  |  |
| UE | 33 |  |  |  |  |  |  |  |  |  |
| UE | 34 |  |  |  |  |  |  |  |  |  |

**For FAIL rows**, attach:

- Full mockserver console transcript for the case
- `agent.log` tail (last 200 lines after the case ran)
- Any `C:\Windows\Temp\Aegis-*.log` installer log if the case used one
- The endpoint-check output

---

## 21. End of guide

Hand this guide + the `dist/` folder's binaries + the fixtures at `docs/fixtures/softdist/` to the tester. They should NOT need any other document to run the full matrix.

Report back the Record Sheet + any FAIL evidence to the agent-track owner (Vijay). Blocks UC-18/UC-19 and the inventory NSH verb details are covered in detail in `tester-guide-patching.md` and `tester-guide-nsh.md` respectively.
