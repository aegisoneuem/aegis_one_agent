# Device Identity & Lifecycle — Server-Team Design Note

**Audience:** Aegis One server/console team.
**Status:** agent-side identity implemented; server-side reconciliation + lifecycle to be built.
**Owner (agent side):** Vijay. This note is the contract + the hard cases the server must handle.

---

## 1. Core principle

Device identity is **not** a single magic number. It is **composite**, and the
**server reconciles** — the agent only produces signals.

1. **`device_id`** (primary) — a UUID the agent generates once and persists.
2. **hardware fingerprint** (reconciliation key) — stable per-machine signal.
3. **hostname is NEVER identity** — only a mutable display attribute.

> **Match devices on `device_id` or fingerprint — never on hostname.** Every
> "duplicate device" problem in UEM comes from keying on hostname/name.

---

## 2. Identity signals and their stability

| Signal | Restart | Uninstall/Reinstall | Reimage | VM template clone | Rename |
|---|---|---|---|---|---|
| `device_id` (persisted UUID) | stable | lost if secure dir wiped | lost | **shared if baked into template** | stable |
| **SMBIOS System UUID** (fingerprint, primary) | stable | stable | **stable** (same HW) | **unique per VM** | stable |
| MachineGuid (fingerprint, fallback) | stable | stable | **changes** | shared unless sysprep'd | stable |

The agent prefers **SMBIOS System UUID**, falling back to **MachineGuid**. The
fingerprint string is prefixed: `smbios:<uuid>` or `machineguid:<guid>`.

---

## 3. Agent-side behavior (IMPLEMENTED — what the server can rely on)

- **First run:** generates a v4 UUID `device_id`, persists `SKVC\identity.json`
  = `{device_id, fingerprint, created_at}`.
- **Every boot:** reads the current hardware fingerprint and compares it to the
  stored one:
  - **match** → reuses the persisted `device_id`.
  - **changed** → treats it as a **clone/reimage**, generates a **fresh**
    `device_id` (so a template-baked id does not collide across spun VMs).
  - **fingerprint unreadable (`""`)** → reuses the persisted id (no false
    regeneration).
- **Override:** `AEGIS_DEVICE_ID` (config/env) pins an explicit id for testing;
  it is not persisted.

> Note: the agent today uses the fingerprint for **local clone detection only**.
> It will **send** `device_id` + fingerprint to the server at **enrollment**
> (next milestone) — see §6.

---

## 4. Server-side responsibilities (TO BUILD)

### 4.1 Reconciliation / de-duplication
On enrollment and on (re)registration, match in this order:
1. exact `device_id` match → same logical device;
2. else fingerprint (SMBIOS UUID) match → **same hardware**, treat as the same
   device (e.g. reimaged or secure-dir wiped) — reactivate/merge, do not create a
   duplicate;
3. else → new device record.

### 4.2 Lifecycle states (last-seen driven, all thresholds configurable)
`online → offline (no heartbeat > N) → stale (> M) → decommissioned → deleted`.
The **server** owns these transitions from last-seen; the agent never self-marks.

### 4.3 Certificate control
- **Decommission / delete** → **revoke** the device's mTLS client cert so it
  cannot reconnect, and stop counting it against the license.
- A returning device with a revoked cert must **re-enroll** (gets a new cert;
  reconciled to the same record via fingerprint).

### 4.4 Cleanup
- Auto-transition stale devices on a schedule; bulk decom/delete from the console.
- Deletion is administrative — a still-alive device that returns must reconcile to
  **one** entry (see §5), not spawn a duplicate.

---

## 5. The hard scenarios — required server behavior

| # | Scenario | Expected outcome | Primarily handled by |
|---|---|---|---|
| 1 | New machine | New `device_id`, no fingerprint match → new record | agent + server |
| 2 | Uninstall / reinstall | Secure dir wiped → new `device_id`, but fingerprint matches → **reactivate same record**, no duplicate | agent (fingerprint) + **server (dedup)** |
| 3 | Duplicate hostname | Two distinct records (different id + fingerprint); hostname is just an attribute | **server (hostname ≠ key)** |
| 4 | OS template, agent baked in | Each spun VM has a different SMBIOS UUID → agent **regenerates** `device_id` → distinct records | **agent (clone detect)** |
| 5 | Re-imaged endpoint | New OS → new `device_id`; **same SMBIOS UUID** → server reconciles as same hardware (retire old / reuse) | agent + **server (policy)** |
| 6 | Cleanup deleted/decom | Revoke cert, drop from license count; stale auto-cleanup on schedule | **server/console** |
| 7 | No heartbeat 30/60d; delete then return; rename | Last-seen → offline/stale/decom. Returning device: id match → **reactivate same entry**; if cert revoked → **re-enroll** → fingerprint dedup → same entry. Rename → same entry, hostname attribute updated. **No duplicates** | **server (reconciliation)** |

**Invariant:** in every case the server must converge to **one** logical record
per physical/virtual machine, keyed on `device_id`/fingerprint, never hostname.

---

## 6. Data the agent will send at enrollment (proposed contract)

To be finalized with the enrollment (`Enroll`) RPC in the next milestone:

```
device_id        string   // persisted UUID (or empty on very first enroll)
system_uuid      string   // SMBIOS System UUID (primary dedup key)
machine_guid     string   // registry MachineGuid (secondary)
hostname         string   // display attribute only — NEVER a dedup key
os               string
agent_version    string
# (enrollment also carries the tenant enrollment token + a CSR — see enrollment note)
```

The server stores `system_uuid`/`machine_guid` as the **reconciliation keys** and
`hostname` as a mutable attribute.

### 6.1 Enrollment RPC (Option B) — IMPLEMENTED (agent + mockserver reference)

`rpc Enroll(EnrollRequest) returns (EnrollResponse)` on `AgentControlPlane`.

Flow (the mockserver `Enroll` handler is the executable reference the real vault
must match):

1. Agent starts with **no client cert** → generates an ECDSA P-256 keypair
   **locally** (private key never leaves the device) and a CSR (`CN = device_id`).
2. Agent calls `Enroll` over a **separate SERVER-AUTH-TLS endpoint**
   (`AEGIS_ENROLL_ADDRESS`, mock default `:50052`) — it has no client cert yet, so
   the main strict-mTLS port cannot be used. The call is authenticated by the
   **per-tenant `enroll_token`**.
3. Server (vault) MUST: validate the token, **enforce licensing + reconcile the
   device** (§4.1), then sign the CSR with the tenant CA → return
   `client_cert_pem` (+ optional `ca_chain_pem`, `assigned_device_id`).
4. Agent writes the cert + key, then connects normally over **strict mTLS**
   (`:50051`). The private key is sealed at rest (DPAPI) on load.

`EnrollResponse.error` (non-empty) rejects enrollment (bad token, over license,
revoked, etc.). The mockserver reference currently validates the token and signs;
**licensing + reconciliation is the real server's job to add here.**

---

## 7. Template / golden-image best practice (to document for customers)

- Prefer **not** baking a provisioned `device_id` into a template; let the agent
  generate on first boot.
- If the agent is baked in, the clone-detection (fingerprint change) handles it —
  but **sysprep / generalize** is still recommended.
- These are operational guidance for the deployment team, surfaced via the console
  docs.

---

## 8. Honesty / non-goals

- No single signal is perfect; identity is **best-effort composite + server
  reconciliation**. This matches how BigFix/Tanium/SCCM operate.
- Edge cases where SMBIOS UUID is absent/zeroed (some older/virtual firmware) fall
  back to MachineGuid; if both are missing the agent keeps its persisted id and
  clone detection is skipped for that boot.
- The agent never decides it is a "duplicate" — that is always a server decision.

---

## 9. Open decisions for the server team

1. Exact lifecycle thresholds (offline N, stale M, auto-decom/delete) + defaults.
2. Reimage policy (§5 row 5): retire-and-replace vs reuse-record on same-hardware.
3. Revocation mechanism (CRL / short-lived certs / server-side allowlist).
4. Bulk delete + return behavior confirmation (reactivate vs new record).
