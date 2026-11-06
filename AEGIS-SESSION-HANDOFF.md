# Aegis One Agent — Session Handoff (2026-11-06)

**Written for:** a Claude Code web session continuing this work (and for Vijay).
Paste this into the web thread so it picks up current state.

## Project context
- **Aegis One** = UEM product (BigFix/Tanium/ManageEngine competitor). Windows agent in **Go**.
- Agent binary is **always** `AegisOneClient.exe`. Repos: agent `aegis_one_agent`, shared proto `aegis_one_proto` (sibling dir), GitHub org `aegisoneuem`.
- `dist/` = server-team handover binaries (tracked); `build/` = local dev (gitignored).
- Hard rules: no PowerShell for the agent's own logic (running a PS *payload* is fine; COM/WMI is allowed), fail-loud, security-first, unit+integration+break tests per feature, explain before coding, push to GitHub only when a whole block is done.

## What this session accomplished (all pushed)

### 1. Audit logging (Layer 1 + Layer 2 + "Option C" refactor)
- `command_received` with full attribution (actor/source/issued_at) + per-command-type payload `details` summary + secret guard.
- **L2** `command_completed` emitted on terminal ActionStatus (SUCCESS→succeeded / FAILED→failed / NOT_APPLICABLE→skipped) with `exit=, duration_ms=, reason=`. Pairs with `command_received` by `command_id`.
- **Option C**: dropped client-side chain-hash + startup quarantine (it was tamper-detection theatre — the SERVER is the authoritative audit surface). Local `audit.log` is now a plain JSONL forensic trace; `auditcheck.exe` pretty-prints it. Proto `AuditEvent` reserved fields 12,13 (was prev_hash/line_hash).

### 2. Driver + BIOS inventory (OEM patching foundation)
- `patch.CollectDrivers()` via `Win32_PnPSignedDriver` (WMI COM) → `FullInventoryReport.drivers` (device/version/date/provider/hardware_id/class/signed). Live-verified (OEM drivers: NVIDIA/Dolby/CrowdStrike/VMware captured).
- BIOS/model/manufacturer already flow via `hwinventory` → `FullInventoryReport.hardware.bios_*`.
- **Scoping decision:** agent-side driver/BIOS = inventory + generic vendor-package deploy (reuses `software_deploy`). OEM deep-automation (Dell/HP/Lenovo catalog) is **server-team**. Agent reports facts; server catalog is the policy point.

### 3. Cab-scaling fix — the big architectural decision
Problem: shipping ~700MB–1GB `wsusscn2.cab` to 10k–50k endpoints every patch cycle doesn't scale (no relay/DP; roaming clients; cab can't be compressed/chunked — signature breaks).
**Decision: cab NEVER goes to endpoints. Server-side diff model** (`docs/server-spec-patch-detection.md`):
- Endpoint sends lean state (build+UBR, arch, installed-KB set) — registry only, no cab, no internet.
- **Server** computes "missing" = applicable(catalog, build+arch) − installed, using its own server-side cab catalog. Build number is authoritative for the cumulative level (O(1) compare — not slow). Supersession resolved once, server-side.
- Long tail self-corrected by the install **NOT_APPLICABLE** feedback (the L2 `command_completed=skipped` the server consumes) — no endpoint cab, no BigFix-style relevance-content pipeline.
- Roaming clients: same lean report over mTLS; never Microsoft-direct.
- Agent did **#1** (send `installed_kbs`) + **#3** (demote Layer 4 cab scan to optional/off-by-default). **#2** (catalog-from-cab + diff + supersession) and **#4** (consume skipped feedback) are **server-team**.

### 4. installed-KB completeness (appwiz.cpl parity)
- Bug: CBS `extractKB` only caught `Package_for_KB<n>` standalone and MISSED cumulative updates (named `Package_for_RollupFix~<build>`). Real box showed only 2 KBs.
- Fix: `CollectInstalledKBs()` = **UNION of WUA `IsInstalled=1` (Online=false, local, no cab) ∪ Win32_QuickFixEngineering ∪ CBS standalone** → canonical `KB<digits>`, deduped/sorted, server-readable. Live: 2 → 10. TTL-cached (1h) + force-refreshed on `report_device_state` and on CBS servicing change; persisted to `installedStore`; sent in `DeviceStateReport.installed_kbs`.

### 5. Deferred driver/BIOS deploy add-ons — CLOSED
- **pnputil INF install**: `software_deploy installer_type:"inf"` (+ `.inf` auto-detect) runs `pnputil /add-driver <inf> /install` (native tool). For OEM drivers with no vendor .exe.
- **BIOS flash AC-power gate**: `spec.require_ac_power` (field 25). Run() refuses on battery before touching the installer; `SystemOnACPower()` = GetSystemPowerStatus syscall (unknown power = not-battery so desktops aren't blocked). Deeper flash safety delegated to the vendor flash utility (industry standard). **Actual BIOS-flash E2E needs real OEM hardware.**

### 6. Patch install — verified against a real BigFix Fixlet
A real Fixlet (KB5126655 cumulative, Win Server 2022) confirmed our detection model (its applicability = `UBR < target`, which maps to our build+UBR report + server catalog; `not pending restart` maps to our `reboot_pending`). Its install uses `wusa.exe /quiet /norestart` guarded by a wuauserv check.
- Our install is **DISM-primary** (DISM uses TrustedInstaller, not wuauserv) with a **wusa fallback** for .msu DISM can't service. Added: the wusa fallback now guards with `ensureWUServiceForWUSA()` — if wuauserv is DISABLED it is temporarily enabled+started, then restored (one better than BigFix, which just refuses). already-installed → NOT_APPLICABLE; reboot (3010) handled; transient retry.

### 7. Log hygiene
- Per-cycle noise ("driver inventory collected", Tier-3 "unchanged → skipping send") demoted to DEBUG.
- Missing optional cab → quiet DEBUG skip (was ERROR "UNKNOWN not compliant" + FAILED status every run). In the new model `AEGIS_WSUS_SCAN_CAB` should be **unset** on endpoints.

## Current state — Windows patching
**Agent-side Windows patching is essentially DONE.** Detection (server-side-diff Layer 1 + demoted Layer 4), remediation (DISM+wusa local-file install / uninstall / stage), reboot tracking, driver+BIOS inventory, installed-KB parity, deferred deploy add-ons — all built + tested (`go test ./...` + `go vet` clean).

**Remaining (NOT agent):**
- Server-team: catalog-from-cab + per-device diff + supersession (#2); consume `command_completed=skipped` (#4). Spec: `docs/server-spec-patch-detection.md`.
- VM/real-HW validation: cumulative LCU install via DISM+wusa path; BIOS flash needs real OEM hardware.

Server-team test guides written: `docs/server-spec-patch-detection.md`, `docs/tester-guide-server-audit-drivers.md`.

## What's next
Windows patching is being treated as closed pending VM/server validation. Next topic is open (Vijay to choose) — candidates from backlog: MSIX/Store inventory, pre-GA closure (pen/VAPT/DSAT), NSH audit verbs, or a new ask.

## Commit references
- Proto (`aegis_one_proto`): `88b3709` (require_ac_power), `2240a16` (installed_kbs), `e1d3d6b` (DriverInventory), earlier Option C audit changes.
- Agent (`aegis_one_agent`): `2eb373b` (wusa/wuauserv), `5970b6d` (cab quiet skip), `370f887` (pnputil INF + AC gate), `134a3db` (installed-KB parity + log hygiene), `92fbd69` (Layer1/4), `643b6ee` (server docs), `18e6873` (driver inventory), `abef85f` (audit L2 + Option C).

## Notes
- Persistent memory lives at `C:\Users\vijay_c\.claude\projects\c--Aegis-One-aegis-one-agent\memory\` (MEMORY.md index + topic files) — decisions above are recorded there.
- BigFix is installed on the dev box (`C:\Program Files (x86)\BigFix Enterprise\BES Client`) and was used to compare folder/registry structure + read a real patch Fixlet. We are intentionally NOT content-site/relevance-driven (we chose server-side diff).
