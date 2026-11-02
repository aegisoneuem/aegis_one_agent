# Aegis One — Tester Setup Guide (Start Here)

**Audience:** QA engineer, first day on Aegis One. Hindi/Hinglish comments OK;
commands and section names are plain English so copy-paste search works.

**Prerequisites assumed:** Windows admin basics (run cmd as Administrator,
`sc.exe`, `services.msc`, `msiexec`). Nothing about this project.

---

## 1. Yeh guide tujhe kya karegi (what this guide gives you)

By the end of this walkthrough you will have:

1. A **mockserver** running on your dev laptop that pretends to be the real
   Aegis One server (same gRPC contract, same mTLS, no production DB needed).
2. An **AegisOneClient** agent installed as a Windows service on a target
   VM, enrolled with a dev certificate, and connected back to your
   mockserver.
3. Three **smoke tests** passing end-to-end — proof that the pipe works.
4. A pointer to the three test guides (patching, softdist, nsh) you will
   execute as your actual QA work.

If any step fails, Section 10 (Troubleshooting) covers the known traps.
If you are stuck for more than ~15 minutes on any step, loop back to Vijay
with the exact error from the agent log or the mockserver console — don't
guess.

---

## 2. Setup ka picture (architecture at a glance)

```
+---------------------------+                     +---------------------------+
| Your dev laptop           |                     | Target Windows VM         |
|                           |                     |                           |
|   mockserver.exe          | <===== mTLS =====>  |   AegisOneClient service  |
|   (dist/, port :50051)    |      (gRPC)         |   (installed from MSI)    |
|                           |                     |                           |
|   NSH REPL (optional)     |                     |   agent.log / SKVC/       |
+---------------------------+                     +---------------------------+
         |                                                      |
         +------------------- certs/ca.crt --------------------+
         (same CA pem on both sides — trust anchor)
```

The agent dials **one TCP port** on the mockserver (default `:50051`) over
mTLS: the agent presents a client cert, the mockserver presents a server
cert, both are signed by the same dev CA. All traffic — enrollment, signed
commands, action status, inventory — rides this single persistent gRPC
stream. There is no second port, no UDP wake, no inbound connection to the
agent. If the stream drops, the agent reconnects by itself and the mock
sees it come back up.

---

## 3. Pre-requisites — Your dev laptop

| Item | Required | Notes |
|---|---|---|
| OS | Windows 10 or 11 | Linux also works for the mockserver, but this guide shows Windows. |
| Rights | Local admin | Needed only to open ports in firewall if you choose to. |
| Shell | `cmd.exe` or PowerShell | No admin shell required to run mockserver itself. |
| Free TCP port | `:50051` on the laptop interface the VM can reach | If taken, override with `AEGIS_MOCK_PORT` — rare. |
| Go / Git | **Not needed** | You run pre-built binaries only. |
| `dist/` bundle | yes | from Vijay / internal share / repo clone — see table below |

### What's in `dist/`

| File | What it is |
|---|---|
| `AegisOneClient.exe` | Agent binary (Windows service exe) |
| `AegisOneClient.msi` | Installer for the agent — this is what runs on the VM |
| `mockserver.exe` | The fake server + NSH REPL — runs on your dev laptop |
| `certgen.exe` | Dev-only CA + server + client cert generator |
| `softdist-handoff.json` | SHA256s + manifest for the above — proves which binaries you have |
| `README.md` | One-line pointer to this guide |

Where to get it: Vijay keeps the latest `dist/` on the shared drive or the
GitHub repo (`aegisoneuem/aegis_one_agent`). Grab the whole folder.

---

## 4. Pre-requisites — Target Windows VM

| Item | Required | Notes |
|---|---|---|
| OS | Windows 10 or 11 | Fresh install preferred — fewer surprise registry keys / installed apps. |
| RAM / Disk | 2 GB RAM, 20 GB disk | More fine; less may fail scan tests. |
| Admin account | yes | Needed to run `msiexec /i AegisOneClient.msi`. |
| Network | Outbound TCP to dev laptop IP + `:50051` | VM must be able to `telnet <laptop-ip> 50051`. |
| Firewall | Allow outbound to that endpoint | Default usually allows; LAN/VPN rules may block. |
| Clock | Within ~5 minutes of real time | mTLS fails if the VM clock is way off. |

### Two VM env vars you WILL want to set BEFORE install

> **⚠ GOTCHA — repeat offender:** `AEGIS_DEVICE_STATE_INTERVAL` is **plain
> INTEGER SECONDS**. It is NOT a Go duration. `"30s"` silently falls back to
> the 5-minute default. Use `30`, `60`, `300` — nothing else.
>
> ```cmd
> setx AEGIS_DEVICE_STATE_INTERVAL 30 /M
> ```
>
> `/M` writes to Machine scope so the service picks it up on next start.
> Verify: `reg query "HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment" /v AEGIS_DEVICE_STATE_INTERVAL`

Default fast-lane cycle is 5 minutes — fine for production, too slow for
testing. 30 seconds is the recommended testing value. If you forget this,
every "wait for inventory refresh" step in the test guides will take 5x
longer than it should.

> **`AEGIS_WSUS_SCAN_CAB` (needed for patch scan tests, UC-5 onward):**
> Path to the downloaded `wsusscn2.cab` file. If this env var is empty the
> patch scan tests silently skip with the log line `patch compliance scan
> disabled (no AEGIS_WSUS_SCAN_CAB configured)`. Download the cab from the
> Microsoft Update Catalog (search `wsusscn2.cab`) and set:
>
> ```cmd
> setx AEGIS_WSUS_SCAN_CAB C:\patches\wsusscn2.cab /M
> ```
>
> Patching smoke tests and UC-1..UC-4 do NOT need the cab; only reconciliation
> scan + install flows do.

---

## 5. Phase A — Certificates (one-time, DEV ONLY)

> **🚨 DEV ONLY — NOT FOR PRODUCTION:** `certgen.exe` writes private keys to
> disk in plain PEM. This is a dev convenience so a tester does not need a
> real PKI. Production deployments use managed PKI or the customer's CA
> service. Do NOT re-use these certs outside a test tenant.

### Step A.1 — Generate the dev trust set

On the dev laptop, in whatever folder you want certs to live:

```cmd
cd C:\aegis-test
..\dist\certgen.exe
```

(Or just copy `certgen.exe` into `C:\aegis-test` and run it directly.)

**Expected output:**
```
wrote CA, server, client certs to certs
  server uses: server.crt + server.key + ca.crt
  agent  uses: client.crt + client.key + ca.crt
```

**Expected files in `C:\aegis-test\certs\`:**
```
ca.crt       ca.key
server.crt   server.key
client.crt   client.key
```

| File | Who uses it |
|---|---|
| `ca.crt` | Both sides — the trust anchor. Agent gets a copy; mockserver gets a copy. |
| `ca.key` | Mockserver only (used to re-sign per-agent client certs during enroll). |
| `server.crt` + `server.key` | Mockserver — presents these to agents. |
| `client.crt` + `client.key` | Pre-generated test client — the agent re-enrolls and gets its own at runtime; these are a fallback for low-level tests. |

### Step A.2 — Pick an enroll token

The enroll token is a shared secret between mockserver and agents. Any
non-empty string works for the dev tenant. Pick something like
`tenant-dev-01` and remember it — you'll put it in both places (mockserver
env var + VM aegisone.cfg).

```cmd
set ENROLL_TOKEN=tenant-dev-01
```

(That's the shell for the current window; set it also in the mockserver
start-up window in Phase B.)

### Step A.3 — (Optional) ask Vijay for pre-generated dev CA

If you want to skip A.1 entirely, Vijay can hand you a ready-made
`certs/` folder + token. Same files, same semantics. Still DEV ONLY.

---

## 6. Phase B — Start mockserver on dev laptop

Admin rights NOT needed to run the mockserver (user-space TCP port).

### Step B.1 — Copy or stay in the folder with `certs/`

```cmd
cd C:\aegis-test
```

(The mockserver looks for `certs/ca.crt`, `certs/server.crt`,
`certs/server.key`, `certs/ca.key` in its current working directory by
default. Override with `AEGIS_TLS_CA` / `AEGIS_TLS_CERT` / `AEGIS_TLS_KEY` /
`AEGIS_CA_KEY` if you keep certs elsewhere.)

### Step B.2 — Start the server

```cmd
set AEGIS_ENROLL_TOKEN=tenant-dev-01
..\dist\mockserver.exe
```

**Expected first line:**
```
Mock server listening on :50051 (single port: mTLS ops + enrollment; token=tenant***01)
```

Leave this window open — logs appear here as agents connect. Ctrl+C stops
the server.

### Step B.3 — Verify from the dev laptop itself

In another cmd window:

```cmd
telnet 127.0.0.1 50051
```

**Expected:** screen clears (connection accepted, raw TCP, no banner).
Hit Ctrl+] + `quit` to exit telnet. If telnet is not installed, use
PowerShell:

```powershell
Test-NetConnection 127.0.0.1 -Port 50051
```

**Expected:** `TcpTestSucceeded : True`.

If NOT connected:
- Check mockserver window — did it print `Mock server listening on :50051`?
- Any other process on `:50051`? `netstat -ano | findstr :50051` should
  show only the mockserver's PID.

### Step B.4 — The `nsh` SUBCOMMAND (use this for NSH tests only)

> **🚨 CORRECTION:** If anyone (including earlier docs or chat history)
> told you to use `AEGIS_NSH=1` as an env var — that is **wrong**. The
> correct invocation is a positional subcommand:
>
> ```cmd
> ..\dist\mockserver.exe nsh
> ```
>
> This starts the normal server AND opens an interactive REPL in the same
> window with Tab completion. You still get agent connection logs; they
> are interleaved with the prompt. Use this ONLY while running the NSH
> test guide. For patching and softdist tests, use plain `mockserver.exe`
> so the window shows clean server logs.

Full subcommand list (for reference):

```
mockserver.exe                     Normal server
mockserver.exe server              Same as above
mockserver.exe nsh                 Server + interactive NSH REPL
mockserver.exe script <file.nsh>   Server + one non-interactive NSH job
mockserver.exe pack <src> <zip>    Build a signed bundle ZIP
mockserver.exe secrets <file>      Enter encrypted test credentials
mockserver.exe help                Print usage
```

---

## 7. Phase C — Install the agent on the target VM

### Step C.1 — Copy files into the VM

Copy these three files from your dev laptop into a folder on the VM
(e.g. `C:\AegisInstall\`):

- `AegisOneClient.msi` (from `dist/`)
- `silent-install.cmd` + `aegisone.cfg` (from the repo's `installer/`
  folder — ask Vijay if you don't have the clone)
- `ca.crt` (from `certs/` in Phase A — this is the trust anchor)

### Step C.2 — Edit `aegisone.cfg`

Open in Notepad. Replace with your real values; `#` lines are comments.

```ini
# AegisOne Client — silent install config
SERVERADDRESS=<DEV_LAPTOP_IP>:50051
SERVERNAME=aegis-server
ENROLLTOKEN=tenant-dev-01
CACERTFILE=C:\AegisInstall\ca.crt
```

| Field | What it means |
|---|---|
| `SERVERADDRESS` | `host:port` the agent dials. Put the dev laptop's IP + `50051`. If the VM is NAT'd, use the host-only IP. |
| `SERVERNAME` | SAN name the agent verifies on the server cert. Default dev value: `aegis-server`. certgen.exe writes a server cert with this SAN. |
| `ENROLLTOKEN` | Must EXACTLY match `AEGIS_ENROLL_TOKEN` on the mockserver. One typo = enrollment refused. |
| `CACERTFILE` | Absolute path on the VM to the `ca.crt` copied from Phase A. |

### Step C.3 — Run the installer

**Open cmd as Administrator** on the VM:

```cmd
cd C:\AegisInstall
silent-install.cmd
```

**Expected console output:**
```
Installing "C:\AegisInstall\AegisOneClient.msi"
Properties: SERVERADDRESS="<DEV_LAPTOP_IP>:50051" SERVERNAME="aegis-server" ENROLLTOKEN="tenant-dev-01" CACERTFILE="C:\AegisInstall\ca.crt"
[OK] install complete
```

**If it fails:** read `C:\AegisInstall\silent-install.log` — msiexec writes
a verbose MSI log there.

Alternative one-liner (equivalent, skips the batch wrapper):
```cmd
msiexec /i AegisOneClient.msi SERVERADDRESS=<DEV_LAPTOP_IP>:50051 SERVERNAME=aegis-server ENROLLTOKEN=tenant-dev-01 CACERTFILE=C:\AegisInstall\ca.crt /qn /l*v install.log
```

### Step C.4 — Verify the service is running

```cmd
sc query AegisOneClient
```

**Expected (relevant lines):**
```
SERVICE_NAME: AegisOneClient
        STATE              : 4  RUNNING
```

### Step C.5 — Agent log

Default install dir is `C:\Program Files\AegisOne\`. The agent writes
runtime files under `C:\ProgramData\SKVC\` (SKVC = secure KV store).

```cmd
type "C:\ProgramData\SKVC\logs\agent.log" | more
```

**Expected recent lines (first startup):**
```
enroll: request sent to <DEV_LAPTOP_IP>:50051 (token=tenant***01)
enroll: cert issued device_id=<guid>
connected to server
heartbeat sent
```

If you see `enroll: refused` → token mismatch, server CA mismatch, or
clock skew. Section 10 covers each.

---

## 8. Phase D — Verify the mockserver sees the agent

### Step D.1 — Watch mockserver console

In the mockserver window on your dev laptop, within ~10 seconds of starting
the agent service, you should see:

```
Received Heartbeat from device_id=<guid> hostname=<vm-name> os=windows agent_version=...
agent connected device_id=<guid>
```

### Step D.2 — NSH `hosts`

Stop the plain mockserver (Ctrl+C) and restart in NSH mode:

```cmd
..\dist\mockserver.exe nsh
```

At the `nsh>` prompt:

```
nsh> hosts
HOSTNAME               DEVICE_ID               STATUS
<vm-hostname>          <guid>                  live
```

Your VM should appear with `STATUS=live`. If not, agent is not connected —
check the agent log and come back.

---

## 9. Smoke tests (3 tests, 5 minutes — go / no-go)

If all three pass, the pipe is end-to-end working and you can move to the
real test guides. If ANY fails, do NOT proceed — go to Section 10 or ping
Vijay.

### Smoke-1 — `hosts` lists the VM

```
nsh> hosts
```

**Pass:** VM hostname + `live` status. **Fail:** VM missing — agent did
not connect. Section 10.

### Smoke-2 — Run a command on the VM

```
nsh> cd <vm-hostname>
<vm-hostname>> run hostname /shell cmd
```

**Pass:** VM's hostname echoes back in NSH, exit=0.
**Fail:** `OFFLINE` or hang → agent reconnected but command channel broken.

### Smoke-3 — On-demand inventory refresh (also exercises UC-19)

```
<vm-hostname>> inventory refresh
```

**Pass (within 5s):**
```
<vm-hostname>: fresh inventory (N apps, collected_at=...)
nsh: refresh complete — 1/1 received, 0 timed out — run `inventory apps` to view
```

Then:

```
<vm-hostname>> inventory apps
```

**Pass:** a non-empty table of installed apps appears with columns
`DISPLAY_NAME / VERSION / PUBLISHER / HIVE`.

If all three pass → you are **ready for the test guides**.

---

## 10. Troubleshooting — common first-time pitfalls

| Symptom | Likely cause | Fix |
|---|---|---|
| `sc query AegisOneClient` → STOPPED immediately after install | enroll failed, service exited | `type "C:\ProgramData\SKVC\logs\agent.log"` → read the enroll error line |
| agent log: `enroll: refused: invalid token` | `ENROLLTOKEN` on VM ≠ `AEGIS_ENROLL_TOKEN` on mockserver | re-check both, redo Phase C.3 |
| agent log: `tls: unknown authority` or `x509: certificate signed by unknown authority` | `ca.crt` on VM ≠ the one mockserver uses | re-copy `ca.crt` from `certs/` on dev laptop to the VM path in `CACERTFILE` |
| agent log: `connection refused` | mockserver not running or wrong IP | restart mockserver, verify `telnet <ip> 50051` from the VM |
| mock: `no cert verified` | mockserver `certs/` folder empty or wrong | run `certgen.exe` again in the mockserver's working directory |
| NSH `inventory apps` → `no DeviceStateReport received yet` | agent hasn't sent one yet | run `inventory refresh` first (UC-19); or wait one `AEGIS_DEVICE_STATE_INTERVAL` cycle |
| `AEGIS_DEVICE_STATE_INTERVAL` ignored, cycle stays at 5 min | value was `30s` not `30` | fix: `setx AEGIS_DEVICE_STATE_INTERVAL 30 /M` and restart service |
| patch scan tests silently skip, log says `patch compliance scan disabled` | `AEGIS_WSUS_SCAN_CAB` unset | download wsusscn2.cab, `setx AEGIS_WSUS_SCAN_CAB C:\patches\wsusscn2.cab /M`, restart service |
| mTLS handshake fails with `bad certificate` and system clock looks weird | clock skew > ~5 min | `w32tm /resync` on VM, redo enroll (`sc stop AegisOneClient` + `sc start AegisOneClient`) |
| NSH prompt looks like `[1 host]>` when you only intended one target | you ran `cd` with a wildcard | plain `cd <hostname>` for single target |
| Trying to run NSH commands and getting `OFFLINE` from the device | plain `mockserver.exe` restarted, lost in-memory `liveAgents` map | agent will reconnect in <30s; retry |

### A few sc / registry quick refs

```cmd
sc stop AegisOneClient
sc start AegisOneClient
sc delete AegisOneClient               :: full uninstall of the service (DOES NOT delete files)
reg query "HKLM\SYSTEM\CurrentControlSet\Services\AegisOneClient"
```

### Where runtime state lives on the VM

```
C:\Program Files\AegisOne\                 program files (binaries, config.json)
C:\ProgramData\SKVC\logs\agent.log         primary agent log
C:\ProgramData\SKVC\logs\softdist\         softdist action transcripts
C:\ProgramData\SKVC\logs\run\              run_command transcripts
C:\ProgramData\SKVC\logs\shell\            open_shell session transcripts
C:\ProgramData\SKVC\logs\nsh\              NSH-driven captures
C:\ProgramData\SKVC\cache\                 encrypted content cache
```

---

## 11. What's next

Three guides, in this order:

| Guide | What it covers | When to run |
|---|---|---|
| `docs/tester-guide-patching.md` | Windows patch install/uninstall, local scan, device state report, third-party installed-apps inventory (UC-18), HKU per-user parity + on-demand refresh (UC-19) | Day 1-2 — core compliance pipeline |
| `docs/tester-guide-softdist.md` | Software distribution A-K blocks: multi-file packages, pre/post commands, dependencies, detection rules, 7-Zip + Firefox real-vendor flows, rollback, crash recovery, descendant kill, reboot handling, bandwidth throttling | Day 2-5 — biggest surface |
| `docs/tester-guide-nsh.md` | NSH REPL: ad-hoc commands, file operations, scripting (heredoc / variables / if / for), live shell, fleet fan-out, inventory | Day 3+ — operator-facing surface |

### When to loop back to Vijay vs keep going

| Situation | What to do |
|---|---|
| A test case PASSES as the guide describes | Record in the sheet, move on |
| A test case FAILS with a clean error that matches a known troubleshooting row | Fix locally, retry once, if still fails → record and move on, flag to Vijay in the end-of-day summary |
| An **UNEXPECTED** error / crash / hang / destroyed state | STOP. Capture logs (`C:\ProgramData\SKVC\logs\agent.log`, mockserver window, `install.log`). Message Vijay with the symptom + logs — don't guess, don't delete, don't retry |
| MSI install itself refuses | STOP. Share `silent-install.log` with Vijay |
| Mockserver crashes | STOP. Share the full mockserver window log + what the last NSH command was |

**Golden rule:** if you are not sure whether something is a known limitation
or a real bug, assume it's a bug and flag it. False positives are cheap;
missed regressions aren't.

---

## 12. One-page quick-start (TL;DR for the second VM onward)

```cmd
:: ==== DEV LAPTOP ====
cd C:\aegis-test
..\dist\certgen.exe                              :: once
set AEGIS_ENROLL_TOKEN=tenant-dev-01
..\dist\mockserver.exe                           :: or `mockserver.exe nsh` for NSH tests

:: ==== TARGET VM (admin cmd) ====
setx AEGIS_DEVICE_STATE_INTERVAL 30 /M           :: GOTCHA — INTEGER seconds, not "30s"
setx AEGIS_WSUS_SCAN_CAB C:\patches\wsusscn2.cab /M     :: only if running patch-scan tests
cd C:\AegisInstall
notepad aegisone.cfg                             :: set SERVERADDRESS / ENROLLTOKEN / CACERTFILE
silent-install.cmd
sc query AegisOneClient                          :: expect STATE = 4 RUNNING

:: ==== BACK ON DEV LAPTOP (NSH) ====
..\dist\mockserver.exe nsh
nsh> hosts                                       :: VM should appear
nsh> cd <vm-hostname>
<vm-hostname>> inventory refresh
<vm-hostname>> inventory apps                    :: non-empty table = PASS
```

That's it. Setup done.
