# Server/Console Integration — DEX, Custom Attributes & Tamper-Protection

**Audience:** the Aegis One **server & console teams**.
**Purpose:** everything the server side needs to consume and act on the agent
features built recently — DEX telemetry, the custom-attribute engine, and
tamper-protection (token/passcode-gated stop & uninstall). All proto is in
`aegis_one_proto/grpc/agent.proto`; field numbers below are authoritative.

> The agent is the **collector + enforcement point**; the server is the **policy +
> scoring + presentation point**. The agent reports facts and verifies
> server-signed commands; the server computes scores, stores history, surfaces
> dashboards, and issues the signed control commands described here.

---

## 1. DEX (Digital Employee Experience)

The agent streams two things: a **windowed performance telemetry** message and a
**device-stability health** block inside the full inventory. Together they are the
BigFix-DEX "scorable" set. The agent does **not** compute a score — that is the
server's job (§1.3).

### 1.1 `DexTelemetry` (windowed performance)

Sent on its own, **buffered + survives disconnect**, outside the change-gated
inventory. Default cadence: sample every **15 s** (`AEGIS_DEX_SAMPLE_INTERVAL`),
aggregate + send every **5 m** (`AEGIS_DEX_WINDOW`). One message = one window.

| # | Field | Meaning |
|---|---|---|
| 1 | `device_id` | device the window is for |
| 2 | `collected_at` | unix seconds at window end |
| 3 | `window_seconds` | window length |
| 4 | `sample_count` | samples aggregated (idle ticks only; see note) |
| 5-7 | `cpu_avg/min/max` | CPU utilization %, over the window |
| 8-10 | `mem_used_avg/min/max` | physical memory in-use % (available% = 100 − used) |
| 11-13 | `disk_queue_avg/min/max` | PhysicalDisk(_Total) avg disk queue length |
| 14-16 | `disk_iops_avg/min/max` | disk transfers/sec |
| 17-19 | `disk_latency_ms_avg/min/max` | avg ms per transfer |
| 20-22 | `net_rtt_ms_avg/min/max` | ICMP RTT to the ping target (reachable probes) |
| 23 | `net_loss_pct` | % of probes with no reply (packet loss) |
| 24 | `net_target` | IP that was probed (default gateway or `AEGIS_DEX_PING_TARGET`) |
| 25 | `battery_present` | false on desktops/servers/VMs |
| 26 | `battery_ac_online` | true = on AC power |
| 27-29 | `battery_charge_pct_avg/min/max` | charge %; min shows drain |
| 30 | `wifi_present` | false when no Wi-Fi adapter |
| 31 | `wifi_connected` | associated to an AP |
| 32-34 | `wifi_signal_pct_avg/min/max` | signal quality % |
| 35 | `wifi_ssid` | current SSID |

**Resource-governor note:** while the agent is running a patch install or software
deploy, sampling is **paused** (those ticks are dropped) so the numbers reflect the
user's experience, not the agent's own install load. So `sample_count` may be lower
than `window_seconds / sample_interval` during a deploy window — that is expected,
not data loss.

### 1.2 `DexHealth` (device stability) — inside `FullInventoryReport.dex_health`

Collected with the full inventory (Tier-3: ~6 h + on-change + reconnect). Read
natively from the Windows event log (no WMI/PowerShell).

| Field | Meaning |
|---|---|
| `unexpected_shutdowns` | System event 6008 over `window_days` |
| `bugchecks` | BSOD (WER-SystemErrorReporting 1001) |
| `dirty_power_events` | Kernel-Power 41 |
| `app_crashes` | Application Error 1000 |
| `app_hangs` | Application Hang 1002 |
| `app_faults[]` | **per-app** breakdown: `{name, crashes, hangs}`, top 25 — "which app is unstable" |
| `boot_duration_ms` | last boot time (Diagnostics-Performance 100); 0 on SKUs where that channel is off (e.g. Windows Server) |
| `window_days` | lookback (30) |

### 1.3 Building a DEX score (server side — NOT built in the agent)

The agent gives raw signals; the server computes/stores the score & trends.
Suggested rollup (BigFix-style weighting is a starting point, tune to taste):

- **Device health** ← reliability counts (shutdowns/bugchecks/dirty-power), boot
  duration, CPU/mem/disk pressure, net RTT/loss.
- **App health** ← app crashes/hangs (+ the per-app `app_faults` for drill-down).
- **Sentiment** ← user surveys — **not provided by the agent**; a separate feature
  if you want the full BigFix 0.34/0.33/0.33 model.

**Still open for full BigFix-DEX parity** (either server-side or future agent work,
flag if you need them): composite score computation, sentiment surveys, per-app
CPU/mem, GPU usage, TCP SRTT / failure-ratio, logon duration, per-crash-event
detail, and the console dashboards. The agent-side **collection foundation is done**;
these are the remaining pieces.

---

## 2. Custom Attributes (BigFix "retrieved properties" / Tanium "sensors")

Unlimited, server-defined properties the **agent evaluates locally** and streams in
`FullInventoryReport.custom_properties` (a `map<string,string>`, field 10). No CMDB,
no per-attribute schema change.

### 2.1 The server pushes definitions — `set_custom_attributes`

Send a **signed** `Command{type:"set_custom_attributes"}` whose `payload` is a
marshaled `CustomAttributeSpec`:

```proto
message CustomAttributeSpec {
  repeated CustomAttributeDefinition definitions = 1;
  int64 issued_at = 2; // must EXCEED the agent's stored value (anti-rollback)
}
message CustomAttributeDefinition {
  string name = 1;                // the property name the agent reports
  string provider = 2;            // one of the providers below
  map<string,string> args = 3;    // provider-specific
}
```

**Providers (all READ-ONLY; unknown providers are refused — no arbitrary exec):**

| provider | args | returns |
|---|---|---|
| `env` | `name` | environment variable value |
| `file` | `path`, `attr`=`exists`\|`size`\|`version` | `true`/`false`, byte size, or `a.b.c.d` version |
| `registry` | `root` (HKLM/HKCU/HKCR/HKU), `key`, `name` | registry value (string or integer→string) |
| `admin_tag` | `name` | value under `HKLM\SOFTWARE\AegisOne\Client\Tags` (deploy-time tags: dept/site) |
| `service_state` | `name` | `running`\|`stopped`\|`not_installed`\|… |

- **Signing + anti-rollback:** the command is Ed25519-verified by the agent; the
  agent persists the signed definitions and re-verifies at startup. `issued_at`
  must strictly increase or the push is `NOT_APPLICABLE`.
- **Reporting:** the agent evaluates the active definitions each inventory cycle; a
  provider that fails/misses is simply omitted (no blank). Values are capped at
  2048 bytes.
- The agent emits audit `custom_attributes_applied` on apply.

---

## 3. Tamper-Protection (stop / disable / uninstall gating)

**Goal:** when enabled, the service cannot be stopped/disabled/uninstalled without a
**server-issued credential**. The server/console is responsible for **issuing** the
credential and for **issuing the control commands** below.

### 3.1 Two credential mechanisms — server team chooses (agent accepts both)

The agent's `authorizeUnprotect(input)` tries a **short passcode** first, then a
**long token**. You can use either.

**A. Short passcode (ManageEngine-style, 8-10 chars) — `set_unprotect_secret`**
Send a signed `Command{type:"set_unprotect_secret"}`, `payload` = JSON:

```json
{ "mode": "static", "passcode": "Open1234", "issued_at": 1794745159 }
```
- The agent stores **only a PBKDF2-HMAC-SHA256 hash** (200k iters, random salt) — it
  never stores the passcode. `issued_at` anti-rollback. Verification is **rate-
  limited** (5 failures → 5-minute lockout) against online brute force.
- **Clear / turn off:** send the same command with `{"mode":"clear","issued_at":…}`
  (removes the passcode; anti-rollback). Do **not** tell admins to delete files — a
  deleted passcode file is **fail-closed** (the service stays locked + emits
  `unprotect_secret_tampered` until you re-push or clear).
- Rotation is your call: re-push a new passcode with a higher `issued_at` whenever
  you like (hourly/daily). The agent accepts whatever verifies.

**B. Long signed token (crypto-grade, stateless)**
A self-contained token the agent verifies statelessly (no stored secret):
`base64url(payloadJSON) + "." + base64url(ed25519_sig)` where
`payloadJSON = {"device":"<id>|*","exp":<unix|0>,"nonce":"<rnd>"}`, signed with the
**same Ed25519 key** the agent trusts for commands. `device:"*"` = any device,
`exp:0` = never expires. This is the strong option for high-security sites.

### 3.2 Stopping the agent — `stop_agent`

Send a signed `Command{type:"stop_agent"}` whose `payload` = the passcode **or** the
token (raw string). The agent verifies via `authorizeUnprotect` and shuts down
cleanly (no recovery-restart). Invalid credential → refused + audited
(`agent_stop_refused`); valid → `agent_stop_authorized` + clean stop.

- **Local operator path (no server):** `AegisOneClient.exe stop --passcode <code>`
  (or `--token <tok>`, or `--passcode-prompt`). It POSTs to the agent's localhost
  control endpoint; same `authorizeUnprotect` check.
- **Start is never gated** — you always want the agent running; only
  stop/disable/uninstall are protected.

### 3.3 What is enforced today vs next

- **DONE:** `sc stop` / services.msc **Stop** fail (ERROR 1052) for everyone incl.
  admin/SYSTEM when a passcode is set (or `AEGIS_TAMPER_PROTECT=1`). Setting/clearing
  a passcode takes effect **immediately** (no restart). Install-dir NTFS ACL
  hardening (non-admin can't modify agent files). Binary self-integrity
  (`binary_tampered`). Passcode-file delete is fail-closed.
- **NEXT (agent work in progress):** service DACL to also block `sc config disabled`
  (disable) and `sc delete`; **MSI uninstall gate** (uninstall requires the
  passcode); **watchdog** self-heal to last-known-good; then Phase-B (PPL/ELAM
  kernel driver + TPM) for absolute admin-proofing — deferred to EDR work.

### 3.4 Server responsibilities for a complete tamper story

1. **Issue credentials:** mint tokens / set passcodes (static or rotating) per your
   policy; store/rotate them in the console; show the current credential to
   authorized operators.
2. **Agent-silent detection + re-deploy:** if a device stops checking in, alert and
   (optionally) re-deploy — this is the automated recovery for the "admin wiped the
   agent" case (the agent cannot defend that alone).
3. **Re-assertion:** the agent self-heals from the server — if its passcode/custom-
   attr/policy state is wiped locally, re-push on reconnect (the agent already treats
   a higher `issued_at` as authoritative).

---

## 4. Audit events the agent emits (for the server audit store)

| event_type | when |
|---|---|
| `custom_attributes_applied` | signed definitions applied |
| `unprotect_secret_set` / `unprotect_secret_cleared` | passcode set / cleared |
| `unprotect_secret_tampered` | passcode file deleted but marker present (fail-closed) |
| `agent_stop_authorized` / `agent_stop_refused` | token/passcode stop accepted / rejected |
| `binary_tampered` | agent exe changed without a version change |
| `key_tampered`, `policy_tampered`, `policy_applied` | existing security events |

All signed-command results also come back as `ActionStatus` (QUEUED→RUNNING→
SUCCESS/FAILED/NOT_APPLICABLE) on the command stream, same as other commands.

---

## 5. Trust model (one paragraph)

Trust is **cryptographic, not binary-based**: command execution requires an Ed25519
server signature, transport is mTLS (client cert), identity is DPAPI/TPM-sealed. A
tampered agent binary can break itself but **cannot** impersonate the server or forge
a valid command/credential — so the worst case of tampering is a self-DoS (which the
server detects via agent-silence), never a trusted-but-malicious node.
