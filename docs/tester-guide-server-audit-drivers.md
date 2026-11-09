# Aegis One — Server-Team Test Guide: Audit Events, Driver + BIOS Inventory

**Audience:** the **server / backend team** building the production control plane that receives agent gRPC messages.
**Goal:** verify your server correctly **receives, persists, and displays** three agent→server contract surfaces that are new (or newly reshaped) and **not yet tested server-side**:

1. **AuditEvent stream** — `command_received`, `command_rejected`, and the **new `command_completed`** outcome event (L2), plus the **Option C** change that **removed** the chain-hash fields.
2. **Driver inventory** — the new `FullInventoryReport.drivers` list for OEM driver patching.
3. **BIOS / system fields** — `FullInventoryReport.hardware` BIOS + manufacturer/model keys for OEM BIOS catalog matching.

> This guide drives the agent with the reference `mockserver.exe` so you can see the **exact bytes on the wire** before wiring your real backend. The mock is not the production server; it prints every message it receives so you can match field-for-field against what your server must store. Where a case cannot be produced with the reference mock, it is marked **CONTRACT** — verify by implementing to the field spec below, not by running the mock.

Shipped in: agent commits `abef85f` (audit L2 + Option C) + `18e6873` (driver inventory), proto `e1d3d6b`.

---

## 1. Setup (shared)

Use the standard bring-up from **`docs/tester-guide-patching.md` §3** (certs, mockserver, agent install, enroll, NSH). You need:

- One Windows VM with the agent enrolled and connected (`/status` → `connected:true`).
- `mockserver.exe` running on your dev machine, console visible.
- For faster inventory cycles, on the VM (**integer SECONDS**, not Go duration):
  ```cmd
  reg add "HKLM\SOFTWARE\AegisOne\Client\Settings" /v AEGIS_FULL_INVENTORY_INTERVAL /t REG_SZ /d 60 /f
  sc stop AegisOneClient & sc start AegisOneClient
  ```

The mock prints each surface in these **exact** formats (verify your parser against these):

```
AUDIT -> <id> | ts=<unix_ms> event=<type> cmd_id=<id> cmd_type=<type> actor=<a> source=<s> sig_ok=<bool> decision=<d> reason="<r>" details="<d>"
Received FULL inventory -> <id> | trigger: <t>, cpu: ... apps: N, OS: ..., system: <vendor> <product>
  BIOS: <bios_vendor> <bios_version> (<bios_release_date>) | drivers: N
  driver <device_name> v<driver_version> <driver_date> <provider_name> signed=<bool> [<device_class>]
```

---

## 2. AuditEvent — wire contract

`AuditEvent` (proto `agent.proto`, message `AuditEvent`) fields your server must persist:

| # | Field | Type | Notes |
|---|---|---|---|
| 1 | `device_id` | string | |
| 2 | `ts` | int64 | **unix MILLIS** (receive-time decision), not seconds, not `issued_at` |
| 3 | `event_type` | string | closed vocab: `command_received`, `command_rejected`, **`command_completed`**, `enrollment_success`, `enrollment_failed`, `cert_renewed`, `audit_degraded` |
| 4 | `command_id` | string | correlation key — **join `command_received` ↔ `command_completed` on this** |
| 5 | `command_type` | string | `run_command` / `install_patch` / … |
| 6 | `actor` | string | from the signed command envelope |
| 7 | `source` | string | from the signed command envelope |
| 8 | `issued_at` | int64 | unix seconds, when the operator issued the command |
| 9 | `signature_ok` | bool | |
| 10 | `decision` | string | `accepted` / `rejected` / **`succeeded`** / **`failed`** / **`skipped`** / `degraded` |
| 11 | `reason` | string | short, no secrets |
| 12,13 | **RESERVED** | — | **were `prev_hash` / `line_hash` — REMOVED (Option C). Your server MUST NOT expect or require chain-hash fields.** |
| 14 | `details` | string | per-type payload summary (see below); empty on rejected events |

> **Option C — critical for the server team.** The local agent audit log is now a best-effort JSONL trace with **no chain hash**. The **server is the sole authoritative audit record** — persist every `AuditEvent` durably with your own integrity controls. Do not build logic that validates `prev_hash`/`line_hash`; they are gone.

---

### SA-1 — `command_received` with payload details

**PURPOSE:** every accepted signed command emits one `command_received` with attribution + a `details` payload summary.

**SETUP / STEPS:** dispatch a signed `install_patch` (see patching guide UC-2 env vars) OR any signed command the mock sends on connect.

**EXPECTED (mock console):**
```
AUDIT -> <id> | ts=<ms> event=command_received cmd_id=<cmd> cmd_type=install_patch actor=server:auto[..] source=mockserver.server sig_ok=true decision=accepted reason="" details="kb=KB5054980"
```

`details` is per command type:
| command type | `details` format |
|---|---|
| `run_command` | `shell=<cmd\|powershell\|..>, command=<text, ≤400B>` |
| `install_patch`/`uninstall_patch`/`stage_patch` | `kb=KB#####` |
| `software_deploy*` | `name=.., version=.., action=..` **or** `<sensitive — secure parameters in use>` when the spec carries secure parameters/password |
| `collect_file` | `paths=N, first=<path>` |
| `file_operation` | `op=.., src=.., dst=..` |
| `reboot` | `delay=Ns` |

**PASS:** event stored with `decision=accepted`, non-empty `details` matching the command type, and `actor`/`source`/`issued_at` all populated. **Secret guard:** a `software_deploy` with secure parameters shows exactly `details="<sensitive — secure parameters in use>"` — your server must store/display that literal and must NOT receive the real secret.

---

### SA-2 — `command_completed` (NEW L2) paired to the command

**PURPOSE:** every signed command that reaches a **terminal** `ActionStatus` emits a `command_completed` sharing the same `command_id`.

**SETUP / STEPS:** dispatch a signed `install_patch` and let it run to a terminal phase (patching guide UC-2 = SUCCESS, UC-5 re-install = NOT_APPLICABLE, UC-14A wrong-hash = FAILED).

**EXPECTED (mock console) — success:**
```
AUDIT -> <id> | ts=<ms> event=command_completed cmd_id=<cmd> cmd_type=install_patch ... decision=succeeded reason="" details="exit=0, duration_ms=12000"
```
Decision mapping (terminal `ActionStatus.phase` → audit `decision`):

| ActionStatus phase | `decision` |
|---|---|
| `ACTION_PHASE_SUCCESS` | `succeeded` |
| `ACTION_PHASE_FAILED` | `failed` |
| `ACTION_PHASE_NOT_APPLICABLE` | `skipped` |

`details` = `exit=<N>, duration_ms=<N>` (+ `, reason=<≤200B>` when the status had a reason). Intermediate phases (QUEUED/DOWNLOADING/VERIFYING/RUNNING) emit **no** audit event.

**PASS:**
- Exactly one `command_completed` per completed command (no events for intermediate phases).
- `command_id` **equals** the earlier `command_received.command_id` → your server joins the two into one dispatch→outcome record.
- `decision` matches the terminal phase per the table.
- `details` carries `exit=` and `duration_ms=`.

---

### SA-3 — `command_rejected` carries NO details — **CONTRACT**

**PURPOSE:** a command failing signature/validation emits `command_rejected` with **empty** `details` (the agent refuses to parse a hostile payload).

**EXPECTED:**
```
AUDIT -> <id> | ts=<ms> event=command_rejected cmd_id=<cmd> cmd_type=run_command ... sig_ok=false decision=rejected reason="signature verification failed" details=""
```

The reference mock signs correctly, so it will not normally produce this. **CONTRACT:** your server must accept `command_rejected` with `details=""` and `signature_ok=false`, and must NOT require a `details` value. (To exercise live, use a harness that sends a tampered-signature command.)

**PASS:** server stores the rejected event with empty `details`; no `command_completed` ever follows a rejected command.

---

### SA-4 — No chain-hash fields — **CONTRACT**

**PURPOSE:** confirm your deserializer tolerates the removed fields 12/13.

**PASS:** server parses every `AuditEvent` with proto `e1d3d6b` (fields 12,13 reserved). No code path reads `prev_hash`/`line_hash`. Audit durability + tamper-resistance is a **server-side** responsibility now.

---

## 3. Driver inventory — wire contract

`FullInventoryReport.drivers` (field **6**), each entry `DriverInventory`:

| # | Field | Example | Server use |
|---|---|---|---|
| 1 | `device_name` | `NVIDIA GeForce RTX 3060` | display |
| 2 | `device_class` | `Display` / `Net` / `System` | grouping |
| 3 | `driver_version` | `31.0.15.3623` | **compare key** vs catalog |
| 4 | `driver_date` | `2023-05-10` (YYYY-MM-DD; empty = unknown) | secondary compare |
| 5 | `provider_name` | `NVIDIA` | **catalog match anchor** |
| 6 | `hardware_id` | `PCI\VEN_10DE&DEV_2504...` | **catalog match anchor** |
| 7 | `inf_name` | `oem123.inf` | reference |
| 8 | `manufacturer` | device manufacturer | reference |
| 9 | `is_signed` | `true` | trust signal |

> **No agent-side vendor filtering.** The agent reports every signed PnP driver with a usable version (Microsoft inbox + OEM). Your **catalog** is the policy point for which drivers are patchable/outdated — same model as `installed_apps`. Windows Update already covers MS-delivered drivers via the cab scan; this list is the **OEM coverage WU does not provide** (the value-add).

---

### SA-5 — Driver inventory arrives in the Tier-3 snapshot

**PURPOSE:** the full inventory carries a non-empty, well-formed driver list.

**STEPS:** restart the agent (startup forces a snapshot) OR wait one `AEGIS_FULL_INVENTORY_INTERVAL`.

**EXPECTED (mock console):**
```
Received FULL inventory -> <id> | trigger: startup, cpu: ... apps: 15, OS: "Windows 11 Pro" "23H2" build="22631.4890", virt: vmware, system: VMware, Inc. VMware20,1
  BIOS: VMware, Inc. <ver> (<date>) | drivers: 42
  driver VMware VMCI Host Device         v9.8.30.0        2025-03-17   Broadcom Inc.  signed=true [SYSTEM]
  driver ...
  ... and 27 more driver(s)
```

**PASS (server contract):**
- `FullInventoryReport.drivers` deserializes with all 9 fields per entry.
- `driver_version` is non-empty for every entry (the agent drops rows with no version/anchor — a row with empty version is a server-side parse bug, not expected on the wire).
- `driver_date`, when present, is `YYYY-MM-DD`; empty is valid (unknown).
- Order is stable across repeated snapshots from an unchanged device (the agent sorts by `hardware_id, device_name, driver_version`) — your diff logic should not treat reordering as change.
- Your server **persists the list per device** and can **match `hardware_id` + `provider_name` + `driver_version`/`driver_date`** against the OEM driver catalog to compute outdated drivers.

> **Note on VMs:** a VMware/Hyper-V guest reports mostly VMware/Broadcom + Microsoft inbox drivers (few OEM). A physical Dell/HP/Lenovo yields a richer OEM set — use one to validate catalog matching end-to-end.

---

## 4. BIOS / system fields — wire contract

`FullInventoryReport.hardware` (`HardwareInventory`) already carries these; likely **untested on your side**. These are the OEM BIOS catalog keys:

| # | Field | Example | Server use |
|---|---|---|---|
| 8 | `bios_vendor` | `Dell Inc.` | catalog vendor |
| 9 | `bios_version` | `1.21.0` | **compare key** vs catalog |
| 10 | `bios_release_date` | `03/15/2024` | secondary |
| 11 | `system_vendor` | `Dell Inc.` | **catalog manufacturer key** |
| 12 | `system_product` | `Latitude 7440` | **catalog model key** |
| 13 | `system_serial_number` | `<serial>` | asset id; MAY be PII — your catalog decides persist/redact |

---

### SA-6 — BIOS + model fields arrive and support catalog matching

**STEPS:** same snapshot as SA-5.

**EXPECTED (mock console):**
```
  BIOS: Dell Inc. 1.21.0 (03/15/2024) | drivers: N
Received FULL inventory -> ... system: Dell Inc. Latitude 7440
```

**PASS (server contract):**
- `bios_vendor`, `bios_version`, `system_vendor`, `system_product` are populated on real OEM hardware (may be `VMware, Inc.` / empty-ish on a VM — not a failure there).
- Your server can key an **OEM BIOS catalog lookup** on `(system_vendor, system_product)` → required `bios_version`, and flag the device when `bios_version` is behind.
- `system_serial_number` handling honors your PII policy (persist or redact per catalog).

> **Deployment reuse (context):** when the catalog decides a driver or BIOS is outdated, remediation reuses the existing `software_deploy` path — the server delivers the vendor package (Dell DUP `.exe` / HP / Lenovo) as a local file and the agent runs it silently. No new deploy contract is needed for OEM driver/BIOS packages.

---

## 5. What the server team must implement (summary)

1. **Persist every `AuditEvent` durably**, server-authoritative, with your own integrity controls (the local agent chain was removed — Option C). Index by `device_id` + `ts`.
2. **Join `command_received` ↔ `command_completed`** on `command_id` to show one dispatch→outcome record per action (decision + exit + duration).
3. **Tolerate reserved fields 12/13**; never require chain-hash or a `details` value on rejected events.
4. **Store `FullInventoryReport.drivers`** per device; match against the OEM **driver** catalog (`hardware_id` + `provider_name` + version/date). No agent-side filtering — your catalog is the policy point.
5. **Store BIOS/model fields**; match against the OEM **BIOS** catalog on `(system_vendor, system_product)` → required `bios_version`.
6. **Drive remediation via the existing `software_deploy`** pipeline for OEM driver/BIOS packages.

---

## 5A. Remote audit query (L3) — `audit_query`

**VM-PASS 2026-11-09.** A new signed command type `audit_query` lets the console
pull a device's LOCAL audit trace remotely (BigFix-style endpoint forensics
without a server portal). The agent reads ONLY its own audit dir
(`<SecureDir>\logs\audit.log` + rotated `audit-*.log`) — there is deliberately no
path field, so this can never become an arbitrary-file read. It returns matching
events (metadata only, no secrets) newest-first, size-capped, in
`ActionStatus.Detail` (a JSON event array). The payload is JSON in `cmd.Payload`
(no proto change): `{version:1, mode:"tail|search|since", limit, pattern,
since_ms, until_ms, include_rotated, event_types[], decisions[]}`.

**Two ways to drive it.**

### 5A.1 Standalone env trigger (mockserver `server` mode)

```cmd
set "AEGIS_TARGET_DEVICE=<DEVICE_UUID>"
set "AEGIS_AUDIT_QUERY=1"
set "AEGIS_AUDIT_MODE=tail"       rem tail | search | since
set "AEGIS_AUDIT_LIMIT=10"
set "AEGIS_AUDIT_PATTERN="        rem substring (used by search, optional elsewhere)
set "AEGIS_AUDIT_SINCE_MS="       rem unix-millis lower bound (mode=since)
set "AEGIS_AUDIT_UNTIL_MS="       rem unix-millis upper bound (optional)
set "AEGIS_AUDIT_ROTATED="        rem set to 1 to also scan rotated files
mockserver.exe server
```

Expected: `Sent command -> id: audit-001, type: audit_query, mode=... (signed)`,
then a terminal `ACTION_PHASE_SUCCESS` with
`reason: audit_query: N event(s) returned (scanned M)` followed by N
pretty-printed rows: `<ts>  <event_type>  <decision>  <command_id>  <actor>  <reason>`.

- **tail** — the N most recent events.
- **search** (`AEGIS_AUDIT_MODE=search` + `AEGIS_AUDIT_PATTERN=command_received`)
  — only events whose text matches (case-insensitive).
- **since** (`AEGIS_AUDIT_MODE=since` + `AEGIS_AUDIT_SINCE_MS=<ms>`) — events
  at/after the timestamp. 0 matches is correct when the device has no events
  after the cutoff.
- **rotated** (`AEGIS_AUDIT_ROTATED=1`) — `scanned M` grows to include rotated files.

### 5A.2 NSH verb (interactive console)

Target a host with `cd \\<host>` (NSH `use` selects the SHELL type, not the
target), then:

```
audit tail 10
audit search deploy
audit since 1794200000000 rotated
```

Each prints the matched events for the selected host(s). `audit tail 10 > file`
redirection works too (reuses the NSH `>` support).

### 5A.3 Server-team contract

The agent already streams every `AuditEvent` to the server (L1/L2) — the
authoritative record. `audit_query` is an on-demand ENDPOINT read for field
forensics and to see pre-enrollment events the server never received. Your
production server issues an `audit_query` signed command (same envelope as
`file_operation`) and renders `ActionStatus.Detail`. Bounds are enforced
agent-side (limit ≤ 1000, size ≤ 2 MiB, audit-dir only), so no server-side
guard is required for safety.

---

## 5B. Audit flood control + enrollment audit (L5)

**Flood control.** Only the high-volume benign `command_received` event is
throttled — a token bucket (`AEGIS_AUDIT_RATE`, default 50/s; `AEGIS_AUDIT_BURST`,
default 100) plus duplicate-coalescing within `AEGIS_AUDIT_DEDUP_WINDOW`
(default 2 s, handles reconnect re-delivery storms). **Security + outcome events
are NEVER suppressed** — `command_rejected`, `command_completed`,
`audit_degraded`, `enrollment*`, `cert_renew*` always pass, so a flood cannot
drown out a rejected attempt and the server's completion-driven logic is never
starved. Suppression is itself audited: an `audit_rate_limited` event records
`duplicates=N, over_rate=N, window_ms=N`. Fail-open: if the guard is absent,
every event passes.

Verify with the L3 query (§5A): lower the cap via `config.json`
(`AEGIS_AUDIT_RATE=1`, restart the service), drive a burst of commands, then
`audit search audit_rate_limited` — the summary appears, while
`audit search command_rejected` still shows every rejected event. Revert the
config afterward.

**Enrollment audit.** `enrollment` (requested/succeeded/failed) and `cert_renew`
(requested/succeeded/failed) events are now written to the LOCAL trace — these
occur before the server connection exists, so they are the agent-side forensic
trail the server (which has the Enroll RPC result) never streamed. To see them:
on a test VM, remove the client cert and let the agent re-enroll, then
`audit search enrollment rotated` (§5A) lists the enrollment lifecycle. These
events are exempt from flood control and are never suppressed.

Server-team note: both are agent-local behaviors; no new server contract. The
`audit_rate_limited` and `enrollment`/`cert_renew` event types simply appear on
the existing AuditEvent stream (flood summaries) or in the local trace
(enrollment, pre-connection).

---

## 6. Record sheet

| Case | Surface | Env / trigger | PASS/FAIL | Notes |
|---|---|---|---|---|
| SA-1 | command_received + details | signed install_patch | | |
| SA-2 | command_completed pair | install → SUCCESS / NOT_APPLICABLE / FAILED | | |
| SA-3 | command_rejected empty details | CONTRACT (tampered-sig harness) | | |
| SA-4 | no chain fields | CONTRACT (proto e1d3d6b parse) | | |
| SA-5 | driver inventory | Tier-3 snapshot | | |
| SA-6 | BIOS + model fields | Tier-3 snapshot | | |
| SA-7 | audit_query tail/search/since | AEGIS_AUDIT_QUERY / NSH `audit` | VM-PASS 2026-11-09 | L3 remote audit query |
| SA-8 | audit flood control (coalesce + rate cap + exempt) | AEGIS_AUDIT_RATE low + burst | | L5 — security events never dropped |
| SA-9 | enrollment / cert_renew audit | re-enroll on test VM | | L5 — local pre-connection trace |
