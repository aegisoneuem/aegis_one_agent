# NSH Test Guide (Tester-Ready)

**Audience:** external QA engineer. No prior Aegis codebase context required.
**Scope:** Network Shell (NSH) — the operator REPL + non-interactive script
runner on `mockserver.exe`.
**Outcome:** every test below should return the stated PASS line. Anything
else is a FAIL — record it with full stdout/stderr in the Record Sheet.

> Hindi/Hinglish mein kuch jargon rakha gaya hai jahan Vijay ko shortcut
> samajh aati hai. Technical terms (`run_command`, `ActionStatus`, etc.)
> English mein hi hain.

---

## 1. Overview

**NSH = Network Shell.** Yeh ek operator REPL hai jo `mockserver.exe` ke
andar chalta hai. BigFix WebUI / BladeLogic nexec ka equivalent samajh —
operator ek console (REPL) pe baithta hai, ek ya ek-se-zyada connected
agents ko ad-hoc commands bhejta hai, aur live stdout/stderr/exit code
dekhta hai. **Pure text streaming, NO remote-desktop.**

Operator ki saari commands server-side (`mockserver.exe`) interpret hoti
hain. Agent ko sirf tiny signed `run_command` / `collect_file` /
`file_operation` tasks milte hain — kabhi `.nsh` script directly agent
pe nahi jaata. Yani:

- **Server-side interpreter:** `let`, `foreach`, `if`, `source`, `exec`,
  `script ... until END`, variables, aliases — sab server (NSH REPL) pe.
- **Agent-side executor:** server har endpoint-targeted command ko ek
  signed `run_command` ya `file_operation` ya `collect_file` task mein
  pack kar ke bhejta hai; agent apni service identity (LocalSystem) mein
  execute karta hai, output stream kar ke bhejta hai.

Isliye NSH ek **scripting + control** layer hai jo multiple agents ko
coordinate kar sakti hai, lekin har agent se communication kyberically
signed, mTLS-protected gRPC hi hai.

### NSH ke two modes

| Mode | Launched by | Purpose |
|---|---|---|
| **Interactive REPL** | `mockserver.exe nsh` | Operator baithti hai prompt pe, commands type karti hai, output turant dekhti hai |
| **Non-interactive script** | `mockserver.exe script <file.nsh>` OR `set AEGIS_NSH_SCRIPT=path & mockserver.exe script` | Pre-authored script (same language as REPL) run hota hai, outputs capture hote hain, server exit kar deta hai |

Dono modes **same engine** chalaate hain (`pkg/nsh` package). Yani
interactive mein jo kaam karta hai, script mode mein bhi same syntax.

### Why NSH is a dev / admin tool, not a fleet-ops surface

NSH **max 100 targets** par cap hai `inventory refresh` jaise commands
mein (reason: parallel polling, bandwidth burst, mock memory). Agar
50,000-endpoint fleet pe inventory refresh karna ho — woh **server-side
scheduled operation** hogi (batched waves + rate limiting), NSH se nahi.
NSH ka purpose: dev debugging, admin ad-hoc, QA testing, ya chhoti scale
(sub-100) operator interventions.

---

## 2. Pre-reqs

### 2.1 Binaries

Pull from `dist/` on the handoff repo (or ask Vijay for the latest):

- `AegisOneClient.exe` — agent binary (installed on each endpoint VM)
- `AegisOneClient.msi` — installer wrapping the agent (preferred install path)
- `mockserver.exe` — development server + NSH REPL

Match versions. Mixing an OLD agent with a NEW mockserver (or vice versa)
breaks NSH `inventory refresh` / `file_operation` because the signed
command types are additive.

### 2.2 How to START NSH — **IMPORTANT**

> **The CORRECT way is `mockserver.exe nsh` (subcommand).**
>
> There was earlier confusion with an `AEGIS_NSH=1` environment variable
> — that approach is NOT supported. **Do NOT use any `AEGIS_NSH=1` /
> `AEGIS_NSH_MODE=interactive` env var.** The `nsh` subcommand on the
> mockserver command line is the only supported start method for
> interactive mode. Script mode uses the `script` subcommand.

```bat
rem Interactive mode — operator sits here:
mockserver.exe nsh

rem Non-interactive script mode — reads a .nsh file:
mockserver.exe script C:\tests\myscript.nsh

rem Or with env var holding the path:
set AEGIS_NSH_SCRIPT=C:\tests\myscript.nsh
mockserver.exe script
```

Plain `mockserver.exe` (no subcommand) stays in normal server mode and
does NOT auto-drop into NSH, even if the AEGIS_NSH env vars are set.

### 2.3 VM / endpoint requirements

- **At least 1 Windows 10/11 VM** with the agent installed via
  `AegisOneClient.msi`. Fresh VM preferred — no customizations.
- **Prefer at least 2 agents online** so fleet tests (fanout, foreach,
  copy across hosts, `[2 hosts]>` prompt) can run. Many tests still
  pass with 1 agent, but the "fleet" section will be partial.
- Admin rights on the VM to:
  - Install the MSI (`msiexec /i AegisOneClient.msi ...`)
  - Start/stop the `AegisOneClient` service (`sc stop/start`)
  - Create test directories like `C:\temp\AegisNSH-...`

### 2.4 Mockserver machine requirements

- Windows machine with:
  - PowerShell 5.1+ (for test fixture generation)
  - A terminal that preserves ANSI output (Windows Terminal, cmd.exe fine)
  - Firewall allows the mockserver port (default 50051) outbound-reachable
    from the agent VMs
- A directory to run `mockserver.exe` from. Agent uploads land under
  `<mockserver-launch-dir>/uploads/<computer_id>/<output-dir>/<host>/`.
  Default output-dir = `nsh`. Pick a disposable dir like
  `C:\test-mockserver\`.

### 2.5 Certificates and enrollment

Mockserver + agent use mTLS. For a fresh test setup you need:

- Mock CA cert + key on the mockserver machine
- Each agent provisioned with the mock CA via `AegisOneClient.msi`
  properties (ENROLL_TOKEN, CA_PATH, SERVER_ADDR, etc. — see the
  installer `README.md` or `aegisone.cfg`)
- `mockserver-devices.json` persists the mock's device registry across
  restarts; keep it (don't delete between tests)

> **DEV ONLY WARNING:** `cmd/certgen` (if used) writes certificate
> keys in CLEARTEXT alongside their certs. This is a DEV-ONLY
> convenience for local testing. **DO NOT USE THESE KEYS IN PRODUCTION.**

### 2.6 Known gotchas (read before testing)

1. **Hostnames with hyphens are OK, but with ambiguity → use device_id**
   Duplicate hostnames are refused: `cd \\win11` with 2 agents named
   `win11` fails. Use `cd \\<device_id>` or `cd \\<computer_id>` to
   disambiguate.
2. **The `mockserver` hostname is REAL, not the local server.** If
   examples use `\\mockserver\c\temp\...`, that `mockserver` is the
   NAME of a Windows VM running an agent and happening to be called
   "mockserver". It does NOT reach the mockserver process's local
   filesystem.
3. **`cd \\` with no name CLEARS the target** — this is intentional,
   not a bug.
4. **Interactive capture defaults OFF.** `set capture on` turns it on.
   If a test expects a file at `uploads/...` and nothing is there,
   check capture state (`vars` command shows it).
5. **`exec` and `source` read SERVER-local files**, not endpoint files.
   The server interprets the script, then sends signed tasks to agents.
6. **Agent service runs as LocalSystem.** Native file ops execute as
   NT AUTHORITY\\SYSTEM on the endpoint, not as the logged-in user.
7. **`AEGIS_DEVICE_STATE_INTERVAL` is INTEGER SECONDS**, not Go duration.
   `setx AEGIS_DEVICE_STATE_INTERVAL 30 /M` sets 30 seconds.
   `setx AEGIS_DEVICE_STATE_INTERVAL 30s /M` silently falls back to the
   5-minute default. This bites `inventory refresh` timing tests.

---

## 3. Setup

### 3.1 Install the agent on the VM

1. Copy `AegisOneClient.msi` + `aegisone.cfg` + CA cert (`ca.pem`) to the VM.
2. Edit `aegisone.cfg` with your mock server address and enroll token:
   ```ini
   SERVER_ADDR=<mock-server-host>:50051
   CA_PATH=C:\AegisOne\ca.pem
   ENROLL_TOKEN=<ENROLL_TOKEN>
   ```
3. Install (as Administrator):
   ```bat
   silent-install.cmd aegisone.cfg AegisOneClient.msi
   ```
4. Confirm service is Running:
   ```bat
   sc query AegisOneClient
   ```
   Expected: `STATE : 4 RUNNING`.
5. Check agent log briefly:
   ```bat
   type "C:\Program Files\AegisOne\Client\SKVC\logs\agent.log"
   ```
   Expected: lines showing `agent connected device_id=...` and periodic
   `heartbeat sent`.

### 3.2 Start the mockserver + NSH REPL

On the mockserver machine, from the launch directory:

```bat
cd C:\test-mockserver
mockserver.exe nsh
```

**Expected first-line output** (within 1-5 seconds):
```
nsh>
```

Confirm the agent connected by checking the mock's log:
```bat
type logs\mockserver.log | findstr /i "connected"
```
Expected: `agent connected device_id=<some-uuid> hostname=<vm-hostname>`.

### 3.3 Verify basic connection from NSH

```
nsh> hosts
```

Expected: a table with your VM's hostname, device_id, computer_id, and
`STATUS = ONLINE`.

If `STATUS = OFFLINE`, wait 15-30s for the agent to reconnect, or
check firewall / cert / enroll_token on the VM.

### 3.4 Prepare a scratch folder on the endpoint

In NSH:
```
nsh> cd \\<your-vm-hostname>
<vm-hostname>> use powershell
<vm-hostname>> mkdir C:\temp\nsh-test
```

Expected: a OK footer `(exit=0, <ms>)`. Verify on the VM:
```powershell
Test-Path C:\temp\nsh-test   # True
```

Setup complete. Move to §5 test matrix.

---

## 4. How to read this guide — NSH prompt and command notation

### 4.1 NSH prompts

| Prompt | Meaning |
|---|---|
| `nsh> ` | No target selected. Verbs like `hosts`, `search`, `cd`, `source` work; endpoint commands refused. |
| `<hostname>> ` | One target selected via `cd \\<hostname>`. Endpoint commands (hostname, dir, etc.) run on that host. |
| `[N hosts]> ` | N > 1 targets selected (via `cd *`, `cd \\a,b`, group names). Single commands refused — use `fanout`. |
| `...> ` | Inside an open block (foreach / for / if). Type the body, finish with `end`. |
| `... [DELIM]> ` | Inside a HEREDOC. Type the script lines, finish with the delimiter alone on its own line. |

### 4.2 Device paths (`\\<host>\<drive>\<path>`)

```
\\win11-vm\c\temp\loop.txt      # C: on managed agent "win11-vm"
\\win11-vm\c$\temp\loop.txt     # same as above (c and c$ equivalent)
\\win11-vm,server1\c\temp\      # TWO targets: win11-vm AND server1
\\*\c\temp\                     # all currently online agents
```

> **IMPORTANT:** these are **NSH managed-device paths** — they resolve
> to enrolled agents known to the server. They are NOT SMB share
> paths. `\\mockserver\c\...` means "endpoint named mockserver",
> NOT the mockserver process local disk.

### 4.3 Verb → task-type mapping (for debugging)

| NSH verb | Agent task type sent | Notes |
|---|---|---|
| `run` / `hostname` / `dir` / etc. | `run_command` | one-shot execution |
| `exec <local.ps1>` | `run_command` (encoded script content) | server reads local file, substitutes vars, encodes |
| `script ... until END` | `run_command` (HEREDOC body) | same path as exec but inline |
| `grab`, `pull` | `collect_file` | server collects endpoint file via UploadContent |
| `copy`, `move`, `delete`, `mkdir`, `touch` | `file_operation` | signed JSON payload (internal/fileops.Spec v1) |
| `shell` (interactive session) | `open_shell` + `ShellSession` stream | one live cmd.exe pipe |
| `inventory refresh` | `report_device_state` | triggers immediate DeviceStateReport |
| `inventory apps` | (no agent command) | reads cached DeviceStateReport |

### 4.4 PASS criteria convention in this doc

Each test case lists:
- **GOAL** — what we're validating
- **STEPS** — exact commands to type (or files to create)
- **EXPECTED** — the output line(s) that count as PASS
- **PASS** — one-line summary; log it in the Record Sheet

Anything materially different from EXPECTED = FAIL. Capture the full
output when flagging a FAIL.

### 4.5 Test IDs

Sections use letters (A, B, C...) matching the historical test plan;
new UC-18 / UC-19 scope is called out explicitly in §5.8.

---

## 5. Test Matrix

### 5.1 — Section A: REPL basics

#### A1 — `hosts` lists devices with online status

**GOAL:** `hosts` returns a bounded, filterable device list.

**STEPS:**
```
nsh> hosts
nsh> hosts --status online --limit 5
nsh> hosts --status offline
nsh> hosts host=win11
nsh> hosts --page 2
```

**EXPECTED:**
- Default `hosts` returns up to 20 rows: columns HOST, DEVICE_ID,
  COMPUTER_ID, STATUS.
- `--limit 5` returns at most 5 rows.
- `--page 2` returns the next 20 rows (or empty if fleet < 21).
- Footer shows `total=<N>` and `next-page` command if applicable.
- `hosts --limit 50000` rejected (max 100).

**PASS:** All five variants return bounded, filtered lists without
printing an unbounded dump.

#### A2 — `cd \\<host>` selects one target

**GOAL:** Target selection via hostname works and the prompt updates.

**STEPS:**
```
nsh> cd \\<your-vm-hostname>
<vm-hostname>> hostname
```

**EXPECTED:**
- Prompt changes to `<vm-hostname>> `
- `hostname` returns the VM's hostname and footer `OK (exit=0, <ms>)`

**PASS:** Prompt updates and endpoint command returns its hostname.

#### A3 — `cd *` targets every online agent (blocks single-cmd exec)

**GOAL:** With multi-target selection, bare commands are refused — must
use `fanout`.

**STEPS:**
```
nsh> cd *
[N hosts]> hostname
```

**EXPECTED:**
- Prompt: `[N hosts]> ` (N = count of online agents)
- `hostname` is REFUSED with
  `nsh: N targets selected; use fanout <cmd> for multi-target`

**PASS:** Single-command refusal happens cleanly.

#### A4 — `fanout` runs on every selected target

**STEPS:**
```
[N hosts]> fanout hostname
```

**EXPECTED:** Table with columns `HOST | RESULT | FIRST LINE | SAVED`,
one row per target.

**PASS:** All targets show OK, each row has that target's hostname in
FIRST LINE.

#### A5 — Unknown target is caught cleanly

**STEPS:**
```
nsh> cd \\does-not-exist
```

**EXPECTED:** `nsh: unknown device: does-not-exist`.
Prior selection (if any) is preserved.

**PASS:** Clean error, no stack trace, no silent accept.

#### A6 — `use` switches interpreter

**STEPS:**
```
nsh> cd \\<vm-hostname>
<vm-hostname>> use powershell
<vm-hostname>> Get-Service wuauserv | Select-Object -ExpandProperty Status
<vm-hostname>> use cmd
<vm-hostname>> net statistics server
```

**EXPECTED:**
- PowerShell variant returns `Running` (or whatever state the service is).
- CMD variant returns the `net statistics` output.
- NO cross-shell errors (e.g. cmd.exe trying to run a PS cmdlet).

**PASS:** Both interpreters work; `use` persists until changed.

#### A7 — pwsh (PowerShell 7) honestly reports absence

**STEPS:**
```
<vm-hostname>> use pwsh
<vm-hostname>> echo hi
```

**EXPECTED (if pwsh NOT installed):**
`pwsh.exe not installed on this endpoint`, ActionPhase `NOT_APPLICABLE`.

**PASS:** No crash. Honest refusal.

#### A8 — Linux shell on Windows is refused

**STEPS:**
```
<vm-hostname>> use bash
<vm-hostname>> ls
```

**EXPECTED:** `bash is a POSIX shell; not available on Windows endpoint`,
ActionPhase `NOT_APPLICABLE`.

**PASS:** Clean refusal, no cmd.exe crash attempting /bin/bash.

After A8, run `use cmd` to reset the interpreter for later tests.

#### A9 — Endpoint CWD persists across commands and host switches

**GOAL:** Each selected endpoint has an independent working directory
that survives commands and host switches.

**STEPS** (requires `C:\temp` on the VM):
```
nsh> cd \\<vm-hostname>
<vm-hostname>> use powershell
<vm-hostname>> cd C:\temp
<vm-hostname>> pwd
<vm-hostname>> Get-Location
<vm-hostname>> cd ..
<vm-hostname>> pwd
<vm-hostname>> cd C:\does-not-exist   # must fail without changing CWD
<vm-hostname>> pwd
```

**EXPECTED:**
- `pwd` shows `C:\temp`, then `C:\` after `cd ..`.
- Invalid path fails, `pwd` still shows `C:\` (prior CWD retained).

If a 2nd VM is available:
```
<vm-hostname>> cd \\<other-vm>
<other-vm>> pwd         # that VM's independent CWD, not C:\
<other-vm>> cd \\<vm-hostname>
<vm-hostname>> pwd      # back to C:\ (preserved)
```

**EXPECTED:** CWD is **per-endpoint**, not per-session.
`cd \\` returns to `nsh>` with no target.

**PASS:** CWD persistence works per endpoint, invalid-path safe,
host switch preserves state.

#### A10 — Known offline target can be selected

**GOAL:** `cd \\<host>` on an OFFLINE device succeeds; command then
fails immediately as offline (no fleet wait).

**STEPS:**
1. Stop the agent service on the VM: `sc stop AegisOneClient`.
2. In NSH:
```
nsh> cd \\<vm-hostname>
<vm-hostname>> hostname
```

**EXPECTED:**
- `cd` succeeds even though device is OFFLINE.
- `hostname` fails IMMEDIATELY with
  `nsh: <vm-hostname> is OFFLINE; command not sent (no fleet/heartbeat wait)`.

Restart agent (`sc start AegisOneClient`), wait 15-30s, retry `hostname`
— must work without reselecting.

**PASS:** OFFLINE selection is allowed, OFFLINE command is refused
cleanly, auto-recovery on reconnect works.

---

### 5.2 — Section B: Output capture + file collection

> **Mock upload layout:** files land at
> `<mockserver-launch-dir>/uploads/<computer_id>/<output-dir>/<hostname>/<filename>`.
> Default output-dir = `nsh`. Repeated filenames get suffixed to avoid
> clobbering.

#### B1 — `grab` saves stdout as a server-side file

**STEPS:**
```
nsh> cd \\<vm-hostname>
<vm-hostname>> use cmd
<vm-hostname>> grab \out\winini.txt type C:\Windows\win.ini
```

**EXPECTED:** Footer line
`---- saved to uploads/<cid>/nsh/<vm-hostname>/out/winini.txt`.

File exists at that path on the mockserver machine and contains the
`win.ini` text.

**PASS:** File written with expected content.

#### B2 — `set capture on` auto-saves every subsequent command

**STEPS:**
```
<vm-hostname>> set capture on
<vm-hostname>> echo capture-check
<vm-hostname>> tasklist
```

**EXPECTED:**
- `echo` footer: `saved to uploads/<cid>/nsh/<vm-hostname>/<ts>-echo_capture-check.out`.
- `tasklist` similarly auto-saved with its own timestamped filename.

**PASS:** Auto-capture creates one file per command.

Reset: `set capture off`.

#### B3 — Multi-host capture → per-host files

**GOAL:** fanout + capture creates DISTINCT files per host, no collision.

**Requires:** 2 agents online.

**STEPS:**
```
nsh> cd *
nsh> set capture on
[2 hosts]> fanout hostname
```

**EXPECTED:** fanout table shows TWO rows. SAVED column shows different
paths like `uploads/<cid-A>/nsh/<hostA>/...` and
`uploads/<cid-B>/nsh/<hostB>/...`.

**PASS:** No file collision, each host has its own save path.

Reset: `set capture off`.

#### B4 — `pull` fetches an endpoint file verbatim

**STEPS:**
```
<vm-hostname>> pull C:\Windows\System32\drivers\etc\hosts \hosts.txt
```

**EXPECTED:** `hosts.txt` lands at
`uploads/<cid>/nsh/<vm-hostname>/hosts.txt`.
Byte-for-byte match with the endpoint file:
```bat
fc /b uploads\<cid>\nsh\<vm-hostname>\hosts.txt C:\Windows\System32\drivers\etc\hosts
```

**Also try:** a binary file (e.g. a small .exe), a quoted path with spaces.
```
<vm-hostname>> pull "C:\temp\has space.bin" copy.bin
```

**EXPECTED:**
- Binary file matches byte-for-byte.
- Quoted path handled.
- Missing files report `no match`, not a crash.
- Wildcards refused: `pull C:\temp\*.log dest.log` → error.
- One source file per `pull`.

**PASS:** All four variants behave correctly.

#### B5 — `set output-dir` changes save prefix

**STEPS:**
```
nsh> set output-dir reports\daily
nsh> set capture on
<vm-hostname>> hostname
```

**EXPECTED:** File lands at
`uploads/<cid>/reports/daily/<vm-hostname>/*.out`.

**PASS:** New prefix honored.

**Cleanup:** `set output-dir nsh` and `set capture off`.

---

### 5.3 — Section C: Variables + substitution

#### C1 — `let` + `${VAR}` substitutes on the server BEFORE sending

**GOAL:** NSH variables are resolved on the mockserver, not on the
endpoint. Agent sees the final text.

**STEPS:**
```
nsh> let TAG = prod-fleet
nsh> cd \\<vm-hostname>
<vm-hostname>> echo tag is ${TAG}
```

**EXPECTED:** Output contains literal `tag is prod-fleet`.
Check the agent log — the command received was already
`echo tag is prod-fleet`, NOT `echo tag is ${TAG}`.

**PASS:** Substitution happened server-side.

#### C2 — `${env:NAME}` picks up a MOCKSERVER-side env var

**STEPS:**
```
nsh> let USR = ${env:USERNAME}
nsh> vars
```

**EXPECTED:** `vars` lists `USR = <mockserver-machine-username>` (NOT
the endpoint username).

**PASS:** Server env is the source.

#### C3 — Last-result variables populate after every run

**STEPS:**
```
<vm-hostname>> echo marker
<vm-hostname>> vars
```

**EXPECTED:** `vars` lists `exit=0`, `stdout=marker`,
`host=<vm-hostname>`, non-zero `duration`, `reason=OK`.

**PASS:** All five last-result vars present.

#### C4 — NSH vars become `AEGIS_PARAM_<NAME>` env vars on endpoint

**GOAL:** Pre-staged scripts on the endpoint can read NSH-authored
parameters without touching the command string.

**STEPS:**
```
nsh> let CUSTOMER = acme
<vm-hostname>> use powershell
<vm-hostname>> Write-Host "customer env = $env:AEGIS_PARAM_CUSTOMER"
```

**EXPECTED:** Output `customer env = acme`.

**PASS:** Env-var channel works.

---

### 5.4 — Section D: Control flow

#### D1 — `foreach host in *` iterates every online host

**Requires:** 2 agents.

**STEPS:**
```
nsh> use powershell
nsh> foreach host in *
...>   log "checking ${host}"
...>   grab status.txt Get-Service wuauserv
...> end
```

**EXPECTED:**
- Two log lines (one per host).
- Two `SAVED` paths at `uploads/<cid-X>/nsh/<hostX>/status.txt`.
- No collision between host files.
- Prompt returns to `nsh>` after `end`.

**PASS:** Loop iterates per host, independent files per host.

#### D2 — `if $exit != 0` branches on failure

**STEPS:**
```
<vm-hostname>> use cmd
<vm-hostname>> dir Z:\does\not\exist
<vm-hostname>> if $exit != 0
...>   log "FAILED, reason=$reason"
...> else
...>   log "OK"
...> end
```

**EXPECTED:** `FAILED, reason=...`.

Repeat with `dir C:\` → `OK`.

**PASS:** Branch flips based on exit code.

#### D3 — Nested blocks

**STEPS:**
```
nsh> for X in 1 2 3
...>   foreach host in *
...>     log "X=${X} host=${host}"
...>   end
...> end
```

**EXPECTED:** 6 log lines (3 × 2 hosts). Order WITHIN each loop is
deterministic; across the fanout it may be concurrent.

**PASS:** 6 total lines, no missing combos.

#### D4 — `break` and `continue`

**STEPS:**
```
nsh> for X in 1 2 3 4
...>   if $X == 3
...>     log "break at 3"
...>     break
...>   end
...>   log "X=${X}"
...> end
```

**EXPECTED:** `X=1`, `X=2`, `break at 3`. Nothing after.

**PASS:** Exactly 3 output lines.

#### D5 — `on-error continue` in script mode

Save `C:\tests\on-error.nsh`:
```nsh
on-error continue
cd \\<vm-hostname>
dir Z:\nope
log "survived (exit was $exit)"
hostname
```

Run:
```bat
set AEGIS_NSH_SCRIPT=C:\tests\on-error.nsh
mockserver.exe script
```

**EXPECTED:** BOTH the `log` line AND the `hostname` output appear.
`on-error stop` (default) would halt after the failed `dir`.

**PASS:** Script continues past failure.

---

### 5.5 — Section E: Aliases + helpers

#### E1 — `alias` creates a reusable verb

**STEPS:**
```
nsh> use powershell
nsh> alias def = Get-MpComputerStatus | Select-Object AMEngineVersion
nsh> cd \\<vm-hostname>
<vm-hostname>> def
```

**EXPECTED:** PowerShell runs the expansion; output has an
`AMEngineVersion` line.

**PASS:** Alias expanded correctly.

#### E2 — `source <file.nsh>` includes another script (vars/aliases survive)

Save `C:\tests\vars.nsh`:
```nsh
let REGION = us-east-1
alias def = Get-MpComputerStatus
```

Interactive:
```
nsh> source C:\tests\vars.nsh
nsh> vars
nsh> echo region=${REGION}
```

**EXPECTED:**
- `vars` lists `REGION`
- `echo` prints `region=us-east-1`

**PASS:** Sourced definitions persist in the REPL.

#### E3 — `history` lists recent commands

**STEPS:**
```
nsh> history
```

**EXPECTED:** Numbered list of recent commands.

**PASS:** Last few typed commands appear.

#### E4 — `set session-log` writes every REPL line to a file

**STEPS:**
```
nsh> set session-log C:\tests\nsh-session.log
nsh> hostname
```

**EXPECTED:**
- `C:\tests\nsh-session.log` grows with each typed line.
- Opens with a `=== session resumed ===` header.

**PASS:** Session log captures lines with timestamps.

---

### 5.6 — Section F: Native script execution (`.ps1`, `.bat`, `.py`)

#### F1 — `exec <local .ps1>` with console-side `${VAR}` substitution

Save `C:\tests\check.ps1` on the MOCKSERVER machine:
```powershell
Write-Host "tag=${TAG} on $env:COMPUTERNAME"
Write-Host "env tag = $env:AEGIS_PARAM_TAG"
Get-Service wuauserv | Select-Object Status
```

```
nsh> let TAG = daily-21oct
nsh> cd \\<vm-hostname>
<vm-hostname>> exec C:\tests\check.ps1
```

**EXPECTED:** Output includes:
- `tag=daily-21oct on <HOSTNAME>` (console substitution)
- `env tag = daily-21oct` (env var channel)
- The service status line

**PASS:** Both substitution paths work; output has all three expected lines.

#### F2 — `exec <local .bat>`

Save `C:\tests\check.bat`:
```bat
@echo off
echo running on %COMPUTERNAME%
echo tag env is %AEGIS_PARAM_TAG%
```

```
<vm-hostname>> exec C:\tests\check.bat
```

**EXPECTED:** Output shows computer name and `tag env is daily-21oct`.

**PASS:** Batch file executes with env substitution.

#### F3 — `exec python <local .py>`

Save `C:\tests\check.py`:
```python
import os
print("py tag =", os.environ.get("AEGIS_PARAM_TAG"))
```

```
<vm-hostname>> exec python C:\tests\check.py
```

**EXPECTED (if python on PATH):** `py tag = daily-21oct`.
**EXPECTED (if NO python):** honest error
`python.exe is not recognized ...`, nonzero exit. NOT a crash.

**PASS:** Either path works correctly without crash.

#### F4 — `exec <nonexistent file>` is caught

**STEPS:**
```
nsh> exec C:\does\not\exist.ps1
```

**EXPECTED:** `nsh: exec: read C:\does\not\exist.ps1: ...`.

**PASS:** Clean file-not-found error.

---

### 5.7 — Section G: HEREDOC — inline multi-line scripts

**How HEREDOC works:** type `script <shell> until <DELIM>` once.
Prompt changes to `... [DELIM]>`. Type body lines. Finish with
the delimiter alone on its own line. **Do not re-type the opener.**

#### G1 — PowerShell inline block with `${VAR}` substitution

**STEPS:**
```
nsh> let REGION = us-west-1
nsh> cd \\<vm-hostname>
<vm-hostname>> script powershell until EOT
          Write-Host "REGION=${REGION}"
          Write-Host "HOST=$env:COMPUTERNAME"
          Get-Service wuauserv | Select-Object Status, Name
          EOT
```

**EXPECTED:** Output has `REGION=us-west-1`, host name, and the
service status line. No file was dropped on the endpoint (verify
nothing new under `C:\Windows\Temp\`).

**PASS:** HEREDOC content encoded + executed; no temp files leaked.

#### G2 — HEREDOC inside `foreach` (per-host execution)

**Requires:** 2 agents.

Save `C:\tests\fleet-herdoc.nsh`:
```nsh
let REGION = ${env:REGION}
use powershell
on-error continue
set capture on
foreach host in *
  log "== running on ${host} =="
  script powershell until BLOCK
    Write-Host "I am $env:COMPUTERNAME, region=${REGION}"
    Get-MpComputerStatus | Select-Object AMEngineVersion
    BLOCK
end
```

Run:
```bat
set REGION=apac
set AEGIS_NSH_SCRIPT=C:\tests\fleet-herdoc.nsh
mockserver.exe script
```

**EXPECTED:**
- Each host prints its name + `region=apac` + engine version.
- Per-host files under `uploads/<cid>/nsh/<host>/*.out` with no collision.

**PASS:** Fleet-wide HEREDOC + variable + capture all work.

**Negative test:** Replace `Get-MpComputerStatus` with
`Get-AegisDeliberatelyMissingCmdlet`, re-run. Both hosts must report
FAIL / nonzero exit; `on-error continue` still visits every host.
No misleading OK after an unhandled PowerShell error.

**PASS:** Negative branch too.

#### G3 — `.bat` HEREDOC

**STEPS:**
```
<vm-hostname>> use cmd
<vm-hostname>> script cmd until END
          echo one
          echo two
          echo three
          END
```

**EXPECTED:** Three lines in order.

**PASS:** cmd HEREDOC runs correctly.

#### G4 — HEREDOC body preserves blank lines and comments

**STEPS:**
```
<vm-hostname>> script powershell until END
          # a comment line

          Write-Host "after blank"
          END
```

**EXPECTED:** PowerShell runs; output is `after blank`. The blank
line and `#` comment are preserved WITHIN the heredoc body (NSH
doesn't strip them — PowerShell's own parser treats `#` as its
comment char).

**PASS:** Body-text integrity preserved.

---

### 5.8 — Section H: Inventory (UC-18, UC-19)

**Context:** As of 2026-11-02, NSH gained two inventory verbs tied
to the DeviceStateReport pipeline. UC-18 shipped third-party
installed-apps inventory via `DeviceStateReport.installed_apps`.
UC-19 added HKU per-user Uninstall hive enumeration + on-demand
`inventory refresh` via a new `report_device_state` signed command.

#### H1 — `inventory apps` reads cached inventory

**GOAL:** Show the current cached `DeviceStateReport.installed_apps`
for the selected target(s).

**STEPS:**
```
nsh> cd \\<vm-hostname>
<vm-hostname>> inventory apps
```

**EXPECTED:**
- Table with columns `DISPLAY_NAME | VERSION | PUBLISHER | HIVE`
- Rows include at least basic apps (Windows components, VC++, etc.)
- Hive column shows `HKLM32`, `HKLM64`, and (if per-user installs
  present) `HKU\S-1-5-21-...` or `HKU\S-1-5-21-...\WOW6432Node`

**PASS:** Table rendered with correct columns.

**If output says "no DeviceStateReport received yet"** → run
`inventory refresh` first (see H3).

#### H2 — `inventory apps <filter>` substring filter

**STEPS:**
```
<vm-hostname>> inventory apps microsoft
```

**EXPECTED:** Only rows whose DisplayName or Publisher match
`microsoft` (case-insensitive).

**PASS:** Filter trims the table.

#### H3 — `inventory refresh` forces a fresh send (no change needed)

**GOAL:** Agent sends on-change by default; `inventory refresh`
resets the fingerprint and triggers an immediate DeviceStateReport.

**STEPS:**
```
<vm-hostname>> inventory refresh
```

**EXPECTED (within 5s):**
```
nsh: refresh dispatched to 1 target(s), waiting up to 5s (shared deadline)...
<vm-hostname>: fresh inventory (N apps, collected_at=<unix-ts>)
nsh: refresh complete — 1/1 received, 0 timed out — run `inventory apps` to view
```

**Verify on agent log** at `C:\Program Files\AegisOne\Client\SKVC\logs\agent.log`:
- `FAST-LANE patch-state: on-demand refresh requested — resetting fingerprint`
- `FAST-LANE patch-state: CHANGED -> sending ... installed_apps=N`

**PASS:** Fresh inventory received within 5s; agent log shows both lines.

#### H4 — `inventory refresh --timeout N`

**STEPS:**
```
<vm-hostname>> inventory refresh --timeout 15
```

**EXPECTED:** Same success, mock prints `waiting up to 15s`.

**PASS:** Timeout flag honored.

#### H5 — Bad `--timeout` value

**STEPS:**
```
<vm-hostname>> inventory refresh --timeout abc
```

**EXPECTED:** `nsh: inventory refresh: --timeout expects a positive integer (seconds)`.

**STEPS:**
```
<vm-hostname>> inventory refresh --fast
```

**EXPECTED:** `nsh: inventory refresh: unknown argument "--fast" (use --timeout <seconds>)`.

**PASS:** Both clean errors; no crash.

#### H6 — Offline target refresh refused

**STEPS:**
1. Stop agent service: `sc stop AegisOneClient`.
2. In NSH:
```
<vm-hostname>> inventory refresh
```

**EXPECTED (immediate, no 5s wait):**
`<vm-hostname>: OFFLINE (no live stream) — refresh skipped`.

Restart agent (`sc start AegisOneClient`), wait for reconnect, re-run — must work.

**PASS:** Offline refusal immediate; recovery works.

#### H7 — HKU per-user app visibility (UC-19A)

**GOAL:** A per-user installed app appears with `HKU\<SID>` hive.

**STEPS:**
1. Note baseline count: `inventory refresh` then `inventory apps` → total.
2. On the VM, install an app that registers per-user. Options:
   - Spotify desktop (defaults to per-user install)
   - Chrome per-user installer (consent prompt → "No" → falls to per-user)
   - Teams per-user installer
   - Any MSI with `MSIEXEC /i file.msi ALLUSERS=""` (empty = per-user)
3. Wait 1 cycle of `AEGIS_DEVICE_STATE_INTERVAL` (default 5 min, or set
   `30` seconds for testing — see §2.6 gotcha). OR force immediately:
   ```
   <vm-hostname>> inventory refresh
   <vm-hostname>> inventory apps spotify
   ```

**EXPECTED:**
- New entry present with `HIVE = HKU\S-1-5-21-...` (not HKLM32/HKLM64).
- `inventory apps` without filter shows total count = baseline + 1.

**PASS:** Per-user app visible with HKU\<SID> hive.

#### H8 — `inventory refresh` without target (needs `cd` first)

**STEPS:**
```
nsh> inventory refresh
```
(Without any prior `cd`.)

**EXPECTED:** `nsh: inventory refresh requires a cd target (or -t <host>)`.

**PASS:** Clear instruction, no crash.

#### H9 — 100-target cap (code-reviewed; not VM-testable with 1 agent)

**NOTE:** NSH refuses `inventory refresh` with >100 targets because NSH
is a dev/admin tool, not a fleet-ops surface. Fleet-wide refresh is a
server-side scheduled operation (batched waves + rate limiting). The
agent-side `report_device_state` handler is identical; only the
dispatcher differs.

If your mock supports simulating 101+ targets:
```
[101 hosts]> inventory refresh
```
**EXPECTED:** `nsh: inventory refresh refuses more than 100 targets
(you have 101). NSH is a dev/admin tool. Use the server-side scheduled
fleet refresh instead ...`.

If 101+ agents not simulatable → SKIP, verify by code review.

**PASS:** Cap enforced (or skipped with reason noted).

#### H10 — `inventory apps` fleet summary (multi-target)

**Requires:** 2+ agents.

**STEPS:**
```
nsh> inventory apps
```
(With no `cd` — mock prints fleet summary.)

**EXPECTED:** Table with
`HOST | INVENTORY_SIZE | LAST_REPORT_AT`, one row per device
that has reported.

**PASS:** Fleet-wide summary rendered.

---

### 5.9 — Section I: File operations (nsh-file-operations)

> All file tests use `C:\temp\nsh-test-<date>` — a disposable
> folder. DO NOT substitute production directories.

#### I1 — Prepare source data

**STEPS:**
```
nsh> set capture off
nsh> cd \\<vm-hostname>
<vm-hostname>> use powershell
<vm-hostname>> mkdir C:\temp\nsh-test-20261102
<vm-hostname>> Set-Content -LiteralPath C:\temp\nsh-test-20261102\loop.txt -Value 'NSH transfer check'
<vm-hostname>> mkdir \\<vm-hostname>,<other-vm>\c\temp\nsh-test-20261102\out
```

**EXPECTED:** Both VMs have the `out` subfolder; source VM has
`loop.txt`.

**PASS:** Fixture ready.

#### I2 — Copy single file (same host)

**STEPS:**
```
<vm-hostname>> copy \\<vm-hostname>\c\temp\nsh-test-20261102\loop.txt \\<vm-hostname>\c\temp\nsh-test-20261102\out\
```

**EXPECTED:** `loop.txt` appears under `out/`. Compare SHA-256:
```powershell
Get-FileHash C:\temp\nsh-test-20261102\loop.txt
Get-FileHash C:\temp\nsh-test-20261102\out\loop.txt
# Hashes must match
```

**PASS:** Identical bytes, source preserved.

#### I3 — Copy existing file WITHOUT overwrite — refused

**STEPS:**
```
<vm-hostname>> copy \\<vm-hostname>\c\temp\nsh-test-20261102\loop.txt \\<vm-hostname>\c\temp\nsh-test-20261102\out\
```
(Same command as I2.)

**EXPECTED:** FAIL with `destination exists; use --overwrite`.

**PASS:** No silent clobber.

#### I4 — `--overwrite` succeeds

**STEPS:**
```
<vm-hostname>> copy --overwrite \\<vm-hostname>\c\temp\nsh-test-20261102\loop.txt \\<vm-hostname>\c\temp\nsh-test-20261102\out\
```

**EXPECTED:** OK, bytes identical.

**PASS:**.

#### I5 — Cross-host copy (requires 2 VMs)

**STEPS:**
```
<vm-hostname>> copy \\<vm-hostname>\c\temp\nsh-test-20261102\loop.txt \\<other-vm>\c\temp\nsh-test-20261102\out\
```

**EXPECTED:** File lands on the second VM. Verify SHA-256 matches
source.

**PASS:** Cross-host transfer verified.

#### I6 — Distribution to multiple hosts (one-to-many)

**STEPS:**
```
<vm-hostname>> copy \\<vm-hostname>\c\temp\nsh-test-20261102\loop.txt \\<vm-hostname>,<other-vm>\c\temp\nsh-test-20261102\out2\
```

**EXPECTED:** Both hosts receive the file. Source exported once,
delivered to both. Source retained.

**PASS:** Both destinations have identical bytes.

#### I7 — Recursive copy (`-r`)

**STEPS:**
```
<vm-hostname>> mkdir C:\temp\nsh-test-20261102\reports
<vm-hostname>> Set-Content C:\temp\nsh-test-20261102\reports\a.txt "A"
<vm-hostname>> Set-Content C:\temp\nsh-test-20261102\reports\b.txt "B"
<vm-hostname>> copy -r \\<vm-hostname>\c\temp\nsh-test-20261102\reports \\<other-vm>\c\temp\nsh-test-20261102\
```

**EXPECTED:** `reports/` dir with `a.txt` and `b.txt` on destination.

**PASS:**.

#### I8 — Glob copy (`*.txt`)

**STEPS:**
```
<vm-hostname>> copy \\<vm-hostname>\c\temp\nsh-test-20261102\reports\*.txt \\<other-vm>\c\temp\nsh-test-20261102\globbed\
```

**EXPECTED:** Only `.txt` files copied.

**PASS:**.

#### I9 — Move (verify-then-remove)

Create a disposable `move-me.txt`:
```
<vm-hostname>> Set-Content C:\temp\nsh-test-20261102\move-me.txt "move this"
<vm-hostname>> move \\<vm-hostname>\c\temp\nsh-test-20261102\move-me.txt \\<other-vm>\c\temp\nsh-test-20261102\moved\
```

**EXPECTED:**
- Destination hash matches.
- Source file removed only AFTER verified copy.
- An offline destination must leave the source intact.

**PASS:** Move succeeds safely.

#### I10 — `mkdir` creates missing parents

**STEPS:**
```
<vm-hostname>> mkdir \\<vm-hostname>,<other-vm>\c\temp\nsh-test-20261102\deep\nested\dir
```

**EXPECTED:** Full chain created on both hosts.

**PASS:**.

#### I11 — `touch` creates empty, refuses existing

**STEPS:**
```
<vm-hostname>> touch \\<vm-hostname>,<other-vm>\c\temp\nsh-test-20261102\empty.txt
<vm-hostname>> touch \\<vm-hostname>\c\temp\nsh-test-20261102\empty.txt
```

**EXPECTED:**
- First succeeds (empty file created).
- Second FAILS with `file exists` — must NOT truncate.

**PASS:**.

#### I12 — `delete` (files + globs + `-r` for dirs)

**STEPS:**
```
<vm-hostname>> delete \\<vm-hostname>,<other-vm>\c\temp\nsh-test-20261102\empty.txt
<vm-hostname>> delete \\<vm-hostname>\c\temp\nsh-test-20261102\reports\*.txt
<vm-hostname>> delete -r \\<vm-hostname>\c\temp\nsh-test-20261102\reports
<vm-hostname>> delete \\<vm-hostname>\c\temp\nsh-test-20261102\reports   # must fail without -r
```

**EXPECTED:**
- File delete removes `empty.txt` on both hosts.
- Glob delete removes `.txt` files.
- `-r` on dir succeeds; without `-r` dir delete refused.

**PASS:** All four variants behave correctly.

#### I13 — Set file-worker tuning

**STEPS:**
```
nsh> set file-workers 2
nsh> set file-max-bytes 10485760   # 10 MiB
nsh> set file-max-files 100
```

**EXPECTED:** Settings accepted. Try a copy exceeding one of the caps
→ clean failure (not silent truncate).

**PASS:** Bounds enforced honestly.

**Cleanup:** `set file-workers 4`, `set file-max-bytes 524288000`.

---

### 5.10 — Section J: Live interactive shell

**NOTE:** As of this handoff, the live `shell` interactive-session verb
(bidirectional `ShellSession` RPC) is agent-side complete but the mock's
auto-trigger (`AEGIS_SHELL=1`) was removed. Interactive shell test
requires either (a) a future console UI wire-up, OR (b) a manually
signed `open_shell` command from a dev tool. These tests are deferred.

#### J1 — `shell` reserved verb exists

**STEPS:**
```
<vm-hostname>> shell
```

**EXPECTED (current):** verb may print `shell session not available
without open_shell wire-up` or similar. Does NOT crash.

**PASS:** Verb recognized, honest status.

#### J2 — Shell transcripts land under SKVC (plumbing verification)

If an admin manually dispatches a signed `open_shell`, the agent
writes transcripts to `C:\Program Files\AegisOne\Client\SKVC\logs\shell\<cmd-id>.transcript`.

**EXPECTED (when triggered):** Files with
`[IN hh:mm:ss]` / `[OUT hh:mm:ss]` / `[ERR hh:mm:ss]` lines, timestamp
per direction.

**PASS:** Transcript file present with correct format.

---

### 5.11 — Section K: Fleet operations

> **Fleet-scale WARNING:** NSH is not a 50k-endpoint ops tool. The
> 100-target cap on `inventory refresh` is intentional. Fleet ops
> like scheduled refresh, mass deploy, etc. are server-side scheduled
> operations outside NSH's scope.

#### K1 — `fanout <cmd>` serializes stdout per host

**Requires:** 2 agents.

**STEPS:**
```
nsh> cd *
[2 hosts]> fanout hostname
```

**EXPECTED:** Aggregate table with per-host result.

**PASS:**.

#### K2 — `job run <file.nsh>` runs a script against selected targets

Save `C:\tests\job-check.nsh`:
```nsh
use powershell
on-error continue
foreach host in *
  log "job host=${host}, tag=${TAG}"
  script powershell until END
    Write-Output "endpoint=$env:COMPUTERNAME tag=${TAG}"
    Get-Service wuauserv | Select-Object Status, Name
  END
end
```

```
nsh> cd \\<vm-hostname>
<vm-hostname>> job run C:\tests\job-check.nsh --param TAG=script-test
```

**EXPECTED:**
- Script runs for the selected target(s).
- Terminal reports `JOB SUCCESS/FAILED/CANCELLED` footer.
- Result file at `logs/nsh-jobs/<job-id>.json` on the mockserver.
- Capture OFF by default (unless `--capture` or `set capture on`).

**PASS:** Job completed with structured result.

#### K3 — `job run --kind software_distribution`

Same script, different kind label (used in production for
SoftDist NSH script actions):
```
<vm-hostname>> job run C:\tests\job-check.nsh --kind software_distribution --param TAG=softdist-test
```

**EXPECTED:** Same behavior, result labels `kind=software_distribution`.

**PASS:**.

#### K4 — `job run --timeout N`

**STEPS:**
```
<vm-hostname>> job run C:\tests\job-check.nsh --param TAG=t --timeout 120
```

**EXPECTED:** Job honors 120s deadline (default is 1800s).

**PASS:** Deadline respected.

#### K5 — Multi-target job

Requires 2 agents.
```
nsh> cd \\<vm-hostname>,<other-vm>
[2 hosts]> job run C:\tests\job-check.nsh --param TAG=fleet-test
```

**EXPECTED:** Each selected target has a terminal result. No unselected
agent executes. Interactive REPL state (shell, targets, aliases) is
not affected by the job.

**PASS:**.

#### K6 — Non-interactive script mode

```bat
set AEGIS_NSH_SCRIPT=C:\tests\job-check.nsh
set AEGIS_NSH_JOB_PARAMS=TAG=env-test
set AEGIS_NSH_TARGETS=<vm-hostname>
mockserver.exe script
```

**EXPECTED:** Same shared Runner executes; mockserver stays running
after script completion (does not exit as CLI).

**PASS:** Script-mode produces equivalent output.

---

### 5.12 — Section L: Safety + limits

#### L1 — Output caps enforced

**STEPS:**
```
nsh> set max-output 1024
<vm-hostname>> type C:\Windows\win.ini
```

**EXPECTED:** Inline output capped at ~1024 bytes.
`truncated=true` reported in structured Detail JSON.
With `grab` or `set capture on`, full stdout still saved to server.

**PASS:** Inline cap enforced; truncation flag set.

#### L2 — Per-command timeout

**STEPS:**
```
nsh> set timeout 2
<vm-hostname>> ping -n 10 localhost
```

**EXPECTED:** Command killed after 2s. Footer: `reason=timeout`,
nonzero exit.

**PASS:**.

#### L3 — Honest refusal when agent disconnects mid-run

**STEPS:**
1. Start a long command (`ping -n 100 8.8.8.8`).
2. During it, stop the agent service on the VM:
   `sc stop AegisOneClient`.

**EXPECTED:** NSH reports
`nsh: timed out waiting for <action_id>` eventually; mock does NOT
hang. Restart agent to continue.

**PASS:** No hang.

#### L4 — Agent-side transcripts under SKVC

**STEPS (on VM):**
```powershell
Get-ChildItem "C:\Program Files\AegisOne\Client\SKVC\logs\nsh\" -File | Measure-Object
```

**EXPECTED:** One `.transcript` file per `run_command` executed.
Each file has a header with `shell=`, `exit=`, `<ms>`, followed by
`CMD:`, `--- STDOUT ---`, `--- STDERR ---` sections.

**PASS:** Transcripts exist with correct format.

#### L5 — Default working-dir on service-launched agents

**GOAL:** `run_command` default CWD is `<SKVC>\tmp`, NOT
`C:\Windows\System32`.

**STEPS:**
```
<vm-hostname>> use cmd
<vm-hostname>> pwd           # before any cd — should show <SKVC>\tmp
<vm-hostname>> cd C:\temp
<vm-hostname>> dir           # must list C:\temp contents
<vm-hostname>> pwd           # C:\temp
```

**EXPECTED:** Service-context default is <SKVC>\tmp, honoring the
default working_dir.

**PASS:** CWD defaults are safe.

---

### 5.13 — Section M: Tab completion

**NOTE:** Interactive REPL uses `peterh/liner` for Tab completion + line
editing + arrow-key history. Non-interactive script mode has no Tab
completion (that's a terminal feature).

#### M1 — Verb Tab completion with one match

**STEPS (interactive only):**
```
nsh> inv<Tab>
```

**EXPECTED:** Completes to `inventory` (single candidate).

**PASS:** Prefix completes.

#### M2 — Verb Tab completion with multiple matches

**STEPS:**
```
nsh> h<Tab>
```

**EXPECTED:** Prints candidate list: `help  history  hosts` (liner
prints candidates when there's no unique completion).

**PASS:** Candidates shown.

#### M3 — No completion when verb already typed

**STEPS:**
```
nsh> inventory <Tab>
```

**EXPECTED:** No completion (space after verb → NSH does not try to
complete sub-verbs like `apps` / `refresh`). Terminal just prints
nothing new.

**PASS:** No crash, no misleading suggestions.

#### M4 — Arrow-key history

**STEPS:**
1. Type `hostname`, press Enter.
2. Type `help`, press Enter.
3. Press Up arrow twice.

**EXPECTED:** Second Up recalls `hostname` (first Up recalls `help`).

**PASS:** History accessible.

#### M5 — `history` verb lists numbered recent commands

**STEPS:**
```
nsh> history
```

**EXPECTED:** Numbered list of recent typed commands.

**PASS:** List printed.

---

## 6. Record Sheet

Use one row per sub-case. Flag any FAIL with full output + agent-side
log excerpts attached.

| Test | Sub | Date | Tester | PASS/FAIL | Notes |
|---|---|---|---|---|---|
| A1 | hosts bounded | | | | |
| A2 | cd single target | | | | |
| A3 | cd * blocks single-cmd | | | | |
| A4 | fanout table | | | | |
| A5 | unknown target | | | | |
| A6 | use interpreter switch | | | | |
| A7 | pwsh honest absence | | | | |
| A8 | bash on Windows refused | | | | |
| A9 | endpoint CWD persistence | | | | |
| A10 | offline target selection | | | | |
| B1 | grab single file | | | | |
| B2 | set capture on auto-save | | | | |
| B3 | multi-host capture no collision | | | | |
| B4 | pull byte-exact | | | | |
| B5 | output-dir prefix | | | | |
| C1 | let + ${VAR} substitution | | | | |
| C2 | ${env:NAME} server-side | | | | |
| C3 | last-result vars | | | | |
| C4 | AEGIS_PARAM_<NAME> env | | | | |
| D1 | foreach host in * | | | | |
| D2 | if $exit != 0 | | | | |
| D3 | nested blocks | | | | |
| D4 | break / continue | | | | |
| D5 | on-error continue script | | | | |
| E1 | alias | | | | |
| E2 | source | | | | |
| E3 | history verb | | | | |
| E4 | session-log file | | | | |
| F1 | exec .ps1 + ${VAR} | | | | |
| F2 | exec .bat | | | | |
| F3 | exec python .py | | | | |
| F4 | exec nonexistent file | | | | |
| G1 | HEREDOC PowerShell + var | | | | |
| G2 | HEREDOC in foreach (+ neg) | | | | |
| G3 | HEREDOC cmd | | | | |
| G4 | HEREDOC blank/comment preserved | | | | |
| H1 | inventory apps table | | | | |
| H2 | inventory apps filter | | | | |
| H3 | inventory refresh default | | | | |
| H4 | inventory refresh --timeout | | | | |
| H5 | inventory refresh bad arg | | | | |
| H6 | inventory refresh offline | | | | |
| H7 | HKU per-user visibility (UC-19A) | | | | |
| H8 | inventory refresh no target | | | | |
| H9 | 100-target cap (code review ok) | | | | |
| H10 | inventory apps fleet summary | | | | |
| I1 | prepare source | | | | |
| I2 | copy single same-host | | | | |
| I3 | copy no-overwrite refused | | | | |
| I4 | copy --overwrite | | | | |
| I5 | cross-host copy | | | | |
| I6 | one-to-many distribution | | | | |
| I7 | recursive copy | | | | |
| I8 | glob copy | | | | |
| I9 | move verify-then-remove | | | | |
| I10 | mkdir deep parents | | | | |
| I11 | touch existing refused | | | | |
| I12 | delete file/glob/-r | | | | |
| I13 | file-worker tuning | | | | |
| J1 | shell verb honest status | | | | |
| J2 | shell transcript under SKVC | | | | |
| K1 | fanout aggregate | | | | |
| K2 | job run script_job | | | | |
| K3 | job run software_distribution | | | | |
| K4 | job run --timeout | | | | |
| K5 | job run multi-target | | | | |
| K6 | mockserver.exe script mode | | | | |
| L1 | output cap + truncated flag | | | | |
| L2 | per-command timeout | | | | |
| L3 | mid-run agent disconnect | | | | |
| L4 | SKVC run_command transcripts | | | | |
| L5 | default working-dir SKVC\tmp | | | | |
| M1 | Tab verb unique completion | | | | |
| M2 | Tab verb multiple candidates | | | | |
| M3 | Tab after verb no completion | | | | |
| M4 | Arrow-key history | | | | |
| M5 | history verb list | | | | |

---

## 7. End notes

- **Scope:** this guide covers NSH as of commit `fa694fb` (UC-19 HKU
  parity + on-demand `inventory refresh` + 100-target cap).
  Future UC-20 (script content audit) and FR1 proto fields
  (`schema_version` / `agent_capabilities` / `device_type`) are NOT in
  scope here.
- **If any PASS line is ambiguous**, record both expected and actual
  verbatim in the Record Sheet. Vijay will triage.
- **Dev-only shortcuts** (certgen cleartext keys, mock's non-RBAC
  authorization, static scope ACLs) are called out in-line and must
  NOT bleed into production.
- **Fleet-scale behavior** (bounded host listing, 100-target cap on
  inventory refresh, serialized per-device stream, bounded fanout
  worker caps) is production-shape — tester should verify limits
  rather than scale behavior at mock scale.
- **Hand-off:** attach the completed Record Sheet, agent + mock logs
  (`SKVC\logs\agent.log` and `logs\mockserver.log`), and any `uploads/`
  artifacts for each failing test.
