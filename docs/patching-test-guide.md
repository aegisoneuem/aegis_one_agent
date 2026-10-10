# Patching Test Guide — for the Server Team

**Audience:** the Aegis One server/console team.
**Goal:** run the agent + the reference mock server on a Windows VM and exercise
real patching jobs — **single patch, multiple patches, success and failure
scenarios** — so you can see exactly what the agent does and what it sends back,
and from that build the production server/console to match.

> The `mockserver.exe` is an **executable reference spec**, not the real server.
> It drives the agent the way your production server must, and prints what the
> agent reports. The section [What the real server must implement](#what-the-real-server-must-implement)
> lists the responsibilities to build.

---

## 1. What you get (in `dist/`)

| File | Purpose |
|---|---|
| `AegisOneClient.msi` | The agent installer (interactive wizard or silent). Installs the `AegisOneClient` Windows service. |
| `AegisOneClient.exe` | The agent binary (also embedded in the MSI) — for reference. |
| `mockserver.exe` | The reference server. Drives the agent and prints what it receives. |
| `certgen.exe` | Generates the dev mTLS certificate set (`certs/`). |

All Windows x64.

---

## 2. Prerequisites

- A **Windows 10/11 VM** (take a **snapshot** first — installs are real; revert after).
- **Administrator** (the agent runs as the `LocalSystem` service; DISM installs need elevation).
- **Two or more `.msu` patch files** that are *actually missing* on the test VM, from the
  [Microsoft Update Catalog](https://www.catalog.update.microsoft.com). Tip: pick a VM a
  few updates behind so there is something to install.
- For each `.msu`, its **SHA-256** (the agent verifies integrity before installing):
  ```cmd
  certutil -hashfile C:\deploy\windows11.0-kb5126052.msu SHA256
  ```

---

## 3. One-time setup

### 3.1 Generate the dev certificates (mTLS)
From the folder where you put the binaries:
```cmd
certgen.exe certs
```
This creates `certs\` with `ca.crt`, `ca.key`, `server.crt`, `server.key`, `client.crt`, `client.key`.
> **Use ONE consistent `certs\` set everywhere.** The agent's `ca.crt` must be the
> same CA that signed the server's cert, or mTLS fails with
> `x509: ... ECDSA verification failure ("Aegis One Dev CA")`. Don't regenerate
> mid-test.

### 3.2 Start the mock server
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
mockserver.exe
```
It listens on `:50051` (single port: mTLS operations **and** enrollment).

> **Important:** the mock server is **env-driven**. It reads the `AEGIS_*` variables
> **at startup** and sends the matching commands when the agent connects. To run a
> new scenario, **stop it (Ctrl+C), set the variables, start it again.**

### 3.3 Install the agent (new VM)
Run `AegisOneClient.msi`. In the wizard (or via `msiexec /qn` properties):

| Setting | Value |
|---|---|
| Server address | `127.0.0.1:50051` (or the server's `host:port`) |
| Server cert name | `aegis-server` |
| Enrollment token | `tenant-abc-123` (same as the server) |
| CA certificate file | full path to your `certs\ca.crt` |

On first run the agent generates its device identity, **enrolls** (keypair + CSR →
server signs → per-device cert), then connects over mTLS. Verify:
```cmd
sc query AegisOneClient                 REM state = RUNNING
curl http://127.0.0.1:50080/status      REM "connected": true, "enrolled": true
```
Agent logs: `C:\Program Files\AegisOne\Client\SKVC\logs\agent-YYYY-MM-DD.log`.

---

## 4. The status model you will see

For **every** dispatched action the agent streams a unified **`ActionStatus`** with a
consistent lifecycle (this is the BigFix/ManageEngine-style per-action status your
console will show):

```
QUEUED → DOWNLOADING → VERIFYING → RUNNING → SUCCESS | FAILED | NOT_APPLICABLE
```
Each carries: `action_id` (= the command id you sent — your console's correlation
key), `action_type` (`patch_install` | `stage` | `scan` | `reboot`), `device_id`,
`exit_code`, `reboot_required`, `reason` (concise), `detail` (verbose). The mock
server prints each one as `Received action status -> ...`.

> **Scheduled / Pending is a SERVER-side state.** Scheduling is server-driven: your
> server holds an action until its window, then dispatches it. The agent only
> reports from `QUEUED` onward.

---

## 5. Test scenarios

For each: **stop** the mock server, `set` the variables, **start** it, then watch both
the mock server output and the agent log.

### A. Single patch — success
```cmd
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_INSTALL_FILE=C:\deploy\windows11.0-kb5126052.msu
set AEGIS_INSTALL_KB=KB5126052
set AEGIS_INSTALL_HASH=<sha256 of that .msu>
mockserver.exe
```
Expected:
```
Received action status -> type=patch_install phase=ACTION_PHASE_QUEUED
Received action status -> type=patch_install phase=ACTION_PHASE_VERIFYING
Received action status -> type=patch_install phase=ACTION_PHASE_RUNNING
Received action status -> type=patch_install phase=ACTION_PHASE_SUCCESS exit_code=0 reboot_required=<true|false>
```
`exit_code` is the DISM result (`0` = installed, `3010` = installed, reboot required).

### B. Multiple patches — serial queue
Comma-separate the lists (same index position across all three):
```cmd
set AEGIS_INSTALL_FILE=C:\deploy\a.msu,C:\deploy\b.msu
set AEGIS_INSTALL_KB=KB5126052,KB5129195
set AEGIS_INSTALL_HASH=<sha256-a>,<sha256-b>
mockserver.exe
```
Expected: `install-001` runs to a terminal phase, **then** `install-002` starts —
patches install **one at a time** (the Windows servicing stack is serial). Other
traffic (heartbeats, other commands) stays responsive meanwhile.

### C. Failure — integrity mismatch (wrong hash)
```cmd
set AEGIS_INSTALL_FILE=C:\deploy\windows11.0-kb5126052.msu
set AEGIS_INSTALL_KB=KB5126052
set AEGIS_INSTALL_HASH=0000000000000000000000000000000000000000000000000000000000000000
mockserver.exe
```
Expected: `phase=ACTION_PHASE_FAILED` with a `reason` about the SHA-256 mismatch.
**The agent refuses to install tampered/corrupt bytes — DISM never runs.**

### D. Failure — file not present
Point to a path that does not exist on the VM:
```cmd
set AEGIS_INSTALL_FILE=C:\deploy\does-not-exist.msu
set AEGIS_INSTALL_KB=KB5126052
mockserver.exe
```
Expected: `phase=ACTION_PHASE_FAILED`, `reason` = file not found. (In production the
server **delivers** the file to the endpoint first — see the contract below.)

### E. Already installed → NOT_APPLICABLE
Send an install for a KB the VM already has:
```cmd
set AEGIS_INSTALL_FILE=C:\deploy\windows11.0-kb5126052.msu
set AEGIS_INSTALL_KB=KB5126052
set AEGIS_INSTALL_HASH=<sha256>
mockserver.exe
```
If `KB5126052` is already installed, expect
`phase=ACTION_PHASE_NOT_APPLICABLE reason="already installed (skipped)"` — no
re-install. (Installed state is read from the CBS servicing store.)

### F. Staging (pre-cache) then install from cache
Pre-cache a patch ahead of a window, then install it with no re-download:
```cmd
REM 1) stage it (server serves the bytes via the content channel, agent caches encrypted)
set AEGIS_SERVE_PATCH=C:\deploy\windows11.0-kb5126052.msu
set AEGIS_STAGE_KB=KB5126052
set AEGIS_STAGE_HASH=<sha256>
set AEGIS_STAGE_FILE=kb5126052.msu
mockserver.exe
```
Expected: `type=stage phase=DOWNLOADING` → `phase=SUCCESS reason="patch staged in cache"`.
Then (keep it staged) run scenario **A** for the same KB → it installs **from cache**
(the agent log shows `patch found in cache (staged), using it` — no second fetch).

> **Cache-full:** if the cache cap is reached and nothing evictable remains, staging
> fails with a `CacheFullReport` (fail-loud) instead of silently dropping.

### (Optional) Reboot
```cmd
set AEGIS_REBOOT_DELAY=90
mockserver.exe
```
Expected `type=reboot phase=SUCCESS reason="reboot scheduled in 90s"`. Cancel with a
run that has `set AEGIS_CANCEL_REBOOT=1`.

---

## 6. What the agent REPORTS (the wire contract to consume)

Over the mTLS stream (`Connect`), agent → server messages:

| Message | When | Key fields |
|---|---|---|
| `ActionStatus` | every dispatched action, at each phase | `action_id`, `action_type`, `phase`, `exit_code`, `reboot_required`, `reason`, `detail`, `device_id`, `kb` |
| `PatchScanResult` | compliance scan (offline cab) | `build`, `missing[]` — each `MissingUpdate` has `kb_article_ids`, `title`, `classification`, `msrc_severity`, `cve_ids`, `security_bulletin_ids`, `superseded` (bool) + `superseded_update_ids`, `size_mb` (max) + `min_size_mb`, `reboot_required` |
| `DeviceStateReport` | on change | `build`, `installed_kbs[]` (installed-state KBs only) |
| `CacheFullReport` | staging can't fit | `pending_id`, `needed_bytes`, `cap_bytes`, `in_use_bytes` |
| `Heartbeat` / `CommandResult` | liveness / quick commands | — |

The full message/field definitions are in the proto: `aegis_one_proto/grpc/agent.proto`.
Patch categorization (missing vs superseded vs installed, critical/important,
grouping, zero-day) is the **console's** job from this data — the agent reports raw,
tagged facts and does not drop anything.

---

## What the real server must implement

The mock server fakes these; production must build them for real:

1. **mTLS + enrollment PKI.** Per-tenant bootstrap token → validate (license-gated) →
   sign the agent's CSR → return a per-device cert + CA chain. Long-lived certs
   (~398 days) + a `Renew` RPC (over the existing mTLS, no token) before expiry.
2. **Signed commands.** Every command (`install_patch`, `stage_patch`, `reboot`, …) is an
   Ed25519-signed envelope; configure agents with the server's public verify key
   (`AEGIS_CMD_VERIFY_KEY`). The signature covers the serialized payload bytes.
3. **Content delivery (peer-cache).** The agent **never** touches the internet. The
   server must deliver each patch `.msu`/cab to the endpoint over the `FetchContent`
   channel (or place it at the `file_path` named in the command). Implement the
   `FetchContent` streaming RPC the mock server demonstrates.
4. **Catalog + diff.** Maintain the patch catalog (incl. supersedence), push
   `PatchBaseline`/`CabManifest`, and compute missing vs the agent's reported
   installed state.
5. **Action lifecycle + scheduling.** Assign `action_id`s, hold actions until their
   scheduled window (Scheduled/Pending is server-side), dispatch, then **persist the
   `ActionStatus` transitions** per device per action for the console.
6. **Device reconciliation + licensing + token lifecycle** (dedup by device identity,
   seat counting, token rotation/expiry/revocation). See
   [server-team-handoff.md](server-team-handoff.md) and
   [device-identity-and-lifecycle.md](device-identity-and-lifecycle.md).

---

## 8. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `x509: ... ECDSA verification failure ("Aegis One Dev CA")` | Agent `ca.crt` ≠ the CA that signed the server cert. Use one consistent `certs\` set; copy that `ca.crt` to the agent (re-enroll: delete `SKVC\certs\client.*`, keep `identity.json`, re-add token, restart service). |
| `token=<unset — enrollment will reject>` | `set AEGIS_ENROLL_TOKEN=tenant-abc-123` (plain, no extra quotes). |
| `AddScanPackageService ... requires Administrator` | Run elevated — normally fine (the service is LocalSystem). A scan during a reboot logs `system shutdown in progress` (expected). |
| Install shows `NOT_APPLICABLE` but you expected an install | That KB is already installed on the VM (CBS "Installed" state). Use a genuinely-missing KB or a behind snapshot. |
| Agent not connecting | Check `curl http://127.0.0.1:50080/status`; check the agent log under `SKVC\logs\`. Use `127.0.0.1`, not `localhost`, for local dev. |

For deeper setup, caching/cab/baseline/renewal scenarios, and the full env-var
reference, see [mockserver-guide.md](mockserver-guide.md).
