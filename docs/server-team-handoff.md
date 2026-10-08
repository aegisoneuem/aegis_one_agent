# Server-Team Handoff — Enrollment, Identity, Licensing (Tiny Vault)

**Audience:** Aegis One server / console team.
**Purpose:** what the server (the "tiny vault") must provision. The **agent side is
done and non-blocking** — it already generates identity, enrolls, and fail-loud
handles every server decision. The mockserver (`cmd/mockserver`) is a working
**minimal reference** of this contract; build the real one to match + extend.

Related: [device-identity-and-lifecycle.md](device-identity-and-lifecycle.md)
(dedup/lifecycle detail + the hard scenarios).

---

## 1. Agent side — DONE (tested from the agent, no blocker)

| Capability | Status |
|---|---|
| Stable `device_id` (persisted UUID) + hardware fingerprint (SMBIOS UUID / MachineGuid) | ✅ |
| Clone/reimage detection (fingerprint change → fresh id) | ✅ tested |
| Uninstall/reinstall → same device (server dedup) | ✅ tested |
| Rename / duplicate hostname (hostname = attribute, never key) | ✅ tested |
| Enrollment client: local keypair + CSR, token-auth, server-auth TLS | ✅ tested |
| Adopts server `assigned_device_id` (dedup) into `identity.json` | ✅ tested |
| Client key encrypted at rest (DPAPI) after enroll | ✅ |
| Single port (enroll + ops), mTLS for ops | ✅ |
| Fail-loud on any `EnrollResponse.error` (bad token / over license / rejected) | ✅ |

**The agent never decides identity/license** — it sends signals and obeys the
server's response. So all of §3 is server-side work with no agent dependency.

---

## 2. The contract (already fixed in proto — build to this)

`rpc Enroll(EnrollRequest) returns (EnrollResponse)` on `AgentControlPlane`,
single port, server-auth TLS (agent has no client cert yet), token-authenticated.

**EnrollRequest** (agent → server):
| field | meaning |
|---|---|
| `enroll_token` | per-tenant bootstrap token (validate + license-gate) |
| `device_id` | agent's persisted UUID (may be empty on first ever enroll) |
| `csr_pem` | PEM CSR, `CN = device_id`; **server CA signs it** (agent keeps the private key) |
| `system_uuid` | SMBIOS System UUID — **primary dedup key** |
| `machine_guid` | registry MachineGuid — secondary dedup key |
| `hostname` | **display attribute only — never a dedup key** |
| `os`, `agent_version` | attributes |

**EnrollResponse** (server → agent):
| field | agent behavior |
|---|---|
| `client_cert_pem` | agent writes it + its key, seals key at rest, connects mTLS |
| `ca_chain_pem` | optional; used only if no CA already provisioned |
| `assigned_device_id` | if different from requested → agent **adopts** it (dedup result) |
| `error` | non-empty → agent **fails loud and exits** (SCM restarts + retries) |

**After enrollment:** agent connects mTLS on the same port; the server identifies
the device from the **cert CN = device_id** (not hostname). Route commands by
`device_id`; results come back tagged with `device_id` (+ `command_id`). Hostname
arrives in the heartbeat as a **mutable attribute**.

---

## 3. What the SERVER / tiny vault must provision (the list)

1. **Enrollment authority (CA / signing)**
   - Validate `enroll_token`, sign the CSR → per-device client cert (CN =
     assigned device_id, ClientAuth EKU, sane validity).
   - **Per-customer CA isolation** (each tenant its own CA root) + **secure CA key
     storage** (OS keystore / HSM / encrypted — not a plaintext file).

2. **Token lifecycle**
   - Issue / **rotate** / **expire** the per-tenant bootstrap token.
   - Options for stricter customers: **one-time-use** tokens; **admin-approval**
     queue (valid token still lands a device in "pending" until approved).

3. **Device reconciliation / de-duplication** (registry/DB)
   - Match order: `device_id` → else `system_uuid` → else new record.
   - Return `assigned_device_id` (may differ after dedup). Hostname is an
     attribute, updated from enroll + heartbeat, **never a match key**.
   - Reimage policy (same `system_uuid`, new OS): retire-and-replace vs reuse.

4. **License enforcement**
   - Entitlement = max devices (+ expiry / features). A **new** device consumes a
     seat; **re-enroll / reinstall / dedup does NOT**.
   - Over cap → reject enrollment with a clear `error`. (Mock: `AEGIS_LICENSE_MAX`.)
   - Count must be reconciliation-aware (don't double-count reinstalls).

5. **Certificate lifecycle**
   - Renewal / rotation before expiry.
   - **Revocation** on decommission/delete (CRL / short-lived certs / server-side
     allowlist checked at connect). *Note:* the agent does not yet auto-re-enroll
     on a revoked cert — see §4.

6. **Device lifecycle states** (last-seen driven, thresholds configurable)
   - `online → offline → stale → decommissioned → deleted`; auto-cleanup schedule;
     bulk decom/delete from console.
   - Returning device after delete → reconcile to the **same** record (id or
     fingerprint), never a duplicate.

7. **Attribute store + console**
   - Store hostname/os/version etc. as mutable attributes keyed by `device_id`.
   - Console targets humans by hostname/groups but **routes actions by device_id**.

---

## 4. Known agent-side follow-ups (future, not blockers)

- **Auto re-enroll on revocation:** today a revoked/failed cert makes the agent
  retry mTLS; it does **not** yet detect "cert rejected → delete cert → re-enroll".
  That's a planned agent item once the server's revocation mechanism is defined.
- Windows cert-store / TPM key storage (currently DPAPI-sealed PEM).

---

## 5. How to test against an agent (what to delete to re-trigger)

To force a fresh **enrollment** on an agent (for license/dedup/token tests), delete
these on the endpoint and restart the agent:

```
<SecureDir>\certs\client.crt
<SecureDir>\certs\client.key
<SecureDir>\certs\client.key.enc
<SecureDir>\identity.json        # also deletes device_id → tests dedup/reconcile
```
(`<SecureDir>` default = `C:\Program Files\AegisOne\Client\SKVC`.)

- Keep the enroll token in the agent config (`AEGIS_ENROLL_TOKEN`) matching the server.
- Deleting only the certs (not `identity.json`) → re-enroll with the **same**
  device_id. Deleting both → new device_id → exercises **dedup** (same hardware →
  server should return the original id).
- Reconciliation / license / token decisions are all observable in the server
  logs + the `EnrollResponse` — no agent rebuild needed to test them.
