# Multi-file packages - manual tests

27 October 2026. Only three useful cases: default cleanup, custom retention/explicit
cleanup, and wrong-hash refusal. Each case has purpose, commands and expected results.

Use `dist/AegisOneClient.exe` on the stopped endpoint service, then restart it
(or install the matching `dist/AegisOneClient.msi`). Use `dist/mockserver.exe` in
C:\temp\mock with existing certs. Replace BOTH agent and mockserver.
Vijay reports all three cases passed on 27 October 2026. The server team can
repeat them independently; actual vendor-installer acceptance remains separate.
All commands below are CMD commands. No .cmd/.ps1 test files or probe EXE needed.
Server team can run and record these cases without waiting for a chat reply.

## Prepare once

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

## Case 1 - Deploy multiple files and clean default staging

### 1. Purpose

Deploy both configuration files and their subfolder from the bundle. Verify the
command runs in the extracted package directory, and default staging is removed
without removing the deployed files.

### 2. Commands

MOCKSERVER:

```cmd
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

### 3. Expected results

- ACTION_PHASE_SUCCESS, exit_code 0.
- app.ini contains REGION=apac; readme.txt contains Aegis multi-file package.
- Mock output includes `package_directory: ...` and terminal `cleanup: removed`.
- This job's extraction directory is absent. With only this test job, the final
  dir command finds no aegis-package-* directory.
- The installed files under AegisBundleInstalled remain. This sample deploys config
  files; no vendor application is claimed installed by the generic action label.

## Case 2 - Retain custom staging, then explicitly delete file/folder

### 1. Purpose

Verify that a custom package location is retained after deployment so its files
can be reused. Then demonstrate the exact cleanup syntax for a file or the whole
package folder.

### 2. Commands

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

### 3. Expected results

- Deployment ACTION_PHASE_SUCCESS; terminal `cleanup: retained` and exact custom path.
- Both extracted files/subfolder exist before manual cleanup.
- After del, readme.txt is absent and settings\app.ini remains.
- After rmdir, AegisRetainedDemo is absent.
- Deployed files under C:\temp\AegisBundleInstalled remain unchanged.

## Case 3 - Wrong integrity hash prevents deployment

### 1. Purpose

Verify a ZIP with the wrong signed SHA256 is refused before its command can copy
files. This protects the package and all supporting files from replacement/corruption.

### 2. Commands

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

### 3. Expected results

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
