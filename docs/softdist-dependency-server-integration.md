# SoftDist package dependencies — server integration

## Ownership and API

Dependencies are catalog/job orchestration on the SERVER. The agent continues to
receive one signed `SoftwareDeploySpec` per package through its existing worker,
with the existing file integrity, scoped detection, execution, post-verification
and cleanup rules. No dependency-specific agent/proto field is required.

The package wizard/API should let an author link exact immutable package revisions
as prerequisites. The backend resolves and authorizes a complete revision snapshot
before deployment. Import the reusable reference from
`github.com/aegisoneuem/aegis_one_agent/pkg/softdistplan`:

1. Construct `map[string]softdistplan.Package` from the catalog. Each value has
   `Requires []string` and its own typed `SoftwareDeploySpec`.
2. `Build(rootRevisionID, catalog)` checks the reachable graph, detects missing
   references/cycles, deduplicates shared dependencies, bounds it to 64 packages
   and clones an immutable execution snapshot in dependency-first order.
3. `Run(ctx, jobID, deviceID, plan, execute)` invokes a server-owned executor
   serially. Keep job/device identity stable and unique for durable correlation.
4. The executor authorizes EACH package and target, signs the command, registers
   its result waiter BEFORE send, and awaits a terminal `software_deploy` status
   from the authenticated target. It must persist state before side effects.
5. Call `plan.Clear()` after the run. Catalog secrets remain caller-owned; secure
   storage/retrieval must use the production vault, not plaintext package JSON.

The API field names are for the production team to design. The linked revision
IDs, per-package fields, signed actions and results are the behavior to preserve.
`AEGIS_*` controls exist only for mock testing, not a production API requirement.

## Package and detection rules

- This first dependency block supports install/ensure plans, not dependency
  uninstall or cascading removal. Revision identifiers are case-sensitive and
  limited to 1–128 letters/digits/dot/underscore/colon/hyphen, starting alphanumeric.
- Every dependency MUST have `product_code`, registry `uninstall_key`/
  `display_name`, or advanced `detection` rules. A successful process exit alone cannot satisfy it.
  Prefer exact registry view/key or MSI product code; ambiguous detection fails.
- Use the dependency's `version` to require the existing minimum installed version
  rule. Package revision ID and installed product version are separate concepts.
- The runner normalizes absent execution settings to explicit SYSTEM/machine,
  ensuring worker-side scope/version detection instead of legacy presence-only
  MSI skipping. Logged-in-user/specified-account dependencies keep their own scope.
- Execution credentials, ordinary parameters and secure stdin values belong to
  EACH package. They are not inherited from the parent. A machine-wide runtime
  can run as SYSTEM while the main package runs as a chosen user.
- Single-file, command, bundle and bundle-with-pre/post specifications are
  supported. The runner derives v2/bundle/workflow/detect signed task type. Agents must
  support the selected task type; unsupported actions block the plan.
- File-based packages require their pinned hash before dispatch. Content IDs must
  identify immutable payloads; the same ID cannot name conflicting hashes in one
  plan. Each content request must be authorized for the dispatched device/digest.
- The agent still performs authoritative payload/path/signature and installed
  product validation. Building a valid graph does not prove a file exists or an
  installer is suitable for that OS.

## Outcomes

| Dependency result | Plan behavior |
|---|---|
| SUCCESS after agent verification | Send next package |
| NOT_APPLICABLE because installed requirement is already satisfied | Skip its process; send next package |
| FAILED, including contradictory post-install detection | Stop; do not send remaining packages |
| Pending/initiated reboot | BLOCKED; do not send remaining packages |
| Timeout, missing/invalid terminal result, disconnect | BLOCKED or CANCELLED; do not send remaining packages |

The main package is last. A main failure fails the plan; success/installed skip
completes it. Dependencies are still ensured before asking the main package to
perform its own detection; this block does not add an initial detect-only RPC for
the main package. Already completed dependencies remain installed on later failure.

`Result` contains job/device/status/reason, aggregate reboot flag and per-package
action IDs, phases, exit codes and reasons. It contains no deployment specs,
credentials or installer stdout/stderr. Wire ActionStatus remains unchanged.
Action IDs derive from job + device + exact revision, so different devices/jobs
cannot share a waiter. Do not treat a generic CommandResult acknowledgement as a
verified software terminal success.

## Mock reference

The normal `AEGIS_DEPLOY_*` fields describe the MAIN package. Add one dependency
using `AEGIS_DEPENDENCY_ID`, `NAME`, `TYPE`, `FILE`, `ARGS`, `PRODUCT_CODE`,
`UNINSTALL_KEY`, `DISPLAY_NAME`, `VERSION`, `PARAMS`, `RUN_AS`, `DETECTION_SCOPE`,
`SESSION_ID`, `SECRETS_FILE`, `HASH`, `HASH_ALGO`, `CONTENT_ID`, `TIMEOUT`,
`SUCCESS_CODES`, `AUTO_HASH`, `REQUIRE_SIG`. Optional dependency bundle/pre/post
fields use the same suffixes as the main package with `AEGIS_DEPENDENCY_` prefix.
Only install is allowed for the dependency.

For dependency server delivery use `AEGIS_DEPENDENCY_SERVE_FILE`; main delivery
uses `AEGIS_SERVE_SOFTWARE`. Content authorization is device/action-bound and
removed after terminal result/cancellation. Plan waiters receive only terminal
results, so progress bursts cannot discard the completion.

`AEGIS_DEPLOY_PLAN_TIMEOUT` bounds waiting for the whole plan (default 3600s,
maximum 86400s). Agent package timeouts remain individual. The mock permits one
configured startup plan attempt per device/process and suppresses reconnect replay.
It prints plan/package results and saves metadata under `logs/softdist-plans`.
Its environment interface exposes one dependency; the shared runner supports
multi-level/diamond graphs without a generated test configuration file.

Run [three manual cases](softdist-today-testing.md#c-package-dependencies).

## Production completion boundary

The production backend still owns RBAC for root AND dependencies, catalog UX,
immutable revisions, job/device locks, fleet concurrency limits, encrypted durable
job state, reconciliation after disconnect/restart and audit/progress APIs.
The reference runner and mock do not supply those services automatically.

A cancelled server wait does not kill an installer already running on the agent.
Never automatically replay uncertain actions. Reboot-blocked plans need a confirmed
device reboot and re-detection before the server resumes; automatic reboot/resume
is not implemented here. Dependency rollback/removal, side-by-side version
constraints, optional dependencies and architecture/version solving are also
outside this block. Authors link the exact appropriate prerequisite revisions.
Real VC++/.NET/MSI/MST/EXE acceptance remains separate from the fixture cases.
