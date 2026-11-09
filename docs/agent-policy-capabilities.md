# Aegis One — Signed Agent Policy: Capability List

**One signed JSON document, pushed from the console, controls every agent
setting below. No per-feature binary, no re-install — change the policy, the
behavior changes.** The document is Ed25519-signed (tamper-proof), anti-rollback
(sealed floor), and every change is audited.

Delivery: a signed `set_policy` command (JSON payload). Persisted + re-verified
at startup. A tampered/rolled-back/deleted policy is refused and alerted
(`policy_tampered` / `policy_lockdown`), and the agent fails **closed** (safe
mode) until the server re-asserts.

**Status key:** ✅ enforced now · ◻️ declared (contract defined; enforcement
wired incrementally). Every field is in the signed schema today — ◻️ means the
knob is accepted and reported, with runtime enforcement landing per release.

## security_floor
| Setting | Status | Notes |
|---|---|---|
| `require_fips` | ✅ | Agent refuses to start unless FIPS 140-3 active (fail-closed) |
| `require_signed_commands` | ✅ | Always on (agent only runs Ed25519-signed commands) |
| `min_tls` | ✅ | Agent enforces TLS 1.3 floor |

## modules — per-capability ON/OFF  (✅ enforced)
`allow` (strict allow-list) / `deny` (deny wins) of any command type:
`install_patch`, `uninstall_patch`, `software_deploy*`, `scan_now`, `reboot`,
`run_command`, `open_shell`, `file_operation`, `collect_file`, `audit_query`,
`report_device_state`, `set_policy` (always allowed — no lock-out), future
`remote_control`. A denied type is refused before any handler runs
(`blocked by agent policy`).

## patch
| Setting | Status |
|---|---|
| `reboot_policy` (never\|if_required\|always) | ✅ (`never` blocks reboots) |
| `install_if_reboot_pending` | ◻️ |
| `direct_vendor_download` | ◻️ |
| `scan_interval_seconds` | ◻️ |
| `max_attempts` | ◻️ |

## inventory
| Setting | Status |
|---|---|
| `full_interval_seconds` | ◻️ |
| `device_state_interval_seconds` | ◻️ |
| `scan_on_login` / `scan_on_ip_change` | ◻️ |

## transfer (bandwidth)
| Setting | Status |
|---|---|
| `fetch_max_bytes_per_second` | ◻️ (agent already supports fetch throttle; policy override wiring incremental) |
| `upload_max_bytes_per_second` | ◻️ |
| `peer_cache_enabled` | ◻️ |

## maintenance (operating window)
| Setting | Status |
|---|---|
| `days` + `start_hour`/`end_hour` (when installs/reboots run) | ◻️ |

## audit (flood control)
| Setting | Status |
|---|---|
| `rate_per_sec` / `burst` / `dedup_window_seconds` | ◻️ (agent tunes these via config today; policy override incremental) |

## logging
| Setting | Status |
|---|---|
| `level` (debug\|info\|warn\|error) | ◻️ |

## checkin
| Setting | Status |
|---|---|
| `heartbeat_interval_seconds` | ◻️ |

---

### Why this beats the competition
- **Ivanti** ships separate agents / rigid module installs per feature set; we
  use ONE agent + signed config — "patch+reboot" vs "patch+no-reboot" is a
  policy field, not a different binary.
- **BigFix** stores client settings in a plain, admin-editable registry; our
  policy is **signed + anti-rollback + tamper-alerted + fail-closed**, so a local
  admin can neither forge nor silently downgrade it.
- Everything is **auditable** (L1–L5) and reported in the startup **security
  posture** — the "prove it" surface for a customer.

### Mock env triggers (testing)
`AEGIS_SET_POLICY=1` with `AEGIS_POLICY_ID`, `AEGIS_POLICY_ISSUED_AT`
(**Unix-epoch integer**, larger = newer), `AEGIS_POLICY_REQUIRE_FIPS`,
`AEGIS_POLICY_DENY`, `AEGIS_POLICY_ALLOW`, `AEGIS_POLICY_REBOOT`,
`AEGIS_POLICY_FETCH_MAX_BPS`, `AEGIS_POLICY_LOG_LEVEL`. Declared-field triggers
(◻️ accepted + persisted): `AEGIS_POLICY_MIN_TLS`, `AEGIS_POLICY_MAINT_DAYS`,
`AEGIS_POLICY_MAINT_START`, `AEGIS_POLICY_MAINT_END`, `AEGIS_POLICY_AUDIT_RATE`,
`AEGIS_POLICY_AUDIT_BURST`, `AEGIS_POLICY_AUDIT_DEDUP`,
`AEGIS_POLICY_INV_FULL_INTERVAL`. Lockdown threshold:
`AEGIS_POLICY_LOCKDOWN_THRESHOLD` (default 3) under
`HKLM\SOFTWARE\AegisOne\Client\Settings`.
