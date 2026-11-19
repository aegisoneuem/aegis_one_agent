# Server/Console — Agent Changes (2026-11-19)

**Audience:** Aegis One server & console teams.
**What this is:** the agent-side changes landed today that the server/console consumes
or should be aware of. Proto repo: `aegis_one_proto` (regenerated); agent repo:
`aegis_one_agent`.

---

## 1. New proto fields to consume

**`DexTelemetry`** (periodic DEX telemetry):
| field | # | meaning |
|---|---|---|
| `battery_health_pct` | 36 | battery wear: full-charged / designed capacity × 100 (100 = like-new). 0 = unknown / no battery |
| `battery_cycle_count` | 37 | charge cycles (0 = firmware doesn't report) |

**`AppFault`** (inside `DexHealth.app_faults[]` — per-app crash/hang breakdown):
| field | # | meaning |
|---|---|---|
| `last_module` | 4 | faulting module of the most recent crash (e.g. `ntdll.dll`) |
| `last_exception` | 5 | exception code (e.g. `0xc0000005`) |
| `last_crash_unix` | 6 | time of the most recent crash (unix seconds) |

**`FullInventoryReport`**:
| field | # | meaning |
|---|---|---|
| `antivirus_products` | 11 | `repeated AntivirusProduct` — installed AV from Windows Security Center |

**New message `AntivirusProduct`**: `name` (display name), `enabled` (real-time
protection on), `up_to_date` (signatures current), `product_state` (raw
SecurityCenter2 bitmask). Empty on Server SKUs / no Security Center.

> None of these are breaking — all are new optional fields. Existing consumers are
> unaffected.

---

## 2. AV exclusions — new behavior

- **The agent auto-excludes ITSELF from Windows Defender** at startup (process +
  install-dir), via `MSFT_MpPreference.Add` (removed on uninstall). Config-gated
  `AEGIS_AV_EXCLUSIONS` (default ON). No console action needed for Defender.
- **Third-party AV cannot be auto-excluded** (security boundary). The agent now
  **reports the installed AV** (`antivirus_products`, §1), so the **console should
  surface the exclusion list** for whatever AV the endpoint runs. The full list +
  per-product guidance is in `docs/av-exclusions.md` (one folder + two processes).

---

## 3. Console integration rename — NSH → **SVSH**

The interactive shell / command feature previously named **NSH** (copied from
BladeLogic "Network Shell") is renamed **SVSH** throughout. If any server/console
integration references it:
- REPL mode name: `svsh` (was `nsh`) — e.g. `mockserver svsh`.
- Script extension: `.svsh` (was `.nsh`).
- Agent command-transcript dir: `<SKVC>\logs\svsh` (was `logs\nsh`).
- No wire/proto change — command types (`run_command`, `open_shell`, `file_operation`)
  are unchanged. This is a naming refactor only.

---

## 4. Inventory correctness — Windows 11 OS name

`FullInventoryReport` hardware `os_product_name` now reports **"Windows 11 ..."** on
Win11 (build ≥ 22000). Previously the registry's Microsoft bug made every Win11
endpoint report "Windows 10 ..." — **OS dashboards/filters will now be accurate**; if
the console cached/normalized the old value, re-check those rules.

---

## 5. Other (FYI, no console impact)

- **Durable identity mirror** (`HKLM\SOFTWARE\AegisOne\identity`, which preserves
  device_id/computer_id across uninstall/reinstall) is now **DPAPI-sealed** (opaque in
  regedit) instead of plaintext. No server impact — identity on the wire is unchanged;
  authorization remains the Ed25519 cert/signature, never the device_id.
- **Quieter INFO log**: routine periodic telemetry/inventory sends moved to Debug; the
  default agent log now shows only meaningful events. `AEGIS_LOG_LEVEL=debug` restores
  the detail.
- **RAM diagnostics**: `/diag` on the localhost health endpoint (goroutine/heap) and
  opt-in `AEGIS_PPROF=1` pprof, for leak/perf investigation.

---

## Not yet (server-team should still honor)

- **Endpoint identity = crypto cert, never IP/MAC/fingerprint.** The agent sends
  hardware signals only for **admin-approved** history linking; the server must never
  identify/target an endpoint by IP/MAC. (Unchanged policy; restated.)
- **CVSS / patch release-date enrichment** for missing-patch detection is server-side
  (agent reports KB + MSRC severity + bulletin IDs).
- **Code-signing** of the agent exe + MSI is a go-live prerequisite (done at release).
