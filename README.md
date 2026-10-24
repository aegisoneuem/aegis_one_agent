# Aegis One Agent

The endpoint agent for Aegis One (UEM) — a single Go binary that runs as a Windows
service, enrolls over mTLS, detects missing patches, installs them (DISM, no
internet), and reports a unified per-action status back to the server.

## Docs

- **[docs/nsh-server-acceptance-2026-10-24.md](docs/nsh-server-acceptance-2026-10-24.md)**
  — today's server-team replay cases and expected results for the NSH handoff.

- **[docs/nsh-file-operations.md](docs/nsh-file-operations.md)** — native/bulk
  copy, move, create/delete, agent-to-agent relay and bounded host search.

- **[docs/nsh-console-integration.md](docs/nsh-console-integration.md)** — Live
  Dashboard/device NSH, Software Distribution workflows, operator identity,
  persistent shell integration and terminal/log separation for both teams.

- **[docs/code-structure.md](docs/code-structure.md)** — source layout: what each
  directory/file does and how the Go packages fit together. Start here as a dev.
- **[docs/patching-test-guide.md](docs/patching-test-guide.md)** — step-by-step
  patching tests (single/multiple patches, success + failure) and what the real
  server must implement. For the server team.
- **[docs/mockserver-guide.md](docs/mockserver-guide.md)** — deploy/test the
  reference mock server; full scenario + env-var reference.
- **[docs/server-team-handoff.md](docs/server-team-handoff.md)** — server-side
  contract and responsibilities.
- **[docs/device-identity-and-lifecycle.md](docs/device-identity-and-lifecycle.md)**
  — device identity, dedup, and lifecycle.
- **[installer/README.md](installer/README.md)** — build/sign the MSI.

## Build

```sh
# product agent (Windows)
GOOS=windows GOARCH=amd64 go build -o build/AegisOneClient.exe ./cmd/agent/
```

Prebuilt artifacts for the server team live in [dist/](dist/). The gRPC/protobuf
contract is a separate repo, `aegis_one_proto` (wired via a local `replace` in
`go.mod`).
