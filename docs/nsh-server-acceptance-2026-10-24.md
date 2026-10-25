# NSH server-team acceptance — 24 October 2026

Vijay reports today's NSH tests passed. Replay the cases below against this
handoff before implementing the console integration. This document covers the
existing interactive/file-operation build; the reusable Script Job executor is
the next coding block and is not a prerequisite for these tests.

## Build and setup

Pull main and use all three matching dist artifacts: AegisOneClient.exe,
mockserver.exe and AegisOneClient.msi (MSI is an alternative way to deploy the
matching agent). Stop/restart processes when replacing files. Do not mix an old
agent with the new mockserver: file_operation is a new signed agent task.

Keep existing certificates, enrollment settings and mockserver-devices.json.
Run at least two managed Windows agents. Examples use WIN-TUES4V46T7I and
mockserver; replace those hostnames with your actual agents. The machine named
mockserver in a device path must itself run an agent; it does not refer to the
mockserver process's local filesystem.

In CMD on the server, before starting mockserver:
```bat
set AEGIS_NSH_SCRIPT=
mockserver.exe nsh
```

Background connection/heartbeat/scan/upload events belong in
logs\mockserver.log relative to launch directory; AEGIS_MOCK_LOG_FILE overrides
that location. Only operator commands/results belong in the terminal.

## 1. Clean prompt, device search and bounded listing

```text
hosts
search WIN-TUES
hosts --status online --limit 1 --page 1
hosts --status online --limit 1 --page 2
hosts --status offline
```

PASS: prompt is not interrupted by background messages; status is ONLINE/OFFLINE.
Default page is 20 rows, maximum 100. Footer reports total matches and next page.
No list is automatically printed on opening the terminal or selecting a device.
`hosts --limit 50000` is rejected. Hostname/device ID/computer ID can be searched.
The automated 50k-inventory test checks bounded results, not live fleet capacity.

Restart the mock with its existing inventory and select a known device before it
reconnects using `cd \\<hostname>`: selection should succeed as OFFLINE. `hostname`
must fail immediately until this device reconnects, independently of other
devices. Do not expect offline command execution or wait for the entire fleet.

## 2. Endpoint working directory and target preservation

```text
cd \\WIN-TUES4V46T7I
use powershell
cd C:\temp
pwd
Get-Location
cd ..
pwd
cd C:\Aegis-This-Folder-Does-Not-Exist
pwd
cd \\mockserver
pwd
cd \\WIN-TUES4V46T7I
pwd
```

PASS: CWD is remotely validated; it changes to C:\temp then C:\, stays unchanged
after the invalid path, and is tracked separately per endpoint. Switch back to
the first endpoint and its prior CWD is retained. Repeat with `use cmd` and
`cd /d C:\temp`. Quoted paths with spaces and `cd ..` work. Argumentless cd/pwd
show CWD; `cd \\` clears selection. This does not persist arbitrary OS environment
state between run_command processes; persistent ShellSession integration is
separate console work.

Run a foreach script from a selected host. After completion, the original
selected-host prompt must be restored, not the last host visited in the loop.

## 3. PowerShell exec/HEREDOC and G2 (positive and negative)

Save this server-local file as C:\tests\fleet-herdoc.nsh:
```nsh
let REGION = apac
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

In NSH, run `source C:\tests\fleet-herdoc.nsh` after intended agents are online.
PASS: both names, region=apac and AMEngineVersion print; per-host terminal status
is OK; captures are separate under uploads/<cid>/nsh/<host>/. Defender must be
available for its cmdlet to succeed. Correct spellings are Get-MpComputerStatus
and AMEngineVersion. Blank lines/comments inside HEREDOC must survive.

Replace the cmdlet with Get-AegisDeliberatelyMissingCmdlet, then source again.
PASS: both commands report FAIL/nonzero exit, and on-error continue still visits
the next host. No misleading OK after an unhandled PowerShell error. Restore
the correct command afterward. File exec uses the same native-script wrapper.
When opening a HEREDOC interactively, enter its opener once and finish with its
delimiter alone; continuation prompt shows the expected delimiter.

## 4. Prepare disposable file-operation data

All file tests below use C:\temp\AegisNSH-20261024. Do not substitute existing
application/data folders. In NSH:
```text
set capture off
cd \\WIN-TUES4V46T7I
use powershell
mkdir C:\temp\AegisNSH-20261024
Set-Content -LiteralPath C:\temp\AegisNSH-20261024\loop.txt -Value 'NSH transfer check'
mkdir \\WIN-TUES4V46T7I,mockserver\c\temp\AegisNSH-20261024\out
```

PASS: both managed endpoints create the requested folders; file lives on the
source endpoint. Use native Get-FileHash on source and destination to compare
SHA-256 after each copy. Native shell actions run as LocalSystem.

## 5. Single, bulk, binary, directory and overwrite copy

```text
copy \\WIN-TUES4V46T7I\c\temp\AegisNSH-20261024\loop.txt \\mockserver\c\temp\AegisNSH-20261024\out\
copy \\WIN-TUES4V46T7I\c\temp\AegisNSH-20261024\loop.txt \\mockserver\c\temp\AegisNSH-20261024\out\
copy --overwrite \\WIN-TUES4V46T7I\c\temp\AegisNSH-20261024\loop.txt \\mockserver\c\temp\AegisNSH-20261024\out\
copy \\WIN-TUES4V46T7I\c\temp\AegisNSH-20261024\loop.txt \\WIN-TUES4V46T7I,mockserver\c\temp\AegisNSH-20261024\out2\
```

PASS: first succeeds with identical bytes; second fails without overwriting;
explicit overwrite succeeds; distribution creates correct files on both hosts
without a user-authored loop. Source is retained. Repeat with a >1 MiB binary,
quoted names containing spaces, a *.txt glob and a disposable directory including
an empty directory. Directory copy requires -r and preserves its tree under the
destination. Source exported once for distribution; default destination workers
are four, configurable via set file-workers 1..32.

Device paths are NSH paths via managed agents, not SMB share access. c/c$ means
C: on that endpoint. Byte/file caps fail clearly rather than silently truncating
files. Defaults: 500 MiB/bundle, 10k entries, 2 MiB manifest bound. See full manual
for set file-max-bytes / file-max-files. Rate throttling remains backlog.

## 6. Move and failed-destination safety

Create a disposable move-me.txt beside loop.txt on the source. Run:
```text
move \\WIN-TUES4V46T7I\c\temp\AegisNSH-20261024\move-me.txt \\mockserver\c\temp\AegisNSH-20261024\moved\
```

PASS: destination hash matches and source is removed only after verified copy.
Move permits one source and one destination host. An offline/failed destination
must leave the source intact. Source hash changes before removal must stop the
removal. Treat partial failures honestly; cross-device move is not transactional.

## 7. Bulk mkdir/touch/delete and failures

```text
mkdir \\WIN-TUES4V46T7I,mockserver\c\temp\AegisNSH-20261024\cleanup
touch \\WIN-TUES4V46T7I,mockserver\c\temp\AegisNSH-20261024\cleanup\empty.txt
delete \\WIN-TUES4V46T7I,mockserver\c\temp\AegisNSH-20261024\cleanup\empty.txt
delete -r \\WIN-TUES4V46T7I,mockserver\c\temp\AegisNSH-20261024\cleanup
```

PASS: actions occur on both endpoints, per-host results are visible, and $exit
is nonzero if any task fails. An existing touch target must not be truncated.
Deleting directories without -r is refused. Missing files/permissions/offline
targets report failure. Check basic native commands still work; use `run copy`
or `run del` for native Windows flags rather than NSH's flags.

## 8. Capture and audit checks

set capture off disables automatic stdout saving; grab/pull still save requested
artifacts. pull must copy bytes, not run type on a quoted path. Empty stdout on a
failed command is not a successful file copy. run_command audit transcripts stay
under SKVC/logs/nsh. File-operation accepted/finished logs identify action IDs,
operation, source/destination and terminal status. Console identity/RBAC/job audit
is real server/console integration work; the mock does not implement login/RBAC.

References: nsh-test-plan.md (full suite), nsh-file-operations.md (syntax/contract),
nsh-console-integration.md (console entry points, identity and execution modes).
