# Aegis One Agent — Verified Test Use Cases (Server Team Replay Guide)

**Audience:** Aegis One server team.
**Purpose:** every scenario below has been tested end-to-end on real VMs
(Windows 11 + Windows Server 2022) with the mockserver. Replay them in the same
order to validate your real-server implementation against the same contract.
Setup (certs, enrollment token, run) is in [mockserver-guide.md](mockserver-guide.md);
patch-specific detail is in [patching-test-guide.md](patching-test-guide.md).

All commands below are **mockserver environment variables** set before starting
`mockserver.exe`. The mock signs every command (Ed25519) exactly like the real
server must. Prebuilt binaries are in `dist/` (agent `AegisOneClient.exe` /
`AegisOneClient.msi`, `mockserver.exe`, `certgen.exe`).

> Multi-device: run two or more agents against one mockserver. By default the
> mock sends the triggered commands to **every** device that connects; set
> `AEGIS_TARGET_DEVICE=<device_id>` to target one (others receive nothing).

---

## 1. Enrollment + identity (stable device_id / computer_id)

**Steps:** install the agent (`AegisOneClient.msi`) with the enroll token; it
enrolls over the no-client-cert path, gets a CA-signed cert, reconnects mTLS.

**Verified:** `device_id` (UUID, agent-generated, stored in HKLM registry) and
`computer_id` (random 10-digit console id, server-assigned) stay **stable**
across agent uninstall/reinstall and service restarts. Mock prints
`device=<uuid> computer_id=<10 digits> host=<hostname>` on every action status.

## 2. Compliance scan (on-demand + cab-triggered)

```
set AEGIS_SCAN_NOW=1
```
**Verified:** agent runs the WUA scan, streams `PatchScanResult`; a new cab
version (`AEGIS_CAB_VERSION`) also triggers a scan. Baseline self-diff:
`AEGIS_BASELINE_BUILD` + `AEGIS_BASELINE_KBS` (comma-separated).

## 3. Patch install (single + multi)

```
set AEGIS_INSTALL_FILE=C:\patches\windows11-kb5044033.msu
set AEGIS_INSTALL_KB=KB5044033
set AEGIS_INSTALL_HASH=<sha256>
```
**Verified:** phased `ActionStatus` (QUEUED → DOWNLOADING → VERIFYING → RUNNING
→ SUCCESS/FAILED/NOT_APPLICABLE) with exit codes; hash verify before run;
multiple files via `;` lists; serial install worker (one OS install at a time).
Staging ahead of install: `AEGIS_STAGE_KB/_FILE/_HASH` (encrypted cache,
StateStaged).

## 4. Patch uninstall (registry-resolved DISM → wusa → QFE honesty)

```
set AEGIS_UNINSTALL_KB=KB5044033
```
**Verified chain:** agent resolves the CBS package name from the registry
(`...\Component Based Servicing\Packages` Owners), tries
`DISM /remove-package`, falls back to `wusa /uninstall /kb:<n>`, then does an
honest QFE re-check (Win32_QuickFixEngineering) and reports the truth.

**Known limitations (by design, reported honestly — do NOT treat as agent bugs):**
- CBS marks the current LCU **permanent** (error `0x800f0825`) — Windows refuses
  removal; the agent honors the refusal (same behavior as manual DISM).
- `wusa /quiet` returns error 87 in the session-0 LocalSystem service context —
  agent reports "Windows does not allow uninstalling it in service context".
  (BigFix patch rollback fails the same way most of the time.)

## 5. Reboot control

```
set AEGIS_REBOOT_DELAY=60      (seconds)
set AEGIS_CANCEL_REBOOT=1      (cancels a pending one)
```
**Verified:** scheduled reboot with delay, cancel, `ActionStatus` both ways.

## 6. Software deploy — install (msi / exe / scripts / command)

One task type `software_deploy`; `action` chooses install/uninstall.

```
set AEGIS_DEPLOY_FILE=C:\pkg\7z2404-x64.exe        (pre-staged local path)  — or —
set AEGIS_DEPLOY_CONTENT_ID=pkg-7zip-2404          (server-delivered via FetchContent)
set AEGIS_DEPLOY_NAME=7-Zip
set AEGIS_DEPLOY_VERSION=24.04
set AEGIS_DEPLOY_TYPE=exe                          (msi|exe|ps1|bat|cmd|py|vbs|command|auto)
set AEGIS_DEPLOY_ARGS=/S                           (admin-authored silent args — agent never guesses)
set AEGIS_DEPLOY_HASH=<sha256>
set AEGIS_DEPLOY_PRODUCT_CODE={GUID}               (MSI idempotency / fileless uninstall)
```
**Verified:** MSI (`msiexec /i /qn`), EXE with custom args (e.g. Sublime
`/VERYSILENT /NORESTART /TASKS=contextentry`), `.ps1` / `.bat` / `.cmd` /
`.py` / `.vbs` payloads, and `type=command` (raw command line, no file
delivery). Hash verified before run; optional Authenticode check
(`AEGIS_DEPLOY_REQUIRE_SIG=1`).

- **Idempotency + version-aware upgrade:** MSI → `MsiQueryProductState` by
  ProductCode; non-MSI → registry DisplayName/DisplayVersion. If installed
  version < target, the agent proceeds **as an upgrade** (verified 7-Zip
  23.01 → 26.04); same/higher → skipped with reason.
- **Post-action verification ("Fixed"):** after install the agent re-detects
  (retry 4×5s for slow NSIS finishes) and only then reports
  `verified installed (version X)`.

## 7. Software deploy — uninstall (fileless)

```
set AEGIS_DEPLOY_ACTION=uninstall
set AEGIS_DEPLOY_PRODUCT_CODE={GUID}            (MSI: msiexec /x {GUID} /qn)   — or —
set AEGIS_DEPLOY_UNINSTALL_KEY=7-Zip            (non-MSI: HKLM Uninstall subkey) — or —
set AEGIS_DEPLOY_DISPLAY_NAME=7-Zip             (non-MSI: DisplayName substring)
```
**Verified (all 4 combos: MSI/EXE × install/uninstall):** no payload file
needed; non-MSI resolves `QuietUninstallString` (preferred) or
`UninstallString` from HKLM native + WOW6432Node; post-action re-detect
confirms removal.

## 8. Parameters (BigFix-style `${name}` substitution)

```
set AEGIS_DEPLOY_PARAMS=LICENSE=ABC-123,INSTALLDIR=C:\app
```
**Verified:** `${name}` tokens in `silent_args` are substituted from author
params + built-in runtime params (e.g. `${hostname}`, resolved **on each
endpoint**, proven on two VMs from one signed command — fleet pattern). GUID
`{...}` strings are collision-safe. All params are also exported to child
scripts as `AEGIS_PARAM_<NAME>` env vars, so loops/scripts can use them freely.

## 9. On-demand file collection (collect_file → UploadContent)

```
set AEGIS_COLLECT_PATHS=C:\Program Files\SKVC\logs\*.log;C:\temp\report.txt
set AEGIS_COLLECT_TAIL=1048576          (optional: last N bytes of each file)
set AEGIS_COLLECT_MAX_EACH=52428800     (optional per-file cap override)
set AEGIS_COLLECT_MAX_TOTAL=524288000   (optional per-task cap override)
```
**Verified (two endpoints simultaneously):** glob expansion, SHA-256
end-to-end integrity (server recomputes + acks), chunked 1 MiB streaming over
the existing mTLS channel, per-file status in `ActionStatus.detail`. Missing
paths are reported as `no match` — not an error for the files that do exist.

- **Pull-only model:** the agent never auto-uploads; it only answers a signed
  `collect_file` command (BigFix/BladeLogic style), so idle agents add no load.
- **Storage (mock):** `uploads/<computer_id>/<action_id>/<filename>`. Keyed by
  **computer_id** because hostnames can collide across a fleet; resolve
  computer_id → hostname from console inventory.
- **Size caps (defaults, overridable per command via `CollectFileSpec`):**
  50 MiB per file, 500 MiB per task, `tail_bytes` to fetch only the end of big
  logs. Caps keep the newest bytes (log tail), not the oldest.
- **Real server:** implement the `UploadContent` streaming RPC exactly like the
  mock (first chunk = header `action_id`/`name`/`total_size`, last chunk =
  `sha256_full`, respond `UploadAck`); store in your artifact store keyed by
  device, expose it to the console per computer. No extra agent-side API is
  needed — this RPC **is** the channel.

## 10. License gate

```
set AEGIS_LICENSE_MAX=1
```
**Verified:** enrollment beyond the licensed count is refused.

---

## Backlog (known, deliberately deferred)

- **Bandwidth throttling** for uploads/downloads (collect_file + FetchContent)
  — not implemented yet; caps bound *size*, not *rate*.
- Per-user (HKCU) software uninstall.
- Patch uninstall in service context (wusa error 87) — research item.
- GUI repair/modify options on the agent MSI.
