# Server-Team Spec — Scalable Patch Detection (server-side diff model)

**Audience:** Aegis One server/console team.
**Status:** design agreed 2026-11-05 (agent track). This spec defines the agent↔server contract and the server-side responsibilities. No agent code depends on the server implementing this immediately — the agent can be tested against `cmd/mockserver` — but the production detection model requires the server side described here.

---

## 1. The problem this solves

The current implementation makes the ENDPOINT run the applicability scan against a local `wsusscn2.cab`. At fleet scale (10k–50k+ endpoints) that means shipping a ~700 MB–1 GB cab to every endpoint on every patch cycle (monthly, plus zero-day/emergency). The cab cannot be compressed, chunked, or diffed (its signature breaks), and there is no relay/DP tier. Result: hours to reach fleet-wide relevance, and a bandwidth/latency problem for corp and roaming clients alike.

**Fix:** the cab never goes to endpoints. It lives **server-side only** (one copy). Each endpoint sends a small inventory of what it can read locally without a cab; the **server** computes "missing" by diffing that against a catalog it builds from the cab.

This is NOT the BigFix relevance-expression model (no per-patch rule authoring / content pipeline). It is a direct server-side catalog diff: as soon as the cab/MSRC data is on the server, detection works — no content-team lag.

---

## 2. Division of responsibility

| Concern | Where | Why |
|---|---|---|
| OS build + UBR | **Endpoint** (registry) | Authoritative cumulative level; cheap, no cab |
| Installed update set (KBs) | **Endpoint** (CBS registry + QFE) | Readable locally, no cab |
| Installed apps / driver / BIOS inventory | **Endpoint** | Already shipped (DeviceStateReport / FullInventoryReport) |
| **Missing** computation | **Server** | Needs the catalog (cab) — endpoint must NOT need the cab |
| Supersession resolution | **Server** | Resolve once centrally, not per endpoint |
| Catalog ingestion from `wsusscn2.cab` | **Server** | One copy server-side; never distributed to endpoints |
| Remediation file delivery | **Server → peer-cache** | Only the missing patch's file, only to affected endpoints |

**Hard rule:** the endpoint never computes "missing" and never needs the cab for detection.

---

## 3. What the agent sends (the contract)

The agent already emits `DeviceStateReport` (lean, send-on-change) and `FullInventoryReport` (Tier-3, 6–12 h). For server-side diff, the lean report is enriched to carry the **installed update set** so the server has both halves of the diff input:

Agent-side report fields relevant to detection:
- `device_id`
- `product_name`, `display_version`, `build` (e.g. `"22631.4890"` — CurrentBuildNumber + UBR), `architecture` (`amd64`/`arm64`/`x86`)
- `installed_kbs[]` — the set of installed KB numbers (from CBS `CurrentState==112`, supplemented by QFE). **This field is being re-added to the lean report** (it was previously dropped as "coarse"; the server-side model needs it). The server must accept it as the device's installed set.
- `collected_at`
- (pending-reboot + installed-apps + drivers continue as today)

All of the above are read from the **registry only** on the endpoint — no cab, no WUA, no network. Cadence: send-on-change with a safety-net interval (already implemented).

> Field numbers / proto message shape for the re-added `installed_kbs` will be coordinated with the server team before the agent change lands (the proto currently reserves the old field). Nothing else in `DeviceStateReport` changes.

---

## 4. What the server must build

### 4.1 Catalog ingestion (one copy of the cab)
- Download `wsusscn2.cab` server-side (monthly + on zero-day). Never ship it to endpoints.
- Extract a catalog: per update → `UpdateID`, `KBArticleIDs`, title, classification (cumulative / security / .NET / driver / …), target `build`, product/arch applicability, supersession chain, severity, CVE IDs.
- Keep the catalog versioned so a device diffed yesterday vs today is explainable.

### 4.2 Per-device diff
For each device, from its reported `(product, build, arch, installed_kbs)`:
- **Cumulative / LCU / SSU:** the OS **build number is authoritative**. If the device build < the latest applicable cumulative for its product+arch, that cumulative is missing. This is an O(1) compare — covers the ~90% bulk.
- **Standalone / .NET / security:** `missing = applicable(catalog, product+arch) − installed_kbs`. A set difference over an indexed catalog.
- Emit, per device, the **missing list** (rich: KB, title, classification, severity, CVE, size, reboot-required) and, if useful, the enriched **installed list**.

**Performance:** this is an indexed set/compare, not a per-device applicability engine. 50k devices × (build compare + KB set-diff) is a bulk DB operation — seconds-to-low-minutes, parallelizable. It is only expensive if the server tries to replicate full WUA applicability per device per update; do NOT do that (see 4.4). Re-diff is triggered by (a) a device report changing, or (b) a catalog update (new patch) → re-diff affected devices.

### 4.3 Supersession (resolve once, centrally)
- The catalog carries supersession chains. The server resolves them **once** and offers only the **latest non-superseded** relevant update per line. Endpoints never see supersession logic. (Today the endpoint cab scan flags superseded per-device — redundant ×fleet; centralizing removes that.)

### 4.4 Self-correcting feedback (handles the long tail without an endpoint cab)
A pure build+KB diff is exact for cumulative updates but approximate for the long tail (updates whose applicability depends on device state beyond build+KB — feature flags, file versions). Do NOT solve this by shipping the cab to endpoints. Instead:
- The server may diff **optimistically** (bias toward "likely missing") for the tail.
- It dispatches the install (`install_patch`) for the likely-missing update.
- The endpoint installs the **local file**; if the update is not actually applicable, Windows (DISM/WUA) reports **NOT_APPLICABLE** — a harmless no-op.
- The agent already emits `command_completed` with `decision=skipped` for NOT_APPLICABLE (and `succeeded`/`failed` otherwise — see the audit/ActionStatus contract). **The server consumes this to correct its applicability view** for that device+update, so it won't re-offer it.

This closes the loop: approximate-but-complete diff, self-corrected by the real install result. False positives = a no-op; false negatives for cumulative don't occur (build is authoritative).

### 4.5 Remediation delivery
- Only the **missing** patch's file is delivered, and only to the **affected** devices, via peer-cache / `FetchContent`. Never bulk-copy all patch files to all devices. (This is already the `install_patch` / `stage_patch` design and is independent of the detection/cab problem — do not conflate the two.)

---

## 5. Roaming clients

Roaming (off-corp) clients use the **same** path: they send the same lean report to the server over mTLS (internet → server), and the server diffs. **No cab download. No direct Windows Update / Microsoft-site cross-check** — that would break the no-internet discipline and hand applicability decisions to Microsoft. MS-direct is at most a last-resort fallback if the server is entirely unreachable, and is not part of this design.

---

## 6. What the endpoint keeps doing

- Collect build + installed KBs + inventory from the **registry** (no cab, no WUA, no network).
- Execute a dispatched `install_patch` against the server-delivered **local file**, and report the terminal `ActionStatus` / `command_completed` (SUCCESS / FAILED / NOT_APPLICABLE) — the feedback the server self-corrects on.
- The endpoint-side offline cab scan (`ScanOffline`, Layer 4) is **demoted**: default OFF; retained only for air-gapped / on-demand reconciliation, never routine fleet distribution.

---

## 7. Why this beats the alternatives

- **vs shipping the cab to endpoints:** no 700 MB × fleet transfer; fleet-wide relevance in minutes; no relay/DP needed for detection; roaming clients trivial.
- **vs BigFix relevance-expression content:** no per-patch rule authoring and no content-team lag — detection works the moment the cab/MSRC data is server-side.
- **Precision:** exact for the cumulative bulk (build authority); the tail is self-corrected by real install results, not by guessing or by distributing the cab.

---

## 8. Open items to confirm with the agent team
1. Proto shape for the re-added `installed_kbs` in `DeviceStateReport` (field number — the old one is reserved).
2. Whether the server wants any additional cheap endpoint signals for tail accuracy (e.g. installed .NET version) before relying solely on build+KB + feedback.
3. Catalog version stamping in the diff result so console can explain "missing as of catalog vN".
