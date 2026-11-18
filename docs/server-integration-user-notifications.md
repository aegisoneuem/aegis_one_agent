# Server/Console Integration — End-User Notifications (Tray UI)

**Audience:** the Aegis One **server & console** teams.
**Purpose:** how to drive the endpoint's end-user notifications — the tray icon and the
info / message / action (reboot) prompts — from the server, the command shape, the
responses you get back, and what the agent does on its own.

> **Status:** agent-side **BUILT & VM-verified** (info balloon, persistent message,
> action request with deadline + snooze + confirmation + real reboot, response path).
> **Self-service app** (tray-click → offers/progress portal) is **Phase 2, not built.**
> Patch-flow auto-hooks (pre/post-patch + reboot prompt fired by the install
> lifecycle) are the **next agent step (3d)**; today everything is server-triggered.

---

## 1. Architecture (why there are two processes)

The agent service runs as **LocalSystem in Session 0**, which has no interactive
desktop and cannot draw UI for the logged-on user. So a small, separately-signed
companion — **`AegisOneClientUI.exe`** — is launched by the service into each
logged-on user session (BigFix ships `BESClientUI.exe` the same way). They talk over a
local, ACL'd named pipe.

```
 server ──signed notify_user command──▶ AegisOneClient.exe (service, Session 0)
                                           │  broadcast over \\.\pipe\AegisOneClientUI
                                           ▼
                                        AegisOneClientUI.exe (user session) ── shows tray icon,
                                           │  balloon / message window / action window
                                           ▼
                                        user clicks a button / snoozes / closes (X)
                                           │  response over the pipe
 server ◀──ActionStatus + audit event───── service (reportUserResponse)
```

- The **tray icon** is always present (silent) — this is a UEM agent, it does **not**
  announce itself on service start/login. A balloon/window appears **only** when the
  server sends a `notify_user`.
- The UI process is tied to the service's lifetime (a shared alive-beacon): no
  orphaned UI, no duplicate icons after a restart.

---

## 2. Driving it — the `notify_user` signed command

Send a standard **Ed25519-signed command** (same signing as every other command),
`type = "notify_user"`, with a JSON payload:

```json
{
  "kind": "action",
  "title": "Restart Required",
  "text":  "Your administrator is requesting that you restart your computer.",
  "detail": "This reboot applies: SCU Update - Dell BIOS, Version 1.11.1",
  "buttons": ["Restart now", "Later"],
  "deadline_unix": 1795000000,
  "allow_snooze": true,
  "snooze_opts": [60, 300, 1800, 3600, 10800, 21600, 43200, 86400],
  "confirm_text": "This will restart your computer now. Save your work first.",
  "on_accept": "reboot"
}
```

### Fields

| field | applies to | meaning |
|---|---|---|
| `kind` | all | `info` (default) · `message` · `action` — see §3 |
| `title` | all | heading (bold) |
| `text` | all | body text (wraps) |
| `detail` | action | optional secondary line (e.g. which patch the reboot applies) |
| `buttons` | action | choice labels, first = **primary/accept** (default button). e.g. `["Take Action","Cancel"]` |
| `deadline_unix` | action | shows "runs automatically at …"; **the server enforces the deadline** (see §5) |
| `allow_snooze` | action | show the Snooze dropdown + button |
| `snooze_opts` | action | snooze durations in **seconds** for the dropdown; omitted → 1m…24h default |
| `confirm_text` | action | if set, choosing the **primary** button opens a "Confirmation Required" dialog that must be acknowledged first |
| `on_accept` | action | a **local** action the agent runs when the user accepts the primary button: `"reboot"` (schedules `shutdown /r`, 60 s grace). Omit → server-driven only |

A minimal info notification is just `{"title":"…","text":"…"}` (kind defaults to info).

---

## 3. The three notification kinds (BigFix taxonomy)

| kind | looks like | position | dismiss | response? |
|---|---|---|---|---|
| **info** | tray **balloon** | by the tray | auto (few seconds) | no |
| **message** | a **window** (icon + title + text) | **bottom-right, near the tray** | user closes with **X / Close** (never auto) | no |
| **action** | a prominent **window** with buttons, deadline, snooze | **centre-screen** | a button, Snooze, or **X** (= dismissed) | **yes** (§4) |

---

## 4. Responses you get back (action only)

The agent reports the user's choice as an **ActionStatus** against the command id, and
an **audit event** `user_action_response` (Decision = the choice):

| user did | ActionStatus phase | reason / Decision |
|---|---|---|
| shown (no UI connected) | `FAILED` | "no tray UI connected — …" (user logged off / UI not running) |
| shown (awaiting) | `RUNNING` | "action shown to N user session(s) — awaiting response" |
| clicked a button | `SUCCESS` | `user chose: <label>` |
| clicked primary + confirmed | `SUCCESS` | `user chose: <primary label>` (+ agent runs `on_accept`) |
| clicked primary + cancelled confirm | `SUCCESS` | `user chose: cancelled` |
| snoozed | `RUNNING` | `user snoozed for <secs>s — will re-prompt` (agent re-shows after the interval) |
| closed with X | `SUCCESS` | `user chose: dismissed` |

**Snooze is handled on the endpoint:** the agent re-shows the same window after the
chosen interval and reports each snooze; it keeps re-prompting until the user picks a
terminal choice. The server just records the `RUNNING`/snooze updates.

---

## 5. Server responsibilities

1. **Send the notification** — a signed `notify_user` with the right `kind`/fields,
   targeted like any command (per-device / per-group).
2. **Act on the response** — for anything other than a local `on_accept`, the **server
   decides** what to do with the user's choice (e.g. user chose "Install now" → send
   the install/patch command; "Later" → reschedule). The notification layer reports
   intent; it does not run server-side actions on its own.
3. **Enforce the deadline** — `deadline_unix` is shown to the user, but the agent does
   **not** auto-run at the deadline. If you want "runs automatically in 24 h", the
   **server** sends the actual action (or an `on_accept` reboot) when the deadline
   passes and the user hasn't acted.
4. **Surface the audit events** — `user_action_response` (Decision = chose/snoozed/
   dismissed/cancelled) in the console.

---

## 6. Audit events

| event_type | meaning |
|---|---|
| `user_action_response` | the user's choice for an action (Decision = button / snoozed / dismissed / cancelled) |

(Delivery itself is reported via the command's ActionStatus, §4.)

---

## 7. Not yet / next

- **Patch-flow auto-hooks (agent step 3d):** pre/post-patch messages + the reboot
  prompt fired automatically by the install lifecycle (policy-driven), and a
  **post-reboot continuation** (a marker before reboot → a "patch complete"
  notification after the machine comes back). Until then, drive these by sending
  `notify_user` from the server around your patch commands.
- **Self-service application (Phase 2):** clicking the tray icon opening an Offers /
  Progress portal (BigFix Support Center style). Not built.
- **Signing:** `AegisOneClientUI.exe` is a separate binary and must be Authenticode-
  signed alongside the service exe before go-live (see the tamper-protection doc).

---

*Build note (for packaging):* the MSI ships **two** exes — `AegisOneClient.exe`
(service) and `AegisOneClientUI.exe` (tray UI) — side by side in the install dir.
