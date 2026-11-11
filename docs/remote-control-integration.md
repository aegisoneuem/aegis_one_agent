# Remote Control — Cross-Team Integration & Open Questions

**Audience:** Aegis One **Server** team (Go / Postgres, `aegis_one_server`) and
**Console** team (React / Node / TypeScript, `aegis_one_webui`).
**From:** Agent track (Vijay). **Status:** agent-side RC-0 (signed session
plumbing) built & VM-verified; this doc aligns the server + console pieces before
the media slices land.

> This doc is grounded in a read of `aegis_one_server` @ main (2026-11-10). Where
> it proposes a module shape it says "proposed — confirm"; the **Open Questions**
> at the end are what each team needs to settle.

---

## 1. What Remote Control is (v1 scope)

One agent-based remote session for service desk: **monitor-only (view), input
control, chat, reboot, in-session command-line (PowerShell/cmd), collaboration
(one controller + viewers), run-with-elevated (agent = SYSTEM/admin-capable)**.
Agentless mode is **deferred** to a later phase. Media transport = **WebRTC
(pion) + coturn TURN**; the server is **signaling only** (SDP/ICE over the
existing agent control stream), media flows agent↔viewer via TURN.

Decisions already fixed (agent track): pion/webrtc+coturn transport;
**unattended-first**; collaboration = **one controller + viewers** (control
explicitly passed); agent-based session runs in **agent SYSTEM/admin context**
(service-desk needs admin — controls are visible-indicator + audit + RBAC +
policy-gate + session token, not privilege reduction); **broker** layer separate
from the app server, cross-VLAN/roaming, ~**200 concurrent / 10k devices per
broker**.

---

## 2. Big finding: the server already has the plumbing RC needs

`aegis_one_server` already ships a **remote-operations subsystem** that RC should
extend as a **sibling**, not reinvent:

- `internal/remoteops/` — HTTP API under `/internal/remote/` for run_command (as
  SYSTEM), file ops, file transfer, and **interactive shells** over a bidirectional
  gRPC stream with long-poll output + **transcript recording**.
- `internal/agentactions/` — fire-and-forget device commands (reboot, scan, etc.).
- Migration `0020_remote_operations.sql` — `agent_uploads`, `file_transfers`,
  `shell_sessions` (session id, device/agent, `created_by_api_client_id`, status,
  ttl/idle/max, `transcript_name`, bytes, exit_code, close_reason).

**There is NO screen-share / WebRTC / TURN / video today** — that part is
greenfield. But the **interactive shell (`ShellSession`) is the direct template
for RC signaling**: a signed kickoff command mints a single-use session id, the
agent dials back a bidi stream and authenticates the session id against its mTLS
identity, and the session is owned/audited/recorded. RC reuses this verbatim for
SDP/ICE signaling.

---

## 3. Proposed RC architecture (three pieces)

```
            PROTO (aegis_one_proto, owner: Vijay/agent track)  ← the contract seam
            + open_rc signed Command, + RCSession bidi RPC (SDP/ICE)
        ┌──────────────┬───────────────────────────────┬───────────────────┐
        │  AGENT        │  SERVER RC module             │  CONSOLE RC viewer │
        │ (built here)  │  internal/remotecontrol/      │  React/TS feature  │
        │ capture+pion  │  (sibling of remoteops)       │  WebRTC client     │
        │ +signaling    │  + migration 0023 + perms     │  + chat + recording│
        └──────┬────────┴───────────────┬───────────────┴─────────┬─────────┘
          RCSession bidi (signaling)  management API (/internal/remotecontrol/)
          media ───────────────── coturn TURN ─────────────── media
```

### The proto is the seam — and the agent track owns it
`proto/aegisone/v1/agent.proto` in the server repo is a **recovered mirror** of
the agent team's `aegis_one_proto` (grpc/agent.proto, owner: Vijay). So the RC
contract is **driven from the agent-track proto**; the server regenerates `gen/`
from it. New wire needed (coordinated, additive — never reuse field numbers;
`buf breaking` WIRE_JSON must pass):
- a signed `Command` type **`open_rc`** (kickoff) + a `ServerMessage` oneof arm if
  needed;
- a bidi RPC on `AgentControlPlane`, e.g. `rpc RCSession(stream RCClientMsg)
  returns (stream RCServerMsg)` **mirroring `ShellSession`'s shape**, carrying
  SDP offer/answer + ICE candidates (the control channel only);
- an agent **capability string** (e.g. `remote_control`) so the server gates per
  device via `agent_capabilities`.

---

## 4. For the SERVER team — proposed module shape (reuse your own patterns)

Build `internal/remotecontrol/` as a sibling of `remoteops`, reusing:

- **Kickoff:** `commands.Sender.Send(...)` for the signed `open_rc` command —
  you get signing (`internal/signing`, Ed25519 canonical bytes), record-before-send,
  idempotency, capability-gating, audit, and online-only delivery for free.
- **Signaling stream:** add `RCSession` to `grpcserver.Server` (mirror
  `ShellSession`): validate session id against the agent's **mTLS identity**, TTL,
  `opening→active`. Attach via an `EnableRemoteControl(...)` method on the gRPC
  server (same pattern as `EnableRemoteOps`).
- **HTTP management API:** mount `mux.Handle("/internal/remotecontrol/",
  auth.Require("", remotecontrol.NewHandler(...)))`; per-route permission checks
  inside (the remoteops pattern).
- **DB:** new migration **`0023_remote_control.sql`** (current max is 0022) —
  `rc_sessions` (reuse `shell_sessions`' columns: id, device/agent, owner, status,
  ttl/idle/max, recording_name, close_reason) + `rc_participants` (collaboration:
  operator, role controller|viewer, joined_at) + recording metadata. The re-applied
  `0004_db_roles.sql` auto-grants new tables to `aegis_app`.
- **RBAC:** new permission consts in `apiauth` (e.g. `remotecontrol.start`,
  `remotecontrol.control`, `remotecontrol.recording.view`) + a seed migration
  (shell/files are admin-only by default today). **RBAC-before-consent**: verify
  the operator's perm for the target device/group **before** the agent sends the
  consent prompt.
- **Session reaper / TURN-cred rotation:** a `Run(ctx)` background loop started in
  `main.go` (like `remote.shells.Run`).
- **Config:** new `AEGIS_*` keys for coturn (TURN URLs, shared secret for
  ephemeral creds), RC toggles.
- **Conventions:** slog structured logging, raw pgx/v5 + pgxpool, `fmt.Errorf("…:
  %w")`, `UserError` 4xx/5xx split, unit tests alongside, `buf lint`/`buf breaking`
  green.

### Server team — open questions
1. **Proto workflow:** RC RPCs/messages originate in the agent-track
   `aegis_one_proto`. How do we coordinate the add + regen `gen/` on your side (PR
   order, who regenerates)?
2. **Viewer identity vs api-client identity:** remoteops authorizes by `api_client`
   token and owns sessions by `created_by_api_client_id`, but RC consent/indicator
   implies a **human operator (user)** identity. `agent_commands` has
   `issued_by_user_id` but the code path only sets `issued_by_api_client_id`. Is RC
   driven by console **user sessions** or API tokens, and how is consent modeled?
3. **Signaling auth for viewers:** where do viewers connect from, and how do they
   authenticate to the signaling endpoint vs the existing `aeg_` token model?
4. **Recording storage:** shells record transcripts to `<contentdir>/shell/`. RC
   recordings are large video — same content dir, object store, retention policy?
5. **Scale/HA:** `remoteops` holds live session state **in-process** (`Shells`
   map). Single-instance, or does RC need shared/sticky session state across
   replicas (the broker is horizontally scaled)?
6. **`source_job_type` CHECK** (`agent_commands`) is
   `IN ('patch_deployment','software_deployment','vulnerability_scan')`. Extend with
   `rc_session`, or keep RC commands NULL-sourced?
7. **Capability advertisement & default roles** for `remote_control` / the new
   `remotecontrol.*` perms.

---

## 5. For the CONSOLE team — RC viewer module

A self-contained React/TS feature module (folder matching your structure), merged
into `aegis_one_webui`:

- **WebRTC viewer:** `RTCPeerConnection` that completes SDP/ICE with the agent via
  the server's signaling API, renders the agent's media (screen) in a `<video>` /
  canvas, and sends input events back (for control). Uses **coturn TURN** ICE
  servers with **ephemeral per-session creds** fetched from the server.
- **Session UI:** connect/disconnect, monitor picker (multi-monitor later),
  **mandatory visible-session state**, chat panel (WebRTC data channel), and
  **collaboration** (one controller + viewers; request/pass control).
- **Recording playback:** gated by its **own** RBAC permission, separate from
  session-initiate.
- **Device search / launch:** start a session from a device found by **hostname /
  IP / logged-in user** (the agent reports logged-in user via inventory — agent
  track is adding that OOB attribute).

### Console team — open questions
1. **Signaling + auth:** which management API does the viewer call to start/signal
   a session, and how does a **browser user** authenticate (session cookie / bearer)
   vs the server's `aeg_` api-client tokens?
2. **TURN creds delivery:** endpoint to fetch ephemeral coturn creds per session.
3. **Routing / feature-flag:** where does the RC route live, and how is RC
   availability surfaced per device (capability)?
4. **Design system:** confirm the component/library conventions so the viewer
   matches `aegis_one_webui` (the agent track will build to your format — please
   share the current repo structure / a sample feature module; `aegis_one_webui`
   may be ahead of what we have).

---

## 6. What's NOT blocked (parallel work, no cross-team dependency)

The agent track continues dependency-free Phase-1 items in the meantime (A3
policy-field enforcement, OOB identity attributes incl. logged-in user, DPAPI
key-tamper alert, agent tamper-proofing, DEX telemetry, custom-attribute engine,
resource governor). RC media slices (capture + pion + browser viewer) resume once
the proto contract + the two questions lists above are settled.

---

## 7. TL;DR
- RC is a **separate, mergeable module** on all three sides. No monolith.
- The server **already has** the command/signing/stream/recording/RBAC plumbing
  (`remoteops` + `ShellSession`) — RC is a **sibling**, reusing it.
- The **contract lives in the agent-track proto** (Vijay-owned), so the agent
  track drives it; the server regenerates. Additive only, `buf breaking` safe.
- The only genuinely new thing is the **media plane** (screen capture + WebRTC +
  TURN + browser viewer). Everything else has a precedent in the repo.
- Settle the two **Open Questions** lists, lock the proto additions, then the media
  slices proceed.
