# NSH: Live Dashboard, device actions and Software Distribution

Server/console handoff, 24 October 2026. Current agent capabilities and proposed
production work are distinguished below. Bandwidth throttling stays in backlog.

## Console entry points and identity

Add an **NSH** tab inside **Live Dashboard** and a device context-menu action
**Open NSH**. Both use the same terminal engine. Right-click opens a session
preselected by immutable device_id; display hostname and computer_id to the
operator. The dashboard tab can start without a target and offer an authorized
device picker. Do not run commands or enable automatic capture just by opening it.

Use the existing console login/session for operator identity and tenant scope;
no second NSH password prompt. Check permission to open a session and permission
for every action/target, including foreach/fanout. Never trust a browser-supplied
user/tenant ID as authentication. Keep signing keys and agent certificates on
the server. Two operators need isolated target lists, variables, aliases and I/O.

**Console identity does not imply Windows impersonation.** Current Windows
execution is LocalSystem, not the logged-in console user's endpoint/domain
account. Console credentials authorize operations and audit them; do not forward
passwords to the agent. Run-as/user-context execution is separate future work.

## NSH versus persistent endpoint shell

| Mode | Current contract | State |
|---|---|---|
| NSH commands/workflows | Server interpreter sends signed run_command / collect_file | NSH variables, aliases, target and configured working_dir persist; each native command creates a new OS process |
| Persistent endpoint shell | Signed open_shell, then ShellSession bidirectional RPC | One live cmd.exe retains directory, environment and process state until closed |

Agent open_shell/ShellSession handlers already exist. Remaining production work:
bridge browser input/output to the authenticated device stream, bind each session
to an authorized operator/tenant/device, implement close/disconnect/logout policy,
enforce idle/max-duration limits and expose audit/transcripts. Create an opaque,
one-use session_id with issued_at/TTL; match the connecting agent to that pending
session. Never connect an arbitrary operator to someone else's stream.

The mock's shell verb is informational. Its legacy ShellSession stdin bridge is
reference code, not a finished console UI; it must not share a production NSH
session's stdin. Agent transcripts are local under SKVC/logs/shell; server-side
collection/storage must be wired and verified. V1 uses plain pipes. ConPTY for
arrow keys/colors/full terminal behavior is later work.

## Clean terminal and fleet responsiveness

Opening a browser NSH tab must attach to the already-running server, not restart
it. A mockserver.exe restart drops its connections and agents reconnect on their
normal backoff; an initially empty online list is a cold-start observation,
not a requirement to wait for every device before any command can run.

The mock now selects known devices from its persisted inventory even while they
are offline. hosts includes ONLINE/OFFLINE status; cd \\<hostname/device_id/
computer_id> does not require listing the fleet first. Unknown/ambiguous names
are refused; exact device_id/computer_id resolve ambiguity. Offline commands fail
immediately and can be retried when that device reconnects. Production should
resolve right-click targets directly by device_id and use indexed/paged inventory
search, rather than loading 50k host rows into every terminal.

Endpoint navigation: cd C:\temp, cd .., quoted paths, pwd and argumentless cd
work on one selected Windows target using cmd/powershell/pwsh. The server sends
and validates the directory change on the endpoint and persists working_dir per
device/session, retaining the old CWD on failure. cd \\ clears targets. foreach
restores the caller's selected targets afterward. This is not a persistent
endpoint process; arbitrary native environment state still does not persist.

Only command output/errors, explicit saves and relevant session status belong in
an operator terminal. Heartbeats, fleet connects, patch reports and background
transfer messages belong in server logs. Put online status in a separate UI
indicator. Never subscribe each operator terminal to the entire fleet event feed.

The mock now routes background events to logs/mockserver.log when AEGIS_NSH or
AEGIS_NSH_SCRIPT is set; AEGIS_MOCK_LOG_FILE overrides the log path. NSH output
stays in the current window. A second CMD window is unnecessary. Without NSH,
normal mock console logging remains. This is reference logging, not a production
retention/rotation implementation.

Commands use an established Connect stream immediately. Do not wait for a new
heartbeat or for all devices to connect before serving one online target. Give
offline targets an immediate clear status; queued offline execution must be an
explicit job option. An operator session does not create connections to the fleet.

The real server should use its device_id connection registry, serialize outbound
writes per device stream, correlate by action_id and isolate operator result
subscriptions. Use bounded fanout, deadlines, cancellation, quotas and output
backpressure. One slow endpoint must not freeze other operators. Snapshot the
authorized target set for a job and report progress per host. The mock's foreach
is serial and fanout is unbounded; it is not proof of 10k/50k-device capacity.
Production scheduling and load tests are server work, independent of heartbeats.

NSH now implements bounded hosts/search output (20 rows by default, maximum 100),
ONLINE/OFFLINE filtering and pagination; do not call a full inventory dump when
opening a device terminal. Right-click binds the known device_id directly. A
search picker may query authorized inventory by name/computer_id with page
cursors; it does not require the operator to remember hostnames. See
[NSH file operations and search](nsh-file-operations.md) for syntax, the new
file_operation/v1 relay contract, bounds and source/destination permissions.
File-operation UI must use the operator's file read/write/delete rights rather
than treating permission to open an NSH tab as unrestricted filesystem access.

AEGIS_NSH_SCRIPT runs at mock startup. foreach * snapshots currently connected
agents; it does not wait for the fleet. For deterministic manual G2 testing,
start interactive NSH, check hosts, then source the local .nsh file. Production
jobs must explicitly resolve target availability instead of copying this race.

## NSH workflows in Software Distribution

Offer **NSH workflow** as a Software Distribution job option: edit NSH syntax or
select a versioned saved .nsh workflow, supply parameters, select targets, preview
and run/schedule it. Expose the same saved workflows in the Live Dashboard NSH
tab. Manual commands and background jobs should use the same interpreter, with
fresh per-run state and scoped output rather than global console state.

An .nsh file is a **server-side orchestration workflow**. The server interprets
let/source/foreach/if/exec/script and sends signed native tasks to agents. Do not
send .nsh to cmd.exe/PowerShell as an installer or add an endpoint NSH/SSH listener.
In the mock, source and exec read server-local files. In production resolve
approved tenant/operator-scoped script-library IDs and versions; do not expose
unrestricted server filesystem access through browser-authored paths.

Current native deploy types are MSI/EXE/PS1/BAT/CMD/Python/VBS/command. There is
currently **no .nsh installer type or NSH package-deploy verb**. The workflow UI
and direct package-action bridge described here remain to implement.

Recommended integration: the job engine supports package-action and native-script
steps. A package step resolves the package/version and creates the existing
SoftwareDeploySpec, sends signed software_deploy, waits for ActionStatus, then
exposes the result to subsequent NSH logic. A future NSH package verb should call
that same service; agree its syntax before adding it. Use run_command for checks
and collect_file for explicit artifacts. Do not substitute a raw installer
command for software_deploy: preserve hash verification, encrypted cache delivery,
idempotency/detection, version-aware upgrade, post-action verification, installer
logs and reboot_required reporting.

Check package/device permissions even inside scripts. Record operator, workflow
version/hash, redacted parameters, target snapshot, parent job ID and child action
IDs/results. Define stop/continue, cancellation, offline scheduling and reboot
policies. Do not blindly retry partially completed deployments. Transfer-rate
limits are not implemented by this handoff.

## Parameters, failure and artifacts

Import server process environment explicitly: let REGION = ${env:REGION}.
CMD set REGION=apac alone does not define an NSH variable. Console jobs should
supply validated named parameters directly. NSH ${env:NAME} refers to the server;
native PowerShell $env:NAME refers to the endpoint's process environment.

NSH exec/HEREDOC PowerShell scripts now default to terminating unhandled errors
and a nonzero catch exit. Scripts may handle expected exceptions explicitly.
on-error continue controls workflow continuation; it does not label failed
endpoint tasks successful. Do not infer success from stdout or treat every
stderr byte as failure. Show stdout/stderr separately and use terminal status.

Interactive capture defaults off. set capture on saves stdout; grab/pull request
artifacts explicitly. An empty stdout artifact can be legitimate on a failed
command. Upload integrity only verifies transferred bytes, not command success.
Mock layout: uploads/<computer_id>/<output-dir>/<hostname>/<filename>, default
output-dir=nsh. Repeated explicit filenames get suffixes. Production artifact
access must enforce tenant/device/operator authorization and retain immutable
job/action metadata alongside human-readable names.

## Acceptance checklist for server and console teams

- Right-click opens the selected device, including when hostnames are duplicated.
- Operator sessions, targets, variables, artifacts and permissions stay isolated.
- Background fleet events never appear in the terminal.
- Online commands do not wait for heartbeats or unrelated fleet connections.
- Offline/unauthorized/revoked devices and logout have explicit behavior.
- Saved workflow/source/native script actions use one interpreter and scoped files.
- Package actions retain deployment verification and reboot semantics.
- G2 prints region=apac per host; an intentionally wrong cmdlet reports FAILED.
- Persistent shell state, close, limits, disconnect and transcript audit are tested.
- Bounded large jobs are separately load-tested for cancellation/backpressure.

References: nsh-test-plan.md, testing-use-cases.md, cmd/mockserver/nsh.go,
cmd/mockserver/nsh_script.go, cmd/agent/runcommand.go, collect.go, shell*.go and
the shared ../aegis_one_proto/grpc/agent.proto contracts.
