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

| Signal | Restart | Uninstall/Reinstall | Reimage/Format | VM template clone | Rename |
|---|---|---|---|---|---|
| `device_id` (persisted UUID) | stable | lost if secure dir wiped | lost | **shared if baked into template** | stable |
| **TPM EK** (fingerprint, primary) | stable | stable | **stable** (firmware) | **unique** (even if SMBIOS dup) | stable |
| **SMBIOS System UUID** (fingerprint, 2nd) | stable | stable | **stable** (same HW) | usually unique, but **some VMs/OEM batches duplicate or zero it** | stable |
| MachineGuid (fingerprint, 3rd) | stable | stable | **changes** | shared unless sysprep'd | stable |

The agent prefers the **TPM Endorsement Key (EK)** — firmware-bound, globally
unique, survives an OS reinstall/format, and clone-proof — falling back to the
**SMBIOS System UUID**, then **MachineGuid**. Devices without a TPM fall back to
SMBIOS automatically. Fingerprint strings are prefixed: `tpm:<hash>`,
`smbios:<uuid>`, `machineguid:<guid>`. All readable signals are sent to the server
at enrollment, so the server can dedup on the strongest one present.

---

## 3. Agent-side behavior (IMPLEMENTED — what the server can rely on)

- **First run:** generates a v4 UUID `device_id`, persists `SKVC\identity.json`
  = `{device_id, fingerprint, signals[], computer_id, created_at}`.
- **Every boot:** reads ALL current hardware signals (TPM/SMBIOS/MachineGuid) and
  compares them to the stored set:
  - **any signal overlaps** → same machine → reuses the persisted `device_id`.
    (So adding a NEW signal — e.g. a TPM read that was not available before — does
    NOT look like a hardware change; the stored set is just refreshed.)
  - **no signal overlaps** → **clone/reimage** → generates a **fresh** `device_id`
    (so a template-baked id does not collide across spun VMs).
  - **no signal readable (`[]`)** → reuses the persisted id (no false regeneration).
- **Enrollment:** sends `device_id` + `tpm_ek` + `system_uuid` + `machine_guid`
  (+ hostname/os). The server dedups on these (TPM EK strongest) and may return an
  `assigned_device_id` (canonical, on dedup) + a **`computer_id`** (console id).
- **Override:** `AEGIS_DEVICE_ID` (config/env) pins an explicit id for testing;
  it is not persisted.

### Server-assigned console id (`computer_id`)
The stable `device_id` (UUID) is the agent's identity and the server's reconcile
key. The **console-facing numeric id** is a SEPARATE, server-assigned value (like
BigFix's ComputerID): the server returns it in the enroll response and the agent
stores + logs it for cross-reference. Design it to **start at 10 digits and simply
increment** — 10 digits holds 10 billion ids, so 5 lakh (500k)+ devices is far from
the ceiling, and it naturally grows to 11+ digits if a tenant ever needs it. Never
fix the width in a way that can overflow. (The mock server demonstrates this:
`nextComputerID` starts at `1000000000`, `AEGIS_COMPUTER_ID_START` overrides.)

---

## 4. Server-side responsibilities (TO BUILD)

### 4.1 Reconciliation / de-duplication
On enrollment and on (re)registration, match in this order (strongest hardware
signal first, so a formatted machine with a fresh `device_id` still reconciles):
1. **`tpm_ek` match** → same hardware (survives format, clone-proof) → same device;
2. else exact `device_id` match → same logical device;
3. else `system_uuid` (SMBIOS) match → **same hardware** (reimaged / secure-dir
   wiped) — reactivate/merge, do not create a duplicate;
4. else → new device record (assign the next `computer_id`).

Keep the stable `device_id`↔`computer_id` mapping: on dedup, return the EXISTING
`computer_id`; only a brand-new device gets a new one. (The mock server implements
exactly this order.)

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

**Single port.** Enrollment and the control plane share one TCP port (default
`:50051`). The server TLS uses **VerifyClientCertIfGiven**: a client cert is
verified against the CA when presented, but a connection without one is allowed so
a not-yet-enrolled agent can call `Enroll`. **All non-`Enroll` RPCs require a
verified client cert at the application layer** (reject `Unauthenticated`
otherwise). This keeps one firewall rule (agent → server, outbound from the
endpoint — no inbound port on the endpoint).

1. Agent starts with **no client cert** → generates an ECDSA P-256 keypair
   **locally** (private key never leaves the device) and a CSR (`CN = device_id`).
2. Agent calls `Enroll` on the **same server port** over server-auth TLS (no client
   cert yet), authenticated by the **per-tenant `enroll_token`**. (`AEGIS_ENROLL_ADDRESS`
   can override only if a separate endpoint is ever desired; default = server address.)
3. Server (vault) MUST: validate the token, **enforce licensing + reconcile the
   device** (§4.1), then sign the CSR with the tenant CA → return
   `client_cert_pem` (+ optional `ca_chain_pem`, `assigned_device_id`).
4. Agent writes the cert + key, then connects over **mTLS on the same port**;
   the server now requires + verifies the client cert for `Connect`/`FetchContent`.
   The private key is sealed at rest (DPAPI) on load.

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
