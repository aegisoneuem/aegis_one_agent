# dist — prebuilt artifacts

Ready-to-run binaries for the server team (no Go build needed). Windows x64.

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
Then install the agent via the MSI (point it at this server) and drive scenarios
per the mockserver guide.

> These are rebuilt on each release; they are NOT the source of truth (the code is).
> For large/clean distribution, GitHub Releases is preferable to committing binaries.
