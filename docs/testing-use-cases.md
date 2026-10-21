# Aegis One Agent — Verified Test Use Cases (Server Team Replay Guide)

**Audience:** Aegis One server team.
**Purpose:** every scenario below has been tested end-to-end on real VMs
(Windows 11 + Windows Server 2022) with the mockserver. Replay them in the same
order to validate your real-server implementation against the same contract.
Setup (certs, enrollment token, run) is in [mockserver-guide.md](mockserver-guide.md);
patch-specific detail is in [patching-test-guide.md](patching-test-guide.md).

All commands below are **mockserver environment variables** set before starting
`mockserver.exe`. The mock signs every command (Ed25519) exactly like the real
server must. Prebuilt binaries are in `dist/` (agent `AegisOneClient.exe` /
`AegisOneClient.msi`, `mockserver.exe`, `certgen.exe`).

> Multi-device: run two or more agents against one mockserver. By default the
> mock sends the triggered commands to **every** device that connects; set
> `AEGIS_TARGET_DEVICE=<device_id>` to target one (others receive nothing).

---

## 1. Enrollment + identity (stable device_id / computer_id)

**Steps:** install the agent (`AegisOneClient.msi`) with the enroll token; it
enrolls over the no-client-cert path, gets a CA-signed cert, reconnects mTLS.

**Verified:** `device_id` (UUID, agent-generated, stored in HKLM registry) and
`computer_id` (random 10-digit console id, server-assigned) stay **stable**
across agent uninstall/reinstall and service restarts. Mock prints
`device=<uuid> computer_id=<10 digits> host=<hostname>` on every action status.

## 2. Compliance scan (on-demand + cab-triggered)

```
set AEGIS_SCAN_NOW=1
```
**Verified:** agent runs the WUA scan, streams `PatchScanResult`; a new cab
version (`AEGIS_CAB_VERSION`) also triggers a scan. Baseline self-diff:
`AEGIS_BASELINE_BUILD` + `AEGIS_BASELINE_KBS` (comma-separated).

## 3. Patch install (single + multi)

```
set AEGIS_INSTALL_FILE=C:\patches\windows11-kb5044033.msu
set AEGIS_INSTALL_KB=KB5044033
set AEGIS_INSTALL_HASH=<sha256>
```
**Verified:** phased `ActionStatus` (QUEUED → DOWNLOADING → VERIFYING → RUNNING
→ SUCCESS/FAILED/NOT_APPLICABLE) with exit codes; hash verify before run;
multiple files via `;` lists; serial install worker (one OS install at a time).
Staging ahead of install: `AEGIS_STAGE_KB/_FILE/_HASH` (encrypted cache,
StateStaged).

## 4. Patch uninstall (registry-resolved DISM → wusa → QFE honesty)

```
set AEGIS_UNINSTALL_KB=KB5044033
```
**Verified chain:** agent resolves the CBS package name from the registry
(`...\Component Based Servicing\Packages` Owners), tries
`DISM /remove-package`, falls back to `wusa /uninstall /kb:<n>`, then does an
honest QFE re-check (Win32_QuickFixEngineering) and reports the truth.

**Known limitations (by design, reported honestly — do NOT treat as agent bugs):**
- CBS marks the current LCU **permanent** (error `0x800f0825`) — Windows refuses
  removal; the agent honors the refusal (same behavior as manual DISM).
- `wusa /quiet` returns error 87 in the session-0 LocalSystem service context —
  agent reports "Windows does not allow uninstalling it in service context".
  (BigFix patch rollback fails the same way most of the time.)

## 5. Reboot control

```
set AEGIS_REBOOT_DELAY=60      (seconds)
set AEGIS_CANCEL_REBOOT=1      (cancels a pending one)
```
**Verified:** scheduled reboot with delay, cancel, `ActionStatus` both ways.

## 6. Software deploy — install (msi / exe / scripts / command)

One task type `software_deploy`; `action` chooses install/uninstall.

```
set AEGIS_DEPLOY_FILE=C:\pkg\7z2404-x64.exe        (pre-staged local path)  — or —
set AEGIS_DEPLOY_CONTENT_ID=pkg-7zip-2404          (server-delivered via FetchContent)
set AEGIS_DEPLOY_NAME=7-Zip
set AEGIS_DEPLOY_VERSION=24.04
set AEGIS_DEPLOY_TYPE=exe                          (msi|exe|ps1|bat|cmd|py|vbs|command|auto)
set AEGIS_DEPLOY_ARGS=/S                           (admin-authored silent args — agent never guesses)
set AEGIS_DEPLOY_HASH=<sha256>
set AEGIS_DEPLOY_PRODUCT_CODE={GUID}               (MSI idempotency / fileless uninstall)
```
**Verified:** MSI (`msiexec /i /qn`), EXE with custom args (e.g. Sublime
`/VERYSILENT /NORESTART /TASKS=contextentry`), `.ps1` / `.bat` / `.cmd` /
`.py` / `.vbs` payloads, and `type=command` (raw command line, no file
delivery). Hash verified before run; optional Authenticode check
(`AEGIS_DEPLOY_REQUIRE_SIG=1`).

- **Hash is MANDATORY for every file-based deploy (supply-chain gate, BigFix
  prefetch model).** The download folder is untrusted staging: a file there is
  trusted only if it matches the sha256 pinned inside the Ed25519-signed
  command. A file-based deploy without `expected_hash` is **refused** —
  `FAILED: unverified payload refused`. No bypass. Fileless uninstalls and
  `type=command` carry no file, so no hash applies (the signed command text is
  their integrity).
- **CONSOLE CONTRACT: compute the sha256 automatically at package upload** and
  put it in the spec — the admin never types a hash (BigFix wizard behavior).
  The mock does the same: with no `AEGIS_DEPLOY_HASH` it auto-computes sha256
  from `AEGIS_SERVE_SOFTWARE` (the package it serves), or from
  `AEGIS_DEPLOY_FILE` when that path is readable on the mock machine. Only
  when the file exists solely on the agent machine must you supply
  `AEGIS_DEPLOY_HASH` yourself (`certutil -hashfile <file> SHA256`).

**Hash-gate test cases (run all three):**

| # | Setup | Expected result |
|---|---|---|
| 6a **Refusal (negative)** | File pre-staged ONLY on the agent machine (`AEGIS_DEPLOY_FILE=C:\...\setup.exe` not readable on the server box), no `AEGIS_DEPLOY_HASH` | Mock prints `WARNING: ... cannot read ... to auto-hash`; agent reports `FAILED — unverified payload refused — expected_hash (sha256) is required for file-based deploys` and nothing executes |
| 6b **Auto-hash (positive)** | `AEGIS_DEPLOY_CONTENT_ID=pkg-x` + `AEGIS_SERVE_SOFTWARE=C:\pkg\setup.msi` on the server box, no `AEGIS_DEPLOY_HASH` | Mock prints `Auto-computed package sha256 (...)`; agent verifies (`installer integrity verified`), installs, post-action verify SUCCESS |
| 6c **Tamper (negative)** | Valid hash pinned in the command, then REPLACE/modify the staged file on the agent machine before it runs | Agent reports `FAILED — integrity check failed (hash mismatch) — refusing to run`; nothing executes |

6c is the attack this gate exists for: a malicious file dropped into
`SKVC\download` under the expected name can never run, because trust lives in
the hash inside the signed command — never in the file on disk.

- **Idempotency + version-aware upgrade:** MSI → `MsiQueryProductState` by
  ProductCode; non-MSI → registry DisplayName/DisplayVersion. If installed
  version < target, the agent proceeds **as an upgrade** (verified 7-Zip
  23.01 → 26.04); same/higher → skipped with reason.
- **Post-action verification ("Fixed"):** after install the agent re-detects
  (retry 4×5s for slow NSIS finishes) and only then reports
  `verified installed (version X)`.

## 7. Software deploy — uninstall (fileless)

```
set AEGIS_DEPLOY_ACTION=uninstall
set AEGIS_DEPLOY_PRODUCT_CODE={GUID}            (MSI: msiexec /x {GUID} /qn)   — or —
set AEGIS_DEPLOY_UNINSTALL_KEY=7-Zip            (non-MSI: HKLM Uninstall subkey) — or —
set AEGIS_DEPLOY_DISPLAY_NAME=7-Zip             (non-MSI: DisplayName substring)
```
**Verified (all 4 combos: MSI/EXE × install/uninstall):** no payload file
needed; non-MSI resolves `QuietUninstallString` (preferred) or
`UninstallString` from HKLM native + WOW6432Node; post-action re-detect
confirms removal.

## 8. Parameters (BigFix-style `${name}` substitution)

```
set AEGIS_DEPLOY_PARAMS=LICENSE=ABC-123,INSTALLDIR=C:\app
```
**Verified:** `${name}` tokens in `silent_args` are substituted from author
params + built-in runtime params (e.g. `${hostname}`, resolved **on each
endpoint**, proven on two VMs from one signed command — fleet pattern). GUID
`{...}` strings are collision-safe. All params are also exported to child
scripts as `AEGIS_PARAM_<NAME>` env vars, so loops/scripts can use them freely.

## 9. On-demand file collection (collect_file → UploadContent)

```
set AEGIS_COLLECT_PATHS=C:\Program Files\SKVC\logs\*.log;C:\temp\report.txt
set AEGIS_COLLECT_TAIL=1048576          (optional: last N bytes of each file)
set AEGIS_COLLECT_MAX_EACH=52428800     (optional per-file cap override)
set AEGIS_COLLECT_MAX_TOTAL=524288000   (optional per-task cap override)
```
**Verified (two endpoints simultaneously):** glob expansion, SHA-256
end-to-end integrity (server recomputes + acks), chunked 1 MiB streaming over
the existing mTLS channel, per-file status in `ActionStatus.detail`. Missing
paths are reported as `no match` — not an error for the files that do exist.

- **Pull-only model:** the agent never auto-uploads; it only answers a signed
  `collect_file` command (BigFix/BladeLogic style), so idle agents add no load.
- **Storage (mock):** `uploads/<computer_id>/<action_id>/<filename>`. Keyed by
  **computer_id** because hostnames can collide across a fleet; resolve
  computer_id → hostname from console inventory.
- **Size caps (defaults, overridable per command via `CollectFileSpec`):**
  50 MiB per file, 500 MiB per task, `tail_bytes` to fetch only the end of big
  logs. Caps keep the newest bytes (log tail), not the oldest.
- **Real server:** implement the `UploadContent` streaming RPC exactly like the
  mock (first chunk = header `action_id`/`name`/`total_size`, last chunk =
  `sha256_full`, respond `UploadAck`); store in your artifact store keyed by
  device, expose it to the console per computer. No extra agent-side API is
  needed — this RPC **is** the channel.

## 10. NSH (console operator REPL) + `run_command` task

The NSH is the console-side REPL for ad-hoc operator work: list devices, pick
targets, run commands, grab output as server-side files, fan out across many
hosts, run non-interactive scripts. BladeLogic NSH / BigFix WebUI equivalent,
without the relevance-language learning curve.

### Start it

```
set AEGIS_NSH=1
mockserver.exe
```
The REPL opens on the mock's stdin after the gRPC server comes up. For scripted
(non-interactive) mode:
```
set AEGIS_NSH_SCRIPT=C:\checks\daily.nsh
mockserver.exe
```

### Verbs (type `help` in the REPL for the full list)

| Verb | What it does |
|---|---|
| `hosts [filter]` | List online devices (`host=sub`, `id=sub`, `cid=sub`) |
| `cd \\<host>` | Set target (single) |
| `cd \\<h1>,<h2>` | Multi-target |
| `cd *` | All online |
| `cd` | Clear target |
| `<command>` or `run <command>` | Run on current target(s) |
| `grab [server-path] <command>` | Run + save stdout to a server-side file |
| `pull <remote> [server-path]` | Fetch an endpoint file verbatim (reuses collect_file plumbing) |
| `fanout <command>` | Explicit multi-target run, aggregated table |
| `use cmd` / `use powershell` / `use pwsh` | Interpreter switch |
| `set timeout <sec>` | Per-command timeout (default 300) |
| `set max-output <bytes>` | Inline capture cap (default 1 MiB) |
| `set capture on/off` | Auto-save every command's stdout (default off) |
| `set output-dir <path>` | Default server-side save prefix (default `nsh`) |
| `script run <file>` | Non-interactive batch (same engine, capture=on) |
| `history` | Recent commands |
| `shell` | Drop into persistent interactive shell (open_shell) |
| `exit` / `quit` | Close REPL |

**Scripting (variables + control flow — in REPL and in `.nsh` files):**

| Keyword | What it does |
|---|---|
| `let NAME = value` | Define variable (supports `${name}`, `${env:NAME}` on RHS) |
| `${VAR}` / `$VAR` | Substitute a REPL variable in any argument/command |
| `${env:NAME}` | Mock-side env var |
| `foreach host in *` ... `end` | Iterate online hosts (binds `$host`, implicit `cd`) |
| `foreach host in <csv>` ... `end` | Iterate specific hosts |
| `for VAR in a b c` ... `end` | Plain loop |
| `if <cond>` ... `else` ... `end` | Conditions: `$exit == 0`, `$exit != 0`, `stdout contains "x"`, `defined NAME`, `!` to negate |
| `break [N]` | Exit N enclosing loops |
| `continue` | Next iteration |
| `on-error continue\|stop` | Script error policy (default stop) |
| `log <message>` | Explicit script output marker |
| `alias NAME = <expansion>` | Reusable command |
| `source <file>` | Include another .nsh (vars/aliases) |
| `wait <sec>` | Sleep |
| `vars` | List defined variables |
| `set format table\|json\|csv` | Fanout output format |
| `set session-log <path>` | Append full REPL to a file (`off` to disable) |

**Last-result variables** (updated after each command, usable in `if` / substitution):
`$exit`, `$stdout`, `$stderr`, `$host`, `$duration`, `$reason`

**Running NATIVE scripts (.ps1 / .bat / .cmd / .py / .sh) with NSH vars inside**

Two patterns — BigFix's "action script with embedded relevance" is covered by #1:

1. **Script lives on the CONSOLE** → `exec [shell] <local-file>`
   The REPL reads the file, substitutes every `${VAR}` / `${env:NAME}` on the
   server side, then ships the final script content to the agent as one
   signed `run_command`. Shell is inferred from the extension (`.ps1` →
   powershell, `.bat`/`.cmd` → cmd, `.py` → python, `.sh` → bash), or named
   explicitly. PowerShell is sent as `-EncodedCommand` (UTF-16LE+base64) so
   complex multi-line scripts with any quoting pass safely. Example:
   ```nsh
   let TAG = prod
   let REGION = us-east-1
   cd *
   exec check.ps1
   ```
   Where `check.ps1` on the console has `Write-Host "Tag: ${TAG} Region: ${REGION}"`
   and gets `${TAG}` resolved BEFORE being sent (plain BigFix relevance
   substitution model).

2. **Script lives on the AGENT** (pre-staged via softdist, or in SKVC\download)
   NSH vars become `AEGIS_PARAM_<NAME>` env vars on the child process — the
   .ps1/.bat/.py reads `$env:AEGIS_PARAM_TAG` to pick them up. Example:
   ```nsh
   let BATCH = daily-$(get-date-slug)
   cd *
   powershell -File C:\scripts\check.ps1 -Batch "${BATCH}"
   ```
   The `${BATCH}` substitutes in the ARG on the console; inside `check.ps1`
   the admin can also read `$env:AEGIS_PARAM_BATCH` — both work.

**Cross-platform promise:** when the agent targets Linux/macOS, `exec` ships
`.sh` as `bash -c "echo <b64> | base64 -d | bash"` — same wrapping idea,
same NSH substitution. No code change needed on the console side.

### Example session

```
nsh> hosts
HOST                DEVICE_ID                   COMPUTER_ID
win11-vm            c8371223-abb8-420c...       7315850516
srv2022             4421938271-...              4421938271

nsh> cd \\win11-vm
win11-vm> hostname
---- win11-vm $ hostname
win11-vm
---- win11-vm OK  (exit=0, 42ms)

win11-vm> use powershell
shell = powershell
win11-vm> Get-Service wuauserv
---- win11-vm $ Get-Service wuauserv
Status   Name       DisplayName
------   ----       -----------
Running  wuauserv   Windows Update
---- win11-vm OK  (exit=0, 320ms)

win11-vm> grab \logs\hosts.txt type C:\Windows\System32\drivers\etc\hosts
---- win11-vm $ type C:\Windows\System32\drivers\etc\hosts
# Copyright (c) 1993-2009 Microsoft Corp. ...
---- saved to uploads/7315850516/\logs\hosts.txt
---- win11-vm OK  (exit=0, 18ms)

nsh> cd *
targets: 2 online host(s)
[2 hosts]> fanout tasklist /svc | findstr defender
HOST                RESULT      FIRST LINE
srv2022             ok (0)      MsMpEng.exe   2104 WinDefend
win11-vm            ok (0)      MsMpEng.exe   3460 WinDefend
```

### Non-interactive script mode (for console-authored tasks)

A `.nsh` file is just REPL lines. Capture is on by default so every command's
stdout is saved to the server. `#` lines are comments.

```nsh
# daily-defender-check.nsh — fleet-wide check with per-host output
let REPORT_DIR = \defender\${env:USERNAME}
set output-dir ${REPORT_DIR}
set session-log C:\logs\nsh-daily.log
use powershell
on-error continue

foreach host in *
  log "== Checking ${host} =="
  grab \engine-${host}.txt Get-MpComputerStatus | Select-Object AMEngineVersion
  if $exit != 0
    log "WARN: Get-MpComputerStatus failed on ${host} (exit=$exit)"
    continue
  end
  grab \sigs-${host}.txt   Get-MpComputerStatus | Select-Object AntivirusSignatureVersion
end

log "done"
```

Run: `set AEGIS_NSH_SCRIPT=daily-defender-check.nsh & mockserver.exe` — fans
out across all online endpoints, outputs land under
`uploads/<computer_id>/\defender\<user>\...` for a WebUI report later.

### What's actually happening

- Each bare command → signed `run_command` task → agent executes with the
  chosen interpreter (`cmd` / `powershell` / `pwsh`) → stdout+stderr+exit come
  back as `ActionStatus.Detail` (JSON `RunCommandResult`). The REPL parses
  that and renders.
- `grab` adds `upload_as=<server-path>` to the spec — agent captures the FULL
  stdout to a temp, uploads via `UploadContent` (same channel as collect_file),
  then returns. Inline capture still rides on `ActionStatus` for the REPL.
- Default upload location: `uploads/<computer_id>/<output-dir>/<host>/<ts>-<slug>.out`.
  Override per command (`grab \my\path\x.out ...`) or globally
  (`set output-dir \reports`).
- Agent transcript of every `run_command` goes to `SKVC\logs\nsh\<action_id>.transcript`
  on the endpoint (future hardening; v1 relies on server-side ActionStatus log).

### Cross-platform promise

The spec's `shell` field accepts `cmd`, `powershell`, `pwsh` (Windows today),
and `bash`, `sh`, `zsh` (Linux/macOS when the agent targets those OSes). An
unsupported shell on this endpoint is reported honestly as `NOT_APPLICABLE` —
never silently coerced to another shell.

## 11. Remote shell (open_shell → ShellSession — deep interactive case)

> `AEGIS_SHELL=1` auto-trigger from the per-connect block was removed. The
> interactive persistent shell is for the deep-investigation case (BladeLogic
> interactive nexec); 90% of ad-hoc work goes through `run_command` via the NSH
> REPL (§10). The console UI (future) will wire a web terminal (xterm.js) to
> `open_shell` + `ShellSession`. Today the plumbing is tested, but the mock no
> longer wires its own stdin to it.

A signed `open_shell` command carries a single-use `session_id`.
The agent dials `ShellSession`, spawns `cmd.exe` as LocalSystem, and pipes stdin
in / stdout+stderr out live over the mTLS channel. From the mock terminal you
type commands directly; the shell output prints live. **This is text streaming
only — NOT RDP, no graphical session, no pixels.** Close with `Ctrl+Z Enter` on
Windows (stdin EOF → `control:"close"`), or let the idle timeout fire.

- **Supply-chain / auth:** every session is bound to a fresh random `session_id`
  carried in the Ed25519-signed `open_shell` command; a replayed or unknown
  session_id is refused (`ShellSession REFUSED`). TTL (default 60s) refuses an
  old signed command played back later.
- **Audit transcript:** every byte in and out is appended to
  `SKVC\logs\shell\<command_id>.transcript` on the endpoint — the console can
  pull it afterwards via `collect_file`. A remote shell without an audit trail
  would be a compliance non-starter.
- **Timeouts:** idle (no stdin AND no stdout for N seconds) and hard max
  duration — the agent kills the shell and reports the reason
  (`idle_timeout`, `max_duration`, `close`, `exited`).
- **v1 limits (deliberate):** line-based plain pipes. Arrow keys, tab
  completion, colors, and interactive tools (ssh client, vim) are NOT supported
  yet. v2 will switch to Windows **ConPTY** (`CreatePseudoConsole`) for full
  interactive terminal — same protocol, better pipe. v2 lands with the real
  console UI (xterm.js).
- **No-PowerShell rule still holds:** the agent spawns `cmd.exe` itself and
  never uses PowerShell for its own logic. If the admin types `powershell` in
  the session, that's the admin's payload (same rule as `.ps1` deploys —
  faithful executor).

## 12. License gate

```
set AEGIS_LICENSE_MAX=1
```
**Verified:** enrollment beyond the licensed count is refused.

---

## Backlog (known, deliberately deferred)

- **Bandwidth throttling** for uploads/downloads (collect_file + FetchContent)
  — not implemented yet; caps bound *size*, not *rate*.
- Per-user (HKCU) software uninstall.
- Patch uninstall in service context (wusa error 87) — research item.
- GUI repair/modify options on the agent MSI.
