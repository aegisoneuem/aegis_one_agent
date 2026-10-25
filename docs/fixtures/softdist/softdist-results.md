# SoftDist test results

Build/commit: ______  Agent SHA256: ______  Mockserver SHA256: ______

Operator: ______  Date: ______  Endpoint hostname/device_id: ______

| Test | Case | Expected | Actual phase/reason | Endpoint check | PASS/FAIL |
|---|---|---|---|---|---|
| 01 | Ambiguous display name | FAILED; ambiguous installed product (2 matches) | PENDING | PENDING | PENDING |
| 02 | Remove only requested version 1.0 | SUCCESS; verified removed | PENDING | PENDING | PENDING |
| 03 | Already absent requested version | NOT_APPLICABLE; not installed | PENDING | PENDING | PENDING |
| 04 | Exact 64-bit key | SUCCESS; verified removed | PENDING | PENDING | PENDING |
| 05 | Same key in both registry views | FAILED; ambiguous installed product (2 matches) | PENDING | PENDING | PENDING |
| 06 | Remove only the 32-bit registration | SUCCESS; verified removed | PENDING | PENDING | PENDING |
| 07 | Remove only the 64-bit registration | SUCCESS; verified removed | PENDING | PENDING | PENDING |
| 08 | Exact name wins over similarly named companion | SUCCESS; verified removed | PENDING | PENDING | PENDING |
| 09 | Key and display name disagree | NOT_APPLICABLE; not installed | PENDING | PENDING | PENDING |
| 10 | No registered uninstall command | FAILED; no uninstall command registered | PENDING | PENDING | PENDING |
| 11 | No quiet command and no author silent arguments | FAILED; supply vendor-specific silent_args | PENDING | PENDING | PENDING |
| 12 | Author silent arguments, spaces and runtime variables | SUCCESS; verified removed | PENDING | PENDING | PENDING |
| 13 | Zero exit is not enough when product remains | FAILED; verification failed: product still detected after uninstall; exit_code=0 | PENDING | PENDING | PENDING |
| 14 | Registered executable is missing | FAILED; could not start installer | PENDING | PENDING | PENDING |
| 15 | Unquoted spaced executable path | FAILED; quote paths with spaces | PENDING | PENDING | PENDING |
| 16 | Malformed MSI ProductCode | FAILED; invalid MSI ProductCode | PENDING | PENDING | PENDING |
| 17 | HKCU is explicitly refused until Run-As | FAILED; per-user/HKCU removal requires user-context support | PENDING | PENDING | PENDING |
| 18 | Custom command overrides default registry uninstaller | SUCCESS; verified removed; exit_code=0 | PENDING | PENDING | PENDING |
| 19 | Hash-pinned CMD script overrides default uninstaller | SUCCESS; verified removed | PENDING | PENDING | PENDING |
| 20 | Tampered or wrong-hash file is refused | FAILED; integrity check failed (hash mismatch) | PENDING | PENDING | PENDING |
| 21 | Malformed registry value is not absence | FAILED; registry detection failed; QuietUninstallString | PENDING | PENDING | PENDING |
| 22 | Server-delivered custom script with detection IDs | SUCCESS; verified removed; FetchContent kind=package | PENDING | PENDING | PENDING |
| 23 | Installer timeout | FAILED; installer timed out after 1s | PENDING | PENDING | PENDING |
| 24 | Nonzero registered uninstaller exit | FAILED; installer failed (exit 19); exit_code=19 | PENDING | PENDING | PENDING |
| 25 | Package-author custom success exit code | SUCCESS; verified removed; exit_code=19 | PENDING | PENDING | PENDING |
| 26 | Install fixture and verify version | SUCCESS; verified installed (version 1.0) | PENDING | PENDING | PENDING |
| 27 | Upgrade old version 1.0 to 2.0 | SUCCESS; verified installed (version 2.0) | PENDING | PENDING | PENDING |
| 28 | Same/newer version skips installation | NOT_APPLICABLE; already installed (version 2.0, skipped) | PENDING | PENDING | PENDING |
| 29 | Missing pinned hash is refused | FAILED; expected_hash (sha256) is required | PENDING | PENDING | PENDING |
| 30 | Required publisher signature rejects unsigned script | FAILED; publisher signature check failed | PENDING | PENDING | PENDING |
| 31 | CMD payload path contains spaces | SUCCESS; verified removed | PENDING | PENDING | PENDING |
| 32 | Installed but requested version cannot be verified | FAILED; installed version is unknown (target 1.0) | PENDING | PENDING | PENDING |
| 33 | NSH Software Distribution job, parameters and fleet loop | JOB SUCCESS; per-target package/tag/hostname output | PENDING | PENDING | PENDING |
| 34 | NSH job propagates native script failure | JOB FAILED; native command exit_code=7 | PENDING | PENDING | PENDING |

Additional real-app results: MSI install / repeat / ProductCode uninstall / repeat / registry uninstall; EXE install / upgrade / uninstall. Record package name/version/architecture and actual vendor arguments.
