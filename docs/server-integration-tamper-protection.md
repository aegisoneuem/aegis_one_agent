# Server/Console Integration — Agent Tamper-Protection (Phase A)

**Audience:** the Aegis One **server & console teams**.
**Purpose:** what the agent's tamper-protection does, the commands/credentials the
server must issue, and the server-side pieces that complete the story.

> **Model:** when enabled, the agent service cannot be stopped / disabled / deleted /
> uninstalled without a **server-issued credential**. The agent enforces this and
> self-heals; the **server/console issues the credential and the control commands**,
> and owns credential rotation + agent-silent detection. Honest bound: pure software
> cannot 100%-stop a local admin on their own box — the goal is *detect + self-heal +
> raise the bar*; the absolute anchor (PPL/ELAM + TPM) is Phase-B (EDR work).

---

## 1. What the agent enforces (no server action needed beyond issuing credentials)

Active whenever a passcode is set (or `AEGIS_TAMPER_PROTECT=1`):

- **Install-dir NTFS ACL** — a non-admin cannot modify/delete agent files.
- **Service does not accept SCM stop** — `sc stop` / services.msc Stop fail (1052) for
  everyone incl. admin. The only stop is a credential-authorized one (§2).
- **Restrictive service DACL** — `sc config disabled` and `sc delete` fail (access
  denied) for non-SYSTEM.
- **Service registry-key ACL** — `HKLM\SYSTEM\CurrentControlSet\Services\<svc>` is
  locked so a regedit `Start=4` (disable) is denied.
- **Watchdog (30s)** self-heals: re-applies the DACLs, re-enables a disabled
  start-type, **re-registers a deleted service definition** (take-own + full
  service-key delete), restores a **deleted or replaced** exe from a last-known-good
  backup — each raising a `service_tampered` / `binary_tampered` alert.
- **Fast restart-on-failure** (~5s) so a `taskkill` is undone quickly.
- **Fail-closed**: deleting the on-disk passcode store keeps the service protected
  (a registry marker) and raises `unprotect_secret_tampered`.

---

## 2. Credentials the server issues (two mechanisms — the agent accepts either)

The agent's `authorizeUnprotect` tries a **short passcode** then a **long token**.

### A. Short passcode — `set_unprotect_secret` (signed command)
`payload` = JSON. The agent stores only a **DPAPI-sealed PBKDF2 hash** (never the
passcode); verification is rate-limited (5 fails → 5-min lockout).

```json
{ "mode": "static", "passcode": "Open1234", "issued_at": 1794745159, "max_tries": 3 }
```
- `issued_at` must strictly increase (anti-rollback). `max_tries` = interactive
  uninstall-prompt attempts (default 3).
- **Rotation (hourly/weekly/monthly):** re-send with a NEW passcode + higher
  `issued_at` on your schedule — the old stops working, the new works. **The
  rotation scheduler is a SERVER component.**
- **One / per-device / per-group / per-computer-id:** your targeting decision — send
  the same passcode to all, or different passcodes to specific devices/computer-ids/
  groups. The agent stores whatever it receives for itself (target-agnostic).
- **Offline:** the passcode is stored + verified **locally** — stop/uninstall with
  the last-set passcode works even with no server connection.
- **Clear / turn off:** send `{ "mode": "clear", "issued_at": … }` (anti-rollback).
  Do NOT tell admins to delete files — that is fail-closed.

### B. Long signed token (crypto-grade, stateless)
`base64url(payloadJSON) + "." + base64url(ed25519_sig)`,
`payloadJSON = {"device":"<id>|*","exp":<unix|0>,"nonce":"<rnd>"}`, signed with the
SAME Ed25519 key the agent trusts for commands. `device:"*"` = any, `exp:0` = never.

### Stop the agent — `stop_agent` (signed command)
`payload` = the passcode or token (raw string). Valid → clean stop
(`agent_stop_authorized`); invalid → refused (`agent_stop_refused`). **Start is never
gated** — only stop/disable/uninstall are.

### Uninstall
`msiexec /x AegisOneClient.msi UNINSTALLPASSCODE=<code>` (silent) or the GUI
(Add/Remove Programs) which **prompts** for the passcode (3 tries, red "incorrect"
feedback). A major-upgrade removal is NOT gated (agent upgrades keep working).

---

## 3. Server-side responsibilities (to complete the tamper story)

1. **Issue + rotate credentials** — mint tokens / set passcodes (static or rotating,
   per-device / per-group); store/rotate in the console; show the current credential
   to authorized operators; run the rotation scheduler.
2. **Agent-silent detection + re-deploy** — if a device stops checking in (the
   determined-admin "wiped everything" case the agent cannot defend alone), alert and
   re-deploy. This is the automated recovery.
3. **Re-assertion** — the agent self-heals from the server: if local
   passcode/policy/custom-attr state is wiped, re-push on reconnect (the agent treats
   a higher `issued_at` as authoritative).
4. **Surface the alerts** (below) in the console.

---

## 4. Audit events the agent emits

| event_type | meaning |
|---|---|
| `unprotect_secret_set` / `unprotect_secret_cleared` | passcode set / cleared |
| `unprotect_secret_tampered` | passcode store deleted but marker present (fail-closed) |
| `agent_stop_authorized` / `agent_stop_refused` | credential stop accepted / rejected |
| `service_tampered` | service DACL reset or disabled start-type restored by the watchdog |
| `binary_tampered` | agent exe deleted/replaced → restored from backup, or changed without a version change |

---

## 5. Not yet / future (so expectations are clear)

- **Code-signing (Authenticode)** of the exe + MSI — a go-live prerequisite (done at
  build/release); enables forge-proof binary self-verification (WinVerifyTrust) and
  is required for Phase-B PPL.
- **Phase-B (PPL/ELAM kernel driver + TPM-anchored state)** — the absolute
  admin-proofing (unkillable process, hardware-rooted state). Deferred to the EDR
  work. Until then, the determined-local-admin edge cases (rapid taskkill-loop,
  service take-ownership, deleting both the exe AND its backup) are covered by
  server-silent-detection + re-deploy.
