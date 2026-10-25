# Shared NSH executor: terminals, Script Jobs and Software Distribution

Server-team test handoff after the accepted 24 October NSH suite. Source and
`dist/mockserver.exe` include the shared job runner; automated tests pass. New
Script Job VM acceptance and production console binding remain pending.

For this update, replace/restart only mockserver.exe. The existing accepted
AegisOneClient.exe/MSI and agent/protobuf contracts are unchanged. Use these two
guides for testing:

1. [NSH acceptance replay](nsh-server-acceptance-2026-10-24.md): regression of
   navigation, capture, file operations and bounded host search.
2. This guide: new non-interactive Script Job and Software Distribution NSH
   execution, isolation, parameters and failure cases.

Production wiring is described in [console integration notes](nsh-console-integration.md)
and the integration contract below. Repository access supplies the reference
implementation; the real server still needs its backend, authorization, job
storage/scheduling and console entry points.

## What is implemented

The existing interpreter now lives in public Go package
`github.com/aegisoneuem/aegis_one_agent/pkg/nsh`. It contains variables/aliases,
control flow, source/exec, HEREDOC encoding, error policy and cancellation-aware
waits. Mockserver delegates to this package; there is no second copy of the
language. pkg/nsh imports only the standard library, not the agent or gRPC.

The shared Runner executes inline NSH text or a .nsh file as a non-interactive
job. Both `script_job` and `software_distribution` kinds use exactly this path.
The kind labels the caller; a script is not required to install a package.
Commands include native/file operations, loops, checks and native script calls.
There is no new package-deploy verb and no agent-side .nsh interpreter/type.

Each job has explicit device IDs, parameters, isolated variables/aliases/CWD,
stdout/stderr capture and structured terminal results. Job wildcards are limited
to the authorized target scope, including explicit device paths and cd selection.
The real server must supply actual tenant/operator authorization; mockserver only
demonstrates a fixed target boundary and is not a login/RBAC implementation.

## Mock test: create a server-local .nsh script

Save C:\tests\job-check.nsh on the mockserver machine:
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

Start the new **dist/mockserver.exe** (or a locally built build/mockserver.exe)
with the explicit `nsh` argument, then choose target(s):
```text
cd \\WIN-TUES4V46T7I
job run C:\tests\job-check.nsh --param TAG=script-test
job run C:\tests\job-check.nsh --kind software_distribution --param TAG=softdist-test
```

Then select a multi-device list (replace names with actual agents) and repeat:
```text
cd \\WIN-TUES4V46T7I,mockserver
job run C:\tests\job-check.nsh --param TAG=fleet-test --timeout 120
```

PASS: script executes without input prompts, each selected target has a terminal
result and the footer reports JOB SUCCESS/FAILED/CANCELLED. Results are written
to logs/nsh-jobs/<job-id>.json (override AEGIS_NSH_JOB_RESULT for tests). No
unselected agent executes commands. The interactive shell, target list, capture
setting, aliases and parameters are unchanged by the job. Explicit parameters
are injected into NSH substitution and AEGIS_PARAM_<NAME> on endpoint children.
Capture is off by default for job run; add --capture or set capture on in script.

The CLI default job deadline is 1800 seconds; --timeout overrides it. Endpoint
per-command timeout is separately set by `set timeout <seconds>` inside the job.
Do not mix up job deadline and individual command timeout.

Negative cases: wrong cmdlet must fail; on-error stop stops subsequent commands;
on-error continue runs remaining work but the aggregate job still fails. A bad
cd must not leave the job executing on a previous target. Attempt to select/copy
to an unselected device must fail. Missing end/HEREDOC delimiter, source recursion
and invalid script syntax must fail rather than produce a successful empty job.

## Explicit non-interactive script mode

Run `mockserver.exe script C:\tests\job-check.nsh` to use the same shared Runner.
Alternatively set AEGIS_NSH_SCRIPT and run `mockserver.exe script` explicitly.
Plain mockserver.exe always stays in normal mode, even with old NSH variables.
Optional settings:

- AEGIS_NSH_TARGETS: comma-separated known device IDs/hostnames/computer IDs.
- AEGIS_NSH_JOB_PARAMS: comma-separated NAME=value parameters (mock convenience).
- AEGIS_NSH_JOB_KIND: script_job or software_distribution.
- AEGIS_NSH_JOB_TIMEOUT_SECONDS: total deadline, default 1800.
- AEGIS_NSH_JOB_CAPTURE: defaults true in this legacy mode for compatibility.
- AEGIS_NSH_JOB_RESULT: optional JSON result path override.

If targets are omitted, the mock snapshots currently connected agents. It does
not wait for devices/fleet startup; an empty set fails with an explicit target
error. For deterministic VM tests use job run after connections are ready. The
mock server stays running after a script finishes; it does not exit as a job CLI.

## Server/console integration contract

Use the same backend and engine in all four entry points:

| Entry point | Execution |
|---|---|
| Endpoint right-click, Open NSH | Fresh interactive Engine with one device preselected |
| Live Configuration / Live Dashboard NSH tab | Fresh interactive Engine with authorized target selection/search |
| Script Job, interpreter NSH | Runner.Run with Kind=script_job |
| Software Distribution, NSH script option or pre/post step | Runner.Run with Kind=software_distribution |

Interactive callers use Engine.ExecLine and State for continuation prompts.
Non-interactive callers supply Job.Script or Job.ScriptFile (exactly one), target
device IDs and parameters to Runner.Run. Runner.NewBackend must create fresh
session state per invocation. Backend.Execute receives a Command and reports
terminal ActionResult values through its callback. Backend handles authorized
device lookups, target selection, existing signed agent actions and session I/O.
SelectedTargets returns a copy. WildcardTargets returns the appropriate snapshot
(online authorized inventory for a general terminal; the fixed scope for a job).

Context controls job deadlines/cancellation. Supply LoadFile as an authorized,
versioned script-library resolver in production; do not allow arbitrary browser
paths into the server filesystem. LookupEnv is injected and can expose a scoped
environment instead of the real process environment. Session-log file access is
disabled unless the host explicitly supplies OpenSessionLog. GUI/editor/script
library, scheduler, durable job storage and event streaming remain console/server
work; there is no new HTTP/WebSocket API in this agent repository.

Result includes identity/kind, status/exit code, per-target actions, stdout/stderr,
reason/artifact path, counts, timestamps, variables and a bounded output log.
Default text output and stored action text are bounded to 1 MiB separately;
stored actions default to 10k, source depth to 16 and script/expanded text to
4 MiB. Individual variable values default to a 1 MiB bound.
Output/result truncation is explicit. Host quotas and permissions still apply.

Cancellation stops further dispatch, waits and waiting for responses. Already
sent agent operations are not remotely rolled back or killed by a new job cancel
RPC; their existing command timeout still applies. Production must expose this
distinction and add explicit operation cancellation if required. This is also
separate from a persistent cmd.exe ShellSession, which retains OS process state.

## Scope of this coding block

Existing agent/protobuf contracts are unchanged. New job execution is server-side
code in pkg/nsh plus cmd/mockserver/nsh_engine.go/nsh_jobs.go. Existing interactive,
native file transfer and real-PowerShell tests run against the extracted engine.
Additional tests cover same-language job kinds, independent/concurrent runs,
source/alias/native script state, cancellation, stop/continue, result persistence,
scope rejection and session-output isolation. No console release is claimed.
