# Aegis One Agent — Tamper-Protection: End-to-End Guide

**Audience:** Aegis One **server & console** engineering teams.
**Scope:** the complete tamper-protection picture — what the agent enforces on the
endpoint, what the server/console must provide, the credential lifecycle, every
control command and its message shape, the audit events to surface, an operational
runbook, and the honest limits.
**Status:** Phase-A **complete and VM-verified**. Phase-B (PPL/ELAM + TPM) deferred to
the EDR work. Code-signing (Authenticode) is a go-live prerequisite, not yet done.

> **One-line model:** when a credential is provisioned, the agent service **cannot be
> stopped, disabled, deleted, its binary altered, or uninstalled** without a
> **server-issued credential**. The agent *enforces + self-heals* locally and works
> offline; the **server owns the credential, its rotation, and agent-silent
> detection**. Pure software cannot 100%-stop a determined local admin on their own
> box — the goal is **detect + self-heal + raise the bar**; the absolute anchor
> (hardware-rooted, unkillable) is Phase-B.

---

## 1. Why this exists (threat model)

Competitor agents (BigFix `BESClient.exe`, Tanium, ManageEngine) can be stopped,
disabled, or have their binary swapped by a **local administrator** — after which the
endpoint silently stops receiving policy/patches, or worse, a swapped binary runs
attacker code under the agent's trust. Our requirement (Vijay, security = top
priority): a local admin — **even LocalSystem-level** — must not be able to turn the
agent off or tamper with it without a credential the **server** controls.

**Adversary assumed:** an interactive local administrator on the endpoint, possibly
"AI-assisted" (will Google / ask an LLM how to kill the agent). **Not** assumed:
kernel-level attacker, physical TPM extraction (that is Phase-B's bar).

**Defense posture:** every easy and most medium bypasses are **blocked or
self-healed within seconds–minutes** and **alerted**; the few remaining
determined-admin edge cases are closed by **server-side silent-detection +
re-deploy** until Phase-B lands.

---

## 2. Architecture at a glance

```
          ┌────────────────────────── SERVER / CONSOLE ──────────────────────────┐
          │  • mints credential (short passcode  OR  long Ed25519 token)          │
          │  • signs control commands with the SAME Ed25519 key the agent trusts  │
          │  • runs rotation scheduler + agent-silent detection + re-deploy       │
          │  • surfaces audit events in the console                               │
          └───────────────▲───────────────────────────────┬──────────────────────┘
                 signed commands (mTLS)          audit events / check-ins
                          │                                ▼
          ┌───────────────┴──────────────── ENDPOINT (AegisOneClient, LocalSystem) ┐
          │  ENFORCE                         SELF-HEAL (watchdog, 30s)             │
          │  • install-dir NTFS ACL          • re-apply service DACL              │
          │  • service rejects SCM stop      • undo `sc config disabled`/Start=4  │
          │  • restrictive service DACL      • restore DELETED exe (~30s)         │
          │  • service registry-key ACL      • restore REPLACED exe (~5min)       │
          │  • fast restart-on-failure(~5s)  • fail-closed if passcode store wiped │
          │  CREDENTIAL STORE                                                      │
          │  • DPAPI-sealed PBKDF2 hash of passcode (never the passcode)          │
          │  • obscure on-disk/registry names; rate-limited verify                │
          └──────────────────────────────────────────────────────────────────────┘
```

Everything the agent enforces works **with no server connection** once a credential
is set (offline-first). The server is needed to *issue/rotate* credentials and to
*recover* the determined-admin "wiped the box" case.

---

## 3. What the agent enforces (no server action beyond issuing a credential)

Active whenever a passcode is set **or** `AEGIS_TAMPER_PROTECT=1`:

| Protection | Effect | Bypass attempt → result |
|---|---|---|
| **Install-dir NTFS ACL** | non-admin cannot modify/delete agent files | non-admin edit → Access denied |
| **Service rejects SCM stop** | agent drops `SERVICE_ACCEPT_STOP` | `sc stop` / services.msc Stop → **1052** (even for admin/SYSTEM) |
| **Restrictive service DACL** | SCM object locked | `sc config … disabled`, `sc delete` → **Access denied (5)** |
| **Service registry-key ACL** | `HKLM\SYSTEM\CurrentControlSet\Services\<svc>` locked | regedit `Start=4` → **Access denied** |
| **Watchdog self-heal (30s)** | re-applies DACLs; undoes a disabled start-type; restores a **deleted or replaced** exe | each raises `service_tampered` / `binary_tampered` |
| **Fast restart-on-failure (~5s)** | SCM recovery restarts a killed process | `taskkill /f` → process back in ~5s |
| **Fail-closed** | deleting the on-disk passcode store keeps protection on (registry marker) | store deleted → still protected + `unprotect_secret_tampered` |

**Resource cost (by design, light):** watchdog ticks every 30s doing cheap ACL
re-applies + `os.Stat` checks; the heavy hash-compare (reads the ~15 MB exe + backup)
runs only every ~5 min. No busy-loop, no CPU/disk hammering.

**Obscured secrets:** the passcode store and registry marker use **non-obvious
names** (not `unprotect.secret` / `UnprotectProvisioned`) so an admin Googling obvious
names finds nothing; the stored value is a **DPAPI-sealed PBKDF2-HMAC-SHA256 hash**
(200k iters), never the passcode. Protection log lines are at **debug** level so they
don't advertise the mechanism in normal logs.

---

## 4. The two credentials (agent accepts either)

The agent's authorizer tries a **short passcode** first, then a **long signed token**.

### 4A. Short passcode — `set_unprotect_secret` (signed command)

ManageEngine-style short code (e.g. 8–10 chars). The agent stores **only** a
DPAPI-sealed PBKDF2 hash; verification is **rate-limited** (5 fails → 5-min lockout).

```json
{ "mode": "static", "passcode": "Open1234", "issued_at": 1794745159, "max_tries": 3 }
```

- `issued_at` **must strictly increase** (anti-rollback — an old captured command
  can't re-apply an old passcode).
- `max_tries` = interactive uninstall-prompt attempts (default 3).
- **Rotation (hourly/weekly/monthly):** re-send a NEW passcode with a higher
  `issued_at`; old stops working, new works. **The rotation scheduler is a SERVER
  component.**
- **Targeting — one / per-device / per-group / per-computer-id:** entirely the
  server's decision. Send the same passcode to all, or different passcodes per
  device/computer-id/group. The agent stores **whatever it receives, for itself**
  (target-agnostic).
- **Offline:** stored + verified **locally** — stop/uninstall with the last-set
  passcode works with no server connection.
- **Clear / turn protection off:** send `{ "mode": "clear", "issued_at": … }`
  (anti-rollback applies). **Never** tell admins to delete files — that is
  fail-closed and keeps protection on.

### 4B. Long signed token (crypto-grade, stateless)

```
base64url(payloadJSON) + "." + base64url(ed25519_sig)
payloadJSON = { "device": "<id>|*", "exp": <unix|0>, "nonce": "<rnd>" }
```

Signed with the **same Ed25519 key** the agent trusts for commands.
`device:"*"` = any device, `exp:0` = never expires. Use this for break-glass /
operator-initiated stops where you don't want to pre-provision a passcode.

### 4C. Stop the agent — `stop_agent` (signed command)

`payload` = the passcode or token (raw string).
- Valid → clean stop, audit `agent_stop_authorized`.
- Invalid → refused, audit `agent_stop_refused`.
- **Start is never gated** — only stop / disable / uninstall are.

### 4D. Local-CLI stop (operator at the box, with the credential)

```
"C:\Program Files\AegisOne\...\AegisOneClient.exe" stop --passcode <code>
"C:\Program Files\AegisOne\...\AegisOneClient.exe" stop --token   <token>
```

Authorized → stops cleanly; else refused. This is how an on-box admin **with the
credential** stops it without the server round-trip.

### 4E. Uninstall

- **Silent:** `msiexec /x AegisOneClient.msi UNINSTALLPASSCODE=<code> /qn`
- **GUI (Add/Remove Programs):** the MSI runs a gate CustomAction that **prompts**
  for the passcode (native dialog, `max_tries` attempts, red "incorrect" feedback,
  cancel = abort). On success the gate stops the self-protected service, then the MSI
  removes everything cleanly.
- **Major upgrade** removal is **NOT** gated (agent self-upgrades keep working).

---

## 5. Credential lifecycle (server responsibilities)

1. **Provision** — on/after enrollment, mint + send a passcode (or rely on token
   break-glass). Store it in the console, show it to authorized operators only.
2. **Rotate** — run the rotation scheduler (static → rotating on your cadence);
   always bump `issued_at`. The agent silently adopts the newest.
3. **Re-assert on reconnect** — if an endpoint's local passcode/policy/custom-attr
   state was wiped, **re-push** on reconnect; the agent treats a higher `issued_at`
   as authoritative (self-heal from server).
4. **Agent-silent detection + re-deploy** — if a device stops checking in (the
   determined-admin "wiped everything incl. the backup" case the agent cannot defend
   alone), **alert and re-deploy**. This is the automated final-recovery and the
   stated boundary before Phase-B.
5. **Surface audit events** (§7) in the console for the SOC.

---

## 6. End-to-end test & verification matrix

Run on a clean box with the **latest** dist MSI installed and a passcode set
(`AEGIS_SET_PASSCODE=Open1234`). All attack commands from an **elevated admin** cmd.

| # | Action | Expected result | Audit |
|---|---|---|---|
| 1 | `sc stop aegisoneclient` | **1052** (can't accept control) | — |
| 2 | `sc config aegisoneclient start= disabled` | **Access denied (5)** | — |
| 3 | `sc delete aegisoneclient` | **Access denied (5)** | — |
| 4 | regedit `…\Services\aegisoneclient` → `Start=4` | Denied; or watchdog restores `Start=2` ≤30s | `service_tampered` |
| 5 | `taskkill /f /im AegisOneClient.exe` | process restarts in ~5s | — |
| 6a | delete the exe | restored from backup ≤30s | `binary_tampered` (restored) |
| 6b | overwrite exe with another binary (e.g. copy notepad over it) | restored from backup ≤~5min | `binary_tampered` (restored) |
| 7 | delete the passcode store file | protection stays on (fail-closed) | `unprotect_secret_tampered` |
| 8 | **Legit stop:** `AegisOneClient.exe stop --passcode Open1234` | stops cleanly | `agent_stop_authorized` |
| 8n | wrong passcode on #8 | refused | `agent_stop_refused` |
| 9 | **GUI uninstall** (appwiz): wrong passcode ×N | red "incorrect", blocked after `max_tries` | `agent_stop_refused` |
| 9b | **GUI uninstall:** correct passcode | clean uninstall (service+files+ARP gone) | `agent_stop_authorized` |
| 10 | **Silent uninstall:** `msiexec /x … UNINSTALLPASSCODE=Open1234 /qn` | clean uninstall | `agent_stop_authorized` |
| 11 | **Rotation:** send new passcode + higher `issued_at`; old passcode on #8 | old refused, new works | — |
| 12 | **Group/computer-id targeting:** passcode to a `computer_id` list only | only targeted devices adopt it | `unprotect_secret_set` on those |

**Clean-slate between runs:**
`taskkill /f` → `sc delete` (only works when protection off/uninstalled) →
`rmdir /s /q "C:\Program Files\AegisOne"` → `reg delete HKLM\SOFTWARE\AegisOne /f` →
reinstall MSI.

---

## 7. Audit events (surface these in the console)

| `event_type` | Meaning |
|---|---|
| `unprotect_secret_set` / `unprotect_secret_cleared` | passcode set / cleared |
| `unprotect_secret_tampered` | passcode store deleted but marker present (fail-closed) |
| `agent_stop_authorized` / `agent_stop_refused` | credentialed stop accepted / rejected |
| `service_tampered` | service DACL reset or disabled start-type restored by watchdog |
| `binary_tampered` | exe deleted/replaced → restored from backup (or changed without a version change) |

A burst of `binary_tampered` / `service_tampered`, or a device going **silent** right
after such events, is the SOC signal to investigate + re-deploy.

---

## 8. Operational runbook (console/SOC)

- **"Agent won't stop for a legit maintenance window"** → issue a `stop_agent`
  command or hand the operator the current passcode for `stop --passcode`. Do **not**
  tell them to `taskkill`/registry-hack (it will just self-heal + alert).
- **"Need to decommission a device"** → silent uninstall with `UNINSTALLPASSCODE`,
  or clear the passcode (`mode:"clear"`) then uninstall.
- **"Lost/forgot the passcode"** → rotate a new one (`issued_at` bump) from the
  console; the old is irrelevant. (There is no local passcode recovery by design —
  the hash is one-way.)
- **"Device went silent after tamper alerts"** → re-deploy via the MSI; enrollment
  re-asserts policy + a fresh passcode.
- **"Turning tamper-protection off fleet-wide"** → push `mode:"clear"` to the target
  set; agents drop self-protection at runtime (no reboot needed).

---

## 9. Honest limits & what's next

**Covered now (Phase-A):** install-dir ACL, no-SCM-stop, service+registry DACL,
watchdog self-heal (disable-undo, deleted/replaced-binary restore), fast recovery,
fail-closed, credentialed stop/uninstall (CLI + silent + GUI-prompt), offline,
rotation-ready, group/computer-id targeting, obscured+sealed secrets, debug-level
logs, light resource footprint. **VM-verified.**

**Remaining determined-admin edges (boundary → server silent-detection + re-deploy):**
rapid `taskkill` loop faster than recovery; service take-ownership by an admin who
fights the DACL continuously; deleting **both** the exe **and** its on-box backup.
These are the stated Phase-B targets.

**Not yet / future:**
- **Code-signing (Authenticode)** of exe + MSI — **go-live prerequisite** (build/
  release step). Enables forge-proof binary self-verification (WinVerifyTrust) and is
  required for Phase-B PPL. *Today the watchdog uses hash-vs-backup compare.*
- **Phase-B (PPL/ELAM kernel driver + TPM-anchored state)** — unkillable process,
  hardware-rooted anti-rollback. Deferred to the **EDR** work.

---

*See also:* `docs/server-integration-tamper-protection.md` (the condensed integration
spec — the command shapes and server responsibilities in brief).
