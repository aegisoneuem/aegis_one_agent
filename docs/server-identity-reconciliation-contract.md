# Device Identity & Enrollment Reconciliation — Server Contract

**Audience:** the server/control-plane team.
**Status:** the agent side and the mock reference are implemented and testable today; the **real server must implement the same reconciliation against its persistent store (DB).**

## The guarantee we must deliver

Within a customer environment, once a physical machine is known:

1. Its **`device_id`** (UUID, the agent's identity/correlation key) **never changes** across agent restart, reboot, reinstall, OS re-image, or disk format.
2. Its **`computer_id`** (the 10-digit console id, BigFix `ComputerID`-style) is **assigned once, kept forever**, and is **unique per environment and never reused** for a different machine.
3. The same `computer_id`/`device_id` is **never handed to a second machine**.

## What went wrong (why this doc exists)

On a test VM, an agent **uninstall + reinstall produced a new `device_id` AND a new `computer_id`**, even though the hardware (TPM EK, SMBIOS UUID, MachineGuid) was **identical**.

Root causes:
- The agent's local identity cache (`SKVC\identity.json`) lived inside the install dir and was wiped on reinstall. (Fixed agent-side — see below.)
- The **mock server kept its device registry in memory only**, so a mock **restart** lost it; the re-enroll then found no matching record and issued fresh ids. **A real server persists this in a DB — it must reconcile by the hardware signals the agent sends, or this regression recurs.**

The reconciliation **logic** was already correct; the gap was **durable storage**.

## Reconciliation algorithm (authoritative, server-side)

On `Enroll`, resolve the request to a single logical device by matching hardware signals, **strongest first**:

| Order | Signal (EnrollRequest field) | Why |
|---|---|---|
| 1 | `tpm_ek` | Firmware-bound, unique, clone-proof, survives OS reinstall/format. Strongest. |
| 2 | `device_id` | The agent's own persisted id (if it still has one). |
| 3 | `system_uuid` (SMBIOS) | Survives OS reinstall; absent/`all-zero` on some VMs. |
| 4 | `machine_guid` | Weakest (changes on OS re-image); use only as a last tiebreaker. |

- **Match found →** reuse the existing record. Return its **existing `device_id` as `AssignedDeviceId`** and its **existing `computer_id` as `ComputerId`**. Do **NOT** allocate new ids. Do **NOT** consume a new license seat.
- **No match →** a genuinely new device. Allocate a fresh `device_id` (or accept the agent's) and a fresh **unique** `computer_id`; consume one license seat.
- Persist/refresh the record (and all its signal indexes) in the DB **inside the same transaction** that issues the cert, so a crash can't half-register.

### `computer_id` rules
- Random 10-digit `[1000000000, 9999999999]` (random, not sequential — don't leak enrolled-device counts). Enforce a **unique index**; retry on collision.
- **Never reused**: once allocated to a device, it is not handed to another even if that device is decommissioned (keep a tombstone / don't recycle).
- Kept across re-enroll/reinstall/dedup; only a brand-new device gets a new one.

## The contract wire-side (already in the proto)

Agent → `EnrollRequest`: `enroll_token, device_id, csr_pem, system_uuid, machine_guid, tpm_ek, hostname, os, agent_version`.
Server → `EnrollResponse`: `client_cert_pem, ca_chain_pem, assigned_device_id, computer_id, error`.

- Sign the issued client cert for the **assigned** id (the reconciled one), not blindly for the CSR's CN.
- `hostname` is a mutable attribute — **never** a dedup key (it changes after a format).

## Agent side (done — for your reference)
- Sends all hardware signals on enroll; **adopts** `assigned_device_id` + `computer_id` from the response and persists them.
- Local identity now also persists to a **durable machine store (HKLM registry)** that survives uninstall/reinstall, so the agent keeps its id even without a server round-trip.
- Clone/reimage detection accumulates signals (union, never shrinks), so an intermittently-readable signal can't trigger a false regeneration.

## Mock reference (test against this now)
The mock server (`cmd/mockserver`) implements the exact reconciliation above and **now persists its registry to a JSON file** (`AEGIS_MOCK_STATE`, default `mockserver-devices.json`) — so dedup survives a mock restart, mirroring a real DB.

**Verify the guarantee:**
1. Enroll an agent → note `device_id` + `computer_id`.
2. Stop the mock, uninstall + reinstall the agent, restart the mock.
3. Re-enroll → the agent must come back with the **same** `device_id` + `computer_id` (mock prints `Enroll OK (RECONCILED/dedup) …`).

Delete `mockserver-devices.json` to simulate a fresh environment.
