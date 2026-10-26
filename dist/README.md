# dist — prebuilt artifacts

Ready-to-run binaries for the server team (no Go build needed). Windows x64.

**26 October - Run-As and parameter handoff:** replace BOTH agent and mockserver
(or install the matching agent MSI). Use ONLY
[the final manual new-feature guide](../docs/softdist-new-features-testing.md)
for the new cases: logged-in user, specified account, ordinary parameters, secure
stdin field delivery and per-user detection/repeat/removal. No launcher scripts
or probe EXE are required. Updated shared protocol source is in aegis_one_proto.
Automated Go tests/vet pass. Actual VM and real-vendor acceptance is pending.
The previous uninstall suite was reported passed by Vijay and the server team.

Includes strict uninstall selection/verification, explicit custom payload
precedence, quoted BAT/CMD paths, and mock controls for timeout/custom success
codes/missing-hash tests. Normal startup is `mockserver.exe server` (or plain
mockserver.exe); inherited NSH flags do not open a prompt. Use
`mockserver.exe nsh` or `mockserver.exe script <file.nsh>` explicitly for NSH.

**Current priority: finish SoftDist before moving to other tracks.** See
[the SoftDist completion checklist](../docs/softdist-roadmap.md). Per-user/Run-As
and complete production workflows are not claimed complete by this handoff.

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
