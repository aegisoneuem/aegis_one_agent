# dist — prebuilt artifacts

Ready-to-run binaries for the server team (no Go build needed). Windows x64.

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
