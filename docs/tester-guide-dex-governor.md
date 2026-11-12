# Tester Guide — DEX Resource-Governor

**Feature:** while the agent is running a patch install or a software deploy, the
DEX telemetry sampler **pauses** (drops the CPU/memory readings taken during that
window). This keeps DEX numbers reflecting the user's device experience — not the
agent's own install load — and avoids piling telemetry work onto a real install.

**What to prove:**
1. Idle → DEX windows report `busy_skipped=0` (governor does NOT false-trigger).
2. During a deploy/install → the overlapping DEX window reports `busy_skipped>0`
   (or, if the whole window overlapped, a "window skipped — agent busy" line).
3. After the deploy finishes → DEX windows return to `busy_skipped=0`.

Setup: two-box VM test. mockserver on `192.168.95.129:50051`; agent on the endpoint
(`WIN-TUES4V46T7I`), already enrolled. Agent binary must be the governor build
(`dist/AegisOneClient.msi` hash `37ab4dfe…`, or `build/AegisOneClient.exe`).

---

## 1. Put the governor binary on the endpoint

If not already installed from the refreshed MSI, swap the service exe:
```bat
sc stop AegisOneClient
copy /Y <new>\AegisOneClient.exe "C:\Program Files\AegisOne\Client\SKVC\AegisOneClient.exe"
```
(Leave the service stopped for now; step 3 sets the DEX cadence then starts it.)

## 2. Pre-stage a 20-second "install" on the ENDPOINT

A batch payload that just sleeps ~20s is the test vehicle (keeps the install worker
busy long enough to span several DEX sample ticks). On the **endpoint**:
```bat
mkdir C:\temp 2>nul
> C:\temp\sleep20.bat echo @echo off
>>C:\temp\sleep20.bat echo ping -n 21 127.0.0.1 ^>nul
>>C:\temp\sleep20.bat echo exit /b 0

rem Pin it — the agent refuses unpinned file payloads. Copy this hash for step 4:
certutil -hashfile C:\temp\sleep20.bat SHA256
```

## 3. Fast DEX cadence + start the agent (ENDPOINT)

Short intervals make the pause obvious. 2 s samples, 30 s window:
```bat
reg add "HKLM\SOFTWARE\AegisOne\Client\Settings" /v AEGIS_DEX_SAMPLE_INTERVAL /t REG_SZ /d 2s  /f
reg add "HKLM\SOFTWARE\AegisOne\Client\Settings" /v AEGIS_DEX_WINDOW          /t REG_SZ /d 30s /f
sc start AegisOneClient
```
Watch the agent log (`C:\Program Files\AegisOne\Client\SKVC\logs\agent.log`).
**Baseline check:** let 1–2 windows pass with NO install running. Each must log:
```
dex-telemetry: window buffered samples=~15 busy_skipped=0 cpu_avg=... mem_used_avg=...
```
`busy_skipped=0` on an idle box is test point #1 (no false trigger).

## 4. Trigger the deploy from the mockserver

On the **server**, with mockserver stopped, set the deploy env then start it
(paste the SHA256 from step 2 into `AEGIS_DEPLOY_HASH`):
```bat
set AEGIS_ENROLL_TOKEN=tenant-abc-123
set AEGIS_DEPLOY_FILE=C:\temp\sleep20.bat
set AEGIS_DEPLOY_NAME=GovernorTest
set AEGIS_DEPLOY_TYPE=bat
set AEGIS_DEPLOY_ACTION=install
set AEGIS_DEPLOY_HASH=<paste SHA256 from step 2>
set AEGIS_DEPLOY_HASH_ALGO=sha256
mockserver.exe
```
When the agent connects, the server pushes one signed `software_deploy`. The agent
log shows `software_deploy: worker picked up job …` and runs the 20 s batch.

## 5. Expected result (PASS criteria)

In the agent log, around the deploy, a DEX window must show the pause. Either:
```
dex-telemetry: window buffered samples=5 busy_skipped=10 cpu_avg=... mem_used_avg=...
```
(some ticks dropped) **or**, if a full 30 s window sat entirely inside the deploy:
```
dex-telemetry: window skipped — agent busy the whole window busy_ticks=15
```
Then the **next** window after the deploy returns to `busy_skipped=0`.

| Check | Expected | Point |
|---|---|---|
| Idle windows | `busy_skipped=0` | no false-trigger |
| Window overlapping the deploy | `busy_skipped>0` (or "window skipped") | governor engages |
| Window after deploy finishes | `busy_skipped=0` again | governor releases |
| Server `dex-telemetry` line | still received (buffered reports drain) | pause ≠ data loss of other windows |

**FAIL signs:** `busy_skipped=0` throughout the deploy (governor not engaging), or
the agent never buffers any DEX window (sampler stuck). Capture `agent.log` around
the deploy and report.

## Notes
- The governor also covers **patch** installs (`install_patch` /
  `uninstall_patch`) and the softdist **fetch** phase — any `heavyWork.Enter()`
  path. The batch-sleep deploy is just the simplest deterministic trigger.
- `busy_skipped` is currently a LOCAL log field only; it is not yet sent to the
  server (would need a proto field — deferred unless the console wants it).
- Restore production cadence afterwards (or just delete the two Settings values):
  `reg delete "HKLM\SOFTWARE\AegisOne\Client\Settings" /v AEGIS_DEX_SAMPLE_INTERVAL /f`
  (default window 5 m, sample 15 s).
