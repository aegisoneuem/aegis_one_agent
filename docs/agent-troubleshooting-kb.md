# Aegis One Agent — Troubleshooting KB

**Audience:** agent, server, and testing teams. A running list of agent issues
seen in dev/test and how to resolve them. Each entry: **Symptom → Cause → Fix**.
Keep adding as new issues are found.

> Paths assume the default install `C:\Program Files\AegisOne\Client\SKVC` (the
> "SKVC" dir). Config precedence is **env > config.json > registry
> (`HKLM\SOFTWARE\AegisOne\Client\*`) > built-in default**.

---

## 1. Enrollment

### 1.1 `enrollment rejected by server: invalid enrollment token`
**Cause:** the enrollment token the agent sends does not match the server's
expected token — OR the server has no token configured (then it rejects ALL
enrollments). The agent token comes from `AEGIS_ENROLL_TOKEN`
(`HKLM\SOFTWARE\AegisOne\Client\Enrollment`), set by the MSI (`ENROLLTOKEN`
property) or manually. The mock server checks it against its own
`AEGIS_ENROLL_TOKEN` env var.
**Fix — both sides must match:**
- Server (mockserver): `set AEGIS_ENROLL_TOKEN=tenant-abc-123` **before** running
  `mockserver.exe` (its startup line shows `token=te****23`; `token=<unset —
  enrollment will reject>` means it will reject everything).
- Agent: `reg add "HKLM\SOFTWARE\AegisOne\Client\Enrollment" /v AEGIS_ENROLL_TOKEN
  /t REG_SZ /d tenant-abc-123 /f` then `sc stop AegisOneClient & sc start
  AegisOneClient`.
- Install via MSI with the right token: `msiexec /i AegisOneClient.msi /qn
  ENROLLTOKEN=tenant-abc-123 SERVERADDRESS=<host:port> SERVERNAME=<cert-name>
  CACERTFILE=<path>`.

### 1.2 `no client cert and no enrollment token (set AEGIS_ENROLL_TOKEN)`
**Cause:** the agent has no client certificate yet AND no enrollment token to get
one (the token is single-use — the agent deletes it from the registry after a
successful enroll). An MSI install done WITHOUT `ENROLLTOKEN=...` writes an empty
token.
**Fix:** re-provide the token (as in 1.1) and restart the service.

### 1.3 `enroll TLS setup: read CA … ca.crt: The system cannot find the file specified` / `enroll_address=localhost:50051`
**Cause:** a BARE MSI install (no config params) leaves the agent on its defaults
— server `localhost:50051` and CA path `SKVC\certs\ca.crt` (which does not exist
unless you put it there). The MSI only RECORDS the CA path in the registry; it
does not copy the CA file.
**Fix — install with ALL four params** (enterprise agents are configured at
deploy time):
```
msiexec /i AegisOneClient.msi /qn ^
  SERVERADDRESS=<host:50051> SERVERNAME=<server-cert-name> ^
  ENROLLTOKEN=<token> CACERTFILE=<full path to the server CA .crt>
```
Or set them by hand and restart:
```
reg add "HKLM\SOFTWARE\AegisOne\Client\Connection" /v AEGIS_SERVER_ADDRESS /t REG_SZ /d <host:50051> /f
reg add "HKLM\SOFTWARE\AegisOne\Client\Connection" /v AEGIS_TLS_SERVER_NAME /t REG_SZ /d <server-cert-name> /f
reg add "HKLM\SOFTWARE\AegisOne\Client\TLS" /v AEGIS_TLS_CA /t REG_SZ /d <path to ca.crt> /f
```
The CA file must actually EXIST at the referenced path, and must be the CA that
signed the server's TLS cert.

---

## 2. Connectivity

### 2.1 Agent can't reconnect; log shows `decrypt client key … message authentication failed`
**Cause:** the DPAPI master key (`SKVC\dpapi.key`) was deleted/corrupted, which
used to also orphan the mTLS client key (sealed under it) → no mTLS → no connect.
**Status:** FIXED — the client key is now sealed with machine DPAPI directly
(survives master-key loss), and existing keys auto-migrate on first load. An agent
broken BEFORE that fix needs a one-time re-enroll:
```
sc stop AegisOneClient
del "C:\Program Files\AegisOne\Client\SKVC\certs\client.crt"
del "C:\Program Files\AegisOne\Client\SKVC\certs\client.key.enc"
reg add "HKLM\SOFTWARE\AegisOne\Client\Enrollment" /v AEGIS_ENROLL_TOKEN /t REG_SZ /d <token> /f
sc start AegisOneClient
```

### 2.2 `connectex: No connection could be made …` / `connection forcibly closed`
**Cause:** the server (mockserver) is down or restarting, not an agent fault. The
agent retries with backoff and reconnects automatically; on reconnect it
re-sends buffered reports.
**Fix:** ensure the server is up and reachable on `host:50051`. Transient during
testing when you restart the mockserver — expected.

### 2.3 `key_tampered` alert at startup (kind=corrupt / deleted)
**Cause:** `dpapi.key` was corrupted or deleted. This is the integrity alert, not
a crash — the agent **self-heals** (regenerates the key) and keeps running. The
re-fetchable at-rest data (cache) and buffered events reset; the client key
survives (see 2.1).
**Fix:** none needed; it is a security alert. Repeated/unexpected occurrences in
production should be investigated (tamper attempt).

---

## 3. Install / MSI

### 3.1 Fresh install shows only `logs` + the exe; other folders missing
**Cause:** older agent builds created `buffer/cache/certs/deferred/download/jobs/
tmp` lazily on first use; a just-installed agent looked bare. Also an MSI built
with an OLD exe installs that old behavior.
**Fix:** current agent pre-creates the full directory layout at startup
(`ensureAgentDirs`). Rebuild the MSI so it bundles the current exe (see 3.2).

### 3.2 Rebuilding the MSI (picks up the current agent exe)
The MSI embeds `..\build\AegisOneClient.exe`, so rebuild it after any agent change:
```
# from installer/ , build the agent to build/ first (stripped):
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o build/AegisOneClient.exe ./cmd/agent/
wix build installer/Package.wxs -ext WixToolset.Util.wixext -ext WixToolset.UI.wixext -o installer/AegisOneClient.msi
```
(WiX toolset required: `dotnet tool install --global wix`, plus the Util/UI
extensions.) A stale MSI = stale exe = the lazy-folder behavior of 3.1.

### 3.3 Reinstall leaves a duplicate in Add/Remove Programs
**Cause:** ProductCode auto-generated per build. **Status:** handled —
`MajorUpgrade AllowSameVersionUpgrades="yes"` removes the prior install in place.

---

## 4. Memory / performance

### 4.1 Agent RSS looks high (e.g. 40+ MB)
**Causes & facts:**
- A fresh agent is **~5-6 MB** private working set. "Active private working set"
  excludes the mapped binary, so binary size does not move that number.
- A value like 40+ MB after a long, churny session (100+ reconnects, repeated
  key corruption/re-enroll) is accumulation that a **reboot / reinstall** clears;
  a plain `sc stop/start` may not. (Under investigation — slow per-reconnect
  growth; not seen under a normal stable connection.)
- WMI is NOT the cause (a fresh agent runs the full WMI driver inventory and
  stays ~5 MB).
**Fixes already in place:** stripped binary (`-s -w`), `tuneMemory` (soft
`GOMEMLIMIT` + periodic `FreeOSMemory`), and removing an inventory "drift storm"
(uptime was in the change-fingerprint, forcing a full re-send every cycle).
**If it creeps in production:** capture it over time and report — investigate the
reconnect path.

---

## 5. Policy

### 5.1 `set_policy` → `NOT_APPLICABLE … policy not newer than the active policy/floor (anti-rollback)`
**Cause:** the policy's `issued_at` is **not greater** than the highest ever
applied (the sealed anti-rollback floor). The floor only goes UP.
**Fix:** always use an increasing `AEGIS_POLICY_ISSUED_AT` (Unix seconds). If an
earlier test pushed a high value (e.g. 1900000000), subsequent policies must
exceed it. Check the current effective policy in the agent log
(`policy: loaded agent policy summary=…`).

### 5.2 A duration override (e.g. `AEGIS_DEX_WINDOW=30s`) seems ignored
**Cause (now fixed):** `durationFromEnv` used to accept only a bare integer
(seconds). **Status:** FIXED — it now accepts Go duration strings (`30s`, `5m`,
`6h`) and integer-seconds. Values like `0` or unparseable keep the default.

### 5.3 cmd.exe `set A=1 & set B=2` chaining breaks numeric env values
**Cause:** cmd captures the space before `&` into the value (`"1 "`), so numeric
parses fail. **Status:** the mockserver trims whitespace on its `AEGIS_POLICY_*`
reads; still, prefer one `set` per line for clarity.

---

## 6. Inventory / DEX

### 6.1 Full inventory sends very frequently
**Expected:** Tier-3 full inventory is **6 h + send-on-change (drift)**; frequent
sends during testing are **reconnect-triggered** (each server restart = a
reconnect snapshot). A stable, connected agent sends at the 6 h cadence.
Verify no low `AEGIS_FULL_INVENTORY_INTERVAL` override in the registry.

### 6.2 `dex-health … boot_ms=0`
**Expected on some SKUs:** boot duration comes from the
Microsoft-Windows-Diagnostics-Performance/Operational log, which is **disabled by
default on Windows Server**. Reliability counts (System log) and CPU/mem
telemetry work regardless.

### 6.3 `app_crashes` count looked inflated / showed non-app "faults"
**Cause (now fixed):** EventIDs 1000/1002 are reused by many providers (service
log lines), so an EventID-only filter over-counted. **Status:** FIXED — queries
now filter by Provider (`Application Error` / `Application Hang`), and the per-app
breakdown (`app_faults`) shows the real faulting app names.

---

## Quick reference — key locations
| What | Where |
|---|---|
| Install / SKVC dir | `C:\Program Files\AegisOne\Client\SKVC` |
| Config (registry) | `HKLM\SOFTWARE\AegisOne\Client\{Connection,Enrollment,TLS,Paths,Settings}` |
| Agent log | `SKVC\logs\agent.log` |
| Audit trace | `SKVC\logs\audit.log` (read with `auditcheck.exe`) |
| Service | `AegisOneClient` (LocalSystem, auto-start, restart-on-failure) |
| Master key / client key | `SKVC\dpapi.key` / `SKVC\certs\client.key.enc` |
| Deferred (maintenance window) | `SKVC\deferred\` |
