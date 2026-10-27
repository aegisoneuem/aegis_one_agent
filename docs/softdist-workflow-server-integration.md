# SoftDist ordered pre/post commands — server contract

27 October 2026 release. Vijay reports the manual pre/post cases passed.
The current agent/mockserver/MSI handoff is in `dist`.

## Signed task

Use `Command.type = software_deploy_workflow`, with `SoftwareDeploySpec.bundle`
and `SoftwareDeploySpec.steps` both present. Older agents reject the unknown type;
new agents reject steps sent under legacy, v2 or bundle-only types. No fallback
may discard the steps. Existing `software_deploy_bundle` remains supported.

The main action, ZIP SHA256, manifest, extraction rules, execution settings,
ordinary parameters and secure values retain their bundle contract. This first
workflow version requires a bundle. MSI/EXE/PS1/BAT member entry points and the
external-command bundle path are supported.

`steps.before` and `steps.after` are ordered lists, each with at most eight
`SoftwareCommandStep` entries. Each entry has:

| Field | Meaning |
|---|---|
| `name` | Required non-secret single-line label, 1–128 bytes |
| `command` | Executable, required; no implicit shell |
| `arguments` | Windows arguments; `${VAR}` uses ordinary job/runtime parameters |
| `timeout_seconds` | Per step: zero means 300 seconds, maximum 3600 |
| `success_exit_codes` | Additional acceptable codes for that step only, maximum 32 |
| `use_secure_parameters` | Explicit opt-in to the job's stdin JSON; defaults false |

At least one step is required. Empty/nil entries and invalid limits fail before
queueing. Credentials/secrets must not be copied into any public step name,
executable or arguments. Use `cmd.exe /D /C ...` explicitly for shell syntax;
explicit CMD shell text preserves author quotes, including redirection to paths
containing spaces. Other executables receive normal Windows argv encoding.

## Execution and results

1. Existing scope-aware detection may skip an already-satisfied job. No steps run
   for such a skip.
2. Authenticate the selected account once and verify/extract the ZIP before any
   step. All commands run serially in the same machine installation lane.
3. Run before commands in order, stopping on the first failure.
4. Run the main action only when all before commands succeed.
5. Run after commands only after main success, stopping on the first failure.
6. Apply existing product post-verification, finalize bundle cleanup and emit the
   terminal result. Failures also finalize the package according to its policy.

All commands share the selected token/profile, extracted package working directory
and ordinary parameter environment. They are separate processes: `cd`, `set` or
PowerShell variables in a step do not persist into subsequent processes. Ordinary
parameters are job inputs; `${package_dir}` is still reserved.

Secure stdin is sent to the main action as before, and to a pre/post step only
when `use_secure_parameters=true`. Private workflows suppress child diagnostic
output; values are never put in argv/environment. A payload can still write its
own files, so author scripts must handle credentials responsibly.

Running `ActionStatus` messages identify stage, one-based step index and label.
`action_type` remains `software_deploy`; use the normal action ID for correlation.
Terminal failure reasons identify the failed step and its exit code. Main failure
skips after commands. There is no continue-on-error setting in this version.
Successful terminal results retain the main exit code; `reboot_required` is ORed
across completed commands. Code 3010 allows continuation and preserves the pending
reboot flag. Code 1641 means a reboot was initiated: remaining required commands
are skipped and the job fails honestly if work is incomplete. There is no reboot
resume journal in this block.

Per-step timeouts are independent of the main action timeout, not an overall job
deadline. Timeout/cancellation can leave installer descendants running, as in the
existing engine; cleanup errors remain failures. After commands are success-only
post-actions, not finally blocks or automatic rollback. Retained custom folders
follow the existing explicit cleanup policy.

## Mock reference and manual tests

The mock exposes one command per stage through `AEGIS_DEPLOY_PRE_*` and
`AEGIS_DEPLOY_POST_*`: `COMMAND`, `ARGS`, `NAME`, `TIMEOUT_SECONDS`, `SUCCESS_CODES`,
`SECURE_INPUT` (0/1 or false/true). Production servers construct the typed lists
directly and can use up to eight commands in either stage. No test helper file is
required. See [three manual cases](softdist-today-testing.md#b-prepost-commands).

This does not implement dependency-package resolution/installation, rollback,
package promotion/version control, crash resume or the production console UI.
