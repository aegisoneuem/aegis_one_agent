# Windows Agent — Coverage vs "Mandatory Features at Launch"

**Audience:** Vijay (track owner) + server/console teams — a clarity check before the
Linux port: which launch-mandatory features are closed **from the Windows agent
standpoint**, and which are inherently **server/console** or **deferred (RC)**.

**Legend**
- ✅ **Agent DONE** (Windows, built + VM-verified)
- 🟡 **Agent does its part; the rest is server/console** (agent provides data/hooks/execution)
- 🖥️ **Server/Console or infra** — not an agent feature (agent only supplies data or connects)
- ⏸️ **Deferred** (Remote Control / Phase 2 — last in the roadmap)
- 🐧 **Next port** (Linux/macOS)

| # | Mandatory feature | Windows-agent status | Notes |
|---|---|---|---|
| 1 | Device Discovery (AD & IP ranges) | 🖥️ | Network/AD scan is a **server/console** job. (Agent "probe role" foundation is designed for later; the agent does not discover other devices.) |
| 2 | Dynamic Grouping of devices | 🖥️ | **Server-side targeting.** Agent applies a per-device signed policy; grouping/merge is server. |
| 3 | Agent install for Windows from Console | 🟡 | Agent side **ready**: signed **AegisOneClient.msi** with enroll-token + server/cert params, silent install, auto-enroll. The **push** from the console is server-side. |
| 4 | Windows & Linux Patching | ✅ (Win) / 🐧 (Linux) | **Windows: DONE** — detect + install `.msu`(wusa)/`.cab`(DISM)/MSI/exe, idempotent, VM-verified. **Linux = next port.** |
| 5 | Third-Party Application Patching | ✅ | Agent installs any third-party installer (MSI/exe) via patch-install + **software-deploy**. The catalog/metadata is server-side. |
| 6 | Missing Patch Detection (Severity, CVSS, KB, Release date) | 🟡 | Agent **detects missing patches** + reports **KB**, **MSRC severity**, **security-bulletin IDs**, OS build. **CVSS score + patch release date come from the server's CVE/patch catalog** (agent reports KB; server enriches). |
| 7 | Centralized Patching Dashboard | 🖥️ | **Console.** Agent emits device-state + inventory + statuses that feed it. |
| 8 | Automated Patch Deployment | 🟡 | Agent **executes** signed install commands automatically (incl. maintenance-window). The automation/orchestration is server. |
| 9 | Patch Approval Management | 🖥️ | **Console** workflow. Agent only runs approved, signed commands. |
| 10 | Patch Testing on Groups & Approve | 🖥️ | **Console** (grouping + approval gate). Agent executes what it is sent. |
| 11 | Patch Rollback & Recovery | ✅ | Agent `uninstall_patch` (remove a KB) + Windows **CBS transactional auto-rollback** on a failed install. Recovery orchestration = server. |
| 12 | Patch Scheduling | 🟡 | Agent **maintenance-window gating** + deferred-command queue (runs when the window opens). Schedule definition = server. |
| 13 | Software Deployment | ✅ | Agent **software-deploy** (install/uninstall catalog packages, resumable sealed-cache fetch, checkpoint-restart). VM-verified. |
| 14 | System Restart Function | ✅ | Agent `reboot` / `cancel_reboot` (shutdown.exe, grace period) **+ end-user reboot prompt** (snooze/deadline/confirm, post-reboot notice). |
| 15 | Granular Access Controls | 🟡 | Agent **policy-module command gating** (disabled command types are refused on-endpoint). Full RBAC (who-can-do-what) = console. |
| 16 | Patch Compliance / ageing / installed-patch reports | 🟡 | Agent supplies the **data** (device-state, installed KBs, missing patches, inventory). The **reports** are rendered by the console. |
| 17 | Detailed Inventory Report | ✅ | Agent **FullInventoryReport** (~64 fields: HW/OS/CPU/RAM/disks/NICs/BIOS/firmware/custom-properties). |
| 18 | Installed Software / Application Inventory | ✅ | Agent reports installed apps **+ MSIX/Appx** (pure-registry, no WMI). |
| 19 | Patch Activity & Audit Logs | ✅ | Agent **audit event stream** (server-authoritative, L1–L5) + local JSONL trace, flood-resistant. |
| 20 | Patch Failure Audit | ✅ | Failed install → **FAILED status with reason** + audit event. |
| 21 | Failed Patch Retry (Automatic) | ✅ | Agent install **maxAttempts + retry backoff** (config-driven). |
| 22 | Distributed Architecture | 🖥️ | Server/relay infra. Agent connects to any server/relay address (config); peer-cache for content exists. |
| 23 | Secure Agent Communication | ✅ | **mTLS (mutual, TLS 1.3)** + **Ed25519 command signing** + DPAPI-sealed identity/keys + optional FIPS 140-3 mode. |
| 24 | On-Premises Deployment | 🖥️ (✅ agent-capable) | Server/infra. Agent is **fully on-prem-capable** (points to any server, offline WSUS cab, no internet). |
| 25 | Remote desktop + Chat + Session recording | ⏸️ | **Remote Control — deferred to last** in the roadmap (NOT built). |
| 26 | Restrict Agent Uninstallation | ✅ | **Tamper-protection Phase A** (uninstall gate w/ passcode/token, no-SCM-stop, service+registry DACL, watchdog self-heal). VM-verified. |

> The table merges the image's duplicated numbering (two "2"s and two "3"s) into a
> single ordered list; all 24 named features are covered.

---

## Summary — from the Windows **agent** standpoint

**Essentially CLOSED on the agent side (✅):** Windows patching (detect/install/retry/
rollback), third-party app patching, software deployment, system restart + user prompt,
detailed + software inventory, audit/activity logs, patch-failure audit, automatic
retry, secure communication, restrict-uninstall (tamper). Plus the agent emits all the
**data** the console needs for the dashboard/compliance/ageing reports (🟡).

**Not the agent's job — server/console (🖥️):** device discovery, dynamic grouping,
dashboard, patch-approval, group-testing workflow, distributed architecture, on-prem
infra. The agent already supplies the data/hooks these need; the console builds the UI
and workflow.

**Needs the server to complete (🟡):** CVSS/patch-release-date enrichment (catalog),
automated-deployment + scheduling + approval orchestration, full RBAC, the report UIs.

**Deferred / not started:**
- ⏸️ **Remote Control + chat + session recording** (#25) — last in the roadmap.
- 🐧 **Linux (and macOS) patching + agent** — the next port.
- 🔏 **Code-signing** the exe + MSI (go-live prerequisite).

**Bottom line:** the Windows **agent** has built everything that is the agent's
responsibility in this list. What remains for "launch" is **(a) the server/console
features** (dashboard, discovery, grouping, approval, reports, distributed/on-prem),
**(b) Remote Control** (deferred), and **(c) the Linux/macOS ports** — none of which
are gaps in the *Windows agent* itself.
