# Mockserver — Deploy & Test Guide (Server Team)

**Audience:** Aegis One server team.
**Purpose:** run the reference `mockserver` (`cmd/mockserver`), drive every agent
scenario (enroll, single/multi patch, caching/staging, status, cab, reboot,
license), and use what it does as the **contract** for the real server.

The mockserver is the **executable spec**: whatever it sends/receives on the wire
is exactly what the real server must implement. Related contracts:
[server-team-handoff.md](server-team-handoff.md) ·
[device-identity-and-lifecycle.md](device-identity-and-lifecycle.md).

---

## 1. Build

```
cd aegis_one_agent
go build -o build/mockserver.exe ./cmd/mockserver     # GOOS=windows for a Windows host
go build -o build/certgen.exe   ./cmd/certgen
```
(Or use the prebuilt exes.) The proto is a sibling repo `aegis_one_proto`; CI checks
both out side-by-side.

## 2. Certificates (the TLS setup)

Everything is **mTLS, TLS 1.3**. Generate a dev PKI with `certgen` into a `certs/`
folder next to the mockserver:
```
certgen.exe certs
```
This creates (ECDSA P-256): `ca.crt` + `ca.key`, `server.crt` + `server.key`,
`client.crt` + `client.key`. SANs on the server cert: **localhost, aegis-server,
127.0.0.1**.

The mockserver reads (defaults shown; override via env):
| Env | Default | Purpose |
|---|---|---|
| `AEGIS_TLS_CA` | `certs/ca.crt` | CA bundle (verify agent client certs) |
| `AEGIS_TLS_CERT` | `certs/server.crt` | server cert presented to agents |
| `AEGIS_TLS_KEY` | `certs/server.key` | server private key |
| `AEGIS_CA_KEY` | `certs/ca.key` | **CA private key — needed to SIGN enrollments** |

> **TLS gotchas (what bit us):**
> - The mockserver needs **`ca.key`** (not just `ca.crt`) because `Enroll` signs the
>   agent's CSR with the CA. Make sure `certs/ca.key` exists.
> - Agent side: set `ServerName` to a cert SAN (`localhost` or `aegis-server`) and
>   **dial `127.0.0.1:50051`** — dialing `localhost` can hang gRPC in CONNECTING on
>   some hosts. It is NOT a cert problem; don't chase certs for a hung localhost.
> - Single port **:50051** serves BOTH enrollment (no client cert) and the mTLS
>   control plane (`VerifyClientCertIfGiven`; non-Enroll RPCs require a verified
>   client cert at the app layer).

## 3. Run

```
set AEGIS_ENROLL_TOKEN=tenant-abc-123
mockserver.exe
```
Prints: `Mock server listening on :50051 (single port: mTLS ops + enrollment; token=te****23)`.
It then prints every agent message it receives and every command it sends.

The agent connects to it per its config (`AEGIS_SERVER_ADDRESS=127.0.0.1:50051`,
`AEGIS_ENROLL_TOKEN=tenant-abc-123`, `AEGIS_TLS_*` pointing at the same `certs/`).

---

## 4. The wire contract (what the real server implements)

gRPC service `AgentControlPlane` (proto repo `grpc/agent.proto`):

- **`Connect(stream AgentMessage) returns (stream ServerMessage)`** — long-lived mTLS stream.
  - Agent → server: `Heartbeat`, `CommandResult`, `PatchScanResult`, `InstallStatus`,
    `DeviceStateReport`, `CacheFullReport`.
  - Server → agent: `Command` (signed), `PatchBaseline`, `CabManifest`.
- **`Enroll(EnrollRequest) returns (EnrollResponse)`** — token-gated, server-auth TLS;
  validate token + license, reconcile device, sign the CSR → per-device client cert.
- **`FetchContent(ContentRequest) returns (stream ContentChunk)`** — stream a cab/patch
  file to the agent (peer-cache delivery). No internet on the endpoint.

**Command signing:** every `Command` is `{command_id, type, payload, signature}` where
`signature = Ed25519(payload)`. The mockserver signs with a **dev** Ed25519 key; the
agent verifies with the matching public key (its built-in dev key, or
`AEGIS_CMD_VERIFY_KEY`). **The real server generates its OWN Ed25519 keypair and
configures agents with its public key** (never ship the dev key to production).

**Command types:** `hostname` (demo), `install_patch`, `stage_patch`, `reboot`,
`cancel_reboot`. Payload for install/stage = serialized `InstallPatchSpec`.

---

## 5. Patch delivery model (important)

The endpoint **never downloads from the internet**. Two ways a patch file reaches
the agent:

- **JIT install:** the server delivers the `.msu`/`.cab` to a **local path on the
  agent** out-of-band (peer-cache), then sends `install_patch` with
  `file_path = <that local path>`. The agent installs from the local file.
- **Staged (pre-cache):** send `stage_patch`; the agent **pulls the bytes via
  FetchContent** (so the server serves them — `AEGIS_SERVE_PATCH`) and stores them
  encrypted in its cache. A later `install_patch` for the same `KB` installs from
  the cache (no second fetch).

For the real server, `FetchContent` (peer-cache) is the delivery mechanism for both
cab and patch bytes.

---

## 6. Test scenarios (env on the mockserver, then start the agent)

All env vars are set on the **mockserver** process. Combine as needed.

### Enrollment + connect
```
set AEGIS_ENROLL_TOKEN=tenant-abc-123
```
Observe: `Enroll OK -> device_id=.. (cert signed)`, then `Agent connected!`,
device-state, heartbeat.

### Single patch install (JIT)
Place the `.msu` on the AGENT at a known path first (peer-cache stand-in), then:
```
set AEGIS_INSTALL_FILE=C:\deploy\windows11.0-kb5126052.msu
set AEGIS_INSTALL_KB=KB5126052
set AEGIS_INSTALL_HASH=<sha256 of the file>        REM optional but recommended
set AEGIS_INSTALL_HASH_ALGO=sha256                 REM optional (sha256 default, sha512 supported)
```
Observe `InstallStatus` phases: `QUEUED → VERIFYING → RUNNING → SUCCESS/FAILED`
(exit 0 / 3010 reboot-required). Already-installed KB → `SUCCESS "already installed"`.

### Multiple patches (serial queue)
Comma-separated lists (same index across lists):
```
set AEGIS_INSTALL_FILE=C:\deploy\a.msu,C:\deploy\b.msu
set AEGIS_INSTALL_KB=KB5126052,KB5129195
set AEGIS_INSTALL_HASH=<hashA>,<hashB>
```
Observe two jobs `install-001`, `install-002` running **one at a time** (the CBS
servicing stack is serial); other RPCs stay responsive meanwhile.

### Patch caching / staging
```
set AEGIS_SERVE_PATCH=C:\deploy\windows11.0-kb5126052.msu
set AEGIS_STAGE_KB=KB5126052
set AEGIS_STAGE_HASH=<sha256>
set AEGIS_STAGE_FILE=kb5126052.msu                 REM filename hint (extension only)
```
Observe `FetchContent: serving ..`, agent `stage_patch: patch staged in cache`.
Then (keep it staged) send `install_patch` for the same KB → it installs **from
cache** (no re-fetch). Comma-separated `AEGIS_STAGE_KB` for multiple.

### Patch status reporting
Already covered — the agent streams `InstallStatus` at each phase; the mockserver
prints `Received install status -> command_id=.. kb=.. phase=.. exit_code=..
reboot_required=.. reason=.. detail=..`. The real server persists these transitions.

### Cache-full report
```
set AEGIS_PRESEED_DEVICES=1          REM not needed for cache-full; this is for license
```
To force cache-full: set a tiny cap on the AGENT (`AEGIS_CACHE_MAX_BYTES=10` in its
config) and stage a new KB → agent sends `CacheFullReport`; mockserver prints
`Received CACHE-FULL report -> pending=.. needed=.. cap=.. in_use=..`.

### Cab (wsusscn2) delivery
```
set AEGIS_SERVE_CAB=C:\deploy\wsusscn2.cab
set AEGIS_CAB_VERSION=v2
set AEGIS_CAB_SHA256=<sha256 of the cab>
```
Agent sees a newer cab version → fetches via FetchContent → verifies SHA-256 →
installs it → next Layer-4 scan uses it.

### Patch baseline (offline self-diff)
```
set AEGIS_BASELINE_BUILD=26200.9457
set AEGIS_BASELINE_KBS=KB5126052,KB5129195
```
Agent caches it and flags likely-missing KBs offline.

### Reboot / cancel
```
set AEGIS_REBOOT_DELAY=90        REM schedules shutdown /r /t 90
set AEGIS_CANCEL_REBOOT=1        REM cancels a scheduled reboot (shutdown /a)
```

### License enforcement
```
set AEGIS_LICENSE_MAX=1
set AEGIS_PRESEED_DEVICES=1       REM seed N dummy devices so a single VM hits the cap
```
A new device over the cap → `Enroll REJECTED ... license limit reached (N/max)`;
the agent fails loud. Re-enroll/dedup of an existing device does NOT consume a seat.

### Concurrency demo
```
set AEGIS_PARALLEL_PINGS=10       REM sends 10 hostname commands alongside an install
```
Shows a long DISM install running while other commands are handled concurrently.

---

## 7. Env var quick reference

| Env | What it does |
|---|---|
| `AEGIS_ENROLL_TOKEN` | enrollment token to validate (required for enroll) |
| `AEGIS_LICENSE_MAX` | license cap (0/unset = unlimited) |
| `AEGIS_PRESEED_DEVICES` | seed N dummy devices (single-VM license testing) |
| `AEGIS_TLS_CA/_CERT/_KEY` | server mTLS material (default `certs/`) |
| `AEGIS_CA_KEY` | CA private key to sign enrollments (default `certs/ca.key`) |
| `AEGIS_INSTALL_FILE/_KB/_HASH/_HASH_ALGO` | send install_patch (comma-sep = multi) |
| `AEGIS_STAGE_KB/_HASH/_FILE/_HASH_ALGO` | send stage_patch (comma-sep = multi) |
| `AEGIS_SERVE_PATCH` | file FetchContent serves for `kind=patch` |
| `AEGIS_SERVE_CAB` | file FetchContent serves for `kind=cab` |
| `AEGIS_CAB_VERSION/_SHA256` | advertise a cab via CabManifest |
| `AEGIS_BASELINE_BUILD/_KBS` | push a PatchBaseline |
| `AEGIS_REBOOT_DELAY` / `AEGIS_CANCEL_REBOOT` | reboot / cancel-reboot command |
| `AEGIS_PARALLEL_PINGS` | N concurrent hostname commands |

---

## 8. What the real server owns (not in the mock)

The mockserver is minimal. The real server must add (see handoff doc):
- **Token lifecycle** — per-tenant, rotatable + expirable + license-gated (issue/
  validate/expire/rotate; optional one-time / admin-approval). Agent only carries
  the token; everything else is server-side.
- **Device reconciliation/dedup** (device_id → system_uuid), lifecycle states,
  cert issuance/rotation/**revocation**, secure CA storage, per-customer CA isolation.
- **Its own Ed25519 command-signing keypair** (configure agents with the public key).
- Persisting `InstallStatus`, `DeviceStateReport`, `CacheFullReport`, scan results.
