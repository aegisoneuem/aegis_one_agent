# SoftDist - single manual testing guide

28 October 2026. This is the ONE testing file for the current server-team handoff.
Use `dist/AegisOneClient.exe` on the stopped endpoint service, then restart it
(or use the matching `dist/AegisOneClient.msi`). Use `dist/mockserver.exe` in
C:\temp\mock with existing certificates. Replace BOTH agent and mockserver.
All six blocks below are included in this release: 12 engine cases and six real
vendor cases. Run the real cases in E and F in their stated order.

| Block | Manual cases | Current reported status |
|---|---|---|
| [A. Multi-file packages](#a-multi-file-packages) | Deploy/cleanup, custom retention, wrong hash | Vijay reports PASS |
| [B. Pre/post commands](#b-prepost-commands) | Success, pre-failure, post-failure | Vijay reports PASS |
| [C. Package dependencies](#c-package-dependencies) | Missing install, installed skip, failure blocks main | Vijay reports PASS |
| [D. Advanced detection](#d-advanced-detection) | File skip, native file version, registry ALL/ANY | Vijay reports PASS |
| [E. Real 7-Zip MSI lifecycle](#e-real-7-zip-msi-lifecycle) | Install, repeat skip, upgrade, uninstall, repeat uninstall | PASS reported; action logs reviewed |
| [F. Real Firefox installer and configuration](#f-real-firefox-installer-and-configuration) | EXE + JSON bundle, post-copy, active browser policy | PASS reported after recovery |
| [G. Rollback-on-failure orchestration](#g-rollback-on-failure-orchestration-new-block-28-october-2026) | Main-failure rollback, partial rollback, success skips rollback | Native regression PASS, VM acceptance pending |
| [H. Crash recovery and durable journal](#h-crash-recovery-and-durable-journal-new-block-28-october-2026) | Agent restart, orphan package directory sweep | Native regression PASS, VM acceptance pending |
| [I. Descendant termination on timeout](#i-descendant-termination-on-timeout-new-block-28-october-2026) | Timeout kills children, reports count hint | Native regression PASS, VM acceptance pending |
| [J. Reboot-sensitive + MST + config-only](#j-reboot-sensitive--msimst--config-only-primary-focus-28-october-2026) | Deterministic 3010 handling, 7-Zip MSI + MST transform, config-only parameter update via post-step | **PASS 2026-10-29 (Vijay VM run)** |
| [K. Bandwidth throttling](#k-bandwidth-throttling-bigfix-aligned-30-october-2026) | Per-task fetch rate, per-task upload rate, lower-wins policy enforcement | Native regression PASS, VM acceptance pending |

All commands are manual CMD commands. Open a NEW MOCKSERVER CMD for each block
with your normal TLS settings; stop the previous server first. Each block states
its own endpoint preparation, purpose, commands and expected results. Replace the
sample target UUID with your own device UUID. There is no requirement to wait for
a chat reply before running another independent case.

For commands using `cmd.exe`, `/D` disables CMD AutoRun startup commands; `/C`
runs the following command and exits CMD. `exit 7` deliberately returns failure.
In repeat/skip tests it must NOT execute because detection is already satisfied.
Expected FAILED results in negative cases mean the protection test PASSED.

Vijay reports the cases in all four blocks passed. The server team can repeat
and record them independently; individual VM logs were not independently reviewed.

Blocks A-D use fixture/configuration packages. E-F record real MSI/EXE and config
acceptance. MST transforms, real runtime dependencies, config-only parameterized
updates and failure/reboot recovery still need separate acceptance. Do not mark
all of SoftDist complete from these cases alone.


## A. Multi-file packages

### Prepare once

MOCKSERVER - in a fresh CMD with your normal TLS settings:

```cmd
cd /d C:\temp\mock
mkdir C:\temp\AegisBundleDemo-src\settings
echo REGION=apac>C:\temp\AegisBundleDemo-src\settings\app.ini
echo Aegis multi-file package>C:\temp\AegisBundleDemo-src\readme.txt
mockserver.exe pack C:\temp\AegisBundleDemo-src C:\temp\AegisDemoBundle.zip
```

`pack` is the actual native packaging feature: it creates a ZIP, file manifest and
SHA256. Output ZIP must be outside the source folder. If recreating this exact
ZIP, delete only the old ZIP with `del C:\temp\AegisDemoBundle.zip` before packing.

The bundle contains `readme.txt` and `settings\app.ini`, preserving the subfolder.
The automatically created `aegis-package.json` is package integrity metadata,
not an executable test helper. No manual manifest authoring is needed.

In the same MOCKSERVER CMD, clear old test settings and select the endpoint:

```cmd
set "AEGIS_DEPLOY_PRE_COMMAND="
set "AEGIS_DEPLOY_PRE_ARGS="
set "AEGIS_DEPLOY_PRE_NAME="
set "AEGIS_DEPLOY_PRE_TIMEOUT_SECONDS="
set "AEGIS_DEPLOY_PRE_SUCCESS_CODES="
set "AEGIS_DEPLOY_PRE_SECURE_INPUT="
set "AEGIS_DEPLOY_POST_COMMAND="
set "AEGIS_DEPLOY_POST_ARGS="
set "AEGIS_DEPLOY_POST_NAME="
set "AEGIS_DEPLOY_POST_TIMEOUT_SECONDS="
set "AEGIS_DEPLOY_POST_SUCCESS_CODES="
set "AEGIS_DEPLOY_POST_SECURE_INPUT="
set "AEGIS_DEPENDENCY_ID="
set "AEGIS_DEPENDENCY_NAME="
set "AEGIS_DEPENDENCY_FILE="
set "AEGIS_DEPENDENCY_CONTENT_ID="
set "AEGIS_NSH="
set "AEGIS_NSH_SCRIPT="
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_HASH_ALGO=sha256"
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_VERSION="
set "AEGIS_DEPLOY_SESSION_ID="
set "AEGIS_DEPLOY_SECRETS_FILE="
set "AEGIS_DEPLOY_PARAMS="
set "AEGIS_INSTALL_FILE="
set "AEGIS_UNINSTALL_KB="
set "AEGIS_STAGE_KB="
set "AEGIS_COLLECT_PATHS="
set "AEGIS_REBOOT_DELAY="
set "AEGIS_SCAN_NOW="
set "AEGIS_CANCEL_REBOOT="
set "AEGIS_TARGET_DEVICE=b3dea9f7-5df8-4765-ad8b-621307ef5f41"
```

Confirm this UUID still belongs to WIN-TUES4V46T7I. Use the real UUID for another
endpoint. Stay in this CMD for the following cases. Ctrl+C before changing job
settings. Wait for the intended host's terminal result, not just QUEUED/RUNNING.

### Case 1 - Deploy multiple files and clean default staging

#### 1. Purpose

Deploy both configuration files and their subfolder from the bundle. Verify the
command runs in the extracted package directory, and default staging is removed
without removing the deployed files.

#### 2. Commands

MOCKSERVER:

```cmd
set "AEGIS_DEPLOY_DETECT_COUNT=0"
set "AEGIS_DEPLOY_DETECT_MODE="
set "AEGIS_DEPENDENCY_DETECT_COUNT=0"
set "AEGIS_DEPENDENCY_DETECT_MODE="
set "AEGIS_DEPLOY_NAME=Multi-file configuration deployment"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_CONTENT_ID=demo-multifile-package"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisDemoBundle.zip"
set "AEGIS_DEPLOY_AUTO_HASH=1"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_BUNDLE_ENTRY="
set "AEGIS_DEPLOY_BUNDLE_COMMAND=xcopy.exe"
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisBundleInstalled /E /I /Y"
mockserver.exe server
```

ENDPOINT - Administrator CMD after the terminal result:

```cmd
type C:\temp\AegisBundleInstalled\settings\app.ini
type C:\temp\AegisBundleInstalled\readme.txt
dir /ad "C:\Program Files\AegisOne\Client\SKVC\download\packages\aegis-package-*"
```

The last command uses the default agent download directory. If your agent has an
AEGIS_DOWNLOAD_DIR override, use that configured directory's packages subfolder.
Mockserver also prints the exact `package_directory` used by this job.

#### 3. Expected results

- ACTION_PHASE_SUCCESS, exit_code 0.
- app.ini contains REGION=apac; readme.txt contains Aegis multi-file package.
- Mock output includes `package_directory: ...` and terminal `cleanup: removed`.
- This job's extraction directory is absent. With only this test job, the final
  dir command finds no aegis-package-* directory.
- The installed files under AegisBundleInstalled remain. This sample deploys config
  files; no vendor application is claimed installed by the generic action label.

### Case 2 - Retain custom staging, then explicitly delete file/folder

#### 1. Purpose

Verify that a custom package location is retained after deployment so its files
can be reused. Then demonstrate the exact cleanup syntax for a file or the whole
package folder.

#### 2. Commands

ENDPOINT - Administrator CMD before starting:

```cmd
dir C:\temp\AegisRetainedDemo
```

This destination must not exist. A custom destination is a NEW dedicated folder;
existing folders are never merged or overwritten. Its parent C:\temp already exists.
If repeating this exact test, first finish its explicit cleanup below.

MOCKSERVER - stop previous mockserver. After the common preparation above,
set the complete job below in that same CMD:

```cmd
set "AEGIS_DEPLOY_NAME=Retained custom package"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_CONTENT_ID=demo-multifile-package"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisDemoBundle.zip"
set "AEGIS_DEPLOY_AUTO_HASH=1"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_BUNDLE_ENTRY="
set "AEGIS_DEPLOY_BUNDLE_COMMAND=xcopy.exe"
set "AEGIS_DEPLOY_BUNDLE_DEST=C:\temp\AegisRetainedDemo"
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisBundleInstalled /E /I /Y"
mockserver.exe server
```

ENDPOINT - Administrator CMD after completion:

```cmd
dir /s C:\temp\AegisRetainedDemo
type C:\temp\AegisRetainedDemo\settings\app.ini
```

Delete one retained file:

```cmd
del /Q "C:\temp\AegisRetainedDemo\readme.txt"
dir C:\temp\AegisRetainedDemo
```

Delete the complete dedicated package folder:

```cmd
rmdir /S /Q "C:\temp\AegisRetainedDemo"
dir C:\temp\AegisRetainedDemo
```

These delete commands target only the exact dedicated test folder. They can also
be authored as an explicit SYSTEM command job from the console; they are not run
automatically for a retained custom package.

#### 3. Expected results

- Deployment ACTION_PHASE_SUCCESS; terminal `cleanup: retained` and exact custom path.
- Both extracted files/subfolder exist before manual cleanup.
- After del, readme.txt is absent and settings\app.ini remains.
- After rmdir, AegisRetainedDemo is absent.
- Deployed files under C:\temp\AegisBundleInstalled remain unchanged.

### Case 3 - Wrong integrity hash prevents deployment

#### 1. Purpose

Verify a ZIP with the wrong signed SHA256 is refused before its command can copy
files. This protects the package and all supporting files from replacement/corruption.

#### 2. Commands

ENDPOINT - confirm this output path is absent before starting:

```cmd
dir C:\temp\AegisBundleShouldNotRun
```

MOCKSERVER - Ctrl+C; after the common preparation above, set the complete job:

```cmd
set "AEGIS_DEPLOY_NAME=Bundle wrong-hash refusal"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_CONTENT_ID=demo-multifile-package"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisDemoBundle.zip"
set "AEGIS_DEPLOY_HASH_ALGO=sha256"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_BUNDLE_ENTRY="
set "AEGIS_DEPLOY_BUNDLE_COMMAND=xcopy.exe"
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_AUTO_HASH=0"
set "AEGIS_DEPLOY_HASH=0000000000000000000000000000000000000000000000000000000000000000"
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisBundleShouldNotRun /E /I /Y"
mockserver.exe server
```

ENDPOINT - after the terminal result:

```cmd
dir C:\temp\AegisBundleShouldNotRun
```

#### 3. Expected results

- ACTION_PHASE_FAILED with a hash/integrity mismatch reason. This failure is the
  expected outcome of this negative test.
- AegisBundleShouldNotRun remains absent: the command did not deploy the files.

Before a later normal deployment, clear the negative-test hash settings:

```cmd
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_AUTO_HASH=1"
```

Record each case's endpoint, phase, reason, package_directory and package_cleanup.
UI/backend implementation details are in [the server integration notes](softdist-bundle-server-integration.md).


## B. Pre/post commands

### Prepare once

**MOCKSERVER:** open a fresh CMD with your normal TLS settings. Copy `dist/mockserver.exe` into `C:\temp\mock`, then create a small package:

```cmd
cd /d C:\temp\mock
mkdir C:\temp\AegisWorkflowSource\settings
echo REGION=apac>C:\temp\AegisWorkflowSource\settings\app.ini
echo Configuration package>C:\temp\AegisWorkflowSource\readme.txt
mockserver.exe pack C:\temp\AegisWorkflowSource C:\temp\AegisWorkflow.zip
```

If recreating the ZIP, delete only the previous `C:\temp\AegisWorkflow.zip` first.

In the same CMD set these common settings. The UUID below selects Vijay's
WIN-TUES4V46T7I endpoint; replace it with your test device UUID if needed.

```cmd
set "AEGIS_DEPENDENCY_ID="
set "AEGIS_DEPENDENCY_NAME="
set "AEGIS_DEPENDENCY_FILE="
set "AEGIS_DEPENDENCY_CONTENT_ID="
set "AEGIS_NSH="
set "AEGIS_NSH_SCRIPT="
set "AEGIS_INSTALL_FILE="
set "AEGIS_UNINSTALL_KB="
set "AEGIS_STAGE_KB="
set "AEGIS_COLLECT_PATHS="
set "AEGIS_REBOOT_DELAY="
set "AEGIS_SCAN_NOW="
set "AEGIS_CANCEL_REBOOT="
set "AEGIS_TARGET_DEVICE=b3dea9f7-5df8-4765-ad8b-621307ef5f41"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_HASH_ALGO=sha256"
set "AEGIS_DEPLOY_AUTO_HASH=1"
set "AEGIS_DEPLOY_CONTENT_ID=workflow-demo"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisWorkflow.zip"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_SESSION_ID="
set "AEGIS_DEPLOY_SECRETS_FILE="
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_VERSION="
set "AEGIS_DEPLOY_REQUIRE_SIG="
set "AEGIS_DEPLOY_SUCCESS_CODES="
set "AEGIS_DEPLOY_TIMEOUT=60"
set "AEGIS_DEPLOY_BUNDLE_ENTRY="
set "AEGIS_DEPLOY_BUNDLE_COMMAND=xcopy.exe"
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_PRE_COMMAND=findstr.exe"
set "AEGIS_DEPLOY_PRE_ARGS=/X /C:REGION=${REGION} settings\app.ini"
set "AEGIS_DEPLOY_PRE_NAME=Check configuration region"
set "AEGIS_DEPLOY_PRE_TIMEOUT_SECONDS=30"
set "AEGIS_DEPLOY_PRE_SUCCESS_CODES="
set "AEGIS_DEPLOY_PRE_SECURE_INPUT=0"
set "AEGIS_DEPLOY_POST_COMMAND=fc.exe"
set "AEGIS_DEPLOY_POST_NAME=Verify deployed configuration"
set "AEGIS_DEPLOY_POST_TIMEOUT_SECONDS=30"
set "AEGIS_DEPLOY_POST_SUCCESS_CODES="
set "AEGIS_DEPLOY_POST_SECURE_INPUT=0"
```

Do not inherit other startup actions from older tests. Keep your normal TLS and
enrollment configuration. Run one mockserver on port 50051 at a time; stop it
with Ctrl+C between cases. Keep the common settings in that CMD for all three.

### Case 1 — Check configuration, deploy files, verify deployed content

#### 1. Purpose

Prove the pre-command reads a supporting file from the extracted package, the
main action deploys it, and the post-command compares the deployed bytes.
`${REGION}` is an ordinary parameter, resolved before the pre-command runs.

#### 2. Commands

**MOCKSERVER**, after common preparation:

```cmd
set "AEGIS_DEPLOY_DETECT_COUNT=0"
set "AEGIS_DEPLOY_DETECT_MODE="
set "AEGIS_DEPENDENCY_DETECT_COUNT=0"
set "AEGIS_DEPENDENCY_DETECT_MODE="
set "AEGIS_DEPLOY_NAME=Workflow successful configuration deployment"
set "AEGIS_DEPLOY_PARAMS=REGION=apac"
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisWorkflowInstalled /E /I /Y"
set "AEGIS_DEPLOY_POST_ARGS=/B settings\app.ini C:\temp\AegisWorkflowInstalled\settings\app.ini"
mockserver.exe server
```

**ENDPOINT**, after the terminal action status arrives:

```cmd
type C:\temp\AegisWorkflowInstalled\settings\app.ini
type C:\temp\AegisWorkflowInstalled\readme.txt
```

Copy the actual `package_directory` from the terminal result and run
`dir "<that exact directory>"` on the endpoint.

#### 3. Expected results

- Progress order is `pre-deployment` → `main` → `post-deployment`.
- Terminal status is `ACTION_PHASE_SUCCESS`, exit code `0`.
- Files print `REGION=apac` and `Configuration package`.
- Terminal `package_cleanup` is `removed`; the extracted directory is absent.
- The deployed `C:\temp\AegisWorkflowInstalled` files remain.

### Case 2 — Wrong configuration blocks deployment

#### 1. Purpose

Prove an unmet prerequisite prevents the main command. The package contains
`REGION=apac`; this job deliberately requires `emea`.

#### 2. Commands

**ENDPOINT**, before starting: confirm this test destination is absent.

```cmd
dir C:\temp\AegisWorkflowBlocked
```

If an earlier run left this test directory, remove that exact test directory
before repeating the case. Do not use a destination holding real data.

**MOCKSERVER**, stop the previous server and run in the same prepared CMD:

```cmd
set "AEGIS_DEPLOY_NAME=Workflow prerequisite refusal"
set "AEGIS_DEPLOY_PARAMS=REGION=emea"
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisWorkflowBlocked /E /I /Y"
set "AEGIS_DEPLOY_POST_ARGS=/B settings\app.ini C:\temp\AegisWorkflowBlocked\settings\app.ini"
mockserver.exe server
```

**ENDPOINT**, after terminal status:

```cmd
dir C:\temp\AegisWorkflowBlocked
```

#### 3. Expected results

- Expected terminal status is **FAILED**, exit code `1` from findstr.
- Reason identifies `pre-deployment step 1 (Check configuration region)`.
- Main and post commands do not run; `AegisWorkflowBlocked` is absent.
- Extracted package is removed; terminal `package_cleanup` is `removed`.
- This expected refusal means the test PASSED.

### Case 3 — Post-check failure does not become false success

#### 1. Purpose

Prove the job fails when files copied successfully but an author-required
post-check fails. This job deliberately checks for a `required.ini` that the
package does not contain. A post-check does not automatically undo deployment.

#### 2. Commands

**ENDPOINT**, first confirm this destination is absent:

```cmd
dir C:\temp\AegisWorkflowPostFailed
```

**MOCKSERVER**, stop the previous server and run in the same prepared CMD:

```cmd
set "AEGIS_DEPLOY_NAME=Workflow post-check refusal"
set "AEGIS_DEPLOY_PARAMS=REGION=apac"
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisWorkflowPostFailed /E /I /Y"
set "AEGIS_DEPLOY_POST_ARGS=/B settings\app.ini C:\temp\AegisWorkflowPostFailed\settings\required.ini"
mockserver.exe server
```

**ENDPOINT**, after terminal status:

```cmd
type C:\temp\AegisWorkflowPostFailed\settings\app.ini
dir C:\temp\AegisWorkflowPostFailed\settings\required.ini
```

#### 3. Expected results

- Pre-check and copy run; `app.ini` prints `REGION=apac`.
- `required.ini` is absent, so fc returns exit code `2`.
- Terminal status is **FAILED**, identifying
  `post-deployment step 1 (Verify deployed configuration)`.
- Extracted package is removed; terminal `package_cleanup` is `removed`.
- Deployed files remain, allowing an operator to inspect the failed deployment.
- This expected failure means the test PASSED.

Production server/console implementation details are in
[the workflow contract](softdist-workflow-server-integration.md).


## C. Package dependencies

### Prepare once

**ENDPOINT — Administrator CMD:**

```cmd
mkdir C:\temp\AegisDependencyDemo
```

**MOCKSERVER:** open a NEW CMD from the Start menu, with your normal TLS settings.
Do not reuse the CMD containing pre/post or bundle settings. Copy `dist/mockserver.exe` to `C:\temp\mock`. Set these common job fields:

```cmd
cd /d C:\temp\mock
set "AEGIS_TARGET_DEVICE=b3dea9f7-5df8-4765-ad8b-621307ef5f41"
set "AEGIS_DEPLOY_DETECT_COUNT=0"
set "AEGIS_DEPLOY_DETECT_MODE="
set "AEGIS_DEPENDENCY_DETECT_COUNT=0"
set "AEGIS_DEPENDENCY_DETECT_MODE="
set "AEGIS_DEPLOY_NAME=Dependency main-package test"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C echo MAIN-RAN>C:\temp\AegisDependencyDemo\main-ran.txt"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_TIMEOUT=60"
set "AEGIS_DEPLOY_PLAN_TIMEOUT=180"
set "AEGIS_DEPENDENCY_NAME=Aegis Dependency Demo"
set "AEGIS_DEPENDENCY_RUN_AS=system"
set "AEGIS_DEPENDENCY_DETECTION_SCOPE=machine"
set "AEGIS_DEPENDENCY_UNINSTALL_KEY=HKLM64\AegisDependencyDemo"
set "AEGIS_DEPENDENCY_TIMEOUT=60"
```

The UUID is Vijay's WIN-TUES4V46T7I. Replace it with your actual test device UUID.
Keep this CMD for all three cases. Stop mockserver with Ctrl+C between cases.
Each fresh process permits one configured dependency plan per selected device;
an endpoint reconnect does not automatically replay that plan.

### Case 1 — Missing dependency installs before the main package

#### 1. Purpose

The dependency registration is absent. Agent runs its install command, verifies
that the registration exists, and only then does the server send the main package.

#### 2. Commands

**ENDPOINT — Administrator CMD**, clear only this test's previous state:

```cmd
reg delete HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisDependencyDemo /f /reg:64
del /Q C:\temp\AegisDependencyDemo\main-ran.txt
```

If the key/file was already absent, the “not found” message is expected.

**MOCKSERVER**, after common preparation:

```cmd
set "AEGIS_DEPLOY_PACKAGE_ID=app-demo-r1"
set "AEGIS_DEPENDENCY_ID=runtime-demo-r1"
set "AEGIS_DEPENDENCY_TYPE=command"
set "AEGIS_DEPENDENCY_FILE=reg.exe"
set "AEGIS_DEPENDENCY_ARGS=add HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisDependencyDemo /v DisplayName /t REG_SZ /d "Aegis Dependency Demo" /f /reg:64"
mockserver.exe server
```

**ENDPOINT**, after the final `PLAN ... SUCCESS` line:

```cmd
reg query HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisDependencyDemo /v DisplayName /reg:64
type C:\temp\AegisDependencyDemo\main-ran.txt
```

#### 3. Expected results

- Plan order prints `runtime-demo-r1 -> app-demo-r1`.
- Dependency phase is `ACTION_PHASE_SUCCESS`, after its registration verification.
- The main package is sent afterward and also succeeds; final plan is `SUCCESS`.
- Registry DisplayName is `Aegis Dependency Demo`; the file prints `MAIN-RAN`.

### Case 2 — Installed dependency skips its command and allows main

#### 1. Purpose

Prove installed-product detection skips the dependency's process. We deliberately
give it an `exit 7` command: if it actually executed, the main package would block.

#### 2. Commands

**ENDPOINT — Administrator CMD**, ensure the test registration exists, then clear
the main marker. This also makes the case independent of case 1:

```cmd
reg add HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisDependencyDemo /v DisplayName /t REG_SZ /d "Aegis Dependency Demo" /f /reg:64
del /Q C:\temp\AegisDependencyDemo\main-ran.txt
```

**MOCKSERVER**, stop the previous server and use the same prepared CMD:

```cmd
set "AEGIS_DEPLOY_PACKAGE_ID=app-demo-r2"
set "AEGIS_DEPENDENCY_ID=runtime-demo-r2"
set "AEGIS_DEPENDENCY_TYPE=command"
set "AEGIS_DEPENDENCY_FILE=cmd.exe"
set "AEGIS_DEPENDENCY_ARGS=/D /C exit 7"
mockserver.exe server
```

**ENDPOINT**, after final plan status:

```cmd
type C:\temp\AegisDependencyDemo\main-ran.txt
```

#### 3. Expected results

- Dependency is `ACTION_PHASE_NOT_APPLICABLE`, with an already-installed reason.
- Its `exit 7` command does not run.
- Main package succeeds; final plan is `SUCCESS`; marker prints `MAIN-RAN`.

### Case 3 — Dependency failure prevents the main package

#### 1. Purpose

Prove a failed prerequisite does not let the application install. The registration
is absent and the dependency command deliberately exits 7.

#### 2. Commands

**ENDPOINT — Administrator CMD:**

```cmd
reg delete HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisDependencyDemo /f /reg:64
del /Q C:\temp\AegisDependencyDemo\main-ran.txt
```

**MOCKSERVER**, stop the previous server and use the same prepared CMD:

```cmd
set "AEGIS_DEPLOY_PACKAGE_ID=app-demo-r3"
set "AEGIS_DEPENDENCY_ID=runtime-demo-r3"
set "AEGIS_DEPENDENCY_TYPE=command"
set "AEGIS_DEPENDENCY_FILE=cmd.exe"
set "AEGIS_DEPENDENCY_ARGS=/D /C exit 7"
mockserver.exe server
```

**ENDPOINT**, after final plan status:

```cmd
dir C:\temp\AegisDependencyDemo\main-ran.txt
```

#### 3. Expected results

- Dependency is **FAILED**, exit code `7`.
- Final plan is **FAILED**; reason identifies `runtime-demo-r3`.
- No main-package send line appears; marker file is absent.
- This expected refusal means the test PASSED.

The real-server implementation contract is
[package dependency orchestration](softdist-dependency-server-integration.md).

### Cleanup after testing

**ENDPOINT — Administrator CMD**, remove only the test registration and marker:

```cmd
reg delete HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisDependencyDemo /f /reg:64
del /Q C:\temp\AegisDependencyDemo\main-ran.txt
```

A not-found message means that test item is already clean.


## D. Advanced detection

### Prepare once

**ENDPOINT — Administrator CMD:**

```cmd
mkdir C:\temp\AegisDetectionDemo
```

**MOCKSERVER:** copy the new mockserver into `C:\temp\mock`. Open a NEW CMD from
Start, with the normal TLS settings, rather than reusing a dependency/bundle CMD:

```cmd
cd /d C:\temp\mock
set "AEGIS_TARGET_DEVICE=b3dea9f7-5df8-4765-ad8b-621307ef5f41"
set "AEGIS_DEPLOY_NAME=Advanced detection test"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE=cmd.exe"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_TIMEOUT=60"
set "AEGIS_DEPLOY_VERSION="
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME="
```

Replace the UUID if testing another endpoint. Stay in this CMD for the cases;
Ctrl+C stops mockserver before changing settings. Wait for the intended endpoint's
terminal phase, not just QUEUED/RUNNING.

### Case 1 — File created, verified, then repeat installation skipped

#### 1. Purpose

Prove that a package without MSI/installed-app registration can use its output
file as detection. A repeated job must skip its install command once satisfied.

#### 2. Commands

**ENDPOINT:** clear only this test output; not-found means already clean.

```cmd
del /Q C:\temp\AegisDetectionDemo\installed.txt
```

**MOCKSERVER**, after common preparation:

```cmd
set "AEGIS_DEPLOY_DETECT_COUNT=1"
set "AEGIS_DEPLOY_DETECT_MODE=all"
set "AEGIS_DEPLOY_DETECT_1_KIND=file_exists"
set "AEGIS_DEPLOY_DETECT_1_PATH=C:\temp\AegisDetectionDemo\installed.txt"
set "AEGIS_DEPLOY_ARGS=/D /C echo INSTALLED>C:\temp\AegisDetectionDemo\installed.txt"
mockserver.exe server
```

**ENDPOINT**, after terminal result:

```cmd
type C:\temp\AegisDetectionDemo\installed.txt
```

**MOCKSERVER**, stop the server, then deliberately configure a command that would
fail if executed. Leave the existing endpoint file in place:

```cmd
set "AEGIS_DEPLOY_ARGS=/D /C exit 7"
mockserver.exe server
```

#### 3. Expected results

- First run: `ACTION_PHASE_SUCCESS`, exit `0`, reason includes advanced verification.
- Endpoint file prints `INSTALLED`.
- Repeat: `ACTION_PHASE_NOT_APPLICABLE`, reason says advanced detection satisfied.
  The `exit 7` install command does not execute.

### Case 2 — File presence alone does not satisfy a version rule

#### 1. Purpose

Prove an ordinary text file named `.exe` cannot falsely satisfy a minimum file
version. Then deploy a real versioned Windows binary to satisfy the same rule.

#### 2. Commands

**ENDPOINT:**

```cmd
del /Q C:\temp\AegisDetectionDemo\app.exe
```

**MOCKSERVER**, stop the previous server and use the same CMD:

```cmd
set "AEGIS_DEPLOY_DETECT_COUNT=1"
set "AEGIS_DEPLOY_DETECT_MODE=all"
set "AEGIS_DEPLOY_DETECT_1_KIND=file_version"
set "AEGIS_DEPLOY_DETECT_1_PATH=C:\temp\AegisDetectionDemo\app.exe"
set "AEGIS_DEPLOY_DETECT_1_MIN_VERSION=1.0.0.0"
set "AEGIS_DEPLOY_ARGS=/D /C echo NOT-A-VERSIONED-EXE>C:\temp\AegisDetectionDemo\app.exe"
mockserver.exe server
```

**ENDPOINT**, after terminal result:

```cmd
dir C:\temp\AegisDetectionDemo\app.exe
```

**MOCKSERVER**, stop the server and replace the payload command. This copies the
endpoint's existing Windows CMD binary; it does not replace any Windows file:

```cmd
set "AEGIS_DEPLOY_ARGS=/D /C copy /Y C:\Windows\System32\cmd.exe C:\temp\AegisDetectionDemo\app.exe"
mockserver.exe server
```

#### 3. Expected results

- Text-file run: command exits `0`, file exists, but action is **FAILED** because
  advanced installed detection is not satisfied. This expected refusal PASSES.
- Real-binary run: unversioned existing output does not cause a skip. Copy executes,
  native file-version detection succeeds, action is `ACTION_PHASE_SUCCESS`.

### Case 3 — ALL requires file AND registry; ANY permits either

#### 1. Purpose

Prove a file plus application configuration can be combined. With ALL, one missing
registry value makes post-verification fail. With ANY, the existing file is enough.

#### 2. Commands

**ENDPOINT — Administrator CMD:**

```cmd
reg delete HKLM\SOFTWARE\AegisOne\DetectionDemo /f /reg:64
del /Q C:\temp\AegisDetectionDemo\combined.txt
```

**MOCKSERVER**, stop the server and set both rules. Clear the previous version
setting because the first rule is now file existence:

```cmd
set "AEGIS_DEPLOY_DETECT_COUNT=2"
set "AEGIS_DEPLOY_DETECT_MODE=all"
set "AEGIS_DEPLOY_DETECT_1_KIND=file_exists"
set "AEGIS_DEPLOY_DETECT_1_PATH=C:\temp\AegisDetectionDemo\combined.txt"
set "AEGIS_DEPLOY_DETECT_1_MIN_VERSION="
set "AEGIS_DEPLOY_DETECT_2_KIND=registry_value"
set "AEGIS_DEPLOY_DETECT_2_HIVE=HKLM"
set "AEGIS_DEPLOY_DETECT_2_KEY=SOFTWARE\AegisOne\DetectionDemo"
set "AEGIS_DEPLOY_DETECT_2_VIEW=64"
set "AEGIS_DEPLOY_DETECT_2_VALUE_NAME=Region"
set "AEGIS_DEPLOY_DETECT_2_VALUE_TYPE=string"
set "AEGIS_DEPLOY_DETECT_2_EXPECTED=apac"
set "AEGIS_DEPLOY_ARGS=/D /C echo COMBINED>C:\temp\AegisDetectionDemo\combined.txt"
mockserver.exe server
```

The command creates only the file. After the expected failed verification,
**ENDPOINT**:

```cmd
type C:\temp\AegisDetectionDemo\combined.txt
reg query HKLM\SOFTWARE\AegisOne\DetectionDemo /v Region /reg:64
```

**MOCKSERVER**, stop the server, switch only the combination mode, and use a
command that would fail if it ran. Do not create the registry value yet:

```cmd
set "AEGIS_DEPLOY_DETECT_MODE=any"
set "AEGIS_DEPLOY_ARGS=/D /C exit 7"
mockserver.exe server
```

Finally prove the registry value satisfies ALL too. **ENDPOINT — Administrator CMD:**

```cmd
reg add HKLM\SOFTWARE\AegisOne\DetectionDemo /v Region /t REG_SZ /d apac /f /reg:64
```

**MOCKSERVER**, stop the server and restore ALL; leave `exit 7` configured:

```cmd
set "AEGIS_DEPLOY_DETECT_MODE=all"
mockserver.exe server
```

#### 3. Expected results

- ALL with missing registry value: **FAILED**, exit `0`, verification failed.
  File prints `COMBINED`; registry query reports absent. Expected refusal PASSES.
- ANY with only the file: `ACTION_PHASE_NOT_APPLICABLE`; `exit 7` does not run.
- ALL after adding `Region=apac`: also `ACTION_PHASE_NOT_APPLICABLE`; both rules
  now match. This proves the registry rule reads the configured key/value.

### Cleanup

**ENDPOINT — Administrator CMD**, remove only these test artifacts:

```cmd
reg delete HKLM\SOFTWARE\AegisOne\DetectionDemo /f /reg:64
del /Q C:\temp\AegisDetectionDemo\installed.txt C:\temp\AegisDetectionDemo\app.exe C:\temp\AegisDetectionDemo\combined.txt
```

Production mapping and uninstall semantics:
[advanced detection contract](softdist-detection-server-integration.md).

## E. Real 7-Zip MSI lifecycle

Reported PASS on 27 October: install 26.04, repeat install skip, upgrade to
26.05, uninstall, repeat uninstall skip. Target was WIN-TUES4V46T7I, UUID
`b3dea9f7-5df8-4765-ad8b-621307ef5f41`. Server team: use your test device UUID.

### Prepare once for E and F

Use an endpoint with the LocalSystem agent service and no existing 7-Zip or
Firefox installation. Do not manually run either installer on the endpoint.
On MOCKSERVER, replace `C:\temp\mock\mockserver.exe` from current `/dist` while
stopped. Keep certificates and your existing TLS/enrollment settings.

In the MOCKSERVER CMD, stop any previous mockserver with Ctrl+C, then run these
resets ONCE. They remove old test-job options; replacing an EXE does not clear
the environment of its CMD. These are manual commands, not a launcher script.

```cmd
cd /d C:\temp\mock
mkdir C:\temp\AegisRealTests
set "AEGIS_NSH="
set "AEGIS_NSH_SCRIPT="
set "AEGIS_INSTALL_FILE="
set "AEGIS_UNINSTALL_KB="
set "AEGIS_STAGE_KB="
set "AEGIS_COLLECT_PATHS="
set "AEGIS_REBOOT_DELAY="
set "AEGIS_SCAN_NOW="
set "AEGIS_CANCEL_REBOOT="
set "AEGIS_DEPENDENCY_ID="
set "AEGIS_DEPENDENCY_NAME="
set "AEGIS_DEPENDENCY_FILE="
set "AEGIS_DEPENDENCY_CONTENT_ID="
set "AEGIS_DEPLOY_PRE_COMMAND="
set "AEGIS_DEPLOY_PRE_ARGS="
set "AEGIS_DEPLOY_PRE_NAME="
set "AEGIS_DEPLOY_PRE_TIMEOUT_SECONDS="
set "AEGIS_DEPLOY_PRE_SUCCESS_CODES="
set "AEGIS_DEPLOY_PRE_SECURE_INPUT="
set "AEGIS_DEPLOY_POST_COMMAND="
set "AEGIS_DEPLOY_POST_ARGS="
set "AEGIS_DEPLOY_POST_NAME="
set "AEGIS_DEPLOY_POST_TIMEOUT_SECONDS="
set "AEGIS_DEPLOY_POST_SUCCESS_CODES="
set "AEGIS_DEPLOY_POST_SECURE_INPUT="
set "AEGIS_DEPLOY_BUNDLE_ENTRY="
set "AEGIS_DEPLOY_BUNDLE_COMMAND="
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_SESSION_ID="
set "AEGIS_DEPLOY_SECRETS_FILE="
set "AEGIS_DEPLOY_PARAMS="
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_REQUIRE_SIG="
set "AEGIS_DEPLOY_SUCCESS_CODES="
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_HASH_ALGO=sha256"
set "AEGIS_DEPLOY_AUTO_HASH=1"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_DETECT_COUNT=0"
set "AEGIS_DEPLOY_TIMEOUT=300"
set "AEGIS_TARGET_DEVICE=b3dea9f7-5df8-4765-ad8b-621307ef5f41"
```

An existing test directory is OK. Stay in this CMD for E1-E5. Wait for the
terminal status for your device before Ctrl+C and the next case. Restarting
mockserver while an installer is running can submit `deploy-001` again; the
agent's worker survives a connection loss. Two attempts sharing that ID can then
appear as FAILED followed by RUNNING. QUEUED alone is not a terminal result.

Download both official x64 MSIs to MOCKSERVER:

- [7-Zip 26.04 x64 MSI](https://github.com/ip7z/7zip/releases/download/26.04/7z2604-x64.msi)
  -> `C:\temp\AegisRealTests\7z2604-x64.msi`.
- [7-Zip 26.05 x64 MSI](https://github.com/ip7z/7zip/releases/download/26.05/7z2605-x64.msi)
  -> `C:\temp\AegisRealTests\7z2605-x64.msi`.

### Case 1 - Fresh MSI install and installed-version verification

#### 1. Purpose

Deliver the actual MSI, verify its hash, install silently as SYSTEM, then verify
the product version from installed state.

#### 2. Commands

**MOCKSERVER**, after preparation:

```cmd
set "AEGIS_DEPLOY_PACKAGE_ID=7zip-x64-msi-26.04"
set "AEGIS_DEPLOY_NAME=7-Zip 26.04 real MSI install"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=msi"
set "AEGIS_DEPLOY_CONTENT_ID=7zip-x64-msi-26.04"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisRealTests\7z2604-x64.msi"
set "AEGIS_DEPLOY_DISPLAY_NAME=7-Zip"
set "AEGIS_DEPLOY_VERSION=26.04"
set "AEGIS_DEPLOY_ARGS=/qn /norestart ALLUSERS=1 /L*v C:\Windows\Temp\Aegis-7zip-install.log"
mockserver.exe server
```

**ENDPOINT - CMD**, after terminal success:

```cmd
dir "C:\Program Files\7-Zip\7z.exe"
"C:\Program Files\7-Zip\7z.exe" | findstr /C:"7-Zip 26.04"
```

#### 3. Expected results

- Terminal `ACTION_PHASE_SUCCESS`, exit `0`; verified installed version
  `26.04.00.0` in the observed run, no restart required.
- `7z.exe` exists and prints the 26.04 version line.
- `/qn` means no UI; `/norestart` prevents automatic restart; `ALLUSERS=1`
  requests a machine installation. `/L*v` writes the detailed MSI log on the
  ENDPOINT, not on mockserver.

### Case 2 - Repeat install skips an already installed version

#### 1. Purpose

Prove the same installed version prevents installer execution.

#### 2. Commands

**MOCKSERVER**, after Case 1 completes: Ctrl+C, then in the same CMD, without
changing any settings:

```cmd
mockserver.exe server
```

**ENDPOINT:** no command required; 26.04 remains installed.

#### 3. Expected results

`ACTION_PHASE_NOT_APPLICABLE` with
`already installed in the selected execution scope (skipped)`.
The current Run-As flow can fetch the MSI before worker-side detection; a
download here does not mean the installer ran. Avoiding that extra fetch remains
an optimization, not something this release claims to fix.

### Case 3 - Upgrade 26.04 to 26.05

#### 1. Purpose

An older installed version must not skip a newer requested version. Leave 26.04
installed; the new vendor MSI performs the upgrade.

#### 2. Commands

**MOCKSERVER:** Ctrl+C, then in the same prepared CMD:

```cmd
set "AEGIS_DEPLOY_PACKAGE_ID=7zip-x64-msi-26.05"
set "AEGIS_DEPLOY_NAME=7-Zip upgrade 26.04 to 26.05"
set "AEGIS_DEPLOY_CONTENT_ID=7zip-x64-msi-26.05"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisRealTests\7z2605-x64.msi"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_VERSION=26.05"
set "AEGIS_DEPLOY_ARGS=/qn /norestart ALLUSERS=1 /L*v C:\Windows\Temp\Aegis-7zip-upgrade.log"
mockserver.exe server
```

**ENDPOINT - CMD**, after terminal success:

```cmd
"C:\Program Files\7-Zip\7z.exe" | findstr /C:"7-Zip 26.05"
```

#### 3. Expected results

`ACTION_PHASE_SUCCESS`, exit `0`, verified installed version `26.05.00.0` in the
observed run. The command prints 26.05. It must not skip because 26.04 existed.
The action stays `install`; higher requested version plus vendor MSI supplies
the upgrade behavior.

### Case 4 - Uninstall through the installed registry registration

#### 1. Purpose

Find the exact installed 26.05 registration, uninstall without downloading an
MSI, and verify the product was removed.

#### 2. Commands

**MOCKSERVER:** Ctrl+C, then in the same CMD:

```cmd
set "AEGIS_DEPLOY_NAME=7-Zip 26.05 uninstall"
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_CONTENT_ID="
set "AEGIS_SERVE_SOFTWARE="
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME=7-Zip"
set "AEGIS_DEPLOY_VERSION=26.05.00.0"
set "AEGIS_DEPLOY_ARGS=/qn /norestart /L*v C:\Windows\Temp\Aegis-7zip-uninstall.log"
mockserver.exe server
```

**ENDPOINT - CMD**, after terminal success:

```cmd
if exist "C:\Program Files\7-Zip\7z.exe" (echo FAIL: 7z.exe still exists) else (echo PASS: 7z.exe removed)
```

#### 3. Expected results

- Agent resolves `7-Zip 26.05 (x64 edition)` in HKLM64. The observed product key
  was `{23170F69-40C1-2702-2605-000001000000}`; the test does not require typing it.
- `ACTION_PHASE_SUCCESS`, exit `0`, reason includes `verified removed`.
- No installer fetch is needed. `7z.exe removed` prints. A leftover settings
  folder alone is not proof that the product is still installed.

### Case 5 - Repeat uninstall skips an absent product

#### 1. Purpose

Prove an already removed application does not run an uninstaller again.

#### 2. Commands

**MOCKSERVER:** after Case 4 completes, Ctrl+C; same CMD, unchanged settings:

```cmd
mockserver.exe server
```

**ENDPOINT:** no command required.

#### 3. Expected results

`ACTION_PHASE_NOT_APPLICABLE`, exit `0`, reason
`not installed in the selected execution scope (nothing to uninstall)`.
No download or uninstaller execution.

## F. Real Firefox installer and configuration

### Case 1 - Deliver EXE and JSON, apply configuration, verify active policy

#### 1. Purpose

One package carries the real Firefox ESR x64 EXE and `distribution\policies.json`.
The main action installs Firefox; the post-command copies the config. Verify
Firefox actually applies the policy, not just that the EXE exists. Vijay reports
this case passed on 27 October after the recovery below; installed version in
the supplied log was `153.6.0`.

#### 2. Commands

Use the E preparation/reset first (or continue in the same CMD after E5).
For the first run Firefox must be absent on the endpoint. Keep this real test
away from an existing managed Firefox installation or policies.

**MOCKSERVER - CMD:**

```cmd
mkdir C:\temp\AegisRealTests\FirefoxPackage\distribution
```

From [Mozilla's ESR downloads](https://www.firefox.com/en-US/download/all/desktop-esr/),
choose Windows 64-bit EXE full installer. Save/rename it as
`C:\temp\AegisRealTests\FirefoxPackage\FirefoxSetup.exe`. Record the downloaded
version for your own run; a later ESR may differ from the accepted run above.

In Notepad paste the following and Save As **All Files, UTF-8** to
`C:\temp\AegisRealTests\FirefoxPackage\distribution\policies.json`.
Check the name is not `policies.json.txt`. This is application config, not a
test script:

```json
{
  "policies": {
    "Homepage": {
      "URL": "https://example.org",
      "Locked": true
    }
  }
}
```

**MOCKSERVER - CMD**, after stopping the previous server:

```cmd
cd /d C:\temp\mock
dir C:\temp\AegisRealTests\FirefoxPackage\FirefoxSetup.exe
type C:\temp\AegisRealTests\FirefoxPackage\distribution\policies.json
mockserver.exe pack C:\temp\AegisRealTests\FirefoxPackage C:\temp\AegisRealTests\FirefoxPackage.zip
set "AEGIS_DEPLOY_PACKAGE_ID=firefox-esr-config-v1"
set "AEGIS_DEPLOY_NAME=Firefox ESR with homepage configuration"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=exe"
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_CONTENT_ID=firefox-esr-config-v1"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisRealTests\FirefoxPackage.zip"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_HASH_ALGO=sha256"
set "AEGIS_DEPLOY_AUTO_HASH=1"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_DETECT_COUNT=0"
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME=Mozilla Firefox"
set "AEGIS_DEPLOY_VERSION="
set "AEGIS_DEPLOY_ARGS=/S"
set "AEGIS_DEPLOY_TIMEOUT=600"
set "AEGIS_DEPLOY_BUNDLE_ENTRY=FirefoxSetup.exe"
set "AEGIS_DEPLOY_BUNDLE_COMMAND="
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_POST_COMMAND=xcopy.exe"
set "AEGIS_DEPLOY_POST_ARGS=distribution "C:\Program Files\Mozilla Firefox\distribution" /E /I /Y"
set "AEGIS_DEPLOY_POST_NAME=Apply Firefox configuration"
set "AEGIS_DEPLOY_POST_TIMEOUT_SECONDS=60"
set "AEGIS_DEPLOY_POST_SUCCESS_CODES=0"
set "AEGIS_DEPLOY_POST_SECURE_INPUT=0"
mockserver.exe server
```

`BUNDLE_ENTRY` selects the EXE inside the extracted ZIP. `/S` silently installs
Firefox. The post-command is `xcopy.exe`: its source `distribution` is relative
to the extracted package directory; the quoted destination is on the endpoint.
`/E` includes subfolders, `/I` treats a new destination as a directory, and `/Y`
suppresses overwrite confirmation. Arguments alone do not define a post-command.

**Before marking PASS**, confirm the server sends
`type: software_deploy_workflow`, followed by main and
`post-deployment step 1: Apply Firefox configuration` progress.

**ENDPOINT - CMD**, after terminal success:

```cmd
dir "C:\Program Files\Mozilla Firefox\firefox.exe"
type "C:\Program Files\Mozilla Firefox\distribution\policies.json"
```

Close all Firefox windows, open Firefox on the endpoint, and type
`about:policies` in the address bar. Inspect Active and Errors. In Settings >
Home, check the managed homepage value.

Copy the actual `package_directory` from the result; on ENDPOINT run:

```cmd
dir "<paste the exact package_directory here>"
```

#### 3. Expected results

- Terminal `ACTION_PHASE_SUCCESS`, workflow steps completed, Firefox installed
  verification when DISPLAY_NAME is set, post-copy successful.
- Both files exist; JSON prints exactly the configured URL and Locked value.
- `about:policies` Active lists Homepage with `https://example.org` and
  `Locked: true`; no error for this policy. The homepage setting is managed.
  An external page loading is not required to prove policy activation.
- Terminal `package_cleanup: removed`; the extracted package directory is gone.
  Installed Firefox and `distribution\policies.json` remain.

#### Recovery used in the accepted run - install succeeded but JSON was missing

The first run installed Firefox and cleaned staging, but had no post-step
progress; it was not a complete workflow acceptance. Replacing mockserver from
`/dist` AND explicitly setting POST_COMMAND/ARGS together resolved the issue.
Those changes were made together, so old binary versus missing settings was
not isolated. Do not claim either as the proven root cause.

On MOCKSERVER stop the server, replace the EXE from current `/dist`, and retain
the Firefox package settings above. Re-enter the six POST settings above. Since
Firefox is now installed, deliberately clear its legacy selector for this
controlled reinstall/config recovery:

```cmd
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_VERSION="
set "AEGIS_DEPLOY_DETECT_COUNT=0"
mockserver.exe server
```

This reruns the EXE and post-copy; it is NOT a config-only update or proof of
automatic installed-product verification. Repeat the endpoint file and active
policy checks above. Vijay confirmed the JSON and then reported all OK. A
config-only update without reinstall is next, still pending. Do not leave this
selector-cleared recovery configuration as a production detection policy.

### Troubleshooting the accepted real cases

- `installer bundle needs entry_point and no bundle.command`: old bundle options
  remained in CMD during a plain MSI test. Run E's explicit bundle reset. No
  installer was sent by that rejected configuration.
- `installer output suppressed for private deployment` is informational. Current
  explicit Run-As contexts, including SYSTEM, suppress diagnostic output to the
  server. For E's non-secret packages the explicit `/L*v` creates a local MSI
  log. Do not add verbose logging to a package carrying credentials/secrets.
- The first 7-Zip attempt timed out after five minutes. It had also been
  resubmitted during a server restart. Later successful runs used explicit MSI
  logging and default install path; the original timeout cause was not isolated.
  This release does not claim a timeout fix or duplicate-submission fix.
- CAB scan errors are separate patch reconciliation actions. Scans do not take
  the SoftDist machine-install gate. A patch INSTALL can hold that gate; the new
  diagnostic agent logs an explicit wait. Do not infer a CAB scan blocked an MSI
  merely from QUEUED.
- For an E failure, on ENDPOINT use the relevant log filename:

```cmd
powershell -NoProfile -Command "Get-Content 'C:\Windows\Temp\Aegis-7zip-install.log' -Tail 60"
powershell -NoProfile -Command "Get-Content 'C:\Windows\Temp\Aegis-7zip-upgrade.log' -Tail 60"
powershell -NoProfile -Command "Get-Content 'C:\Windows\Temp\Aegis-7zip-uninstall.log' -Tail 60"
```

### Remaining acceptance after E and F

Next: ordinary parameters/variables and an installed-app config-only update
without reinstall. Then real runtime dependencies and failure/reboot handling;
MST transforms remain untested. Automatic JSON template substitution is not
implemented: current substitution applies to command arguments. Production
server/API/UI/vault and durable orchestration remain server-team integration.
SoftDist stays the priority before the other module tracks.

## Related fix included

Explicit CMD arguments retain author quotes around paths containing spaces,
including redirection. Native regression tests cover this fix. No extra test
launcher or generated probe EXE is required.

The diagnostic agent additionally logs queue handoff, worker pickup, install-gate
wait/acquisition and execution stages without credentials or installer arguments.
Waiting for another patch/software installer reports QUEUED with an explicit
reason; RUNNING starts after the machine gate is acquired. A native regression
test holds that gate and checks status ordering. This is diagnostic visibility,
not a claimed fix for the original vendor MSI timeout.


## G. Rollback-on-failure orchestration (new block, 28 October 2026)

This block exercises the signed `SoftwareDeploymentSteps.rollback` field. The
agent runs the rollback steps IN ORDER when the main action or any pre/post
step fails. Rollback is best-effort: a failing rollback step does NOT stop the
remaining rollback commands — the admin's signed sequence is executed in full.

### Prepare once

MOCKSERVER - in a fresh CMD with your normal TLS settings:

```cmd
cd /d C:\temp\mock
if not exist C:\temp\AegisBundleDemo-src\settings mkdir C:\temp\AegisBundleDemo-src\settings
echo REGION=apac>C:\temp\AegisBundleDemo-src\settings\app.ini
echo Rollback sample bundle>C:\temp\AegisBundleDemo-src\readme.txt
if exist C:\temp\AegisRollbackBundle.zip del C:\temp\AegisRollbackBundle.zip
mockserver.exe pack C:\temp\AegisBundleDemo-src C:\temp\AegisRollbackBundle.zip
set "AEGIS_DEPLOY_FILE=C:\temp\AegisRollbackBundle.zip"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_REQUIRE_SIG=0"
set "AEGIS_DEPLOY_TIMEOUT=60"
set "AEGIS_DEPLOY_BUNDLE_ENTRY="
set "AEGIS_DEPLOY_BUNDLE_COMMAND=xcopy.exe"
set "AEGIS_DEPLOY_BUNDLE_DEST="
set "AEGIS_DEPLOY_BUNDLE_CLEANUP="
set "AEGIS_DEPLOY_PRE_COMMAND="
set "AEGIS_DEPLOY_PRE_ARGS="
set "AEGIS_DEPLOY_PRE_NAME="
set "AEGIS_DEPLOY_PRE_TIMEOUT_SECONDS="
set "AEGIS_DEPLOY_POST_COMMAND="
set "AEGIS_DEPLOY_POST_ARGS="
set "AEGIS_DEPLOY_POST_NAME="
set "AEGIS_DEPLOY_POST_TIMEOUT_SECONDS="
set "AEGIS_DEPLOY_ROLLBACK_COMMAND="
set "AEGIS_DEPLOY_ROLLBACK_ARGS="
set "AEGIS_DEPLOY_ROLLBACK_NAME="
set "AEGIS_DEPLOY_ROLLBACK_TIMEOUT_SECONDS="
set "AEGIS_DEPLOY_DETECT_COUNT=0"
set "AEGIS_DEPLOY_DETECT_MODE="
set "AEGIS_DEPENDENCY_DETECT_COUNT=0"
set "AEGIS_DEPENDENCY_DETECT_MODE="
set "AEGIS_DEPENDENCY_ID="
set "AEGIS_DEPENDENCY_NAME="
set "AEGIS_NSH="
```

### Case G1 — Main action fails: rollback runs and leaves a marker

#### 1. Purpose

Prove that when the main `xcopy` deliberately targets an invalid flag so it
fails, the agent executes the rollback command. The rollback writes a marker
file so the ENDPOINT check does not depend on log parsing.

#### 2. Commands

**ENDPOINT**, before the run:

```cmd
if exist C:\temp\AegisRollbackMarker.txt del C:\temp\AegisRollbackMarker.txt
```

**MOCKSERVER**, after the common preparation:

```cmd
set "AEGIS_DEPLOY_NAME=Rollback-on-main-failure"
set "AEGIS_DEPLOY_ARGS=/this-flag-does-not-exist"
set "AEGIS_DEPLOY_ROLLBACK_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ROLLBACK_ARGS=/D /C echo rollback-fired > C:\temp\AegisRollbackMarker.txt"
set "AEGIS_DEPLOY_ROLLBACK_NAME=Write rollback marker file"
set "AEGIS_DEPLOY_ROLLBACK_TIMEOUT_SECONDS=30"
mockserver.exe server
```

**ENDPOINT**, after the terminal action status arrives:

```cmd
type C:\temp\AegisRollbackMarker.txt
```

#### 3. Expected results

- Terminal status is `ACTION_PHASE_FAILED` with a reason that includes
  `rollback after main failure completed (1 step(s))`.
- `C:\temp\AegisRollbackMarker.txt` exists and contains `rollback-fired`.
- The package extraction directory is removed.

### Case G2 — Rollback itself has a failing step: later rollback commands still run

#### 1. Purpose

Prove rollback is best-effort. The first rollback step is deliberately broken;
the second must still run and leave its marker. The final reason must call
out the first rollback failure explicitly.

#### 2. Commands

**ENDPOINT**, before the run:

```cmd
if exist C:\temp\AegisRollbackSecondMarker.txt del C:\temp\AegisRollbackSecondMarker.txt
```

**MOCKSERVER**, after the common preparation:

```cmd
set "AEGIS_DEPLOY_NAME=Partial-rollback-visibility"
set "AEGIS_DEPLOY_ARGS=/this-flag-does-not-exist"
set "AEGIS_DEPLOY_ROLLBACK_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ROLLBACK_ARGS=/D /C exit 9"
set "AEGIS_DEPLOY_ROLLBACK_NAME=Rollback step that fails"
set "AEGIS_DEPLOY_ROLLBACK_TIMEOUT_SECONDS=30"
mockserver.exe server
```

> This mock exposes ONE rollback command for readable manual cases. The real
> protocol accepts up to eight rollback steps per workflow; the agent native
> regression tests cover the partial-rollback semantics across multiple
> rollback steps (`TestRollbackContinuesPastFailure`,
> `TestRollbackRunsOnMainFailure`,
> `TestRollbackRunsOnPostFailure`).

#### 3. Expected results

- Terminal status is `ACTION_PHASE_FAILED`.
- Reason mentions `rollback after main failure partial — rollback step 1
  (Rollback step that fails) exit 9` so the operator sees the partial state.
- Package directory removed; agent did not re-queue the job.

### Case G3 — Successful deployment skips rollback

#### 1. Purpose

Prove rollback is NOT executed when the main deployment succeeds, so a healthy
install never undoes itself.

#### 2. Commands

**ENDPOINT**, before the run:

```cmd
if exist C:\temp\AegisRollbackMustNotFire.txt del C:\temp\AegisRollbackMustNotFire.txt
if exist C:\temp\AegisWorkflowSuccess rmdir /S /Q C:\temp\AegisWorkflowSuccess
```

**MOCKSERVER**, after the common preparation:

```cmd
set "AEGIS_DEPLOY_NAME=Success-skips-rollback"
set "AEGIS_DEPLOY_ARGS=. C:\temp\AegisWorkflowSuccess /E /I /Y"
set "AEGIS_DEPLOY_ROLLBACK_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ROLLBACK_ARGS=/D /C echo rollback-should-not-fire > C:\temp\AegisRollbackMustNotFire.txt"
set "AEGIS_DEPLOY_ROLLBACK_NAME=Must not fire"
set "AEGIS_DEPLOY_ROLLBACK_TIMEOUT_SECONDS=30"
mockserver.exe server
```

**ENDPOINT**, after the terminal action status arrives:

```cmd
dir C:\temp\AegisRollbackMustNotFire.txt
dir C:\temp\AegisWorkflowSuccess
```

#### 3. Expected results

- Terminal status is `ACTION_PHASE_SUCCESS`.
- `C:\temp\AegisRollbackMustNotFire.txt` DOES NOT exist (rollback skipped).
- `C:\temp\AegisWorkflowSuccess` was created by the main xcopy.


## H. Crash recovery and durable journal (new block, 28 October 2026)

The agent writes a per-job state file to `C:\Program Files\AegisOne\Client\
SKVC\jobs\<command_id>.state` and deletes it on terminal finish. A journal
record that outlives the agent process is reported as `ACTION_PHASE_FAILED`
with reason `agent restarted while <state>; result unknown — not retried
automatically` the next time the agent starts, and any orphaned package
extraction directory under `<download_dir>/packages/` is removed.

### Case H1 — Agent restart during install is reported honestly

#### 1. Purpose

Prove in-flight state survives a service restart as an honest failure signal.
The console learns the install did NOT reach a terminal result; it is NOT
silently retried.

#### 2. Commands

**ENDPOINT**, before the run:

```cmd
if exist "C:\Program Files\AegisOne\Client\SKVC\jobs\recovery-demo.state" del "C:\Program Files\AegisOne\Client\SKVC\jobs\recovery-demo.state"
```

Create a FAKE in-flight record so the recovery sweep sees it on next start.
Using PowerShell for the here-doc write (JSON content, UTF-8, no BOM required):

```cmd
powershell -NoProfile -Command "@{command_id='recovery-demo'; device_id='test'; name='Recovery demo'; action='install'; state='running'; started_at=1709999999; last_updated=1709999999} | ConvertTo-Json | Set-Content -Encoding ASCII 'C:\Program Files\AegisOne\Client\SKVC\jobs\recovery-demo.state'"
```

Then restart the agent service:

```cmd
sc stop AegisOneAgent
sc start AegisOneAgent
```

**MOCKSERVER**, keep running (or restart it; the ActionStatus is buffered
until the next connect so the order does not matter).

#### 3. Expected results

- Mock prints an ActionStatus for `action_id = recovery-demo` with phase
  `ACTION_PHASE_FAILED` and reason starting
  `agent restarted while running; result unknown — not retried automatically`.
- The journal file at `C:\Program Files\AegisOne\Client\SKVC\jobs\
  recovery-demo.state` is deleted.

### Case H2 — Orphan package directory from an interrupted bundle is removed

#### 1. Purpose

Prove orphaned extraction directories do not leak to disk after an
interrupted bundle deploy — the recovery sweep removes them.

#### 2. Commands

**ENDPOINT**, before the run:

```cmd
set "AEGIS_PACKAGES_ROOT=C:\Program Files\AegisOne\Client\SKVC\download\packages"
if not exist "%AEGIS_PACKAGES_ROOT%" mkdir "%AEGIS_PACKAGES_ROOT%"
mkdir "%AEGIS_PACKAGES_ROOT%\aegis-package-recovery-h2"
echo leftover>"%AEGIS_PACKAGES_ROOT%\aegis-package-recovery-h2\leftover.txt"
powershell -NoProfile -Command "@{command_id='recovery-h2'; device_id='test'; name='Recovery H2'; action='install'; state='extracting'; package_dir='C:\Program Files\AegisOne\Client\SKVC\download\packages\aegis-package-recovery-h2'; started_at=1709999999; last_updated=1709999999} | ConvertTo-Json | Set-Content -Encoding ASCII 'C:\Program Files\AegisOne\Client\SKVC\jobs\recovery-h2.state'"
sc stop AegisOneAgent
sc start AegisOneAgent
```

#### 3. Expected results

- The directory `C:\Program Files\AegisOne\Client\SKVC\download\packages\
  aegis-package-recovery-h2` and its contents are gone.
- Mock shows ActionStatus for `recovery-h2` with `package_cleanup = removed`.
- Journal file deleted.


## I. Descendant termination on timeout (new block, 28 October 2026)

The agent wraps each installer in a Windows Job Object. On timeout or kill,
every descendant process dies with the parent; a QUEUED patch install, a
scanning child, or an installer helper cannot be left behind. The agent
reports a descendant hint in the failure reason so a timeout root cause can
be isolated: `; 2 descendant process(es) killed with parent` vs
`; no descendants were alive at kill (parent process was likely stuck)`.

### Case I1 — Timeout reports descendant count honestly

#### 1. Purpose

Prove the agent kills every descendant on timeout AND reports the live child
count in the terminal status — the Firefox / MSI acceptance gap flagged as
"timeout root cause not isolated" is now observable.

#### 2. Commands

**MOCKSERVER**, after Case G1's common preparation (bundle re-used):

```cmd
set "AEGIS_DEPLOY_NAME=Descendant kill with diagnostic"
set "AEGIS_DEPLOY_BUNDLE_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C ping -n 30 127.0.0.1 ^> nul"
set "AEGIS_DEPLOY_TIMEOUT=5"
set "AEGIS_DEPLOY_ROLLBACK_COMMAND="
mockserver.exe server
```

**ENDPOINT**, during the five-second window, confirm `ping.exe` runs under
`cmd.exe` under the agent service:

```cmd
tasklist /FI "IMAGENAME eq ping.exe"
```

#### 3. Expected results

- Terminal status is `ACTION_PHASE_FAILED` after ~5 seconds.
- Reason includes `installer timed out after 5s` and either:
  - `; 1 descendant process(es) killed with parent (child installer helpers
    were still running)` — when the ping child is still alive at kill, or
  - `; no descendants were alive at kill (parent process was likely stuck)` —
    when the kernel race has already reaped the ping child; retry if needed.
- After the status, `tasklist /FI "IMAGENAME eq ping.exe"` returns no rows.


## J. Reboot-sensitive + MSI/MST + config-only (PRIMARY FOCUS 28 October 2026)

This block is the remaining real-vendor matrix after E (7-Zip) and F (Firefox):
3010 reboot-required handling, MSI + MST transform acceptance, and a
config-only parameter update that does NOT reinstall an already-present
product. Each case names a concrete sample package so no extra inputs are
needed to begin running.

### Preflight — one-time endpoint setup

**ENDPOINT**, as the LocalSystem agent will see it:

1. **7-Zip MSI** for J1 harness and J2 transform. Download the 7-Zip MSI
   (`7z<version>-x64.msi`) from <https://www.7-zip.org/download.html>. For
   this guide the server team can reuse the exact file used in Block E.
2. **ORCA** for authoring the MST in J2. ORCA ships with the Windows SDK
   (`Microsoft Windows SDK/bin/x64/Orca-x86_en-us.msi`) or install from
   <https://learn.microsoft.com/en-us/windows/win32/msi/platform-sdk-components-for-windows-installer-developers>.
3. **PsExec** optional for running MSI logs under a specific user if you
   need to retest Run-As from §I. Not required for J1/J2/J3.

### Case J1 — 3010 reboot-required is accepted as SUCCESS + reboot_required

#### 1. Purpose

Prove the agent accepts `ERROR_SUCCESS_REBOOT_REQUIRED` (exit code 3010) as a
successful install and surfaces `reboot_required = true` without re-queuing
the install or marking it FAILED. The exact deterministic signal (exit 3010)
is produced by `cmd /C exit 3010` so the behavior under test is the agent's
3010 handling, not whether a specific vendor's installer requires reboot on
this particular VM today.

#### 2. Commands

**MOCKSERVER**, after common preparation (clear PRE/POST/ROLLBACK/dependency
envs from the top of the guide, keep TLS):

```cmd
if not exist C:\temp\AegisBundleDemo-src\settings mkdir C:\temp\AegisBundleDemo-src\settings
echo REGION=apac>C:\temp\AegisBundleDemo-src\settings\app.ini
echo J1 bundle>C:\temp\AegisBundleDemo-src\readme.txt
if exist C:\temp\AegisJ1Bundle.zip del C:\temp\AegisJ1Bundle.zip
mockserver.exe pack C:\temp\AegisBundleDemo-src C:\temp\AegisJ1Bundle.zip

set "AEGIS_DEPLOY_FILE=C:\temp\AegisJ1Bundle.zip"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_NAME=J1 - deterministic 3010 handling"
set "AEGIS_DEPLOY_BUNDLE_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C exit 3010"
set "AEGIS_DEPLOY_TIMEOUT=60"
mockserver.exe server
```

#### 3. Expected results

- Terminal status is `ACTION_PHASE_SUCCESS` with `exit_code = 3010` and
  `reboot_required = true`. The reason mentions `restart required`.
- The agent does NOT re-queue the signed command (no second RUNNING for the
  same action_id).
- The package extraction directory is removed (default cleanup for a
  non-custom destination).

> OPTIONAL real-vendor variant: replace the main command with
> `AEGIS_DEPLOY_BUNDLE_COMMAND=vc_redist.x64.exe` plus `AEGIS_DEPLOY_ARGS=
> /quiet /norestart` after copying `vc_redist.x64.exe` into
> `C:\temp\AegisBundleDemo-src`. Microsoft's Visual C++ 2015-2022
> Redistributable returns 3010 only when a reboot is already queued on the
> endpoint; its 0 return is also an accepted PASS. The deterministic case
> above remains the acceptance test for the agent's 3010 policy.

### Case J2 — 7-Zip MSI + MST transform acceptance

#### 1. Purpose

Prove the agent runs an MSI with an MST transform packaged alongside it in the
same signed bundle. The transform changes the install directory to a
non-default path so the test does not depend on the vendor logging the
transform id itself.

#### 2. Author a tiny MST once (ONE-TIME, on MOCKSERVER)

With `7z<version>-x64.msi` already downloaded:

1. Open ORCA, `File > Open > 7z<version>-x64.msi`.
2. `Transform > New Transform`.
3. In the `Property` table, locate `INSTALLDIR` (or `TARGETDIR`) and change
   its value to `C:\7ZipMST`.
4. `Transform > Generate Transform` and save as
   `C:\temp\7zip-installdir.mst`.
5. Close ORCA without saving over the MSI.

#### 3. Commands

**ENDPOINT**, before the run (if 7-Zip is already installed from block E,
uninstall it first so the transform's install directory can be observed):

```cmd
wmic product where "Name like '7-Zip%%'" call uninstall /nointeractive
rmdir /S /Q C:\7ZipMST
```

**MOCKSERVER**, after common preparation:

```cmd
mkdir C:\temp\AegisJ2-src
copy C:\path\to\7z<version>-x64.msi C:\temp\AegisJ2-src\7zip.msi
copy C:\temp\7zip-installdir.mst C:\temp\AegisJ2-src\
if exist C:\temp\AegisJ2Bundle.zip del C:\temp\AegisJ2Bundle.zip
mockserver.exe pack C:\temp\AegisJ2-src C:\temp\AegisJ2Bundle.zip

set "AEGIS_DEPLOY_FILE=C:\temp\AegisJ2Bundle.zip"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_NAME=J2 - 7-Zip MSI with MST transform"
set "AEGIS_DEPLOY_BUNDLE_COMMAND=msiexec.exe"
set "AEGIS_DEPLOY_ARGS=/i 7zip.msi TRANSFORMS=7zip-installdir.mst /qn /norestart /L*v C:\Windows\Temp\Aegis-7zip-mst.log"
set "AEGIS_DEPLOY_TIMEOUT=600"
mockserver.exe server
```

**ENDPOINT**, after terminal status:

```cmd
dir C:\7ZipMST
powershell -NoProfile -Command "Get-Content 'C:\Windows\Temp\Aegis-7zip-mst.log' -Tail 80"
```

#### 4. Expected results

- Terminal status is `ACTION_PHASE_SUCCESS` with `exit_code = 0`.
- `C:\7ZipMST` exists and contains `7-Zip.chm`, `7z.exe` and sibling files -
  the transform took effect; without the transform, 7-Zip installs under
  `C:\Program Files\7-Zip`.
- `Aegis-7zip-mst.log` tail shows `TRANSFORMS=` set and the install actions
  running under the transformed INSTALLDIR path.
- The package extraction directory is removed.

### Case J3 — Config-only parameter update without reinstall

#### 1. Purpose

Prove that a signed `parameters` map carrying a configuration-only update
flows to a post-step that writes/updates a product's config file for an
already-installed product, WITHOUT reinstalling the main package. The main
action is a deliberate no-op (`cmd /C echo config-only ...`) that only runs
through the workflow so the post-step applies the parameter. This exercises
the parameter substitution + post-step pipeline; it is NOT a product_code
detection skip test (ProductCode detection short-circuits before any workflow
step runs, so a config-only update must drive through the main action).

#### 2. Commands

**ENDPOINT**, before the run:

```cmd
if not exist C:\ProgramData\AegisVendor mkdir C:\ProgramData\AegisVendor
if exist C:\ProgramData\AegisVendor\settings.ini del C:\ProgramData\AegisVendor\settings.ini
```

**MOCKSERVER**, after common preparation (make sure
`AEGIS_DEPLOY_PRODUCT_CODE` is empty so the main workflow runs; the post-step
is where the config change lives):

```cmd
if not exist C:\temp\AegisJ3-src mkdir C:\temp\AegisJ3-src
echo J3 bundle>C:\temp\AegisJ3-src\readme.txt
if exist C:\temp\AegisJ3Bundle.zip del C:\temp\AegisJ3Bundle.zip
mockserver.exe pack C:\temp\AegisJ3-src C:\temp\AegisJ3Bundle.zip

set "AEGIS_DEPLOY_FILE=C:\temp\AegisJ3Bundle.zip"
set "AEGIS_DEPLOY_HASH="
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_NAME=J3 - config-only parameter update"
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_PARAMS=REGION=emea,CONFIG_PATH=C:\ProgramData\AegisVendor\settings.ini"
set "AEGIS_DEPLOY_BUNDLE_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C echo config-only run on %COMPUTERNAME%"
set "AEGIS_DEPLOY_TIMEOUT=60"
set "AEGIS_DEPLOY_POST_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_POST_ARGS=/D /C echo REGION=${REGION}> ${CONFIG_PATH}"
set "AEGIS_DEPLOY_POST_NAME=Apply config-only update"
set "AEGIS_DEPLOY_POST_TIMEOUT_SECONDS=30"
mockserver.exe server
```

**ENDPOINT**, after terminal status:

```cmd
type C:\ProgramData\AegisVendor\settings.ini
```

#### 3. Expected results

- Terminal status is `ACTION_PHASE_SUCCESS`, exit code 0.
- `C:\ProgramData\AegisVendor\settings.ini` exists and contains `REGION=emea`.
- Progress order was `main` -> `post-deployment`. The post-step consumed the
  substituted path and value from the signed parameters.
- Package directory removed.

> Note: config-only updates that must bypass the main action entirely require
> product_code to be absent (so the ProductCode gate does not short-circuit)
> and a benign main command that always succeeds. Driving the parameter
> change through the post-step is the recommended production pattern because
> post-step failures appear separately from main-install failures in the
> console.

### J block run record

2026-10-29 (Vijay VM run on WIN-TUES4V46T7I, device_id b3dea9f7-5df8-4765-ad8b-621307ef5f41):

- **J1 PASS** — `cmd /D /C exit 3010` returned
  `ACTION_PHASE_SUCCESS exit_code=3010 reboot_required=true reason="installed — RESTART REQUIRED to complete"`.
  Mock did not re-queue the signed command.
- **J2 PASS** — 7-Zip MSI with INSTALLDIR=C:\7ZipMST transform. `C:\7ZipMST`
  on the endpoint lists `7z.exe`, `7z.dll`, `7zFM.exe`, `Lang\`, etc. with
  LastWriteTime at the J2 run (20:08 PM). Agent delivered the bundle via
  content_id, msiexec applied the transform, terminal status
  `ACTION_PHASE_SUCCESS exit_code=0 cleanup=removed`.
- **J3 PASS** — After pre-creating `C:\ProgramData\AegisVendor` on the
  endpoint, the POST step consumed resolved `${REGION}` and `${CONFIG_PATH}`
  parameters and wrote `REGION=emea` into
  `C:\ProgramData\AegisVendor\settings.ini`. Terminal status
  `ACTION_PHASE_SUCCESS`.

> J3 setup caveat: the POST echo+redirect fails with exit 1 if the parent
> directory of `${CONFIG_PATH}` does not exist. The endpoint pre-cleanup step
> (`mkdir C:\ProgramData\AegisVendor`) is required before the first run.

### Remaining acceptance after J

- Specific reboot-sensitive EXE packages the server team curates (vendor
  driver EXEs, kernel-mode runtimes) beyond the deterministic J1 case.
- Additional MST transforms the server team authors per vendor beyond the
  INSTALLDIR override exercised in J2.
- Config-only updates that write to registry hives in addition to the file
  path covered by J3.
- Section 4 (production backend/UI/authorization, package library,
  targeting, scheduling, retry/job persistence, audit views) remains the
  server-team track and is not gated on this handoff.

The additive agent-side durability updates summarized in
`dist/softdist-handoff.json` (`additive_engine_updates`) carry their own
native regression coverage. Their VM acceptance is tracked in blocks G, H
and I of this guide and is not a prerequisite for closing the J block.


## K. Bandwidth throttling (BigFix-aligned, 30 October 2026)

Agent now caps `FetchContent` (server-to-agent download) and `UploadContent`
(agent-to-server upload) at a bytes-per-second budget. Two sources of truth
feed each cap, and BigFix "lower wins" semantics apply:

1. **Agent config cap** (admin policy, endpoint-wide):
   - `AEGIS_FETCH_MAX_BYTES_PER_SECOND` — downstream (server to agent)
   - `AEGIS_UPLOAD_MAX_BYTES_PER_SECOND` — upstream (agent to server)
   - 0 = unlimited (default; existing deploys unchanged)
2. **Signed per-task rate** (console operator per-package):
   - `SoftwareDeploySpec.max_fetch_bytes_per_second`
   - `CollectFileSpec.max_upload_bytes_per_second`
   - 0 = no per-task cap; falls back to agent config
3. **Effective rate** = min(agent_cap, signed_rate), treating 0 as unlimited.
   Signed command CANNOT bypass the admin cap — admin policy wins.

Server-side cumulative throttle (BigFix `_BESRelay_HTTPServer_ThrottleKBPS`
equivalent) is server-team work; gRPC backpressure makes agent-side plus
server-side combine naturally as min() at the network layer.

### Preflight — one-time endpoint setup

**ENDPOINT**, in an admin CMD:

```cmd
if not exist C:\temp\AegisThrottleOut mkdir C:\temp\AegisThrottleOut
if exist C:\temp\AegisThrottleOut\big.log del C:\temp\AegisThrottleOut\big.log
powershell -NoProfile -Command "$path='C:\temp\AegisThrottleOut\big.log'; $size=5MB; $buf=[byte[]]::new($size); (New-Object Random).NextBytes($buf); [IO.File]::WriteAllBytes($path, $buf)"
dir C:\temp\AegisThrottleOut\big.log
```

Expected: ~5,242,880 bytes random-content log file for the upload test (K2).

### Case K1 — Throttled FETCH (signed per-task rate)

#### 1. Purpose

Prove the agent honors `SoftwareDeploySpec.max_fetch_bytes_per_second`. We
cap a 1.9 MB 7-Zip MSI bundle fetch at 500,000 bytes/sec. Expected wall-clock
~4 seconds (1,900,000 / 500,000).

#### 2. Commands

**MOCKSERVER**, after common preparation (clear PRE/POST/ROLLBACK/dependency
and K1-specific stale envs):

```cmd
set "AEGIS_FETCH_MAX_BYTES_PER_SECOND="
set "AEGIS_UPLOAD_MAX_BYTES_PER_SECOND="
set "AEGIS_DEPLOY_FETCH_MAX_BYTES_PER_SEC=500000"
set "AEGIS_COLLECT_UPLOAD_MAX_BYTES_PER_SEC="

copy /Y C:\temp\AegisRealTests\7z2605-x64.msi C:\temp\AegisK1-src\7zip.msi >nul 2>&1
if not exist C:\temp\AegisK1-src mkdir C:\temp\AegisK1-src
copy /Y C:\temp\AegisRealTests\7z2605-x64.msi C:\temp\AegisK1-src\7zip.msi
if exist C:\temp\AegisK1Bundle.zip del C:\temp\AegisK1Bundle.zip
mockserver.exe pack C:\temp\AegisK1-src C:\temp\AegisK1Bundle.zip

set "AEGIS_DEPLOY_FILE=C:\temp\AegisK1Bundle.zip"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisK1Bundle.zip"
set "AEGIS_DEPLOY_CONTENT_ID=k1-throttled-fetch-v1"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_NAME=K1 - throttled fetch 500 KB/s"
set "AEGIS_DEPLOY_BUNDLE_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C echo throttled-fetch-run"
set "AEGIS_DEPLOY_TIMEOUT=60"
mockserver.exe server
```

#### 3. Expected results

- Mock command line shows `fetch_rate=500000 B/s` on the deploy send.
- `ACTION_PHASE_DOWNLOADING` lasts ~4 seconds (not instant). Wall-clock
  between `fetching installer from server` and `preparing and verifying
  software bundle` status messages = ~ `bundle_bytes / 500000`.
- Terminal `ACTION_PHASE_SUCCESS`, `exit_code = 0`, `cleanup: removed`.

### Case K2 — Throttled UPLOAD (signed per-task rate)

#### 1. Purpose

Prove the agent honors `CollectFileSpec.max_upload_bytes_per_second`. We
upload a 5 MB log at 1,000,000 bytes/sec cap. Expected wall-clock ~5 seconds.

#### 2. Commands

**MOCKSERVER**, after common preparation:

```cmd
set "AEGIS_FETCH_MAX_BYTES_PER_SECOND="
set "AEGIS_UPLOAD_MAX_BYTES_PER_SECOND="
set "AEGIS_DEPLOY_FETCH_MAX_BYTES_PER_SEC="
set "AEGIS_COLLECT_UPLOAD_MAX_BYTES_PER_SEC=1000000"
set "AEGIS_COLLECT_PATHS=C:\temp\AegisThrottleOut\big.log"
mockserver.exe server
```

#### 3. Expected results

- Mock command line shows `upload_rate=1000000 B/s` on the collect send.
- Wall-clock between `collecting files` and the terminal action status is
  ~5 seconds for the 5 MB file (not instant, not 10 s either).
- Server stores the file under `uploads/<computer_id>/collect-001/big.log`
  with `integrity: true`. The 5 MB on-disk size matches the endpoint source.

### Case K3 — Lower wins: agent config caps a looser signed rate

#### 1. Purpose

Prove the signed per-task rate cannot override the admin-wide endpoint cap.
Signed rate = 2 MB/s, agent config cap = 500 KB/s → effective = 500 KB/s.
Admin policy wins.

#### 2. Commands

**ENDPOINT**, stop and relaunch the agent service with the endpoint cap set:

```cmd
sc stop AegisOneClient
setx AEGIS_FETCH_MAX_BYTES_PER_SECOND 500000 /M
sc start AegisOneClient
```

> `setx /M` persists the env var at the machine level so the service picks
> it up on next start. To revert later: `setx AEGIS_FETCH_MAX_BYTES_PER_SECOND "" /M`
> then stop/start the service.

**MOCKSERVER**:

```cmd
set "AEGIS_DEPLOY_FETCH_MAX_BYTES_PER_SEC=2000000"
set "AEGIS_DEPLOY_FILE=C:\temp\AegisK1Bundle.zip"
set "AEGIS_SERVE_SOFTWARE=C:\temp\AegisK1Bundle.zip"
set "AEGIS_DEPLOY_CONTENT_ID=k3-lower-wins-v1"
set "AEGIS_DEPLOY_NAME=K3 - lower-wins policy enforcement"
set "AEGIS_DEPLOY_BUNDLE_COMMAND=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C echo lower-wins-run"
mockserver.exe server
```

#### 3. Expected results

- Mock command line shows `fetch_rate=2000000 B/s` on send (signed value).
- Actual DOWNLOADING wall-clock is ~4 seconds (not 1 second). The agent
  applied `min(500000 admin cap, 2000000 signed) = 500000 B/s` — admin cap
  is non-negotiable.
- Terminal `ACTION_PHASE_SUCCESS`, `exit_code = 0`.

### Server-team contract note

The server team's cumulative throttle (BigFix's
`_BESRelay_HTTPServer_ThrottleKBPS` equivalent) is independent. Enforce it
at the server HTTP/gRPC send layer so N active connections share one
total-outbound budget — divided across live `FetchContent` and
`UploadContent` streams. gRPC flow control makes server-side plus
agent-side combine naturally as the lower of the two bytes/sec rates at the
network layer. Nothing in the agent needs to change to coexist with the
server-side cap.

Agent config env vars documented for the admin:
`AEGIS_FETCH_MAX_BYTES_PER_SECOND`,
`AEGIS_UPLOAD_MAX_BYTES_PER_SECOND`.
Signed proto fields: `SoftwareDeploySpec.max_fetch_bytes_per_second`,
`CollectFileSpec.max_upload_bytes_per_second`.
All in BYTES/sec. 0 = unlimited.
