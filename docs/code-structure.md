# Code Structure — Aegis One Agent

**Audience:** developers new to the agent codebase (what lives where, and how Go
wires it together). For the product/runtime behaviour see the other docs in this
folder; this one is about the **source layout**.

---

## 1. How this Go project is organized (read this first)

A few Go rules explain everything below:

- **One module.** `go.mod` declares the module path
  `github.com/aegisoneuem/aegis_one_agent`. Every internal import starts with that
  path.
- **A directory = a package.** All `.go` files in the same directory share one
  `package` and can call each other's functions/types **with no import**. Example:
  every file in `cmd/agent/` is `package main`, so `main.go` calls
  `startHealthServer()` (in `health.go`) and `ensureEnrolled()` (in `enroll.go`)
  directly.
- **To use another directory's code you import its full path**, then call it
  qualified: `import ".../internal/patch"` → `patch.ScanOffline(...)`.
- **`internal/` is private.** Go only lets code inside this module import packages
  under `internal/`. It cannot leak to other projects.
- **`cmd/<name>/` = one executable each.** A folder under `cmd/` with a `func
  main()` builds to one `.exe`. This repo has several (the product agent plus the
  mock server and dev tools). **The product agent is `cmd/agent/`.**
- **Platform split by filename.** A `_windows.go` / `_other.go` pair (with
  `//go:build windows` / `//go:build !windows`) lets the SAME function have a real
  Windows implementation and a no-op/stub elsewhere. The Go toolchain compiles only
  the file matching `GOOS`. This is how the agent cross-compiles while DISM/WUA/
  registry/DPAPI code stays Windows-only.
- **The proto types are a separate module.** `github.com/aegisoneuem/aegis_one_proto`
  (imported as `pb`) holds the gRPC/protobuf contract. `go.mod` has
  `replace github.com/aegisoneuem/aegis_one_proto => ../aegis_one_proto`, so a local
  sibling checkout is used for builds (CI checks both repos out side by side).

Entry point: **`cmd/agent/main.go` → `func main()`** → dispatches subcommands
(`install`/`uninstall`/`start`/`stop`/`config`/…), else runs as the Windows service
(when launched by the SCM) or as a console process.

---

## 2. Top-level directories

| Dir | Role |
|---|---|
| `cmd/` | Executables — one subfolder per binary (`func main()` each). |
| `internal/` | Private library packages (the agent's real logic). Not importable outside this module. |
| `pkg/` | Reserved for future exportable packages (currently empty). |
| `docs/` | Documentation (this file, the server-team guides, architecture notes). |
| `installer/` | WiX v5 MSI source (`Package.wxs`), branding assets, build/sign README. |
| `build/` | Local build output (git-ignored) — compiled `.exe`s for dev/VM testing. |
| `dist/` | Prebuilt artifacts committed for the server team (agent exe, mockserver, certgen, MSI). |

The proto repo (`../aegis_one_proto`) is a **separate sibling repository**, not a
folder here.

---

## 3. `cmd/` — the executables

| Binary | What it is |
|---|---|
| **`cmd/agent/`** | **The product agent.** Windows service / console; enrolls, connects over mTLS, runs patch scans + installs, reports status. |
| `cmd/mockserver/` | Reference server (executable spec). Drives the agent and prints what it reports. See the server-team guides. |
| `cmd/certgen/` | Generates the dev mTLS PKI (`certs/`: CA, server, client). |
| `cmd/keygen/` | Generates an Ed25519 command-signing keypair. |
| `cmd/localscan`, `cmd/scantest`, `cmd/scandiag`, `cmd/resettest` | Dev tools for the WUA/offline scan path (diagnose, reset, compare). |
| `cmd/installtest`, `cmd/deploytest` | Dev tools for the DISM install path (install a local `.msu`, verify hashes). |
| `cmd/cachetest` | Dev tool for the encrypted patch cache (`put`/`get`/`list`/`enforce`). |
| `cmd/ipctest` | Dev tool for the local IPC bus. |

> The `*test`/`diag` tools are developer utilities, not shipped to customers.

---

## 4. `cmd/agent/` — the agent files (all `package main`)

| File | Role |
|---|---|
| `main.go` | Entry point. Subcommand dispatch; the connect loop (`runAgent`): one sender goroutine owns the stream, a receiver dispatches server commands (`install_patch`/`stage_patch`/`reboot`/…); reconnect + backoff; wires everything together. |
| `service_windows.go` / `service_other.go` | Run as a Windows service via the SCM (graceful stop) vs run as a console process. |
| `enroll.go` | First-run enrollment: generate ECDSA keypair + CSR, call `Enroll`, write the per-device cert, erase the single-use token. |
| `renew.go` | Renew the client cert before expiry over the existing mTLS (no token). |
| `deviceid.go` | Stable device identity: persisted UUID in `identity.json` + clone/reimage detection. |
| `fingerprint_windows.go` / `fingerprint_other.go` | Hardware fingerprint (SMBIOS System UUID / MachineGuid) used by device identity. |
| `detection.go` | Buffer drain: sends buffered reports (`DeviceStateReport`, `PatchScanResult`, `ActionStatus`) over the stream; the drain-signal plumbing. |
| `detection_windows.go` / `detection_other.go` | The two detection jobs — the frequent **patch state report** (installed KBs + build, on change) and the occasional **patch compliance scan** (offline cab → missing/superseded). Builds the `PatchScanResult`. |
| `install.go` | The serial patch worker (`patchWorker`/`runInstall`) — one install at a time; the cache janitor. |
| `install_windows.go` / `install_other.go` | `handleInstallPatch`: verify hash → DISM install → terminal `ActionStatus`. Stub on non-Windows. |
| `stage.go` | `stage_patch` pre-caching: fetch via peer-cache, store encrypted as "staged"; reports `ActionStatus` + `CacheFullReport`. |
| `cab.go` | Cab-manifest handling + `fetchContent` (the peer-cache streaming download: temp → SHA-256 verify → atomic rename). |
| `baseline.go` | Caches the server's `PatchBaseline`; offline self-diff of installed KBs vs baseline. |
| `reboot_windows.go` / `reboot_other.go` | Server-driven reboot / cancel via `shutdown.exe`. |
| `actionstatus.go` | Helpers for building the unified `ActionStatus` (phase/type constants, `simpleActionStatus`, `rebootPhase`). |
| `health.go` | Localhost-only HTTP endpoint: `GET /health`, `GET /status` (JSON liveness). |
| `cleanup.go` | Startup sweep of leftover plaintext temp files from an interrupted operation. |
| `safe.go` | `safeRun`/`recoverLog` — per-goroutine panic isolation so one failure doesn't take the agent down. |

---

## 5. `internal/` — the library packages

| Package | Role |
|---|---|
| `internal/config` | Load configuration with precedence **env > file > registry > default** (`config.go`); Windows registry tier (`registry_windows.go`) incl. token delete; no-op elsewhere. |
| `internal/patch` | All Windows patch logic: WUA COM scan (`scan_windows.go`), offline cab scan (`scan_offline_windows.go`), DISM install + failure classification (`deploy_windows.go`, `install_windows.go`), local state from registry/CBS (`local_windows.go`), superseded tagging (`supersede_windows.go`), COM/decimal helpers (`com_windows.go`). `local.go` holds the cross-platform struct + pure helpers. |
| `internal/patchcache` | Encrypted, size-bounded, state-aware (staged/cached/installed) patch cache with LRU + TTL eviction. |
| `internal/secure` | Security primitives: AES-256-GCM (`crypto.go`), streaming file encryption (`filecrypt.go`), DPAPI machine-key provider (`dpapi_windows.go`/`key_*.go`), mTLS credentials (`tls.go`), secure delete (`wipe.go`). |
| `internal/buffer` | Offline store-and-forward buffer (interface in `buffer.go`, encrypted file-backed impl in `file.go`) so reports survive while the server is unreachable. |
| `internal/logging` | `log/slog` setup with custom levels + daily log-file rotation (`daily.go`). |
| `internal/ipcbus` | Local inter-process bus (named pipe on Windows, unix socket elsewhere) for future local integrations. |

---

## 6. The proto contract (separate module)

`../aegis_one_proto/grpc/agent.proto` defines the wire types (`AgentMessage`,
`ServerMessage`, `ActionStatus`, `PatchScanResult`, `InstallPatchSpec`, `Enroll*`,
etc.) and the `AgentControlPlane` service. It is generated into Go and imported here
as `pb`. Because the agent and proto are **two repos**, a change to a message means:
edit `agent.proto` → regenerate both Go trees → update the agent → push the proto
repo first (CI uses it via the side-by-side checkout / local `replace`).

---

## 7. Adding new code — the usual patterns

- **New logic used only by the agent:** add a file to `cmd/agent/` (same `package
  main`, no import needed) or, if reusable/testable, a package under `internal/` and
  import it.
- **New Windows-only behaviour:** put it in `*_windows.go` with `//go:build windows`
  and add a matching `*_other.go` stub so non-Windows still builds.
- **New wire field/message:** edit the proto, regenerate, then consume it here.
- **Reference another package:** `import "github.com/aegisoneuem/aegis_one_agent/internal/<pkg>"`
  then call `<pkg>.Func(...)`.
