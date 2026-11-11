# Tester Guide — Policy A3 + Key-Tamper + Identity OOB + Maintenance Window

**Batch (2026-11-10):** A3 enforcement (logging/audit/fetch/upload), `key_tampered`
alert, Custom-Attr A identity inventory, maintenance-window defer. Two-box VM
(mockserver + endpoint `WIN-TUES4V46T7I`). Agent service `AegisOneClient`.

> **Anti-rollback floor:** every policy's `AEGIS_POLICY_ISSUED_AT` must EXCEED the
> current floor. If earlier tests pushed 1900000000, use **1900000100+** here and
> keep increasing. Verify audit with `auditcheck.exe "C:\Program Files\AegisOne\Client\SKVC\logs\audit.log"`.
> Deploy the new `dist\AegisOneClient.exe` to the endpoint (hash-verify) + restart the service first.

---

## B-1 — A3 logging.level (live)
Server: `set AEGIS_SET_POLICY=1 & set AEGIS_POLICY_ID=b1 & set AEGIS_POLICY_ISSUED_AT=1900000100 & set AEGIS_POLICY_LOG_LEVEL=debug` → `mockserver.exe`.
**Expect:** endpoint log starts emitting **DEBUG** lines + `policy: log level applied level=debug`. Push again with `AEGIS_POLICY_LOG_LEVEL=info` (issued_at 1900000101) → DEBUG stops. Unknown value → falls back to info.

## B-2 — A3 audit flood-control (live)
Server: add `set AEGIS_POLICY_AUDIT_RATE=1 & set AEGIS_POLICY_AUDIT_BURST=1 & set AEGIS_POLICY_AUDIT_DEDUP=5` (issued_at +1).
**Expect:** `policy: audit flood-control retuned rate_per_sec=1 burst=1`. Under a burst of commands only the first `command_received` is admitted; `audit_rate_limited` summary appears (security/outcome events never dropped).

## B-3 — A3 fetch + upload throttle (live)
Server: `set AEGIS_POLICY_FETCH_MAX_BPS=200000` (issued_at +1), then push a content fetch (e.g. a softdist/cab deploy).
**Expect:** `policy: fetch bandwidth cap in force bytes_per_second=200000`; the download is paced (visibly slower). Upload cap (`transfer.upload_max_bytes_per_second`) throttles `collect_file` uploads the same way (lower-wins vs agent/command caps).

---

> ⚠️⚠️ **B-4 / B-5 are DESTRUCTIVE — run them LAST.** The DPAPI master key also
> protects the client cert's private key (`client.key.enc`). Destroying the master
> key makes the client key undecryptable, so after the alert fires the agent
> **cannot do mTLS and will not reconnect** until you RE-ENROLL it. This is
> expected (the key destruction resets at-rest state, which includes the client
> key). **Recovery after B-4/B-5:**
> ```cmd
> sc stop AegisOneClient
> del "C:\Program Files\AegisOne\Client\SKVC\certs\client.crt"
> del "C:\Program Files\AegisOne\Client\SKVC\certs\client.key.enc"
> reg add "HKLM\SOFTWARE\AegisOne\Client\Enrollment" /v AEGIS_ENROLL_TOKEN /t REG_SZ /d <enroll-token> /f
> sc start AegisOneClient
> ```
> (mockserver running with the same `AEGIS_ENROLL_TOKEN`.) The agent re-enrolls,
> seals a fresh client key with the new master key, and reconnects as the same
> device. The `key_tampered corrupt`/`deleted` audit events are the PASS signal.

## B-4 — key_tampered alert (corrupt)  ⚠️ admin
1. `sc stop AegisOneClient`
2. Overwrite the key blob with garbage: `echo garbage > "C:\Program Files\AegisOne\Client\SKVC\dpapi.key"`
3. `sc start AegisOneClient`
**Expect:** startup log `SECURITY: master key integrity alert kind=corrupt`; audit `key_tampered` decision=corrupt; agent **self-heals** (bad blob dropped, fresh key generated) and keeps running. (At-rest state resets — expected; TPM NV is the roadmap anchor.)

## B-5 — key_tampered alert (deleted)  ⚠️ admin
1. `sc stop AegisOneClient`
2. Delete the key: `del "C:\Program Files\AegisOne\Client\SKVC\dpapi.key"` (the `KeyProvisioned` marker in `HKLM\SOFTWARE\AegisOne\Client` remains)
3. `sc start AegisOneClient`
**Expect:** audit `key_tampered` decision=**deleted**; key regenerated. (On a true first install — no marker — there is NO alert.)

---

## B-6 — Custom-Attr A identity inventory
Trigger a full inventory (reconnect: restart the agent, or wait for the Tier-3 cycle). On the **mockserver** console, the `Received FULL inventory` block now prints:
```
  identity: user="<logged-on user>" domain="<AD/workgroup>" joined=<bool> uptime=<N>s
```
**Expect:** `user` = the interactive console user, `domain` + `joined` reflect the machine's AD/workgroup state, `uptime` > 0. (Headless/service-only session may show empty user — expected.)

---

## B-7 — Maintenance-window defer (the big one)
Set a window that is **closed now** (pick hours NOT containing the current local hour). Example if it is 14:xx local — window 02:00–03:00:
`set AEGIS_SET_POLICY=1 & set AEGIS_POLICY_ID=b7 & set AEGIS_POLICY_ISSUED_AT=1900000110 & set AEGIS_POLICY_MAINT_START=2 & set AEGIS_POLICY_MAINT_END=3` → `mockserver.exe` (apply, Ctrl+C).
Then push an install/reboot, e.g. `set AEGIS_SET_POLICY= & set AEGIS_REBOOT_DELAY=90` → `mockserver.exe`.
**Expect (outside window):**
- reboot → **ActionStatus QUEUED reason="deferred to maintenance window"**; box does NOT reboot.
- audit `command_deferred`; a file appears under `C:\Program Files\AegisOne\Client\SKVC\deferred\`.
- `install_patch` / `software_deploy` behave the same; `scan_now` / inventory / `audit_query` are NOT deferred (run immediately).

**Open the window → deferred runs:** push a policy whose window includes the current hour, e.g. `set AEGIS_SET_POLICY=1 & set AEGIS_POLICY_ID=b7b & set AEGIS_POLICY_ISSUED_AT=1900000111 & set AEGIS_POLICY_MAINT_START=<currentHour> & set AEGIS_POLICY_MAINT_END=<currentHour+1>` → `mockserver.exe`.
**Expect:** within ~1 min the agent logs `maintenance: window open — running deferred command` and the reboot/install executes; the `deferred\` file is removed.

**Restart-safety:** defer a command (closed window), then `sc stop/start AegisOneClient` BEFORE opening the window → the `deferred\` file survives; opening the window still runs it.

**Break:** a deferred command whose signature no longer verifies, or whose type a newer policy denies, is **dropped on drain** (logged), not run.

---

## Record
| Case | PASS/FAIL | Notes |
|---|---|---|
| B-1 logging.level live | | |
| B-2 audit retune | | |
| B-3 fetch/upload throttle | | |
| B-4 key_tampered corrupt + self-heal | | |
| B-5 key_tampered deleted | | |
| B-6 identity inventory | | |
| B-7 maintenance defer → open → run | | |
| B-7 restart-safety | | |
