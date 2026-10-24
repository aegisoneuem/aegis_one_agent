# Aegis One NSH — Full Test Plan (Hand-over Draft)

**Audience:** Vijay for VM verification, then the server team as the acceptance
checklist for the real console/server implementation.
**Binaries under test:** `build/AegisOneClient.exe`, `build/mockserver.exe`
(same ones to copy into `dist/` after the run passes).
**Setup prerequisite:** finished §1–§9 of
[testing-use-cases.md](testing-use-cases.md) so the agent is enrolled,
`certs/` ready, mockserver reachable, and at least one agent connected.

## REPL recap

**REPL = Read-Eval-Print Loop.** An interactive prompt (`nsh>`) where you type
one command, the engine reads + evaluates it, prints the result, and loops.
Everything in this doc runs in that prompt — the same engine runs in
**non-interactive script mode** when `AEGIS_NSH_SCRIPT` points at a `.nsh`
file. The engine behaves identically either way, so these tests also validate
console-authored tasks.

Start interactive:
```
set AEGIS_NSH=1
mockserver.exe
```
Start scripted:
```
set AEGIS_NSH_SCRIPT=C:\tests\<name>.nsh
mockserver.exe
```

Expected outcomes are stated per test. **PASS** = output matches the row;
anything else is a FAIL to flag.

---

## A. REPL basics

### A1 — `hosts` lists known devices with connection status
```
nsh> hosts
```
**PASS:** known devices appear with HOST, DEVICE_ID, COMPUTER_ID and STATUS
(ONLINE/OFFLINE). Inventory can be listed before agents reconnect after a server
restart. `hosts host=win11` filters the output. You do not need to run hosts or
wait for the full fleet before selecting a known target.

### A2 — `cd \\<host>` sets a single target
```
nsh> cd \\win11-vm
win11-vm> hostname
```
**PASS:** prompt becomes `win11-vm>`; output block prints the host's hostname
and `OK (exit=0, <ms>)` footer.

### A3 — `cd *` targets every online agent (block single-command execution)
```
nsh> cd *
[1 hosts]> hostname
```
**PASS:** REPL refuses `hostname` with
`nsh: N targets selected; use fanout <cmd> for multi-target`.

### A4 — `fanout` runs the same command on every target, aggregated
```
[1 hosts]> fanout hostname
```
**PASS:** table with `HOST`, `RESULT`, `FIRST LINE`, `SAVED` columns.

### A5 — Unknown target is caught cleanly
```
nsh> cd \\does-not-exist
```
**PASS:** `nsh: unknown device: does-not-exist`. Existing selection is preserved.

### A6 — `use` switches interpreter
```
nsh> cd \\win11-vm
win11-vm> use powershell
win11-vm> Get-Service wuauserv | Select-Object -ExpandProperty Status
```
**PASS:** output shows `Running` (or whatever the real state is). No cmd-shell
error. `use cmd` + `net statistics server` also works.

### A7 — pwsh (PowerShell 7) honestly reports absence when not installed
```
win11-vm> use pwsh
win11-vm> echo hi
```
**PASS (if pwsh not installed):** `pwsh.exe not installed on this endpoint`
and ActionPhase `NOT_APPLICABLE`.

### A8 — Linux shell on Windows agent is refused honestly
```
win11-vm> use bash
win11-vm> ls
```
**PASS:** `bash is a POSIX shell; not available on Windows endpoint`,
phase `NOT_APPLICABLE`.

---

### A9 — Endpoint directory navigation persists across commands and host switches
Requires an existing C:\temp folder on the selected Windows endpoint.
```
nsh> cd \\win11-vm
win11-vm> use powershell
win11-vm> cd C:\temp
win11-vm> pwd
win11-vm> Get-Location
win11-vm> cd ..
win11-vm> pwd
```
**PASS:** CWD is C:\temp, then C:\. Repeat using `use cmd`, `cd /d C:\temp`
and `dir`. Relative paths and quoted paths containing spaces work. A nonexistent
directory fails and retains the previous CWD. Switch to another host: it has its
own CWD, not the first host's directory. Switch back: the first CWD is retained.
`cd` without arguments displays the selected endpoint's CWD; `cd \\` clears
targets and returns to nsh>. Each OS command remains a fresh process: this
persists the configured working_dir, not arbitrary environment/process state.

### A10 — Known offline target selection does not wait for fleet connections
After a mock restart with an existing mockserver-devices.json registry, run:
```
nsh> cd \\win11-vm
win11-vm> hostname
```
**PASS:** selection succeeds even if the device is OFFLINE. Its command is
immediately refused as offline (no queue, fleet barrier or heartbeat wait).
After that endpoint reconnects, `hostname` works without reselecting it, even
if other devices are still offline. Duplicate hostnames must be refused as
ambiguous; use `cd \\<device_id>` or `cd \\<computer_id>` instead.

## B. Output capture + file collection

After A8, run `use cmd` before continuing: `use` persists until changed.
Windows refuses `bash`/`sh`/`zsh` commands as `NOT_APPLICABLE`; they do not
execute or upload stdout. The footer must show the refusal reason.

Uploads are saved on the **mockserver machine**, relative to the directory
from which `mockserver.exe` was started. NSH files live under
`uploads/<computer_id>/<output-dir>/<host>/` (default output-dir: `nsh`).
Action IDs remain in logs, not folder names. Repeated explicit filenames get
numbered suffixes to preserve earlier files. Auto capture uses timestamps.

### B1 — `grab` saves stdout as a server-side file
```
nsh> cd \\win11-vm
win11-vm> use cmd
win11-vm> grab \out\winini.txt type C:\Windows\win.ini
```
**PASS:** footer line `---- saved to uploads/<computer_id>/nsh/win11-vm/out/winini.txt`.
File exists on the mock host at that path, contains the `win.ini` text.

### B2 — `set capture on` auto-saves every subsequent command
```
win11-vm> set capture on
win11-vm> use cmd
win11-vm> echo capture-check
```
**PASS:** output contains `capture-check` and footer
`saved to uploads/<cid>/nsh/win11-vm/<ts>-echo_capture-check.out`.
Then try `tasklist` (or `tasklist | findstr /i AegisOneClient`).
`findstr skvc` can match no process: the executable is `AegisOneClient.exe`.

### B3 — Multi-host capture → per-host files, no collision
Requires two agents online.
```
nsh> cd *
nsh> set capture on
[2 hosts]> fanout hostname
```
**PASS:** table lists both hosts, each `SAVED` column shows a DIFFERENT path
(`uploads/<cid-A>/nsh/<hostA>/...` and `uploads/<cid-B>/nsh/<hostB>/...`).

### B4 — `pull` fetches an endpoint file verbatim
```
win11-vm> pull C:\Windows\System32\drivers\etc\hosts \hosts.txt
```
**PASS:** `hosts.txt` lands at `uploads/<cid>/nsh/win11-vm/hosts.txt` and matches
the endpoint file byte-for-byte. `pull` uses signed `collect_file`, independent
of the selected shell. Try a binary file and a quoted path containing spaces
too: `pull "C:\temp\with spaces.bin" copy.bin`. Missing files report no match.
One source file per pull; wildcard paths are refused.

### B5 — `set output-dir` changes the default save prefix
```
nsh> set output-dir reports\daily
nsh> set capture on
win11-vm> hostname
```
**PASS:** file lands under `uploads/<cid>/reports/daily/win11-vm/*.out`.
Reset `set output-dir nsh` after this test if you want the default layout.

---

## C. Variables + substitution

### C1 — `let` + `${VAR}` substitutes on the console side before send
```
nsh> let TAG = prod-fleet
nsh> cd \\win11-vm
win11-vm> echo tag is ${TAG}
```
**PASS:** output contains literal `tag is prod-fleet`. The command sent to
the agent is already resolved — the agent did not need to know `${TAG}`.

### C2 — `${env:NAME}` picks up a mock-side env var
```
nsh> let USR = ${env:USERNAME}
nsh> vars
```
**PASS:** `USR = <your-windows-user>`.

### C3 — Last-result vars populate after every run
```
win11-vm> echo marker
win11-vm> vars
```
**PASS:** `vars` lists `exit=0`, `stdout=marker`, `host=win11-vm`, non-zero
`duration`, `reason` set.

### C4 — NSH vars become `AEGIS_PARAM_<NAME>` env vars on the endpoint
```
nsh> let CUSTOMER = acme
win11-vm> use powershell
win11-vm> Write-Host "customer env = $env:AEGIS_PARAM_CUSTOMER"
```
**PASS:** output `customer env = acme`. This is the pattern a pre-staged
.ps1 on the endpoint uses to read NSH-authored params without touching the
command string.

---

## D. Control flow

### D1 — `foreach host in *` iterates every online host
Requires 2 agents.
```
nsh> use powershell
nsh> foreach host in *
...>   log "checking ${host}"
...>   grab status.txt Get-Service wuauserv
...> end
```
**PASS:** both hosts show their log line; two different `SAVED` paths; no
collision; prompt returns to `nsh>` after `end`.

### D2 — `if $exit != 0` branches on failure
```
win11-vm> use cmd
win11-vm> dir Z:\does\not\exist
win11-vm> if $exit != 0
...>   log "FAILED, reason=$reason"
...> else
...>   log "OK"
...> end
```
**PASS:** prints `FAILED, reason=...`. If you repeat with `dir C:\` the
branch flips to `OK`.

### D3 — Nested blocks replay cleanly
```
nsh> for X in 1 2 3
...>   foreach host in *
...>     log "X=${X} host=${host}"
...>   end
...> end
```
**PASS:** 6 log lines (3 × 2 hosts). Order within a loop is deterministic;
across the fanout it may be concurrent.

### D4 — `break` / `continue` work inside a loop
```
nsh> for X in 1 2 3 4
...>   if $X == 3
...>     log "break at 3"
...>     break
...>   end
...>   log "X=${X}"
...> end
```
**PASS:** prints `X=1`, `X=2`, `break at 3`. Nothing after.

### D5 — `on-error continue` keeps a script running past a failure
Save to `C:\tests\on-error.nsh`:
```nsh
on-error continue
cd \\win11-vm
dir Z:\nope
log "survived (exit was $exit)"
hostname
```
Run: `set AEGIS_NSH_SCRIPT=C:\tests\on-error.nsh & mockserver.exe`.
**PASS:** both the `log` line and `hostname` output appear.

---

## E. Aliases + helpers

### E1 — `alias` creates a reusable verb
```
nsh> use powershell
nsh> alias def = Get-MpComputerStatus | Select-Object AMEngineVersion
nsh> cd \\win11-vm
win11-vm> def
```
**PASS:** PowerShell runs the expansion; output has an `AMEngineVersion`
line.

### E2 — `source` includes another .nsh (vars/aliases survive)
`C:\tests\vars.nsh`:
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
**PASS:** `vars` lists `REGION`; echo prints `region=us-east-1`.

### E3 — `history` lists recent commands
```
nsh> history
```
**PASS:** numbered list of the last few commands typed.

### E4 — `set session-log` writes every REPL line to a file
```
nsh> set session-log C:\tests\nsh-session.log
nsh> hostname
```
**PASS:** `C:\tests\nsh-session.log` grows with each line typed and a
timestamped `=== session resumed ===` header on open.

---

## F. Native-script execution (`.ps1`, `.bat`, `.cmd`, `.py`)

### F1 — `exec <local .ps1>` with console-side `${VAR}` substitution
Save `C:\tests\check.ps1`:
```powershell
Write-Host "tag=${TAG} on $env:COMPUTERNAME"
Write-Host "env tag = $env:AEGIS_PARAM_TAG"
Get-Service wuauserv | Select-Object Status
```
Interactive:
```
nsh> let TAG = daily-21oct
nsh> cd \\win11-vm
win11-vm> exec C:\tests\check.ps1
```
**PASS:** output includes `tag=daily-21oct on <HOSTNAME>` and
`env tag = daily-21oct` (both resolution channels work).

### F2 — `exec <local .bat>` runs via cmd
Save `C:\tests\check.bat`:
```bat
@echo off
echo running on %COMPUTERNAME%
echo tag env is %AEGIS_PARAM_TAG%
```
```
nsh> exec C:\tests\check.bat
```
**PASS:** output shows the computer name and `tag env is daily-21oct`.

### F3 — `exec python <local .py>` runs through python.exe
Save `C:\tests\check.py`:
```python
import os
print("py tag =", os.environ.get("AEGIS_PARAM_TAG"))
```
```
nsh> exec python C:\tests\check.py
```
**PASS (if python on PATH):** `py tag = daily-21oct`.
**PASS (if python NOT installed):** honest error `python.exe is not
recognized ...` and non-zero exit — not a crash.

### F4 — `exec <nonexistent file>` is caught
```
nsh> exec C:\does\not\exist.ps1
```
**PASS:** `nsh: exec: read C:\does\not\exist.ps1: ...`.

---

## G. HEREDOC — inline multi-line script (BigFix `createfile until` ergonomic)

Type the `script ... until ...` opener **once**, then enter only the script
body lines. The prompt changes to `... [EOT]>` (or your chosen delimiter).
Finish by typing the delimiter alone on its own line. Do not type the prompt
text or repeat the opener: a repeated opener becomes part of the script body.
No local file needs to be created for these tests.

### G1 — PowerShell inline block with `${VAR}` substitution
```
nsh> let REGION = us-west-1
nsh> cd \\win11-vm
win11-vm> script powershell until EOT
          Write-Host "REGION=${REGION}"
          Write-Host "HOST=$env:COMPUTERNAME"
          Get-Service wuauserv | Select-Object Status, Name
          EOT
```
**PASS:** output has `REGION=us-west-1`, the host name, and the service
status line. No file was dropped on the endpoint (confirm nothing new under
`C:\Windows\Temp\`).

### G2 — HEREDOC inside a `foreach` block runs per host
Requires 2 agents. Save `C:\tests\fleet-herdoc.nsh`:
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
```
set REGION=apac
set AEGIS_NSH_SCRIPT=C:\tests\fleet-herdoc.nsh
mockserver.exe
```
**PASS:** every host prints its name + the engine version; per-host files
land under `uploads/<cid>/nsh/<host>/*.out` without collision.

`REGION` above is imported explicitly from the **mockserver process** environment;
`set REGION=apac` alone does not create an NSH variable. For interactive testing,
use `let REGION = apac`. Ensure the names are `Get-MpComputerStatus` and
`AMEngineVersion`. A missing cmdlet or other unhandled PowerShell error must
produce `FAIL` with nonzero exit, even when a later command would succeed.
`on-error continue` continues to the next host; it does not turn a failed
endpoint command into success. The Defender command requires Defender support
on that endpoint; unavailable cmdlets should fail honestly.

### G3 — `.bat` HEREDOC flattens correctly
```
win11-vm> use cmd
win11-vm> script cmd until END
          echo one
          echo two
          echo three
          END
```
**PASS:** output is three lines in order.

### G4 — HEREDOC body preserves blank lines and comments
```
win11-vm> script powershell until END
          # a comment line
          
          Write-Host "after blank"
          END
```
**PASS:** PowerShell runs; output is `after blank`. The blank line and
`#` comment were preserved inside the heredoc (they are NOT NSH comments
there — the PowerShell parser treats `#` as its own comment).

---

## H. Agent-side artifacts

### H1 — `run_command` transcripts land under SKVC
After running a few commands:
```
dir "C:\Program Files\AegisOne\Client\SKVC\logs\nsh\"
```
**PASS:** one `.transcript` file per command, each containing a header
with `shell=`, `exit=`, `<ms>`, followed by `CMD:`, `--- STDOUT ---`,
`--- STDERR ---` sections.

### H2 — Shell transcripts land under SKVC (open_shell path)
Open an interactive session (open_shell plumbing is intact; test with a
future console wire-up or by manually triggering a signed open_shell).
**PASS:** files under
`C:\Program Files\AegisOne\Client\SKVC\logs\shell\<cmd-id>.transcript`
with `[IN hh:mm:ss]` / `[OUT hh:mm:ss]` / `[ERR hh:mm:ss]` lines.
*Note:* the mock no longer auto-opens shells on connect
(`AEGIS_SHELL=1` auto-trigger was removed). This test is deferred until
the console UI wires the session; the agent-side plumbing is already
validated.

### H3 — Service-context CWD does NOT bite us
```
win11-vm> use cmd
win11-vm> pwd
win11-vm> cd C:\temp
win11-vm> dir
win11-vm> pwd
```
Default `working_dir` for `run_command` is `<SKVC>\tmp`, so an unqualified
command runs inside `<SKVC>\tmp`, NOT `C:\Windows\System32`.
**PASS:** before explicit navigation on a fresh session, pwd shows SKVC\tmp.
After `cd C:\temp`, commands use C:\temp (which must already exist on endpoint).

---

## I. Safety + limits

### I1 — Output caps enforced
```
nsh> set max-output 1024
win11-vm> type C:\Windows\win.ini
```
**PASS:** inline output is capped at ~1024 bytes; `truncated` is reported
in the structured `Detail` JSON. Full stdout is still saved to the server
when `grab` / `set capture on` is active.

### I2 — Per-command timeout
```
nsh> set timeout 2
win11-vm> ping -n 10 localhost
```
**PASS:** command killed after 2s; `reason=timeout`; non-zero exit.

### I3 — Honest refusal when the agent disconnects mid-run
Pull the agent service while a long command runs.
**PASS:** `nsh: timed out waiting for <action_id>`; mock does not hang.

---

## J. Non-interactive script mode (future console task simulation)

### J1 — Full fleet-check script
Save `C:\tests\fleet-check.nsh`:
```nsh
# Fleet-wide defender posture check
let TAG = daily-${env:USERNAME}
set output-dir reports\${TAG}
set session-log C:\tests\fleet-check.log
use powershell
on-error continue

foreach host in *
  log "== check ${host} =="
  grab engine.txt Get-MpComputerStatus | Select-Object AMEngineVersion
  if $exit != 0
    log "FAILED on ${host}: $reason"
    continue
  end
  grab sigs.txt Get-MpComputerStatus | Select-Object AntivirusSignatureVersion
end

log "all done"
```
```
set AEGIS_NSH_SCRIPT=C:\tests\fleet-check.nsh
mockserver.exe
```
**PASS:** mock exits after the script runs; session log at
`C:\tests\fleet-check.log` has every line; per-host files under
`uploads/<cid>/reports/daily-<user>/<host>/engine.txt` and `sigs.txt`;
mock console shows `all done`.

### J2 — Script with HEREDOC + variable + conditional + loop
Save `C:\tests\pack.nsh`:
```nsh
let PATH_PREFIX = C:\temp\report
set output-dir reports\pack
on-error continue
use powershell

foreach host in *
  script powershell until BLOCK
    $p = "${PATH_PREFIX}-$env:COMPUTERNAME.txt"
    "generated at $(Get-Date)" | Out-File -FilePath $p -Encoding utf8
    Write-Output $p
    BLOCK
  if $exit == 0
    log "wrote on ${host}: $stdout"
  else
    log "skip ${host}: $reason"
  end
end
```
```
set AEGIS_NSH_SCRIPT=C:\tests\pack.nsh
mockserver.exe
```
**PASS:** every host prints a `wrote on ...: C:\temp\report-<HOSTNAME>.txt`
line; files exist on the endpoints.

---

## K — Native file jobs and bounded inventory search

See [nsh-file-operations.md](nsh-file-operations.md) for the full manual and
acceptance cases. Use disposable test folders and updated agent + mockserver.

```text
search win11
hosts --status offline --page 1 --limit 20
hosts --page 2
mkdir \\vm1,vm2\c\temp\nsh-test
copy \\srv1\c\temp\loop.txt \\vm1,vm2\c\temp\nsh-test\
touch \\vm1,vm2\c\temp\nsh-test\empty.txt
delete \\vm1,vm2\c\temp\nsh-test\empty.txt
```

**PASS:** host list is bounded (default 20, max 100 rows); status and total/next
page are visible; copy preserves the source and exact bytes on both targets.
Verify existing-file refusal, explicit --overwrite, binary/recursive/glob copy,
one-to-one move, offline/permission failures and per-host status. No manual
foreach loop or SMB credentials are required. Source/destination endpoints must
be managed agents. A no-target right-click/session open must not print any list.

## Hand-off note for the server team

The reference engine the real console will embed is at
`cmd/mockserver/nsh.go` + `cmd/mockserver/nsh_script.go`. The agent-side
contract is `RunCommandSpec` + `RunCommandResult` in `grpc/agent.proto`
and the handler at `cmd/agent/runcommand.go`. The console UI team should
reuse the engine verbatim (verb set, variable substitution, loop/if
semantics) and expose it in a web terminal; the server team should
implement `UploadContent`, operator sessions, device-targeting ACLs, and
command-signing on top of the same `AgentControlPlane` contract.
