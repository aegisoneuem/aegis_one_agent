# SoftDist - runnable VM acceptance tests

Use **CMD**, not PowerShell, for every command below. Run one test at a time.

## Copy these files once

1. **Endpoint VM:** replace the stopped agent's EXE with latest `dist/AegisOneClient.exe`; restart its service. The matching `dist/AegisOneClient.msi` is an alternative installer. Developers can use the matching `build` EXE.
2. **Mockserver machine:** stop old mockserver with Ctrl+C; replace it with latest `dist/mockserver.exe` (or matching developer `build` EXE). Keep existing certs and device registry.
3. Copy the complete [test-pack folder](fixtures/softdist) to **`C:\temp\AegisSoftDistTests` on BOTH machines**. Do not copy only the .reg file. Keep `server`, `endpoint`, payload scripts and .svsh files together.
4. Keep an **Administrator CMD on the endpoint VM** and a **CMD in `C:\temp\mock` on the mockserver machine** (use your actual mockserver folder if different).
5. These fixtures are dummy registry products. They validate routing/detection and scripts; they do not replace real MSI/EXE vendor acceptance.

## Every test follows the same three commands

1. Stop mockserver with **Ctrl+C**. If CMD asks `Terminate batch job (Y/N)?`, enter **Y**.
2. On the **ENDPOINT**, run `endpoint-setup.cmd NN`. It resets ONLY the named Aegis test registrations and marker files, so cases 01-32 do not depend on earlier cases.
3. On the **MOCKSERVER**, run `server-test.cmd NN`. It clears stale SoftDist/SVSH and unrelated startup-action test settings and starts normal server mode. Keep your existing TLS/enrollment settings.
4. Wait for the intended endpoint to connect and for terminal SUCCESS/FAILED/NOT_APPLICABLE. QUEUED or RUNNING is not a final result. If it does not reconnect, restart that test agent service.
5. On the **ENDPOINT**, run `endpoint-check.cmd NN`. PASS requires BOTH the expected server status and the endpoint checks.

**Two connected agents:** the mock sends the request to each connected device. Check the line containing your intended hostname/device ID. In Vijay's first run, host `mockserver` correctly FAILED ambiguity; `WIN-TUES4V46T7I` had no fixtures and correctly returned NOT_APPLICABLE. This does not mean one host had two conflicting results. To test both, copy/setup the pack on both endpoints and check each separately. Use dedicated test endpoints.

## Test index

| Test | Check | Expected terminal result |
|---|---|---|
| [01](#test-01) | Ambiguous display name | FAILED; ambiguous installed product (2 matches) |
| [02](#test-02) | Remove only requested version 1.0 | SUCCESS; verified removed |
| [03](#test-03) | Already absent requested version | NOT_APPLICABLE; not installed |
| [04](#test-04) | Exact 64-bit key | SUCCESS; verified removed |
| [05](#test-05) | Same key in both registry views | FAILED; ambiguous installed product (2 matches) |
| [06](#test-06) | Remove only the 32-bit registration | SUCCESS; verified removed |
| [07](#test-07) | Remove only the 64-bit registration | SUCCESS; verified removed |
| [08](#test-08) | Exact name wins over similarly named companion | SUCCESS; verified removed |
| [09](#test-09) | Key and display name disagree | NOT_APPLICABLE; not installed |
| [10](#test-10) | No registered uninstall command | FAILED; no uninstall command registered |
| [11](#test-11) | No quiet command and no author silent arguments | FAILED; supply vendor-specific silent_args |
| [12](#test-12) | Author silent arguments, spaces and runtime variables | SUCCESS; verified removed |
| [13](#test-13) | Zero exit is not enough when product remains | FAILED; verification failed: product still detected after uninstall; exit_code=0 |
| [14](#test-14) | Registered executable is missing | FAILED; could not start installer |
| [15](#test-15) | Unquoted spaced executable path | FAILED; quote paths with spaces |
| [16](#test-16) | Malformed MSI ProductCode | FAILED; invalid MSI ProductCode |
| [17](#test-17) | HKCU is explicitly refused until Run-As | FAILED; per-user/HKCU removal requires user-context support |
| [18](#test-18) | Custom command overrides default registry uninstaller | SUCCESS; verified removed; exit_code=0 |
| [19](#test-19) | Hash-pinned CMD script overrides default uninstaller | SUCCESS; verified removed |
| [20](#test-20) | Tampered or wrong-hash file is refused | FAILED; integrity check failed (hash mismatch) |
| [21](#test-21) | Malformed registry value is not absence | FAILED; registry detection failed; QuietUninstallString |
| [22](#test-22) | Server-delivered custom script with detection IDs | SUCCESS; verified removed; FetchContent kind=package |
| [23](#test-23) | Installer timeout | FAILED; installer timed out after 1s |
| [24](#test-24) | Nonzero registered uninstaller exit | FAILED; installer failed (exit 19); exit_code=19 |
| [25](#test-25) | Package-author custom success exit code | SUCCESS; verified removed; exit_code=19 |
| [26](#test-26) | Install fixture and verify version | SUCCESS; verified installed (version 1.0) |
| [27](#test-27) | Upgrade old version 1.0 to 2.0 | SUCCESS; verified installed (version 2.0) |
| [28](#test-28) | Same/newer version skips installation | NOT_APPLICABLE; already installed (version 2.0, skipped) |
| [29](#test-29) | Missing pinned hash is refused | FAILED; expected_hash (sha256) is required |
| [30](#test-30) | Required publisher signature rejects unsigned script | FAILED; publisher signature check failed |
| [31](#test-31) | CMD payload path contains spaces | SUCCESS; verified removed |
| [32](#test-32) | Installed but requested version cannot be verified | FAILED; installed version is unknown (target 1.0) |
| [33](#test-33) | SVSH Software Distribution job, parameters and fleet loop | JOB SUCCESS; per-target package/tag/hostname output |
| [34](#test-34) | SVSH job propagates native script failure | JOB FAILED; native command exit_code=7 |

## Test 01

**Check:** Ambiguous display name.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 01
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 01
```

**PASS in the intended host's result:** FAILED; ambiguous installed product (2 matches).

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 01
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present; `AegisSoftDistFixture-v2` view 64: present.

Vijay PASS on host mockserver. WIN-TUES4V46T7I had no fixtures and correctly returned NOT_APPLICABLE. Other tests remain pending.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 02

**Check:** Remove only requested version 1.0.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 02
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 02
```

**PASS in the intended host's result:** SUCCESS; verified removed.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 02
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: absent; `AegisSoftDistFixture-v2` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 03

**Check:** Already absent requested version.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 03
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 03
```

**PASS in the intended host's result:** NOT_APPLICABLE; not installed.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 03
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: absent; `AegisSoftDistFixture-v2` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 04

**Check:** Exact 64-bit key.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 04
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 04
```

**PASS in the intended host's result:** SUCCESS; verified removed.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 04
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present; `AegisSoftDistFixture-v2` view 64: absent.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 05

**Check:** Same key in both registry views.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 05
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 05
```

**PASS in the intended host's result:** FAILED; ambiguous installed product (2 matches).

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 05
```

**Expected endpoint state:** `AegisSoftDistFixture-view` view 32: present; `AegisSoftDistFixture-view` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 06

**Check:** Remove only the 32-bit registration.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 06
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 06
```

**PASS in the intended host's result:** SUCCESS; verified removed.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 06
```

**Expected endpoint state:** `AegisSoftDistFixture-view` view 32: absent; `AegisSoftDistFixture-view` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 07

**Check:** Remove only the 64-bit registration.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 07
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 07
```

**PASS in the intended host's result:** SUCCESS; verified removed.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 07
```

**Expected endpoint state:** `AegisSoftDistFixture-view` view 32: present; `AegisSoftDistFixture-view` view 64: absent.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 08

**Check:** Exact name wins over similarly named companion.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 08
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 08
```

**PASS in the intended host's result:** SUCCESS; verified removed.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 08
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: absent; `AegisSoftDistFixture-tool` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 09

**Check:** Key and display name disagree.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 09
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 09
```

**PASS in the intended host's result:** NOT_APPLICABLE; not installed.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 09
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present; `AegisSoftDistFixture-v2` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 10

**Check:** No registered uninstall command.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 10
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 10
```

**PASS in the intended host's result:** FAILED; no uninstall command registered.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 10
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 11

**Check:** No quiet command and no author silent arguments.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 11
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 11
```

**PASS in the intended host's result:** FAILED; supply vendor-specific silent_args.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 11
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present; `results\custom.txt`: must not exist.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 12

**Check:** Author silent arguments, spaces and runtime variables.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 12
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 12
```

**PASS in the intended host's result:** SUCCESS; verified removed.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 12
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: absent; `results\custom.txt`: exists (read its content).

custom.txt must contain tag=daily release, tag-environment=daily release, and matching endpoint host-argument/host-environment.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 13

**Check:** Zero exit is not enough when product remains.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 13
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 13
```

**PASS in the intended host's result:** FAILED; verification failed: product still detected after uninstall; exit_code=0.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 13
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 14

**Check:** Registered executable is missing.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 14
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 14
```

**PASS in the intended host's result:** FAILED; could not start installer.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 14
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 15

**Check:** Unquoted spaced executable path.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 15
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 15
```

**PASS in the intended host's result:** FAILED; quote paths with spaces.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 15
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 16

**Check:** Malformed MSI ProductCode.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 16
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 16
```

**PASS in the intended host's result:** FAILED; invalid MSI ProductCode.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 16
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present; `AegisSoftDistFixture-v2` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 17

**Check:** HKCU is explicitly refused until Run-As.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 17
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 17
```

**PASS in the intended host's result:** FAILED; per-user/HKCU removal requires user-context support.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 17
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present; `AegisSoftDistFixture-v2` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 18

**Check:** Custom command overrides default registry uninstaller.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 18
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 18
```

**PASS in the intended host's result:** SUCCESS; verified removed; exit_code=0.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 18
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: absent; `results\custom.txt`: exists (read its content).

Default registry command exits 19. SUCCESS plus CUSTOM_REMOVE proves the supplied custom command ran instead.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 19

**Check:** Hash-pinned CMD script overrides default uninstaller.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 19
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 19
```

**PASS in the intended host's result:** SUCCESS; verified removed.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 19
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: absent; `results\custom.txt`: exists (read its content).

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 20

**Check:** Tampered or wrong-hash file is refused.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 20
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 20
```

**PASS in the intended host's result:** FAILED; integrity check failed (hash mismatch).

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 20
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present; `AegisSoftDistFixture-v2` view 64: present; `results\custom.txt`: must not exist.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 21

**Check:** Malformed registry value is not absence.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 21
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 21
```

**PASS in the intended host's result:** FAILED; registry detection failed; QuietUninstallString.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 21
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 22

**Check:** Server-delivered custom script with detection IDs.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 22
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 22
```

**PASS in the intended host's result:** SUCCESS; verified removed; FetchContent kind=package.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 22
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: absent; `results\custom.txt`: exists (read its content).

No local installer file_path is sent. Server streams the script; client cache/decrypted temp are exercised. If rerunning, prior cached content may avoid a second download.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 23

**Check:** Installer timeout.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 23
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 23
```

**PASS in the intended host's result:** FAILED; installer timed out after 1s.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 23
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present; `AegisSoftDistFixture-v2` view 64: present.

The payload pings loopback only and never removes a key. Allow about ten seconds for an already-started child to finish; timeout is not a promise to kill all descendants.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 24

**Check:** Nonzero registered uninstaller exit.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 24
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 24
```

**PASS in the intended host's result:** FAILED; installer failed (exit 19); exit_code=19.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 24
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 25

**Check:** Package-author custom success exit code.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 25
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 25
```

**PASS in the intended host's result:** SUCCESS; verified removed; exit_code=19.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 25
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: absent; `results\custom.txt`: exists (read its content).

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 26

**Check:** Install fixture and verify version.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 26
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 26
```

**PASS in the intended host's result:** SUCCESS; verified installed (version 1.0).

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 26
```

**Expected endpoint state:** install fixture DisplayVersion = `1.0`; `results\install.txt`: exists (read its content).

This payload registers a dummy app. It tests the EXE/script pipeline and detection, not Windows Installer MSI behavior.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 27

**Check:** Upgrade old version 1.0 to 2.0.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 27
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 27
```

**PASS in the intended host's result:** SUCCESS; verified installed (version 2.0).

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 27
```

**Expected endpoint state:** install fixture DisplayVersion = `2.0`; `results\install.txt`: exists (read its content).

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 28

**Check:** Same/newer version skips installation.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 28
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 28
```

**PASS in the intended host's result:** NOT_APPLICABLE; already installed (version 2.0, skipped).

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 28
```

**Expected endpoint state:** install fixture DisplayVersion = `2.0`; `results\install.txt`: must not exist.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 29

**Check:** Missing pinned hash is refused.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 29
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 29
```

**PASS in the intended host's result:** FAILED; expected_hash (sha256) is required.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 29
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present; `AegisSoftDistFixture-v2` view 64: present; `results\custom.txt`: must not exist.

This test deliberately disables only the mock console auto-hash. Mandatory agent hash verification remains enabled.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 30

**Check:** Required publisher signature rejects unsigned script.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 30
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 30
```

**PASS in the intended host's result:** FAILED; publisher signature check failed.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 30
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: present; `AegisSoftDistFixture-v2` view 64: present; `results\custom.txt`: must not exist.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 31

**Check:** CMD payload path contains spaces.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 31
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 31
```

**PASS in the intended host's result:** SUCCESS; verified removed.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 31
```

**Expected endpoint state:** `AegisSoftDistFixture-v1` view 64: absent; `results\custom.txt`: exists (read its content).

Requires the newest agent build: a real CMD quoting bug was found and fixed while preparing this pack.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 32

**Check:** Installed but requested version cannot be verified.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 32
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 32
```

**PASS in the intended host's result:** FAILED; installed version is unknown (target 1.0).

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 32
```

**Expected endpoint state:** `AegisSoftDistInstall` view 64: present; install fixture has no DisplayVersion; `results\install.txt`: exists (read its content).

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 33

**Check:** SVSH Software Distribution job, parameters and fleet loop.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 33
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 33
```

**In the SVSH prompt** (replace the hostname with the endpoint where you ran setup):

```text
cd \\mockserver
job run C:\temp\AegisSoftDistTests\software-workflow.svsh --kind software_distribution --param PACKAGE=fixture --param TAG=daily-run
```

For two prepared endpoints select `cd \\WIN-TUES4V46T7I,mockserver` before the job.

**PASS in the intended host's result:** JOB SUCCESS; per-target package/tag/hostname output.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 33
```

**Expected endpoint state:** `results\svsh.txt`: exists (read its content).

This is the shared SVSH Runner Software Distribution caller mode. It is not an agent installer_type=svsh or proof that the production console is already wired.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Test 34

**Check:** SVSH job propagates native script failure.

**ENDPOINT - Administrator CMD:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 34
```

**MOCKSERVER - CMD in the folder containing mockserver.exe:**

```cmd
call C:\temp\AegisSoftDistTests\server-test.cmd 34
```

**In the SVSH prompt** (replace the hostname with the endpoint where you ran setup):

```text
cd \\mockserver
job run C:\temp\AegisSoftDistTests\software-workflow-fail.svsh --kind software_distribution --param PACKAGE=fixture --param TAG=daily-run
```

For two prepared endpoints select `cd \\WIN-TUES4V46T7I,mockserver` before the job.

**PASS in the intended host's result:** JOB FAILED; native command exit_code=7.

**ENDPOINT - check after the terminal result:**

```cmd
call C:\temp\AegisSoftDistTests\endpoint-check.cmd 34
```

This is the shared SVSH Runner Software Distribution caller mode. It is not an agent installer_type=svsh or proof that the production console is already wired.

**If it fails:** save the intended host's complete ActionStatus (phase, exit_code, reason, detail) plus endpoint-check output. Mark PASS/FAIL in the checklist.

## Real MSI and EXE acceptance - required in addition to fixtures

Choose an approved disposable app in a VM snapshot. Do NOT use the Aegis agent itself as the uninstall target. Vendor-specific values cannot be guessed; get the actual ProductCode and silent switches from the package owner. First complete cases 01-32.

### MSI install from server

On the mockserver, put the approved MSI at `C:\temp\AegisSoftDistTests\packages\test-app.msi`. Set PRODUCT_CODE to its real GUID below. The mock automatically hashes the actual server file. Use an endpoint where this product is absent.

```cmd
call C:\temp\AegisSoftDistTests\server-common.cmd
set AEGIS_DEPLOY_NAME=Approved MSI test
set AEGIS_DEPLOY_ACTION=install
set AEGIS_DEPLOY_TYPE=msi
set AEGIS_DEPLOY_PRODUCT_CODE={REPLACE-WITH-ACTUAL-MSI-PRODUCTCODE}
set AEGIS_DEPLOY_CONTENT_ID=approved-msi-test
set AEGIS_SERVE_SOFTWARE=C:\temp\AegisSoftDistTests\packages\test-app.msi
mockserver.exe server
```

PASS: SUCCESS; verified installed; product actually appears in Windows Installed Apps. Repeat unchanged: NOT_APPLICABLE; no second install.

### MSI uninstall by ProductCode

Stop mockserver. Use the same real ProductCode:

```cmd
call C:\temp\AegisSoftDistTests\server-common.cmd
set AEGIS_DEPLOY_NAME=Approved MSI test
set AEGIS_DEPLOY_ACTION=uninstall
set AEGIS_DEPLOY_TYPE=msi
set AEGIS_DEPLOY_PRODUCT_CODE={REPLACE-WITH-ACTUAL-MSI-PRODUCTCODE}
mockserver.exe server
```

PASS: SUCCESS; verified removed; product gone. Repeat unchanged: NOT_APPLICABLE.

### MSI uninstall by registry registration

Reinstall using the first MSI test, then stop mockserver. Select the actual uninstall registry subkey/view (often the ProductCode) with PRODUCT_CODE unset:

```cmd
call C:\temp\AegisSoftDistTests\server-common.cmd
set AEGIS_DEPLOY_NAME=Approved MSI registry uninstall
set AEGIS_DEPLOY_ACTION=uninstall
set AEGIS_DEPLOY_TYPE=msi
set AEGIS_DEPLOY_UNINSTALL_KEY=HKLM64\{REPLACE-WITH-ACTUAL-UNINSTALL-SUBKEY}
mockserver.exe server
```

PASS: true removal and SUCCESS; no repair/maintenance UI. For a 32-bit registration use HKLM32. Do not edit a real application's MSI registration to force a test; the synthetic/parser tests cover malformed command variants.

### EXE install and fileless uninstall

Put an approved EXE at `C:\temp\AegisSoftDistTests\packages\test-app.exe` on the mockserver. Replace DISPLAY_NAME and ARGS with actual vendor values. VERSION, if used, is the actual target installed version.

```cmd
call C:\temp\AegisSoftDistTests\server-common.cmd
set AEGIS_DEPLOY_ACTION=install
set AEGIS_DEPLOY_TYPE=exe
set AEGIS_DEPLOY_DISPLAY_NAME=REPLACE-WITH-ACTUAL-APP-DISPLAY-NAME
set AEGIS_DEPLOY_ARGS=REPLACE-WITH-VENDOR-SILENT-INSTALL-ARGS
set AEGIS_DEPLOY_CONTENT_ID=approved-exe-test
set AEGIS_SERVE_SOFTWARE=C:\temp\AegisSoftDistTests\packages\test-app.exe
mockserver.exe server
```

PASS: silent install, SUCCESS and verified installed. Repeat: NOT_APPLICABLE. To test an upgrade, first install the approved older version, then serve the newer installer with its target VERSION; verify Windows Installed Apps shows that newer version.

For uninstall, stop mockserver and use its actual key:

```cmd
call C:\temp\AegisSoftDistTests\server-common.cmd
set AEGIS_DEPLOY_ACTION=uninstall
set AEGIS_DEPLOY_TYPE=exe
set AEGIS_DEPLOY_UNINSTALL_KEY=HKLM64\REPLACE-WITH-ACTUAL-APP-SUBKEY
mockserver.exe server
```

PASS: SUCCESS and verified removed. If the product has no QuietUninstallString, add `AEGIS_DEPLOY_ARGS` with its actual vendor-specific silent uninstall switches. Do not assume every vendor uses /S.

## Send these results back

Copy the [results checklist](softdist-results.md), record the agent/mock build, endpoint hostname, case number, terminal phase/reason and endpoint-check result. New cases are PENDING until someone runs them; automated helper checks are not VM/vendor acceptance.

Run-As/HKCU execution is not implemented yet (test 17 expects refusal). Production console wiring of SVSH is a server-team acceptance item. If removal needs reboot and registration remains, current strict verification fails with the reboot flag retained; durable post-reboot verification is a remaining SoftDist item.
