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
