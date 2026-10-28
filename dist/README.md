# dist - current prebuilt artifacts

Windows x64 server-team handoff, 28 October 2026. Replace BOTH agent and
mockserver, or install the matching agent MSI.

**Testing: use only [the single manual guide](../docs/softdist-today-testing.md).**
It contains 18 manual cases in six blocks: multi-file packages, pre/post commands,
package dependencies, advanced file/version/registry detection with ALL/ANY,
five real 7-Zip MSI lifecycle cases and Firefox EXE/config/active-policy acceptance.
Every case states purpose, exact CMD commands and expected results. No test
launcher scripts or probe EXE are needed. Old split test guides redirect there.

Vijay reports all four engine blocks and both real-vendor blocks passed. The guide
records the Firefox recovery and the first MSI timeout without claiming a proven
root cause. Go tests/vet and protocol tests pass; the
rebuilt MSI's extracted agent matches the distributed EXE. Artifact hashes and the
required shared protocol revision are in `softdist-handoff.json`.

27 October diagnostic rebuild: agent EXE/MSI now log SoftDist queue handoff,
worker pickup, machine-install gate waits and execution stages. A job waiting
behind another installer reports QUEUED with an explicit wait reason. Mockserver
is unchanged. This improves diagnostics; the real 7-Zip MSI timeout remains under
investigation. Restarting mockserver can submit the same `deploy-001` again while
an earlier attempt is still running on the agent.

Included related fix: explicit CMD quotes/redirection preserve paths containing
spaces. Normal server mode is `mockserver.exe server`; NSH is explicit.
Existing Run-As/secure parameters/uninstall and NSH capabilities remain included.
Remaining: parameterized config-only update, real runtime dependencies, MST and
failure/reboot acceptance, plus production backend/UI integration. SoftDist
remains the priority before the other module tracks.

## Historical references

The notes below describe earlier increments. For current testing instructions,
EXE replacement and reported acceptance, use the single guide above.

**Previous NSH replay guide (24 October):**
[NSH acceptance — 24 October](../docs/nsh-server-acceptance-2026-10-24.md).
Vijay reports the earlier NSH suite passed. The latest mockserver also includes
the shared NSH Script Job/Software Distribution runner. Test with both the replay
guide above and [NSH Script Jobs](../docs/nsh-script-jobs.md). Replace/restart only
mockserver.exe for this runner update; agent/MSI are unchanged. Automated tests
pass; new job VM acceptance is pending. Production server/console integration
remains server-team work, detailed in the job guide and console integration notes.

The older dated notes below describe earlier increments; their pending VM-test
statements are superseded by Vijay's accepted NSH suite reported above.

2026-10-24 file-operations build: replace BOTH agent and mockserver (or install
the matching MSI). Native/bulk copy, move, mkdir/touch/delete and bounded host
search/pagination are now implemented. See
[file operations and search](../docs/nsh-file-operations.md) for syntax and test
cases. Defaults: 4 file workers, 500 MiB/bundle, 10k entries; hosts prints 20 rows,
maximum 100. No automatic host-list dump on terminal open. Automated tests pass;
new multi-VM file-operation acceptance remains pending.

2026-10-24 mockserver update: NSH background fleet events now go to
`logs/mockserver.log` (override `AEGIS_MOCK_LOG_FILE`); terminal output stays
focused on operator commands. PowerShell exec/HEREDOC blocks fail on unhandled
errors. G2 explicitly imports REGION from the server environment. See
[console integration notes](../docs/nsh-console-integration.md) for Live
Dashboard/device NSH, Software Distribution workflows and persistent shell work.
Automated G2 and logging checks passed; VM retest of this update is pending.

Also includes known-offline target selection (no fleet-list prerequisite),
ONLINE/OFFLINE inventory status, endpoint cd/pwd with per-device working_dir,
ambiguous-hostname rejection, and caller target restoration after foreach.
See nsh-test-plan.md A9/A10 for navigation and cold-start tests. G2 was verified
by Vijay; these newer navigation changes still need VM acceptance.

2026-10-23 mockserver update: fixes F1/G1 PowerShell `exec` and HEREDOC dispatch
(`-NoLogo` was incorrectly treated as script text). HEREDOC collection shows
the closing delimiter in its continuation prompt and preserves comments/blank
lines. Replace/restart mockserver for this update; the agent/MSI are unchanged.

2026-10-22 NSH test build: refresh BOTH the agent and mockserver (the MSI bundles
the matching agent). Includes zero-byte stdout upload fix, quoted Windows command
paths, native file `pull`, and readable NSH output folders with `set output-dir`.
Run [../docs/nsh-test-plan.md](../docs/nsh-test-plan.md), especially B1/B2/B4/B5.
Uploads are stored on the mockserver machine relative to its launch directory:
`uploads/<computer_id>/<output-dir>/<hostname>/<filename>` (default output-dir
`nsh`). Existing folders are retained. Automated checks passed; VM acceptance
retest is pending.

| File | What it is |
|---|---|
| `mockserver.exe` | Reference server. Run it to exercise the agent. See [../docs/mockserver-guide.md](../docs/mockserver-guide.md). |
| `certgen.exe` | Generates the dev mTLS PKI: `certgen certs` → `certs/` (ca, server, client). |
| `AegisOneClient.exe` | The agent binary (also bundled inside the MSI). Useful for console testing. |
| `AegisOneClient.msi` | The AegisOne Client installer (interactive + silent). See [../installer/README.md](../installer/README.md). |

Quick start (server team):
```
certgen.exe certs
set AEGIS_ENROLL_TOKEN=tenant-abc-123
mockserver.exe
```
Then install the agent via the MSI (point it at this server) and drive scenarios.

**👉 Start here:** [../docs/patching-test-guide.md](../docs/patching-test-guide.md) —
step-by-step patching tests (single patch, multiple patches, success + failure
scenarios) **and what the real server must implement**. Deeper setup / caching /
cab / renewal: [../docs/mockserver-guide.md](../docs/mockserver-guide.md).

> These are rebuilt on each release; they are NOT the source of truth (the code is).
> For large/clean distribution, GitHub Releases is preferable to committing binaries.
