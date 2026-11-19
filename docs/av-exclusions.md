# Aegis One Agent — Antivirus / EDR Exclusions

**Audience:** customer AV/security admins + the Aegis One server/console team.
**Why:** a UEM agent constantly decrypts and runs installers, caches patches, and scans
the OS — work an antivirus/EDR may false-positive-block or slow by double-scanning. The
exclusions below let the agent run cleanly and fast.

> **Windows Defender is handled automatically** by the agent (no action needed).
> **Third-party AV/EDR must be excluded in that product's own console** — a local
> process cannot add its own exclusions to a third-party AV (that is a security
> boundary every AV enforces). The agent **detects** which AV is installed and reports
> it, so the console can show you exactly this list for your product.

---

## What to exclude (the whole list)

Everything the agent touches lives **under one install tree** (unlike some UEM tools
that scatter temp files into `C:\Windows\Temp`), so the exclusion list is short:

**Folder (and sub-folders):**
```
C:\Program Files\AegisOne\*
```
(The agent decrypts, stages, installs, caches and logs entirely under this tree —
`...\Client\SKVC\` and below. Adjust the path if you installed elsewhere.)

**Processes:**
```
AegisOneClient.exe      (the LocalSystem service)
AegisOneClientUI.exe    (the per-user tray UI)
```

That is the complete set — one folder + two processes.

---

## Windows Defender — AUTOMATIC (no action required)

At startup the agent adds the folder + both processes to Microsoft Defender's
exclusions via the supported API (`MSFT_MpPreference.Add`, what `Add-MpPreference`
wraps — no PowerShell, and it works even with Defender Tamper Protection on because the
agent runs as LocalSystem). It is removed again on uninstall.

- Turn it off (if you manage Defender exclusions centrally yourself): set
  `AEGIS_AV_EXCLUSIONS=0`.
- Verify: **Windows Security → Virus & threat protection → Manage settings →
  Exclusions**, or the agent log line `av-exclusions: Windows Defender exclusions
  applied`.

---

## Third-party AV / EDR — apply once in that product's console

Add the **folder** and **two processes** above in your AV/EDR management console. This
is a one-time, fleet-wide action (the console pushes it to all endpoints) — not a
per-endpoint manual step. The agent reports which AV it sees (below) so the console can
prompt you with this exact list.

| Product | Where to add exclusions |
|---|---|
| **CrowdStrike Falcon** | Falcon console → Configuration → Exclusions (ML/Sensor Visibility) → add the folder + process paths |
| **SentinelOne** | Management console → Policy → Exclusions → Path + Process |
| **Microsoft Defender for Endpoint (MDE)** | Security portal / Intune → add file + process exclusions (in addition to the local Defender exclusions the agent sets) |
| **Sophos Intercept X** | Sophos Central → Threat Protection policy → Scanning Exclusions (process + folder) |
| **Trend Micro Apex One** | Apex One console → Agent → Behavior Monitoring / scan exclusions |
| **McAfee / Trellix ENS** | ePO policy → On-Access Scan → Exclusions |
| **Others** | Add the one folder + two processes above as path + process exclusions |

> Why not automatic for these? If any local process could add its own AV exclusion,
> malware would exclude itself — so every third-party AV blocks local self-exclusion by
> design. The strongest reducer of false positives across all of them is **code-signing**
> the agent (reputation/SmartScreen trust), which is done at release.

---

## Agent-reported AV (for the console)

The agent reports the installed AV products (Windows Security Center) in its full
inventory — `FullInventoryReport.antivirus_products[]` (`name`, `enabled`,
`up_to_date`, raw `product_state`). The console uses this to:

- confirm Defender auto-exclusion applies, and
- surface the exact exclusion list above for whatever third-party AV is present.

(Empty on Server SKUs / machines with no Security Center.)
