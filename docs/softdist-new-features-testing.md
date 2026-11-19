# SoftDist new features - server team manual test guide

26 October 2026. Use CMD for every command below. No test scripts, .cmd launchers,
probe EXEs or generated fixtures are required. Run the cases and record results;
there is no chat reply/approval gate. Previously passed cases need not be repeated.

## Preparation - once

1. ENDPOINT: replace the stopped agent service's EXE with `dist/AegisOneClient.exe`,
   then restart that service. Alternatively install the matching
   `dist/AegisOneClient.msi`. Keep the existing identity/config/certs.
2. MOCKSERVER: stop old mockserver, copy `dist/mockserver.exe` into `C:\temp\mock`.
   Keep its existing certs, device registry and normal TLS settings.
3. Use the existing test endpoint WIN-TUES4V46T7I. Confirm its Device ID is
   `b3dea9f7-5df8-4765-ad8b-621307ef5f41`; substitute the actual UUID for another
   endpoint. Do not use the computer ID or hostname for AEGIS_TARGET_DEVICE.
4. Keep exactly one interactive user active on this endpoint for cases 1 and 5.

ENDPOINT - Administrator CMD:

```cmd
mkdir C:\temp\softdist-manual
icacls C:\temp\softdist-manual /grant "*S-1-5-32-545:(OI)(CI)M"
```

This permission applies only to the test-output folder. If it already exists,
`mkdir` saying so is normal.

MOCKSERVER - open a fresh CMD from Start. Use the existing TLS settings, then:

```cmd
cd /d C:\temp\mock
set "AEGIS_SVSH="
set "AEGIS_SVSH_SCRIPT="
set "AEGIS_DEPLOY_CONTENT_ID="
set "AEGIS_DEPLOY_PRODUCT_CODE="
set "AEGIS_DEPLOY_UNINSTALL_KEY="
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_VERSION="
set "AEGIS_DEPLOY_SESSION_ID="
set "AEGIS_DEPLOY_SECRETS_FILE="
set "AEGIS_DEPLOY_PARAMS="
set "AEGIS_SERVE_SOFTWARE="
set "AEGIS_INSTALL_FILE="
set "AEGIS_UNINSTALL_KB="
set "AEGIS_STAGE_KB="
set "AEGIS_COLLECT_PATHS="
set "AEGIS_REBOOT_DELAY="
set "AEGIS_SCAN_NOW="
set "AEGIS_CANCEL_REBOOT="
set "AEGIS_TARGET_DEVICE=b3dea9f7-5df8-4765-ad8b-621307ef5f41"
```

Stay in this SAME CMD for all cases. Stop mockserver with Ctrl+C before changing
settings and starting the next job. Wait for the intended host's terminal status;
QUEUED/RUNNING is not PASS. Restart its existing agent service if it does not
reconnect. For a rerun, first delete only that case's old output file so it cannot
be mistaken for a new result.

## Case 1 - Run as the logged-in user

### 1. Purpose

Verify that a job configured for the logged-in user runs under that user's
identity rather than SYSTEM. This is needed for user-profile configuration and
per-user packages. It does not give the user administrator/elevated rights.

### 2. Commands

ENDPOINT - normal CMD belonging to the logged-in user:

```cmd
whoami /user
```

Keep this username/SID for comparison.

MOCKSERVER:

```cmd
set "AEGIS_DEPLOY_NAME=Logged-in user identity"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C whoami /user > C:\temp\softdist-manual\logged-in-user.txt"
set "AEGIS_DEPLOY_RUN_AS=logged_in_user"
set "AEGIS_DEPLOY_DETECTION_SCOPE=user"
set "AEGIS_DEPLOY_SECRETS_FILE="
set "AEGIS_DEPLOY_PARAMS="
mockserver.exe server
```

ENDPOINT:

```cmd
type C:\temp\softdist-manual\logged-in-user.txt
```

### 3. Expected results

- Intended endpoint: ACTION_PHASE_SUCCESS, exit_code 0.
- File username and SID match the endpoint's `whoami /user` exactly.
- It must not show SYSTEM/S-1-5-18 or another user's identity.

## Case 2 - Run as a specified account

### 1. Purpose

Verify that the supplied account can start a noninteractive job even while a
different user is logged into the desktop. This is the execution path for packages
that need a designated account. Permissions remain those of the chosen account.

### 2. Commands

ENDPOINT - Administrator CMD; skip creation if this test account already exists
and its password is known:

```cmd
net user AegisSoftDistUser * /add
```

Enter the password at the hidden prompts. Do not add this account to Administrators.
Then open on the ENDPOINT:

```cmd
secpol.msc
```

Local Policies -> User Rights Assignment -> Log on as a batch job -> add
`AegisSoftDistUser` -> Apply/OK. Preserve existing entries. If domain policy denies
this right, record the policy block rather than changing unrelated policies.
The test account does not need to sign into the desktop.

MOCKSERVER - after stopping mockserver:

```cmd
mockserver.exe secrets specified-account.dpapi
```

| Prompt | Answer |
|---|---|
| Run-As username | AegisSoftDistUser |
| Domain | . |
| Account password | Enter that account's password at the hidden prompt |
| Secure parameter NAME | Blank Enter |

The dot means the endpoint's local account. This encrypted credential-data file
is created by the mock application; it is not a test script. Use the same Windows
operator account/machine to run mockserver. Reuse an existing correct bundle;
the command refuses to overwrite files. Never put the password in a `set` command.

MOCKSERVER - same CMD:

```cmd
set "AEGIS_DEPLOY_NAME=Specified account identity"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C whoami /user > C:\temp\softdist-manual\specified-account.txt"
set "AEGIS_DEPLOY_RUN_AS=specified_account"
set "AEGIS_DEPLOY_DETECTION_SCOPE=user"
set "AEGIS_DEPLOY_PARAMS="
set "AEGIS_DEPLOY_SECRETS_FILE=C:\temp\mock\specified-account.dpapi"
mockserver.exe server
```

ENDPOINT:

```cmd
type C:\temp\softdist-manual\specified-account.txt
```

### 3. Expected results

- Intended endpoint: ACTION_PHASE_SUCCESS, exit_code 0.
- File shows WIN-TUES4V46T7I\AegisSoftDistUser (case-insensitive) and its user SID.
- It must not show SYSTEM or the other desktop user. The account may remain signed out.
- A batch-logon error is a failed result; record the reason, not the password.

## Case 3 - Ordinary package parameter sets endpoint configuration

### 1. Purpose

Verify that the server can supply a non-secret package setting, REGION=apac,
and the agent substitutes it in the command that creates an endpoint config file.

### 2. Commands

MOCKSERVER - stop previous mockserver, then:

```cmd
set "AEGIS_DEPLOY_NAME=Ordinary parameter configuration"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE=cmd.exe"
set "AEGIS_DEPLOY_ARGS=/D /C echo REGION=${REGION}>C:\temp\softdist-manual\region.ini"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_SECRETS_FILE="
set "AEGIS_DEPLOY_PARAMS=REGION=apac"
mockserver.exe server
```

ENDPOINT:

```cmd
type C:\temp\softdist-manual\region.ini
```

### 3. Expected results

- Intended endpoint: ACTION_PHASE_SUCCESS, exit_code 0.
- File contains `REGION=apac`, not the literal `${REGION}`.

## Case 4 - Secure parameter is delivered over stdin

### 1. Purpose

Verify that a receiving command gets the private API_TOKEN field over stdin
without putting its value in command arguments. Windows findstr checks field
presence. This is input delivery, not a real vendor install or a full security audit.

### 2. Commands

MOCKSERVER - stop previous mockserver, then:

```cmd
mockserver.exe secrets installer-input.dpapi
```

| Prompt | Answer |
|---|---|
| Run-As username | Blank Enter |
| Secure parameter NAME | API_TOKEN |
| Value (hidden) | DEMO9274 |
| Next Secure parameter NAME | Blank Enter |

Use this dummy value only. Reuse an existing correct bundle if already created.
Then in the same MOCKSERVER CMD:

```cmd
set "AEGIS_DEPLOY_NAME=Secure installer input delivery"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE=findstr.exe"
set "AEGIS_DEPLOY_ARGS=/L /C:API_TOKEN"
set "AEGIS_DEPLOY_RUN_AS=system"
set "AEGIS_DEPLOY_DETECTION_SCOPE=machine"
set "AEGIS_DEPLOY_PARAMS="
set "AEGIS_DEPLOY_SECRETS_FILE=C:\temp\mock\installer-input.dpapi"
mockserver.exe server
```

ENDPOINT: no manual command or input file is needed. The agent runs findstr and
supplies its stdin pipe. Do not create a plaintext secret JSON file.

### 3. Expected results

- Intended endpoint: ACTION_PHASE_SUCCESS, exit_code 0: the API_TOKEN field was found.
- Arguments contain only `/L /C:API_TOKEN`, not DEMO9274.
- Normal job output must not print the JSON/token. Private stdout/stderr is
  suppressed; a suppression notice is expected.
- This does not compare the private value or prove a vendor supports the format.
  A real vendor package needs a supported secure interface/adapter.

## Case 5 - Per-user detection, repeat protection and verified removal

### 1. Purpose

Verify that detection and execution both use the selected user's registry.
A safe, explicitly named dummy registration stands in for a per-user product:
create it, confirm a repeat is skipped, then remove it and verify removal.
No real application is installed or uninstalled.

### 2. Commands

Keep the same single endpoint user active as in Case 1.

ENDPOINT - that user's normal CMD, before starting:

```cmd
reg query HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\AegisOneManualUserTest
```

The key should not exist. If it is left from an earlier run, delete only this
named dummy key before the install step:

```cmd
reg delete HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\AegisOneManualUserTest /f
```

MOCKSERVER - stop previous mockserver, then:

```cmd
set "AEGIS_DEPLOY_NAME=Per-user detection and removal"
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_FILE=reg.exe"
set "AEGIS_DEPLOY_ARGS=add HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\AegisOneManualUserTest /v DisplayName /t REG_SZ /d AegisOneManualUserTest /f"
set "AEGIS_DEPLOY_RUN_AS=logged_in_user"
set "AEGIS_DEPLOY_DETECTION_SCOPE=user"
set "AEGIS_DEPLOY_SECRETS_FILE="
set "AEGIS_DEPLOY_PARAMS="
set "AEGIS_DEPLOY_UNINSTALL_KEY=HKCU64\AegisOneManualUserTest"
mockserver.exe server
```

ENDPOINT - same user's normal CMD:

```cmd
reg query HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\AegisOneManualUserTest /v DisplayName
```

MOCKSERVER - Ctrl+C and start the unchanged job again:

```cmd
mockserver.exe server
```

After checking the repeat result, Ctrl+C again. Change only the action/command:

```cmd
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_ARGS=delete HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\AegisOneManualUserTest /f"
mockserver.exe server
```

ENDPOINT - same user's normal CMD:

```cmd
reg query HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\AegisOneManualUserTest
```

### 3. Expected results

- First install job: ACTION_PHASE_SUCCESS with `verified installed`; endpoint
  DisplayName is AegisOneManualUserTest.
- Repeat: ACTION_PHASE_NOT_APPLICABLE, already installed in selected scope.
- Uninstall: ACTION_PHASE_SUCCESS with `verified removed`; endpoint reports key not found.
- All three outcomes must belong to the intended endpoint and the same logged-in user.

## Record and next work

Record case number, hostname, terminal phase/exit code and endpoint observation.
Record failures with their reason; independent cases can continue. No passwords
or decrypted bundles in reports. These five cases do not certify all vendor installs.

The generic `install` success label in command-only cases does not mean software
was installed. Logged-in user mode does not automatically elevate or install prerequisites.

Production server may use its own vault/UI instead of mock .dpapi files/env settings.
Use updated bindings from aegis_one_proto and signed software_deploy_v2 for execution
settings/secrets. Secure installer input remains stdin, never ordinary arguments/env.
Real per-user vendor MSI/EXE and remaining package-authoring/dependency/lifecycle work
stay on [the SoftDist roadmap](softdist-roadmap.md).
